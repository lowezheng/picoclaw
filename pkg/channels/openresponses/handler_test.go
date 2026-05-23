package openresponses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestWebhookPath(t *testing.T) {
	ch := &OpenResponsesChannel{
		cfg: &config.OpenResponsesSettings{EndpointPath: "/v1/responses"},
	}
	if got := ch.WebhookPath(); got != "/v1/responses/" {
		t.Errorf("WebhookPath() = %q, want %q", got, "/v1/responses/")
	}

	ch.cfg.EndpointPath = "/custom/"
	if got := ch.WebhookPath(); got != "/custom/" {
		t.Errorf("WebhookPath() = %q, want %q", got, "/custom/")
	}
}

func TestServeHTTP_ChannelNotRunning(t *testing.T) {
	ch := &OpenResponsesChannel{
		BaseChannel: newTestBaseChannel(),
		cfg:         &config.OpenResponsesSettings{Token: *testSecureString("test-token")},
	}
	// Not running
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", nil)
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Type != "server_error" {
		t.Errorf("expected server_error, got %q", resp.Error.Type)
	}
}

func TestServeHTTP_AuthFailure(t *testing.T) {
	ch := newTestChannel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestServeHTTP_EmptyInput(t *testing.T) {
	ch := newTestChannel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", strings.NewReader(`{"input":""}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestServeHTTP_InvalidContentType(t *testing.T) {
	ch := newTestChannel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", strings.NewReader(`{"input":"hello"}`))
	req.Header.Set("Authorization", "Bearer test-token")
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestServeHTTP_404(t *testing.T) {
	ch := newTestChannel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/responses/unknown", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	ch := newTestChannel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/responses/chat", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestBuildResponse_EmptyStream(t *testing.T) {
	ch := newTestChannel()
	s := newPendingStream()
	s.close()

	resp := ch.buildResponse(s, "conv_test", CreateResponseRequest{})
	if resp.Status != "completed" {
		t.Errorf("expected status completed, got %q", resp.Status)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "message" {
		t.Errorf("expected message type, got %q", resp.Output[0].Type)
	}
}

func TestBuildResponse_TextEvent(t *testing.T) {
	ch := newTestChannel()
	s := newPendingStream()
	s.push(streamEvent{kind: eventKindText, content: "Hello world"})
	s.close()

	resp := ch.buildResponse(s, "conv_test", CreateResponseRequest{})
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Content[0].Text != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", resp.Output[0].Content[0].Text)
	}
}

func TestBuildResponse_TextDeltaThenTurnEnd(t *testing.T) {
	ch := newTestChannel()
	s := newPendingStream()
	s.push(streamEvent{kind: eventKindTextDelta, content: "Hello "})
	s.push(streamEvent{kind: eventKindTextDelta, content: "world"})
	s.push(streamEvent{kind: eventKindTurnEnd})
	s.close()

	resp := ch.buildResponse(s, "conv_test", CreateResponseRequest{})
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Content[0].Text != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", resp.Output[0].Content[0].Text)
	}
}

func TestBuildResponse_ReasoningEvent(t *testing.T) {
	ch := newTestChannel()
	s := newPendingStream()
	s.push(streamEvent{kind: eventKindReasoning, content: "Let me think..."})
	s.close()

	resp := ch.buildResponse(s, "conv_test", CreateResponseRequest{})
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "reasoning" {
		t.Errorf("expected reasoning type, got %q", resp.Output[0].Type)
	}
}

func TestBuildResponse_FunctionCallEvent(t *testing.T) {
	ch := newTestChannel()
	s := newPendingStream()
	s.push(streamEvent{kind: eventKindFunctionCall, callID: "call_1", name: "read_file", arguments: `{"path":"/tmp/test.txt"}`})
	s.close()

	resp := ch.buildResponse(s, "conv_test", CreateResponseRequest{})
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "function_call" {
		t.Errorf("expected function_call type, got %q", resp.Output[0].Type)
	}
}

func TestBuildResponse_StripImageContent(t *testing.T) {
	ch := newTestChannel()
	s := newPendingStream()
	s.push(streamEvent{kind: eventKindImage, imageURL: "data:image/png;base64,abc123"})
	s.close()

	resp := ch.buildResponse(s, "conv_test", CreateResponseRequest{})
	item := stripContentsFromItem(resp.Output[0])
	if item.Content[0].Text != "" {
		t.Errorf("expected empty image content after strip, got %q", item.Content[0].Text)
	}
}


// -- helpers --

func newTestChannel() *OpenResponsesChannel {
	ch := &OpenResponsesChannel{
		BaseChannel: newTestBaseChannel(),
		cfg:         &config.OpenResponsesSettings{Token: *testSecureString("test-token")},
		convs:       make(map[string]*conversationState),
	}
	ch.SetRunning(true)
	return ch
}

func newTestBaseChannel() *channels.BaseChannel {
	return channels.NewBaseChannel("openresponses", nil, nil, []string{"*"})
}

func testSecureString(s string) *config.SecureString {
	ss := config.NewSecureString(s)
	return ss
}

// -- SSE helpers --

type sseEvent struct {
	Event string
	Data  string
}

func parseSSEEvents(body string) []sseEvent {
	var events []sseEvent
	lines := strings.Split(body, "\n")
	var currentEvent string
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			events = append(events, sseEvent{Event: currentEvent, Data: data})
			currentEvent = ""
		}
	}
	return events
}

func findEventByType(events []sseEvent, eventType string) *sseEvent {
	for _, ev := range events {
		if ev.Event == eventType {
			return &ev
		}
	}
	return nil
}

func assertEventSequence(t *testing.T, events []sseEvent, expected []string) {
	t.Helper()
	var got []string
	for _, ev := range events {
		if ev.Event != "" {
			got = append(got, ev.Event)
		}
	}
	if len(got) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(got), got)
	}
	for i, exp := range expected {
		if got[i] != exp {
			t.Errorf("event %d: expected %q, got %q", i, exp, got[i])
		}
	}
}

// -- serveStream SSE event sequence tests --

func TestServeStream_TextOnly(t *testing.T) {
	ch := newTestChannel()
	stream := newPendingStream()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", nil)

	go func() {
		stream.push(streamEvent{kind: eventKindTextDelta, content: "Hello"})
		stream.push(streamEvent{kind: eventKindTextDelta, content: " world"})
		stream.push(streamEvent{kind: eventKindTurnEnd})
		stream.close()
	}()

	ch.serveStream(rr, req, stream, "conv_test", CreateResponseRequest{Stream: true})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	events := parseSSEEvents(rr.Body.String())
	assertEventSequence(t, events, []string{
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	})

	// Verify last event is [DONE]
	last := events[len(events)-1]
	if last.Data != "[DONE]" {
		t.Errorf("expected last data [DONE], got %q", last.Data)
	}

	// Verify delta contents
	var deltas []string
	for _, ev := range events {
		if ev.Event == "response.output_text.delta" {
			var payload map[string]any
			_ = json.Unmarshal([]byte(ev.Data), &payload)
			if d, ok := payload["delta"].(string); ok {
				deltas = append(deltas, d)
			}
		}
	}
	if len(deltas) != 2 || deltas[0] != "Hello" || deltas[1] != " world" {
		t.Errorf("expected deltas [Hello, ' world'], got %v", deltas)
	}
}

func TestServeStream_ReasoningAfterText(t *testing.T) {
	ch := newTestChannel()
	stream := newPendingStream()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", nil)

	go func() {
		stream.push(streamEvent{kind: eventKindTextDelta, content: "Hello"})
		stream.push(streamEvent{kind: eventKindReasoning, content: "Let me think"})
		stream.push(streamEvent{kind: eventKindTurnEnd})
		stream.close()
	}()

	ch.serveStream(rr, req, stream, "conv_test", CreateResponseRequest{Stream: true})
	events := parseSSEEvents(rr.Body.String())

	// Text closes, then reasoning starts
	assertEventSequence(t, events, []string{
		"response.in_progress",
		"response.output_item.added",     // message
		"response.content_part.added",    // output_text
		"response.output_text.delta",     // Hello
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",      // message done
		"response.output_item.added",     // reasoning
		"response.content_part.added",    // reasoning_text
		"response.reasoning_text.delta",  // Let me think
		"response.reasoning_text.done",
		"response.content_part.done",
		"response.output_item.done",      // reasoning done
		"response.completed",
	})
}

func TestServeStream_FunctionCallSequence(t *testing.T) {
	ch := newTestChannel()
	stream := newPendingStream()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", nil)

	go func() {
		stream.push(streamEvent{kind: eventKindTextDelta, content: "Let me"})
		stream.push(streamEvent{kind: eventKindFunctionCall, callID: "call_1", name: "read_file", arguments: `{"path":"/tmp"}`})
		stream.push(streamEvent{kind: eventKindTurnEnd})
		stream.close()
	}()

	ch.serveStream(rr, req, stream, "conv_test", CreateResponseRequest{Stream: true})
	events := parseSSEEvents(rr.Body.String())

	assertEventSequence(t, events, []string{
		"response.in_progress",
		"response.output_item.added",              // message
		"response.content_part.added",             // output_text
		"response.output_text.delta",              // Let me
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",               // message done
		"response.output_item.added",              // function_call
		"response.content_part.added",             // function_call_arguments
		"response.function_call_arguments.delta",  // args
		"response.function_call_arguments.done",
		"response.content_part.done",
		"response.output_item.done",               // function_call done
		"response.completed",
	})

	// Verify function call metadata
	for _, ev := range events {
		if ev.Event == "response.output_item.added" {
			var payload map[string]any
			_ = json.Unmarshal([]byte(ev.Data), &payload)
			item, _ := payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				if item["call_id"] != "call_1" {
					t.Errorf("expected call_id call_1, got %v", item["call_id"])
				}
				if item["name"] != "read_file" {
					t.Errorf("expected name read_file, got %v", item["name"])
				}
			}
		}
	}
}

func TestServeStream_ImageEvent(t *testing.T) {
	ch := newTestChannel()
	stream := newPendingStream()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", nil)

	go func() {
		stream.push(streamEvent{kind: eventKindImage, imageURL: "data:image/png;base64,abc"})
		stream.push(streamEvent{kind: eventKindTurnEnd})
		stream.close()
	}()

	ch.serveStream(rr, req, stream, "conv_test", CreateResponseRequest{Stream: true})
	events := parseSSEEvents(rr.Body.String())

	assertEventSequence(t, events, []string{
		"response.in_progress",
		"response.output_item.added",     // message
		"response.content_part.added",    // output_image
		"response.content_part.done",     // output_image
		"response.output_item.done",      // message done
		"response.completed",
	})
}

// -- serveJSON response tests --

func TestServeJSON_TextDeltaAccumulation(t *testing.T) {
	ch := newTestChannel()
	stream := newPendingStream()

	go func() {
		stream.push(streamEvent{kind: eventKindTextDelta, content: "Hello "})
		stream.push(streamEvent{kind: eventKindTextDelta, content: "world"})
		stream.push(streamEvent{kind: eventKindTurnEnd})
		stream.close()
	}()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", nil)
	ch.serveJSON(rr, req, stream, "conv_json", CreateResponseRequest{})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status completed, got %q", resp.Status)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Content[0].Text != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", resp.Output[0].Content[0].Text)
	}
}

func TestServeJSON_FunctionCallOutput(t *testing.T) {
	ch := newTestChannel()
	stream := newPendingStream()

	go func() {
		stream.push(streamEvent{kind: eventKindFunctionCall, callID: "c1", name: "test", arguments: `{"x":1}`})
		stream.push(streamEvent{kind: eventKindTurnEnd})
		stream.close()
	}()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/chat", nil)
	ch.serveJSON(rr, req, stream, "conv_fc", CreateResponseRequest{})

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "function_call" {
		t.Errorf("expected function_call type, got %q", resp.Output[0].Type)
	}
	if resp.Output[0].Content[0].Text != `{"x":1}` {
		t.Errorf("expected arguments, got %q", resp.Output[0].Content[0].Text)
	}
}

// -- Full HTTP integration via ServeHTTP --
// Note: ServeHTTP → dispatch → HandleInboundContext requires a real MessageBus.
// The core SSE/JSON business logic is covered by serveStream/serveJSON tests above.
// Full end-to-end tests should be run against a running server with test_curl.sh.
