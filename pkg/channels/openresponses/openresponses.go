package openresponses

import (
	"context"
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
	stream *pendingStream
	done   chan struct{}
	active atomic.Bool
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
		return nil, nil
	}

	raw := msg.Context.Raw
	var ev streamEvent

	if raw["message_kind"] == "turn_end" {
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
	} else {
		ev = streamEvent{kind: eventKindText, content: msg.Content}
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

		c.HandleInboundContext(ctx, conversationID, content, nil, inboundCtx, sender)
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

	c.HandleInboundContext(ctx, conversationID, content, nil, inboundCtx, sender)
	return s, false, nil
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
