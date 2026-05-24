package openresponses

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

// -- pendingStream tests --

func TestPendingStream_PushAndClose(t *testing.T) {
	s := newPendingStream()
	if !s.push(streamEvent{kind: eventKindText, content: "hello"}) {
		t.Fatal("expected push to succeed")
	}
	s.close()
	if s.push(streamEvent{kind: eventKindText, content: "world"}) {
		t.Fatal("expected push to fail after close")
	}
}

func TestPendingStream_Unbounded(t *testing.T) {
	s := newPendingStream()
	// Push many more events than the old fixed buffer size (64)
	for i := 0; i < 5000; i++ {
		if !s.push(streamEvent{kind: eventKindText, content: "x"}) {
			t.Fatalf("expected push %d to succeed", i)
		}
	}
	// Verify all events can be drained
	count := 0
	for {
		ev, ok := s.tryNext()
		if !ok {
			break
		}
		if ev.content != "x" {
			t.Fatalf("unexpected event content: %q", ev.content)
		}
		count++
	}
	if count != 5000 {
		t.Fatalf("expected 5000 events, got %d", count)
	}
}

func TestPendingStream_DoneChannel(t *testing.T) {
	s := newPendingStream()
	s.close()
	select {
	case <-s.done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("done channel should be closed")
	}
}

// -- extractRequestContent tests --

func TestExtractRequestContent_Nil(t *testing.T) {
	content, media, err := extractRequestContent(nil)
	if err != nil {
		t.Fatal(err)
	}
	if content != "" || len(media) != 0 {
		t.Fatalf("expected empty, got %q %v", content, media)
	}
}

func TestExtractRequestContent_String(t *testing.T) {
	content, media, err := extractRequestContent("  hello world  ")
	if err != nil {
		t.Fatal(err)
	}
	if content != "hello world" || len(media) != 0 {
		t.Fatalf("expected 'hello world', got %q %v", content, media)
	}
}

func TestExtractRequestContent_EmptyString(t *testing.T) {
	content, media, err := extractRequestContent("   ")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" || len(media) != 0 {
		t.Fatalf("expected empty, got %q %v", content, media)
	}
}

func TestExtractRequestContent_ContentParts(t *testing.T) {
	input := []ContentPart{
		{Type: "input_text", Content: "Hello"},
		{Type: "input_text", Content: "World"},
		{Type: "input_image", Content: "data:image/png;base64,abc"},
	}
	content, media, err := extractRequestContent(input)
	if err != nil {
		t.Fatal(err)
	}
	if content != "Hello\nWorld" {
		t.Fatalf("expected 'Hello\nWorld', got %q", content)
	}
	if len(media) != 1 || media[0] != "data:image/png;base64,abc" {
		t.Fatalf("expected 1 media, got %v", media)
	}
}

func TestExtractRequestContent_AnySlice(t *testing.T) {
	input := []any{
		map[string]any{"type": "input_text", "content": "Hello"},
		map[string]any{"type": "input_file", "content": "data:application/pdf;base64,xyz"},
	}
	content, media, err := extractRequestContent(input)
	if err != nil {
		t.Fatal(err)
	}
	if content != "Hello" {
		t.Fatalf("expected 'Hello', got %q", content)
	}
	if len(media) != 1 {
		t.Fatalf("expected 1 media, got %v", media)
	}
}

func TestExtractRequestContent_MapSlice(t *testing.T) {
	input := []map[string]any{
		{"type": "input_text", "content": "A"},
		{"type": "input_text", "content": "B"},
	}
	content, media, err := extractRequestContent(input)
	if err != nil {
		t.Fatal(err)
	}
	if content != "A\nB" {
		t.Fatalf("expected 'A\nB', got %q", content)
	}
	if len(media) != 0 {
		t.Fatalf("expected 0 media, got %v", media)
	}
}

// -- utility tests --

func TestIsImageDataURL(t *testing.T) {
	if !isImageDataURL("data:image/png;base64,abc") {
		t.Error("expected true for image data URL")
	}
	if isImageDataURL("data:application/pdf;base64,abc") {
		t.Error("expected false for non-image data URL")
	}
	if isImageDataURL("https://example.com/image.png") {
		t.Error("expected false for regular URL")
	}
}

func TestExtFromMime(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{"application/pdf", ".pdf"},
		{"text/plain", ".txt"},
		{"text/html", ".html"},
		{"text/markdown", ".md"},
		{"application/json", ".json"},
		{"image/jpeg", ".jpg"},
		{"image/png", ".png"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
		{"application/msword", ".doc"},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
		{"application/vnd.ms-excel", ".xls"},
		{"application/xml", ".xml"},
		{"text/xml", ".xml"},
		{"application/javascript", ".js"},
		{"text/javascript", ".js"},
		{"text/css", ".css"},
		{"unknown/type", ".type"},
		{"application/x-custom", ".custom"},
		{"", ".bin"},
	}
	for _, tc := range tests {
		got := extFromMime(tc.mime)
		if got != tc.want {
			t.Errorf("extFromMime(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}

func TestMimeFromExt(t *testing.T) {
	if got := mimeFromExt("test.png"); got != "image/png" {
		t.Errorf("mimeFromExt('test.png') = %q, want image/png", got)
	}
	if got := mimeFromExt("test.unknown"); got != "application/octet-stream" {
		t.Errorf("mimeFromExt('test.unknown') = %q, want application/octet-stream", got)
	}
	if got := mimeFromExt("test"); got != "application/octet-stream" {
		t.Errorf("mimeFromExt('test') = %q, want application/octet-stream", got)
	}
}

// -- Send method tests --

func TestSend_ChannelNotRunning(t *testing.T) {
	ch := newTestChannel()
	ch.SetRunning(false)
	_, err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "test"})
	if err != channels.ErrNotRunning {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestSend_EmptyChatID(t *testing.T) {
	ch := newTestChannel()
	_, err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: ""})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestSend_NoActiveConversation(t *testing.T) {
	ch := newTestChannel()
	_, err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "nonexistent"})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestSend_TextEvent(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_send_text"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs[convID] = st

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  convID,
		Content: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	ev, _ := st.stream.next()
	if ev.kind != eventKindText || ev.content != "hello" {
		t.Fatalf("expected text event, got %+v", ev)
	}
}

func TestSend_ThoughtEvent(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_send_thought"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs[convID] = st

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  convID,
		Content: "thinking...",
		Context: bus.InboundContext{Raw: map[string]string{"message_kind": "thought"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ev, _ := st.stream.next()
	if ev.kind != eventKindReasoning || ev.content != "thinking..." {
		t.Fatalf("expected reasoning event, got %+v", ev)
	}
}

func TestSend_FunctionCallEvent(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_send_func"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs[convID] = st

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  convID,
		Content: "",
		Context: bus.InboundContext{Raw: map[string]string{
			"message_kind": "function_call",
			"call_id":      "call_1",
			"name":         "read_file",
			"arguments":    `{"path":"/tmp/test.txt"}`,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ev, _ := st.stream.next()
	if ev.kind != eventKindFunctionCall || ev.callID != "call_1" || ev.name != "read_file" {
		t.Fatalf("expected function_call event, got %+v", ev)
	}
}

func TestSend_TurnEndEvent(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_send_turnend"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs[convID] = st

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  convID,
		Content: "",
		Context: bus.InboundContext{Raw: map[string]string{"message_kind": "turn_end"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read turn_end event
	ev, _ := st.stream.next()
	if ev.kind != eventKindTurnEnd {
		t.Fatalf("expected turn_end event, got %+v", ev)
	}

	// stream should be closed
	if !st.stream.isClosed() {
		t.Fatal("expected stream to be closed")
	}

	// done should be closed
	select {
	case <-st.done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("done channel should be closed")
	}

	// conv should be deleted
	ch.convMu.RLock()
	_, exists := ch.convs[convID]
	ch.convMu.RUnlock()
	if exists {
		t.Fatal("expected conversation to be deleted")
	}
}

func TestSend_FinalOutboundTriggersTurnEnd(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_send_final"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs[convID] = st

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  convID,
		Content: "Final answer",
		Context: bus.InboundContext{Raw: map[string]string{"outbound_kind": "final"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should get text event followed by turn_end
	ev1, _ := st.stream.next()
	if ev1.kind != eventKindText || ev1.content != "Final answer" {
		t.Fatalf("expected text event, got %+v", ev1)
	}

	ev2, _ := st.stream.next()
	if ev2.kind != eventKindTurnEnd {
		t.Fatalf("expected turn_end event, got %+v", ev2)
	}

	// stream should be closed after turn_end
	if !st.stream.isClosed() {
		t.Fatal("expected stream to be closed after turn_end")
	}
}

func TestSend_StreamerFiltersNonAllowedKinds(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_send_filter"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	st.hasStreamer.Store(true)
	ch.convs[convID] = st

	// tool_feedback should be filtered
	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  convID,
		Content: "tool feedback",
		Context: bus.InboundContext{Raw: map[string]string{"message_kind": "tool_feedback"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// No event should be pushed (buffer empty)
	select {
	case <-st.stream.done:
		t.Fatal("expected no event for filtered kind")
	case <-time.After(100 * time.Millisecond):
		// OK
	}
}

func TestSend_StreamerAllowsAllowedKinds(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_send_allow"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	st.hasStreamer.Store(true)
	ch.convs[convID] = st

	// function_call should be allowed
	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  convID,
		Content: "",
		Context: bus.InboundContext{Raw: map[string]string{
			"message_kind": "function_call",
			"call_id":      "c1",
			"name":         "test",
			"arguments":    "{}",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ev, _ := st.stream.next()
	if ev.kind != eventKindFunctionCall {
		t.Fatalf("expected function_call event, got %+v", ev)
	}
}

// -- Streamer tests --

func TestStreamer_Update(t *testing.T) {
	s := newPendingStream()
	streamer := &openResponsesStreamer{stream: s}

	streamer.Update(context.Background(), "Hello")
	ev, _ := s.next()
	if ev.kind != eventKindTextDelta || ev.content != "Hello" {
		t.Fatalf("expected Hello delta, got %+v", ev)
	}

	streamer.Update(context.Background(), "Hello World")
	ev, _ = s.next()
	if ev.kind != eventKindTextDelta || ev.content != " World" {
		t.Fatalf("expected ' World' delta, got %+v", ev)
	}
}

func TestStreamer_UpdateReasoning(t *testing.T) {
	s := newPendingStream()
	streamer := &openResponsesStreamer{stream: s}

	streamer.UpdateReasoning(context.Background(), "Let me think")
	ev, _ := s.next()
	if ev.kind != eventKindReasoning || ev.content != "Let me think" {
		t.Fatalf("expected reasoning, got %+v", ev)
	}

	streamer.UpdateReasoning(context.Background(), "Let me think about this")
	ev, _ = s.next()
	if ev.kind != eventKindReasoning || ev.content != " about this" {
		t.Fatalf("expected ' about this' delta, got %+v", ev)
	}
}

func TestStreamer_Finalize(t *testing.T) {
	s := newPendingStream()
	streamer := &openResponsesStreamer{stream: s}

	streamer.Finalize(context.Background(), "done")
	ev, _ := s.next()
	if ev.kind != eventKindText || ev.content != "done" {
		t.Fatalf("expected text event with 'done', got %+v", ev)
	}
	ev, _ = s.next()
	if ev.kind != eventKindTurnEnd {
		t.Fatalf("expected turn_end, got %+v", ev)
	}
}

func TestStreamer_Cancel(t *testing.T) {
	s := newPendingStream()
	streamer := &openResponsesStreamer{stream: s}

	// Cancel should never close the stream. The pipeline calls Cancel when
	// LLM returns tool_calls (turn continues) or on errors (Finalize still
	// called). Only Finalize should close the stream.
	streamer.Cancel(context.Background())
	select {
	case <-s.done:
		t.Fatal("expected stream to stay open after Cancel")
	default:
	}

	// Even after content is streamed, Cancel should NOT close the stream
	streamer.Update(context.Background(), "hello")
	streamer.Cancel(context.Background())
	// Drain the event pushed by Update
	s.next()
	select {
	case <-s.done:
		t.Fatal("expected stream to stay open after Cancel even with streamed content")
	default:
	}

	// Finalize is what actually closes the stream
	streamer.Finalize(context.Background(), "done")
	// Drain the text event pushed by Finalize (lastContent was "", content="done")
	s.next()
	// Drain the turn_end event pushed by Finalize
	s.next()
	// Now the stream should be closed
	if !s.isClosed() {
		t.Fatal("expected stream to be closed after Finalize")
	}
}

// -- Channel Start/Stop tests --

func TestChannel_StartStop(t *testing.T) {
	ch := &OpenResponsesChannel{
		BaseChannel: channels.NewBaseChannel("openresponses", nil, nil, []string{"*"}),
		cfg:         &config.OpenResponsesSettings{},
		convs:       make(map[string]*conversationState),
	}
	if ch.IsRunning() {
		t.Fatal("expected not running initially")
	}

	ctx := context.Background()
	if err := ch.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !ch.IsRunning() {
		t.Fatal("expected running after Start")
	}

	if err := ch.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if ch.IsRunning() {
		t.Fatal("expected not running after Stop")
	}
}

func TestChannel_StopClosesConversations(t *testing.T) {
	ch := newTestChannel()
	ch.Start(context.Background())

	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs["conv1"] = st

	ch.Stop(context.Background())

	if !st.stream.isClosed() {
		t.Fatal("expected stream to be closed")
	}
}

// -- BeginStream tests --

func TestBeginStream_Success(t *testing.T) {
	ch := newTestChannel()
	ch.Start(context.Background())

	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs["conv_stream"] = st

	streamer, err := ch.BeginStream(context.Background(), "conv_stream")
	if err != nil {
		t.Fatal(err)
	}
	if streamer == nil {
		t.Fatal("expected streamer")
	}
	if !st.hasStreamer.Load() {
		t.Fatal("expected hasStreamer to be true")
	}
}

func TestBeginStream_NotFound(t *testing.T) {
	ch := newTestChannel()
	ch.Start(context.Background())

	_, err := ch.BeginStream(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent conversation")
	}
}

// -- SendMedia tests --

func TestSendMedia_ChannelNotRunning(t *testing.T) {
	ch := newTestChannel()
	ch.SetRunning(false)
	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "test"})
	if err != channels.ErrNotRunning {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestSendMedia_EmptyChatID(t *testing.T) {
	ch := newTestChannel()
	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: ""})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestSendMedia_NoActiveConversation(t *testing.T) {
	ch := newTestChannel()
	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "nonexistent"})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestSendMedia_NoMediaStore(t *testing.T) {
	ch := newTestChannel()
	convID := "conv_media"
	st := &conversationState{
		stream: newPendingStream(),
		done:   make(chan struct{}),
	}
	st.active.Store(true)
	ch.convs[convID] = st

	_, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: convID,
		Parts:  []bus.MediaPart{{Type: "image", Ref: "media://test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Should return empty without media store
}

// -- Session API tests --

func TestSessionList_Empty(t *testing.T) {
	ch := newTestChannel()
	items := ch.listSessions(0, 20)
	if len(items) != 0 {
		t.Fatalf("expected empty list, got %d items", len(items))
	}
}

func TestGetSession_NotFound(t *testing.T) {
	ch := newTestChannel()
	result := ch.getSession("nonexistent")
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	ch := newTestChannel()
	if ch.deleteSession("nonexistent") {
		t.Fatal("expected false for nonexistent session")
	}
}

