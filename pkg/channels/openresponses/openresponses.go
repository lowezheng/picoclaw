package openresponses

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func truncatePreview(s string) string {
	const maxLen = 100
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

type OpenResponsesChannel struct {
	*channels.BaseChannel
	bc              *config.Channel
	cfg             *config.OpenResponsesSettings
	convMu          sync.RWMutex
	convs           map[string]*conversationState
	ctx             context.Context
	cancel          context.CancelFunc
	sessionRegMu    sync.RWMutex
	sessionRegistry map[string]*sessionRegistryEntry
}

func NewOpenResponsesChannel(bc *config.Channel, cfg *config.OpenResponsesSettings, b *bus.MessageBus) (*OpenResponsesChannel, error) {
	base := channels.NewBaseChannel(
		bc.Name(),
		cfg,
		b,
		bc.AllowFrom,
		channels.WithMaxMessageLength(0),
	)
	c := &OpenResponsesChannel{
		BaseChannel:     base,
		bc:              bc,
		cfg:             cfg,
		convs:           make(map[string]*conversationState),
		sessionRegistry: make(map[string]*sessionRegistryEntry),
	}
	base.SetOwner(c)
	return c, nil
}

func (c *OpenResponsesChannel) Start(ctx context.Context) error {
	logger.InfoC("openresponses", "Starting OpenResponses channel")
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)
	logger.InfoC("openresponses", "OpenResponses channel started")
	return nil
}

func (c *OpenResponsesChannel) Stop(ctx context.Context) error {
	logger.InfoC("openresponses", "Stopping OpenResponses channel")
	c.SetRunning(false)
	c.convMu.Lock()
	for _, st := range c.convs {
		st.stream.close()
	}
	clear(c.convs)
	c.convMu.Unlock()
	c.sessionRegMu.Lock()
	clear(c.sessionRegistry)
	c.sessionRegMu.Unlock()
	c.cancel()
	logger.InfoC("openresponses", "OpenResponses channel stopped")
	return nil
}

func (c *OpenResponsesChannel) dispatch(ctx context.Context, conversationID, content string, media []string) (*pendingStream, bool, error) {
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
		Raw:       map[string]string{"conversation_id": conversationID},
	}

	// Update session registry (user message)
	c.sessionRegMu.Lock()
	if entry, ok := c.sessionRegistry[conversationID]; ok {
		entry.msgCount++
		entry.updatedAt = time.Now()
		if content != "" {
			entry.preview = truncatePreview(content)
		}
	} else {
		now := time.Now()
		c.sessionRegistry[conversationID] = &sessionRegistryEntry{
			id:        conversationID,
			createdAt: now,
			updatedAt: now,
			msgCount:  1,
			preview:   truncatePreview(content),
		}
	}
	c.sessionRegMu.Unlock()

	c.convMu.Lock()
	defer c.convMu.Unlock()

	if st, ok := c.convs[conversationID]; ok && st.active.Load() {
		c.HandleInboundContext(ctx, conversationID, content, media, inboundCtx, sender)
		return nil, true, nil
	}

	s := newPendingStream()
	st := &conversationState{
		stream: s,
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	c.convs[conversationID] = st

	c.HandleInboundContext(ctx, conversationID, content, media, inboundCtx, sender)
	return s, false, nil
}

func (c *OpenResponsesChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	if msg.ChatID == "" {
		return nil, nil
	}

	c.convMu.RLock()
	st, ok := c.convs[msg.ChatID]
	c.convMu.RUnlock()
	if !ok {
		logger.WarnCF("openresponses", "No active conversation for chatID", map[string]any{
			"chat_id": msg.ChatID,
		})
		return nil, nil
	}

	kind := ""
	if msg.Context.Raw != nil {
		kind = msg.Context.Raw["message_kind"]
	}

	if st.hasStreamer.Load() {
		allowed := map[string]bool{
			"function_call": true,
			"turn_end":      true,
			"tool_timing":   true,
			"llm_timing":    true,
			"error":         true,
		}
		if !allowed[kind] && kind != "" {
			return nil, nil
		}
	}

	switch kind {
	case "turn_end":
		st.stream.push(streamEvent{kind: eventKindTurnEnd})
		c.convMu.Lock()
		delete(c.convs, msg.ChatID)
		c.convMu.Unlock()
		c.sessionRegMu.Lock()
		if entry, ok := c.sessionRegistry[msg.ChatID]; ok {
			entry.msgCount++
			entry.updatedAt = time.Now()
			if msg.Content != "" {
				entry.preview = truncatePreview(msg.Content)
			}
		}
		c.sessionRegMu.Unlock()
		close(st.done)
		st.stream.close()
		return nil, nil

	case "thought":
		st.stream.push(streamEvent{kind: eventKindReasoning, content: msg.Content})
		return nil, nil

	case "function_call":
		callID := ""
		name := ""
		arguments := ""
		if msg.Context.Raw != nil {
			callID = msg.Context.Raw["call_id"]
			name = msg.Context.Raw["name"]
			arguments = msg.Context.Raw["arguments"]
		}
		st.stream.push(streamEvent{
			kind:      eventKindFunctionCall,
			callID:    callID,
			name:      name,
			arguments: arguments,
		})
		return nil, nil

	default:
		// Non-streaming: outbound_kind="final" triggers turn end
		if msg.Context.Raw != nil && msg.Context.Raw["outbound_kind"] == "final" {
			st.stream.push(streamEvent{kind: eventKindText, content: msg.Content})
			st.stream.push(streamEvent{kind: eventKindTurnEnd})
			c.convMu.Lock()
			delete(c.convs, msg.ChatID)
			c.convMu.Unlock()
			c.sessionRegMu.Lock()
			if entry, ok := c.sessionRegistry[msg.ChatID]; ok {
				entry.msgCount++
				entry.updatedAt = time.Now()
				if msg.Content != "" {
					entry.preview = truncatePreview(msg.Content)
				}
			}
			c.sessionRegMu.Unlock()
			close(st.done)
			st.stream.close()
			return nil, nil
		}
		st.stream.push(streamEvent{kind: eventKindText, content: msg.Content})
		c.sessionRegMu.Lock()
		if entry, ok := c.sessionRegistry[msg.ChatID]; ok {
			entry.msgCount++
			entry.updatedAt = time.Now()
			if msg.Content != "" {
				entry.preview = truncatePreview(msg.Content)
			}
		}
		c.sessionRegMu.Unlock()
		return nil, nil
	}
}

func (c *OpenResponsesChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	if msg.ChatID == "" {
		return nil, nil
	}

	c.convMu.RLock()
	st, ok := c.convs[msg.ChatID]
	c.convMu.RUnlock()
	if !ok {
		return nil, nil
	}

	store := c.GetMediaStore()
	if store == nil {
		logger.WarnC("openresponses", "No media store configured")
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
			continue
		}
		if st.stream.push(streamEvent{kind: eventKindImage, imageURL: dataURL, caption: part.Caption}) {
			sentIDs = append(sentIDs, part.Ref)
		}
	}
	return sentIDs, nil
}

func (c *OpenResponsesChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	c.convMu.RLock()
	st, ok := c.convs[chatID]
	c.convMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no active conversation for %s", chatID)
	}
	st.hasStreamer.Store(true)
	return &openResponsesStreamer{
		channel: c,
		convID:  chatID,
		stream:  st.stream,
	}, nil
}

type openResponsesStreamer struct {
	channel       *OpenResponsesChannel
	convID        string
	stream        *pendingStream
	lastContent   string
	lastReasoning string
}

func (s *openResponsesStreamer) Update(ctx context.Context, content string) error {
	if len(content) <= len(s.lastContent) {
		return nil
	}
	delta := content[len(s.lastContent):]
	s.lastContent = content
	s.stream.push(streamEvent{kind: eventKindTextDelta, content: delta})
	return nil
}

func (s *openResponsesStreamer) UpdateReasoning(ctx context.Context, content string) error {
	if len(content) <= len(s.lastReasoning) {
		return nil
	}
	delta := content[len(s.lastReasoning):]
	s.lastReasoning = content
	s.stream.push(streamEvent{kind: eventKindReasoning, content: delta})
	return nil
}

func (s *openResponsesStreamer) Finalize(ctx context.Context, content string) error {
	// If no text was streamed via Update, push the final content as a text event
	// so buildResponse can capture it before turn_end.
	if s.lastContent == "" && strings.TrimSpace(content) != "" {
		s.stream.push(streamEvent{kind: eventKindText, content: content})
	}
	s.stream.push(streamEvent{kind: eventKindTurnEnd})
	if s.channel != nil {
		s.channel.convMu.Lock()
		delete(s.channel.convs, s.convID)
		s.channel.convMu.Unlock()
		s.channel.sessionRegMu.Lock()
		if entry, ok := s.channel.sessionRegistry[s.convID]; ok {
			entry.msgCount++
			entry.updatedAt = time.Now()
			if content != "" {
				entry.preview = truncatePreview(content)
			}
		}
		s.channel.sessionRegMu.Unlock()
	}
	s.stream.close()
	return nil
}

func (s *openResponsesStreamer) Cancel(ctx context.Context) {
	// Never close the stream on Cancel. The pipeline calls Cancel when:
	// - LLM returns tool_calls (turn continues, follow-up LLM will reuse stream)
	// - Steering arrives after direct response (turn continues)
	// - Error paths (Finalize will still be called to close properly)
	// Only Finalize should close the stream when the turn is truly complete.
}
