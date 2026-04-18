package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newTestChannel(t *testing.T, token string) (*OpenResponsesChannel, *bus.MessageBus) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{
		Token:          *config.NewSecureString(token),
		RequestTimeout: 5,
	}
	ch, err := NewOpenResponsesChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("failed to create channel: %v", err)
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("failed to start channel: %v", err)
	}
	return ch, msgBus
}

func TestServeHTTPMethodNotAllowed(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestServeHTTPUnauthorized(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	body, _ := json.Marshal(CreateResponseRequest{Input: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestServeHTTPBadContentType(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestServeHTTPEmptyInput(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	body, _ := json.Marshal(CreateResponseRequest{Input: "   "})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestServeHTTPJSONResponse(t *testing.T) {
	ch, msgBus := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	// Simulate an agent by reading inbound messages and directly calling Send.
	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				// Echo back with "Echo: " prefix.
				time.Sleep(20 * time.Millisecond)
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "Echo: " + msg.Content,
				})
			}
		}
	}()

	body, _ := json.Marshal(CreateResponseRequest{Input: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content type, got %s", ct)
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Object != "response" {
		t.Errorf("expected object 'response', got %s", resp.Object)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", resp.Status)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "message" {
		t.Errorf("expected type 'message', got %s", resp.Output[0].Type)
	}
	if len(resp.Output[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(resp.Output[0].Content))
	}
	if resp.Output[0].Content[0].Text != "Echo: hello" {
		t.Errorf("expected text 'Echo: hello', got %s", resp.Output[0].Content[0].Text)
	}
}

func TestServeHTTPSSEStream(t *testing.T) {
	ch, msgBus := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				// Simulate agent reply.
				time.Sleep(20 * time.Millisecond)
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "Stream reply",
				})
			}
		}
	}()

	body, _ := json.Marshal(CreateResponseRequest{Input: "hi", Stream: true})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected SSE content type, got %s", ct)
	}

	bodyStr := rr.Body.String()
	if !strings.Contains(bodyStr, "event: response.created") {
		t.Error("missing response.created event")
	}
	if !strings.Contains(bodyStr, "event: response.completed") {
		t.Error("missing response.completed event")
	}
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Error("missing [DONE] terminator")
	}
	if !strings.Contains(bodyStr, "Stream reply") {
		t.Error("missing actual reply content in SSE stream")
	}
}

func TestServeHTTPPayloadTooLarge(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	ch.config.MaxBodySize = 10

	body := []byte(`{"input":"this is way more than ten bytes"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rr.Code)
	}
}

func TestServeHTTPTimeout(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	ch.config.RequestTimeout = 1

	body, _ := json.Marshal(CreateResponseRequest{Input: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (empty fallback), got %d: %s", rr.Code, rr.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", resp.Status)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(resp.Output))
	}
	if resp.Output[0].Content[0].Text != "" {
		t.Errorf("expected empty text on timeout, got %q", resp.Output[0].Content[0].Text)
	}
}

func TestServeHTTPConversationID(t *testing.T) {
	ch, msgBus := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				// Simulate agent reply.
				time.Sleep(20 * time.Millisecond)
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "OK",
				})
			}
		}
	}()

	body, _ := json.Marshal(CreateResponseRequest{Input: "test", ConversationID: "conv_custom_42"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ConversationID != "conv_custom_42" {
		t.Errorf("expected conversation_id 'conv_custom_42', got %s", resp.ConversationID)
	}
}

func TestServeHTTPNotRunning(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	ch.Stop(context.Background())

	body, _ := json.Marshal(CreateResponseRequest{Input: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestBuildResponse(t *testing.T) {
	resp := buildResponse("resp_1", "msg_1", "conv_1", "prev_1", "Hello")
	if resp.ID != "resp_1" {
		t.Errorf("expected ID resp_1, got %s", resp.ID)
	}
	if resp.Object != "response" {
		t.Errorf("expected object 'response', got %s", resp.Object)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status 'completed', got %s", resp.Status)
	}
	if resp.ConversationID != "conv_1" {
		t.Errorf("expected conversation_id conv_1, got %s", resp.ConversationID)
	}
	if resp.PreviousResponseID != "prev_1" {
		t.Errorf("expected previous_response_id prev_1, got %s", resp.PreviousResponseID)
	}
	if len(resp.Output) != 1 || resp.Output[0].Content[0].Text != "Hello" {
		t.Errorf("unexpected output content")
	}
}

func TestWriteJSONResponse(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	rr := httptest.NewRecorder()
	ch.writeJSONResponse(rr, "resp_1", "msg_1", "conv_1", "", "Test")

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON, got %s", ct)
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Output[0].Content[0].Text != "Test" {
		t.Errorf("unexpected content: %s", resp.Output[0].Content[0].Text)
	}
}

func TestWriteSSEResponse(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	rr := httptest.NewRecorder()
	ch.writeSSEResponse(rr, "resp_1", "msg_1", "conv_1", "", "SSE test")

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	required := []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.output_item.done",
		"event: response.completed",
		"data: [DONE]",
		"SSE test",
	}
	for _, s := range required {
		if !strings.Contains(body, s) {
			t.Errorf("SSE response missing: %s", s)
		}
	}
}
