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

// pendingResponse holds the state for a single in-flight HTTP request.
type pendingResponse struct {
	done    chan struct{}
	content string
	once    sync.Once
}

func newPendingResponse() *pendingResponse {
	return &pendingResponse{
		done: make(chan struct{}),
	}
}

func (p *pendingResponse) complete(content string) {
	p.once.Do(func() {
		p.content = content
		close(p.done)
	})
}

// OpenResponsesChannel implements a PicoClaw channel that exposes an HTTP API
// compatible with the OpenResponses specification.
type OpenResponsesChannel struct {
	*channels.BaseChannel
	bc     *config.Channel
	config *config.OpenResponsesSettings

	pendingMu sync.Mutex
	pending   map[string]*pendingResponse // requestID -> pending

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
		pending:     make(map[string]*pendingResponse),
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

	// Complete all pending requests with an empty response so HTTP handlers unblock.
	c.pendingMu.Lock()
	for _, p := range c.pending {
		p.complete("")
	}
	clear(c.pending)
	c.pendingMu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}

	logger.InfoC("openresponses", "OpenResponses channel stopped")
	return nil
}

// Send implements channels.Channel. It matches outbound messages to pending
// HTTP requests using the requestID embedded in ChatID.
func (c *OpenResponsesChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	requestID, ok := extractRequestID(msg.ChatID)
	if !ok {
		// No requestID embedded — this response is not associated with an
		// active HTTP request (e.g. a cron message or heartbeat). Ignore it.
		return nil, nil
	}

	c.pendingMu.Lock()
	p, found := c.pending[requestID]
	if found {
		delete(c.pending, requestID)
	}
	c.pendingMu.Unlock()

	if !found {
		// Request already timed out or was cancelled.
		return nil, nil
	}

	p.complete(msg.Content)
	return nil, nil
}

// registerPending creates a new pendingResponse for the given requestID.
// It also starts a timeout goroutine that auto-cleans the entry.
func (c *OpenResponsesChannel) registerPending(requestID string, timeout time.Duration) *pendingResponse {
	p := newPendingResponse()

	c.pendingMu.Lock()
	c.pending[requestID] = p
	c.pendingMu.Unlock()

	go func() {
		select {
		case <-p.done:
			return
		case <-time.After(timeout):
			c.pendingMu.Lock()
			if _, still := c.pending[requestID]; still {
				delete(c.pending, requestID)
			}
			c.pendingMu.Unlock()
			p.complete("")
		}
	}()

	return p
}

// dispatch sends the user's input into the PicoClaw MessageBus and returns
// a pendingResponse that will be signalled when the agent reply arrives.
func (c *OpenResponsesChannel) dispatch(
	ctx context.Context,
	conversationID string,
	content string,
) (*pendingResponse, string, error) {
	requestID := "req_" + uuid.New().String()

	// Embed the requestID into ChatID so Send() can correlate the reply.
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
	p := c.registerPending(requestID, timeout)

	c.HandleInboundContext(ctx, deliveryChatID, content, nil, inboundCtx, sender)
	return p, requestID, nil
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
