package openresponses

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// OpenResponsesChannel implements a PicoClaw channel that exposes an HTTP API
// compatible with the OpenResponses specification.
type OpenResponsesChannel struct {
	*channels.BaseChannel
	bc     *config.Channel
	config *config.OpenResponsesSettings

	convMu sync.Mutex
	convs  map[string]*conversationState // conversationID -> state

	ctx    context.Context
	cancel context.CancelFunc
}

// conversationState tracks a single active conversation request.
type conversationState struct {
	stream      *pendingStream
	done        chan struct{}
	active      atomic.Bool
	hasStreamer atomic.Bool
}

// NewOpenResponsesChannel creates a new OpenResponses channel.
func NewOpenResponsesChannel(
	bc *config.Channel,
	cfg *config.OpenResponsesSettings,
	messageBus *bus.MessageBus,
) (*OpenResponsesChannel, error) {
	base := channels.NewBaseChannel(
		bc.Name(),
		cfg,
		messageBus,
		bc.AllowFrom,
		channels.WithMaxMessageLength(0), // no limit; we return full response
	)

	return &OpenResponsesChannel{
		BaseChannel: base,
		bc:          bc,
		config:      cfg,
		convs:       make(map[string]*conversationState),
	}, nil
}

// Start implements channels.Channel.
func (c *OpenResponsesChannel) Start(ctx context.Context) error {
	logger.InfoC("openresponses", "Starting OpenResponses channel")
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)
	logger.InfoC("openresponses", "OpenResponses channel started")
	return nil
}

// Stop implements channels.Channel.
func (c *OpenResponsesChannel) Stop(ctx context.Context) error {
	logger.InfoC("openresponses", "Stopping OpenResponses channel")
	c.SetRunning(false)

	// Close all active conversation streams so HTTP handlers unblock.
	c.convMu.Lock()
	for _, st := range c.convs {
		st.stream.close()
	}
	clear(c.convs)
	c.convMu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}

	logger.InfoC("openresponses", "OpenResponses channel stopped")
	return nil
}

// Send implements channels.Channel. It pushes each outbound message into the
// pending stream so the HTTP handler can read it in real time.
func (c *OpenResponsesChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	conversationID := msg.ChatID
	if conversationID == "" {
		return nil, nil
	}

	c.convMu.Lock()
	st, found := c.convs[conversationID]
	c.convMu.Unlock()

	if !found {
		logger.WarnCF("openresponses", "Send: conversation not found, dropping message",
			map[string]any{
				"conversation_id": conversationID,
				"message_kind":    msg.Context.Raw["message_kind"],
			})
		return nil, nil
	}

	// If a streamer is actually in use, skip the full-text message because it
	// has already been delivered incrementally via Update().
	// Allow thought/function_call/turn_end events through.
	raw := msg.Context.Raw
	logger.DebugCF("openresponses", "Send received", map[string]any{
		"conversation_id": conversationID,
		"has_streamer":    st.hasStreamer.Load(),
		"message_kind":    raw["message_kind"],
		"content_preview": truncateString(msg.Content, 200),
	})
	// Allow error messages through even when a streamer is active so that
	// LLM failures are shown to the user instead of being silently dropped.
	if st.hasStreamer.Load() &&
		raw["message_kind"] != "thought" &&
		raw["message_kind"] != "function_call" &&
		raw["message_kind"] != "turn_end" &&
		raw["message_kind"] != "tool_timing" &&
		raw["message_kind"] != "llm_timing" &&
		raw["message_kind"] != "error" {
		logger.DebugCF("openresponses", "Send skipped (streamer active, not thought/fc/turn_end/timing)", map[string]any{
			"conversation_id": conversationID,
		})
		return nil, nil
	}

	var ev streamEvent

	if raw["message_kind"] == "turn_end" {
		logger.DebugCF("openresponses", "Send push turn_end", map[string]any{
			"conversation_id": conversationID,
		})
		ev = streamEvent{kind: eventKindTurnEnd}
		st.stream.push(ev)
		c.convMu.Lock()
		delete(c.convs, conversationID)
		c.convMu.Unlock()
		close(st.done)
		st.stream.close()
		return nil, nil
	}

	if raw["message_kind"] == "thought" {
		ev = streamEvent{kind: eventKindReasoning, content: msg.Content}
		logger.DebugCF("openresponses", "Send push reasoning", map[string]any{
			"conversation_id": conversationID,
			"content_preview": truncateString(msg.Content, 200),
		})
	} else if raw["message_kind"] == "function_call" {
		ev = streamEvent{
			kind:      eventKindFunctionCall,
			callID:    raw["call_id"],
			name:      raw["name"],
			arguments: raw["arguments"],
		}
		logger.DebugCF("openresponses", "Send push function_call", map[string]any{
			"conversation_id": conversationID,
			"name":            raw["name"],
		})
	} else {
		ev = streamEvent{kind: eventKindText, content: msg.Content}
		logger.DebugCF("openresponses", "Send push text", map[string]any{
			"conversation_id": conversationID,
			"content_preview": truncateString(msg.Content, 200),
		})
	}
	st.stream.push(ev)
	return nil, nil
}

// dispatch sends the user's input into the PicoClaw MessageBus and returns
// a pendingStream that the HTTP handler reads from.
// If the conversation already has an active request, it publishes a steering
// message instead and returns (nil, true, nil).
func (c *OpenResponsesChannel) dispatch(
	ctx context.Context,
	conversationID string,
	content string,
	media []string,
) (*pendingStream, bool, error) {
	c.convMu.Lock()
	if st, ok := c.convs[conversationID]; ok && st.active.Load() {
		c.convMu.Unlock()

		// Active request exists — enqueue steering and tell caller.
		sender := bus.SenderInfo{
			Platform:    "openresponses",
			PlatformID:  "user",
			CanonicalID: identity.BuildCanonicalID("openresponses", "user"),
		}

		inboundCtx := bus.InboundContext{
			Channel:   c.Name(),
			ChatID:    conversationID,
			ChatType:  "direct",
			SenderID:  sender.CanonicalID,
			MessageID: conversationID,
			Raw: map[string]string{
				"conversation_id": conversationID,
			},
		}

		c.HandleInboundContext(ctx, conversationID, content, media, inboundCtx, sender)
		return nil, true, nil
	}

	s := newPendingStream(64)
	st := &conversationState{
		stream: s,
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	c.convs[conversationID] = st
	c.convMu.Unlock()

	sender := bus.SenderInfo{
		Platform:    "openresponses",
		PlatformID:  "user",
		CanonicalID: identity.BuildCanonicalID("openresponses", "user"),
	}

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    conversationID,
		ChatType:  "direct",
		SenderID:  sender.CanonicalID,
		MessageID: conversationID,
		Raw: map[string]string{
			"conversation_id": conversationID,
		},
	}

	c.HandleInboundContext(ctx, conversationID, content, media, inboundCtx, sender)
	return s, false, nil
}

// SendMedia implements channels.MediaSender. It converts image MediaParts
// into output_image stream events for the matching conversation.
func (c *OpenResponsesChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	logger.DebugCF("openresponses", "SendMedia received", map[string]any{
		"conversation_id": msg.ChatID,
		"parts_count":     len(msg.Parts),
	})

	conversationID := msg.ChatID
	if conversationID == "" {
		return nil, nil
	}

	c.convMu.Lock()
	st, found := c.convs[conversationID]
	c.convMu.Unlock()

	if !found {
		return nil, nil
	}

	store := c.GetMediaStore()
	if store == nil {
		logger.WarnC("openresponses", "SendMedia: no media store available")
		return nil, nil
	}

	var sentIDs []string
	for _, part := range msg.Parts {
		if part.Type != "image" {
			continue
		}
		if !strings.HasPrefix(part.Ref, "media://") {
			continue
		}

		localPath, meta, err := store.ResolveWithMeta(part.Ref)
		if err != nil {
			logger.WarnCF("openresponses", "SendMedia: failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		mime := meta.ContentType
		if mime == "" {
			mime = mimeFromExt(part.Filename)
		}
		if !strings.HasPrefix(mime, "image/") {
			continue
		}

		dataURL, err := encodeFileToDataURL(localPath, mime)
		if err != nil {
			logger.WarnCF("openresponses", "SendMedia: failed to encode image", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			continue
		}

		ev := streamEvent{
			kind:     eventKindImage,
			imageURL: dataURL,
			caption:  part.Caption,
		}
		if ok := st.stream.push(ev); ok {
			sentIDs = append(sentIDs, part.Ref)
		}
	}

	return sentIDs, nil
}

// encodeFileToDataURL reads a local file and returns a base64 data URL.
func encodeFileToDataURL(localPath, mime string) (string, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", err
	}
	prefix := "data:" + mime + ";base64,"
	return prefix + base64.StdEncoding.EncodeToString(data), nil
}

// mimeFromExt guesses a MIME type from a filename extension.
func mimeFromExt(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/png"
	}
}

func (c *OpenResponsesChannel) maxBodySize() int64 {
	sz := c.config.MaxBodySize
	if sz <= 0 {
		sz = 1024 * 1024 // 1 MB default
	}
	return sz
}

func (c *OpenResponsesChannel) endpointPath() string {
	p := strings.TrimSpace(c.config.EndpointPath)
	if p == "" {
		return "/v1/responses"
	}
	return p
}

// BeginStream implements channels.StreamingCapable.
// It returns a Streamer that pushes incremental deltas into the conversation's pendingStream.
func (c *OpenResponsesChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	c.convMu.Lock()
	st, found := c.convs[chatID]
	if found {
		st.hasStreamer.Store(true)
	}
	c.convMu.Unlock()

	if !found {
		return nil, fmt.Errorf("no active conversation for %s", chatID)
	}

	return &openResponsesStreamer{
		channel: c,
		convID:  chatID,
		stream:  st.stream,
	}, nil
}

// openResponsesStreamer bridges bus.Streamer to pendingStream.
type openResponsesStreamer struct {
	channel       *OpenResponsesChannel
	convID        string
	stream        *pendingStream
	lastContent   string
	lastReasoning string
}

func (s *openResponsesStreamer) Update(ctx context.Context, accumulated string) error {
	if len(accumulated) <= len(s.lastContent) {
		return nil
	}
	delta := accumulated[len(s.lastContent):]
	s.lastContent = accumulated

	logger.DebugCF("openresponses", "Streamer.Update push text delta", map[string]any{
		"conversation_id": s.convID,
		"delta_preview":   truncateString(delta, 200),
		"accumulated_len": len(accumulated),
	})
	// Push delta (not accumulated) so handler emits true incremental SSE
	s.stream.push(streamEvent{kind: eventKindTextDelta, content: delta})
	return nil
}

func (s *openResponsesStreamer) UpdateReasoning(ctx context.Context, accumulatedReasoning string) error {
	if len(accumulatedReasoning) <= len(s.lastReasoning) {
		return nil
	}
	delta := accumulatedReasoning[len(s.lastReasoning):]
	s.lastReasoning = accumulatedReasoning

	logger.DebugCF("openresponses", "Streamer.UpdateReasoning push reasoning delta", map[string]any{
		"conversation_id": s.convID,
		"delta_preview":   truncateString(delta, 200),
	})
	s.stream.push(streamEvent{kind: eventKindReasoning, content: delta})
	return nil
}

func (s *openResponsesStreamer) UpdateToolCall(ctx context.Context, callID, name, arguments string) error {
	s.stream.push(streamEvent{kind: eventKindFunctionCall, callID: callID, name: name, arguments: arguments})
	return nil
}

func (s *openResponsesStreamer) Finalize(ctx context.Context, content string) error {
	logger.DebugCF("openresponses", "Streamer.Finalize push turn_end", map[string]any{
		"conversation_id": s.convID,
		"final_content":   truncateString(content, 200),
	})
	// Signal turn end so handler can close the active output_item
	s.stream.push(streamEvent{kind: eventKindTurnEnd})
	return nil
}

func (s *openResponsesStreamer) Cancel(ctx context.Context) {
	s.stream.close()
}

// truncateString truncates s to maxLen characters, appending "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
