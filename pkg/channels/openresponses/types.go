package openresponses

import (
	"sync"
	"sync/atomic"
)

// -- Request types --

type CreateResponseRequest struct {
	Model              string        `json:"model,omitempty"`
	Input              any           `json:"input"`
	Content            []ContentPart `json:"content,omitempty"`
	Instructions       string        `json:"instructions,omitempty"`
	PreviousResponseID string        `json:"previous_response_id,omitempty"`
	ConversationID     string        `json:"conversation_id,omitempty"`
	Stream             bool          `json:"stream,omitempty"`
	Tools              []Tool        `json:"tools,omitempty"`
	ToolChoice         any           `json:"tool_choice,omitempty"`
	Temperature        *float64      `json:"temperature,omitempty"`
	MaxOutputTokens    int           `json:"max_output_tokens,omitempty"`
	Truncation         string        `json:"truncation,omitempty"`
}

type ContentPart struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// -- Response types --

type Response struct {
	ID                 string         `json:"id"`
	Object             string         `json:"object"`
	CreatedAt          int64          `json:"created_at"`
	Status             string         `json:"status"`
	Model              string         `json:"model,omitempty"`
	Output             []ResponseItem `json:"output"`
	ConversationID     string         `json:"conversation_id,omitempty"`
	PreviousResponseID string         `json:"previous_response_id,omitempty"`
	Usage              Usage          `json:"usage"`
}

type ResponseItem struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Status  string          `json:"status"`
	Role    string          `json:"role,omitempty"`
	Content []ContentOutput `json:"content"`
}

type ContentOutput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// -- Error response --

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// -- SSE event types --

type streamEventKind int

const (
	eventKindTextDelta streamEventKind = iota
	eventKindText
	eventKindReasoning
	eventKindImage
	eventKindFunctionCall
	eventKindTurnEnd
)

type streamEvent struct {
	kind      streamEventKind
	content   string
	imageURL  string
	caption   string
	callID    string
	name      string
	arguments string
}

// -- pendingStream --

const bufSize = 64

type pendingStream struct {
	events chan streamEvent
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newPendingStream() *pendingStream {
	return &pendingStream{
		events: make(chan streamEvent, bufSize),
		done:   make(chan struct{}),
	}
}

func (s *pendingStream) push(ev streamEvent) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	select {
	case s.events <- ev:
		return true
	default:
		return false
	}
}

func (s *pendingStream) close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.events)
		close(s.done)
	})
}

// -- conversationState --

type conversationState struct {
	stream      *pendingStream
	done        chan struct{}
	active      atomic.Bool
	hasStreamer atomic.Bool
}

// -- Session types --

type sessionListItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
	Created      string `json:"created"`
	Updated      string `json:"updated"`
}
