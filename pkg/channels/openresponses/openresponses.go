package openresponses

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// reqSep is the delimiter used to embed the requestID into ChatID.
// It is a null byte, which never appears in normal conversation IDs.
const reqSep = "\x00"

// OpenResponsesChannel implements a PicoClaw channel that exposes an HTTP API
// compatible with the OpenResponses specification.
type OpenResponsesChannel struct {
	*channels.BaseChannel
	bc     *config.Channel
	config *config.OpenResponsesSettings

	pendingMu sync.Mutex
	pending   map[string]*pendingStream // requestID -> pending

	ctx    context.Context
	cancel context.CancelFunc
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
		pending:     make(map[string]*pendingStream),
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

	// Close all pending streams so HTTP handlers unblock.
	c.pendingMu.Lock()
	for _, s := range c.pending {
		s.close()
	}
	clear(c.pending)
	c.pendingMu.Unlock()

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

	requestID, ok := extractRequestID(msg.ChatID)
	if !ok {
		return nil, nil
	}

	c.pendingMu.Lock()
	stream, found := c.pending[requestID]
	c.pendingMu.Unlock()

	if !found {
		return nil, nil
	}

	raw := msg.Context.Raw
	var ev streamEvent

	if raw["message_kind"] == "turn_end" {
		ev = streamEvent{kind: eventKindTurnEnd}
		stream.push(ev)
		stream.close()
		return nil, nil
	}

	if raw["message_kind"] == "thought" {
		ev = streamEvent{kind: eventKindReasoning, content: msg.Content}
	} else {
		ev = streamEvent{kind: eventKindText, content: msg.Content}
	}
	stream.push(ev)
	return nil, nil
}

// registerPending creates a new pendingStream for the given requestID.
// It starts a timeout goroutine that auto-closes the stream after the timeout.
func (c *OpenResponsesChannel) registerPending(requestID string, timeout time.Duration) *pendingStream {
	s := newPendingStream(64)

	c.pendingMu.Lock()
	c.pending[requestID] = s
	c.pendingMu.Unlock()

	go func() {
		select {
		case <-s.done:
			return
		case <-time.After(timeout):
			c.pendingMu.Lock()
			if _, still := c.pending[requestID]; still {
				delete(c.pending, requestID)
			}
			c.pendingMu.Unlock()
			s.close()
		}
	}()

	return s
}

// dispatch sends the user's input into the PicoClaw MessageBus and returns
// a pendingStream that the HTTP handler reads from.
func (c *OpenResponsesChannel) dispatch(
	ctx context.Context,
	conversationID string,
	content string,
) (*pendingStream, string, error) {
	requestID := "req_" + uuid.New().String()

	deliveryChatID := conversationID + reqSep + requestID

	sender := bus.SenderInfo{
		Platform:    "openresponses",
		PlatformID:  "user",
		CanonicalID: identity.BuildCanonicalID("openresponses", "user"),
	}

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    deliveryChatID,
		ChatType:  "direct",
		SenderID:  sender.CanonicalID,
		MessageID: requestID,
		Raw: map[string]string{
			"request_id":      requestID,
			"conversation_id": conversationID,
		},
	}

	timeout := c.requestTimeout()
	s := c.registerPending(requestID, timeout)

	c.HandleInboundContext(ctx, deliveryChatID, content, nil, inboundCtx, sender)
	return s, requestID, nil
}

func (c *OpenResponsesChannel) requestTimeout() time.Duration {
	t := c.config.RequestTimeout
	if t <= 0 {
		t = 60
	}
	return time.Duration(t) * time.Second
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

// extractRequestID extracts the requestID from a ChatID that was encoded by
// dispatch. Returns ("", false) if the ChatID does not contain the marker.
func extractRequestID(chatID string) (string, bool) {
	idx := strings.LastIndex(chatID, reqSep)
	if idx < 0 {
		return "", false
	}
	return chatID[idx+len(reqSep):], true
}

// stripRequestID returns the conversationID portion of an encoded ChatID.
func stripRequestID(chatID string) string {
	idx := strings.LastIndex(chatID, reqSep)
	if idx < 0 {
		return chatID
	}
	return chatID[:idx]
}
