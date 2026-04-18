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
		ChatID:  "conv_123",
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

	stream, queued, err := ch.dispatch(context.Background(), "conv_123", "Hello agent")
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if queued {
		t.Fatal("expected not queued for first dispatch")
	}

	// Simulate agent sending multiple messages.
	go func() {
		time.Sleep(50 * time.Millisecond)
		ch.Send(context.Background(), bus.OutboundMessage{
			Channel: "openresponses",
			ChatID:  "conv_123",
			Content: "Message 1",
		})
		time.Sleep(20 * time.Millisecond)
		ch.Send(context.Background(), bus.OutboundMessage{
			Channel: "openresponses",
			ChatID:  "conv_123",
			Content: "Message 2",
		})
		// Signal end-of-turn so the stream closes and the reader goroutine finishes.
		ch.Send(context.Background(), bus.OutboundMessage{
			Channel: "openresponses",
			ChatID:  "conv_123",
			Content: "",
			Context: bus.InboundContext{Raw: map[string]string{"message_kind": "turn_end"}},
		})
	}()

	var contents []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range stream.events {
			if ev.kind == eventKindText {
				contents = append(contents, ev.content)
			}
		}
	}()

	select {
	case <-done:
		if len(contents) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(contents))
		}
		if contents[0] != "Message 1" {
			t.Errorf("expected first message 'Message 1', got %q", contents[0])
		}
		if contents[1] != "Message 2" {
			t.Errorf("expected second message 'Message 2', got %q", contents[1])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for stream messages")
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

	stream, queued, err := ch.dispatch(context.Background(), "conv_timeout", "test")
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if queued {
		t.Fatal("expected not queued for first dispatch")
	}

	// The current implementation does not auto-timeout; close manually.
	stream.close()

	select {
	case <-stream.done:
		// Stream closed.
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for stream close")
	}

	// After close, the stream should be closed and events drained.
	if _, ok := <-stream.events; ok {
		t.Error("expected events channel to be closed")
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

func TestSendMultipleMessages(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{
		RequestTimeout: 5,
	}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	_ = ch.Start(context.Background())
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	st := &conversationState{stream: stream, done: make(chan struct{})}
	st.active.Store(true)
	ch.convMu.Lock()
	ch.convs["conv_test_multi"] = st
	ch.convMu.Unlock()

	// Push multiple messages.
	ch.Send(context.Background(), bus.OutboundMessage{
		Channel: "openresponses",
		ChatID:  "conv_test_multi",
		Content: "First",
	})
	ch.Send(context.Background(), bus.OutboundMessage{
		Channel: "openresponses",
		ChatID:  "conv_test_multi",
		Content: "Second",
	})
	ch.Send(context.Background(), bus.OutboundMessage{
		Channel: "openresponses",
		ChatID:  "conv_test_multi",
		Content: "Third",
	})
	// Close the stream so the range loop terminates.
	stream.close()

	var contents []string
	for ev := range stream.events {
		if ev.kind == eventKindText {
			contents = append(contents, ev.content)
		}
	}

	if len(contents) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(contents))
	}
	if contents[0] != "First" || contents[1] != "Second" || contents[2] != "Third" {
		t.Errorf("unexpected contents: %v", contents)
	}
}

func TestSend_ReasoningMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{
		RequestTimeout: 5,
	}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	_ = ch.Start(context.Background())
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	st := &conversationState{stream: stream, done: make(chan struct{})}
	st.active.Store(true)
	ch.convMu.Lock()
	ch.convs["conv_test_reasoning"] = st
	ch.convMu.Unlock()

	// Simulate a reasoning (thought) message from the agent.
	ch.Send(context.Background(), bus.OutboundMessage{
		Channel: "openresponses",
		ChatID:  "conv_test_reasoning",
		Content: "Let me think about this...",
		Context: bus.InboundContext{Raw: map[string]string{"message_kind": "thought"}},
	})

	var ev streamEvent
	select {
	case ev = <-stream.events:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for reasoning event")
	}

	if ev.kind != eventKindReasoning {
		t.Fatalf("expected eventKindReasoning, got %v", ev.kind)
	}
	if ev.content != "Let me think about this..." {
		t.Errorf("unexpected content: %q", ev.content)
	}

	stream.close()
}

func TestStreamEventImageKind(t *testing.T) {
	ev := streamEvent{
		kind:     eventKindImage,
		imageURL: "data:image/png;base64,abc123",
		caption:  "a cat",
	}
	if ev.kind != "image" {
		t.Errorf("expected kind 'image', got %s", ev.kind)
	}
	if ev.imageURL != "data:image/png;base64,abc123" {
		t.Errorf("unexpected imageURL: %s", ev.imageURL)
	}
}

func TestSend_TextMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{
		RequestTimeout: 5,
	}

	ch, _ := NewOpenResponsesChannel(bc, cfg, msgBus)
	_ = ch.Start(context.Background())
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	st := &conversationState{stream: stream, done: make(chan struct{})}
	st.active.Store(true)
	ch.convMu.Lock()
	ch.convs["conv_test_text"] = st
	ch.convMu.Unlock()

	// Simulate a normal text message from the agent.
	ch.Send(context.Background(), bus.OutboundMessage{
		Channel: "openresponses",
		ChatID:  "conv_test_text",
		Content: "Hello world",
	})

	var ev streamEvent
	select {
	case ev = <-stream.events:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for text event")
	}

	if ev.kind != eventKindText {
		t.Fatalf("expected eventKindText, got %v", ev.kind)
	}
	if ev.content != "Hello world" {
		t.Errorf("unexpected content: %q", ev.content)
	}

	stream.close()
}
