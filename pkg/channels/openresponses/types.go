package openresponses

import (
	"strings"
	"sync"
	"time"
)

// --- Request Types ---

// CreateResponseRequest mirrors the OpenResponses POST /v1/responses request body.
type CreateResponseRequest struct {
	Model             string        `json:"model,omitempty"`
	Input             any           `json:"input"` // string or []InputItem
	Instructions      string        `json:"instructions,omitempty"`
	PreviousResponseID string       `json:"previous_response_id,omitempty"`
	ConversationID    string        `json:"conversation_id,omitempty"`
	Stream            bool          `json:"stream,omitempty"`
	Tools             []Tool        `json:"tools,omitempty"`
	ToolChoice        any           `json:"tool_choice,omitempty"`
	Temperature       *float64      `json:"temperature,omitempty"`
	MaxOutputTokens   int           `json:"max_output_tokens,omitempty"`
	Truncation        string        `json:"truncation,omitempty"`
}

// InputItem represents a single turn in the conversation history.
type InputItem struct {
	Type    string `json:"type"` // "message"
	Role    string `json:"role"` // "user" | "assistant" | "system"
	Content any    `json:"content"` // string or []ContentPart
}

// ContentPart represents a multimodal content part.
type ContentPart struct {
	Type     string `json:"type"` // "input_text" | "input_image" | "input_audio"
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	AudioURL string `json:"audio_url,omitempty"`
}

// Tool represents a tool definition.
type Tool struct {
	Type     string          `json:"type"` // "function"
	Function FunctionTool    `json:"function,omitempty"`
}

// FunctionTool describes a callable function.
type FunctionTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// --- Response Types ---

// Response is the top-level response object for a completed request.
type Response struct {
	ID             string         `json:"id"`
	Object         string         `json:"object"`
	CreatedAt      int64          `json:"created_at"`
	Status         string         `json:"status"` // "completed" | "in_progress" | "incomplete"
	Model          string         `json:"model,omitempty"`
	Output         []ResponseItem `json:"output"`
	ConversationID string         `json:"conversation_id,omitempty"`
	PreviousResponseID string    `json:"previous_response_id,omitempty"`
	Usage              Usage     `json:"usage"`
}

// Usage represents token consumption for a response.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ResponseItem is a polymorphic output item.
type ResponseItem struct {
	Type       string      `json:"type"` // "message" | "function_call" | "reasoning"
	ID         string      `json:"id"`
	Status     string      `json:"status"` // "completed" | "in_progress" | "incomplete"
	Role       string      `json:"role,omitempty"` // "assistant"
	Content    []Content   `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	CallID     string      `json:"call_id,omitempty"`
	Arguments  string      `json:"arguments,omitempty"`
}

// Content is a polymorphic content block inside a message or reasoning item.
type Content struct {
	Type      string `json:"type"` // "output_text" | "output_image" | "reasoning_text" | "function_call_arguments"
	Text      string `json:"text,omitempty"`
	ImageURL  string `json:"image_url,omitempty"`  // NEW
	Arguments string `json:"arguments,omitempty"`  // NEW: for function_call_arguments
}

// --- SSE Event Types ---

// ResponseEvent is a server-sent event in the OpenResponses stream.
type ResponseEvent struct {
	Type           string        `json:"type"`
	SequenceNumber int           `json:"sequence_number"`
	Response       Response      `json:"response,omitempty"`
	Item           ResponseItem  `json:"item,omitempty"`
	ItemID         string        `json:"item_id,omitempty"`
	OutputIndex    int           `json:"output_index"`
	ContentIndex   int           `json:"content_index"`
	Delta          string        `json:"delta,omitempty"`
	Part           Content       `json:"part,omitempty"`
}

// --- Internal Helpers ---

func nowUnix() int64 {
	return time.Now().Unix()
}

// normalizeInput extracts a plain-text string from the OpenResponses "input" field.
// It accepts either a single string or an array of InputItem and concatenates user messages.
func normalizeInput(input any) string {
	if input == nil {
		return ""
	}
	if s, ok := input.(string); ok {
		return s
	}

	// Helper to extract user text from an InputItem.
	extract := func(it InputItem) string {
		if it.Type != "message" || it.Role != "user" {
			return ""
		}
		if text, ok := it.Content.(string); ok {
			return text
		}
		return ""
	}

	var parts []string

	switch v := input.(type) {
	case []InputItem:
		for _, it := range v {
			if text := extract(it); text != "" {
				parts = append(parts, text)
			}
		}
	case []any:
		for _, elem := range v {
			var it InputItem
			switch m := elem.(type) {
			case map[string]any:
				if t, ok := m["type"].(string); ok {
					it.Type = t
				}
				if r, ok := m["role"].(string); ok {
					it.Role = r
				}
				it.Content = m["content"]
			case InputItem:
				it = m
			}
			if text := extract(it); text != "" {
				parts = append(parts, text)
			}
		}
	}

	return strings.Join(parts, "\n")
}

// --- Streaming Types (Path A) ---

// streamEventKind categorizes events in the pending stream.
type streamEventKind string

const (
	eventKindText         streamEventKind = "text"
	eventKindTextDelta    streamEventKind = "text_delta"    // incremental text token
	eventKindReasoning    streamEventKind = "reasoning"
	eventKindImage        streamEventKind = "image"        // NEW
	eventKindFunctionCall streamEventKind = "function_call" // NEW
	eventKindTurnEnd      streamEventKind = "turn_end"
)

// streamEvent represents one piece of agent output in the stream.
type streamEvent struct {
	kind      streamEventKind
	content   string
	imageURL  string // NEW: base64 data URL for output_image
	caption   string // NEW: optional caption text
	callID    string // NEW: for function_call
	name      string // NEW: for function_call
	arguments string // NEW: for function_call
}

// pendingStream holds a queue of agent messages for a single HTTP request.
// The HTTP handler reads from events; Send() pushes into it.
// Once closed, no more events are accepted.
type pendingStream struct {
	events chan streamEvent
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newPendingStream(bufSize int) *pendingStream {
	return &pendingStream{
		events: make(chan streamEvent, bufSize),
		done:   make(chan struct{}),
	}
}

// push adds an event to the stream. Returns false if the stream is closed or full.
func (s *pendingStream) push(ev streamEvent) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

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

// close marks the stream as done. Safe to call multiple times.
func (s *pendingStream) close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.events)
		close(s.done)
		s.mu.Unlock()
	})
}
