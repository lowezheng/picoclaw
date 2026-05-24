package openresponses

import (
	"context"
	"testing"
)

// TestStreamerLifecycle_MultiRoundLLM verifies that a second BeginStream
// works correctly after the first streamer's Cancel (simulating tool-call
// path where the first LLM returns tool_calls and a follow-up LLM produces
// the final answer).
func TestStreamerLifecycle_MultiRoundLLM(t *testing.T) {
	c := &OpenResponsesChannel{
		convs:           make(map[string]*conversationState),
		sessionRegistry: make(map[string]*sessionRegistryEntry),
	}
	chatID := "test-chat-1"

	// Round 1: dispatch creates the conversation and stream
	s1 := newPendingStream()
	st := &conversationState{
		stream: s1,
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	c.convs[chatID] = st

	// Round 1 LLM begins streaming
	streamer1, err := c.BeginStream(context.Background(), chatID)
	if err != nil {
		t.Fatalf("round 1 BeginStream: %v", err)
	}

	// Round 1 LLM produces reasoning
	if rs, ok := streamer1.(interface{ UpdateReasoning(context.Context, string) error }); ok {
		_ = rs.UpdateReasoning(context.Background(), "thinking about tools")
	}

	// Round 1 LLM returns tool_calls -> pipeline calls Cancel
	streamer1.Cancel(context.Background())

	// At this point the conversation should still exist
	c.convMu.RLock()
	_, exists := c.convs[chatID]
	c.convMu.RUnlock()
	if !exists {
		t.Fatal("conversation deleted after Cancel")
	}

	// Round 2 LLM begins streaming (new streamer, same pendingStream)
	streamer2, err := c.BeginStream(context.Background(), chatID)
	if err != nil {
		t.Fatalf("round 2 BeginStream: %v", err)
	}

	// Round 2 LLM produces final answer text
	_ = streamer2.Update(context.Background(), "final answer part 1")
	_ = streamer2.Update(context.Background(), "final answer part 1 and part 2")

	// Round 2 LLM done -> Finalize
	_ = streamer2.Finalize(context.Background(), "final answer part 1 and part 2")

	// Build response and verify
	resp := c.buildResponse(s1, chatID, CreateResponseRequest{})

	if resp.Status != "completed" {
		t.Errorf("expected status completed, got %q", resp.Status)
	}
	if len(resp.Output) == 0 {
		t.Fatal("expected at least 1 output item")
	}

	var hasMessage bool
	for _, item := range resp.Output {
		if item.Type == "message" {
			hasMessage = true
			if len(item.Content) == 0 || item.Content[0].Text == "" {
				t.Error("message item has empty content")
			}
		}
	}
	if !hasMessage {
		t.Fatalf("expected message item in output, got: %+v", resp.Output)
	}
}
