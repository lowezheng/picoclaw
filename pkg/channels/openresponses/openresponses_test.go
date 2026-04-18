package openresponses

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNewOpenResponsesChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{}

	ch, err := NewOpenResponsesChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	if ch.Name() != "openresponses" {
		t.Errorf("expected name openresponses, got %s", ch.Name())
	}
}

func TestStartStop(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	ctx := context.Background()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !ch.IsRunning() {
		t.Error("expected channel to be running")
	}

	if err := ch.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if ch.IsRunning() {
		t.Error("expected channel to be stopped")
	}
}

func TestSendNotRunning(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		Channel: "openresponses",
		ChatID:  "conv_123\x00req_abc",
		Content: "hello",
	})
	if err != channels.ErrNotRunning {
		t.Errorf("expected ErrNotRunning, got %v", err)
	}
}

func TestSendNoRequestID(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	_ = ch.Start(context.Background())
	defer ch.Stop(context.Background())

	// ChatID without request separator should be silently ignored.
	ids, err := ch.Send(context.Background(), bus.OutboundMessage{
		Channel: "openresponses",
		ChatID:  "conv_123",
		Content: "hello",
	})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if ids != nil {
		t.Errorf("expected nil ids, got %v", ids)
	}
}

func TestDispatchAndSend(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{
		RequestTimeout: 5,
	}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	_ = ch.Start(context.Background())
	defer ch.Stop(context.Background())

	p, _, err := ch.dispatch(context.Background(), "conv_123", "Hello agent")
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	// Simulate agent replying via the bus.
	go func() {
		// Small delay to mimic async processing.
		time.Sleep(50 * time.Millisecond)
		ch.Send(context.Background(), bus.OutboundMessage{
			Channel: "openresponses",
			ChatID:  "conv_123\x00" + extractReqIDFromPending(ch, "conv_123"),
			Content: "Hello user",
		})
	}()

	select {
	case <-p.done:
		if p.content != "Hello user" {
			t.Errorf("expected content 'Hello user', got %q", p.content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestPendingTimeout(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{
		RequestTimeout: 1,
	}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	_ = ch.Start(context.Background())
	defer ch.Stop(context.Background())

	p, _, err := ch.dispatch(context.Background(), "conv_timeout", "test")
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	select {
	case <-p.done:
		if p.content != "" {
			t.Errorf("expected empty content on timeout, got %q", p.content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for pending timeout")
	}
}

func TestExtractRequestID(t *testing.T) {
	tests := []struct {
		chatID   string
		wantID   string
		wantOK   bool
	}{
		{"conv_123\x00req_abc", "req_abc", true},
		{"conv_123", "", false},
		{"", "", false},
		{"conv_123\x00req_abc\x00extra", "extra", true}, // last occurrence wins
	}

	for _, tc := range tests {
		id, ok := extractRequestID(tc.chatID)
		if ok != tc.wantOK {
			t.Errorf("extractRequestID(%q) ok=%v, want %v", tc.chatID, ok, tc.wantOK)
		}
		if ok && id != tc.wantID {
			t.Errorf("extractRequestID(%q) id=%q, want %q", tc.chatID, id, tc.wantID)
		}
	}
}

func TestStripRequestID(t *testing.T) {
	if got := stripRequestID("conv_123\x00req_abc"); got != "conv_123" {
		t.Errorf("stripRequestID: got %q, want conv_123", got)
	}
	if got := stripRequestID("conv_123"); got != "conv_123" {
		t.Errorf("stripRequestID: got %q, want conv_123", got)
	}
}

func TestNormalizeInput(t *testing.T) {
	if got := normalizeInput("hello"); got != "hello" {
		t.Errorf("normalizeInput(string): got %q, want hello", got)
	}
	if got := normalizeInput(nil); got != "" {
		t.Errorf("normalizeInput(nil): got %q, want empty", got)
	}

	items := []InputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "user", Content: "world"},
	}
	if got := normalizeInput(items); got != "hello\nworld" {
		t.Errorf("normalizeInput(items): got %q, want 'hello\\nworld'", got)
	}

	// Simulate what json.Unmarshal produces for a JSON array into an `any` field.
	jsonAny := []any{
		map[string]any{"type": "message", "role": "user", "content": "from json"},
		map[string]any{"type": "message", "role": "assistant", "content": "ignored"},
	}
	if got := normalizeInput(jsonAny); got != "from json" {
		t.Errorf("normalizeInput([]any): got %q, want 'from json'", got)
	}
}

// extractReqIDFromPending is a test helper that finds the requestID for a
// given conversationID by inspecting the pending map.
func extractReqIDFromPending(ch *OpenResponsesChannel, convID string) string {
	ch.pendingMu.Lock()
	defer ch.pendingMu.Unlock()
	for reqID := range ch.pending {
		return reqID
	}
	return ""
}
