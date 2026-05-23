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
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
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
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestServeHTTP_EmptyInput(t *testing.T) {
	ch := newTestChannel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":""}`))
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
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"hello"}`))
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
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
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
