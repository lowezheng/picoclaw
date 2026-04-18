package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
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
	if !strings.Contains(bodyStr, "event: response.in_progress") {
		t.Error("missing response.in_progress event")
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

func TestServeHTTPStreamMultipleMessages(t *testing.T) {
	ch, msgBus := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	// Simulate an agent that sends 3 messages in sequence.
	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				for i := 1; i <= 3; i++ {
					time.Sleep(30 * time.Millisecond)
					ch.Send(context.Background(), bus.OutboundMessage{
						Channel: msg.Context.Channel,
						ChatID:  msg.Context.ChatID,
						Content: fmt.Sprintf("Part %d", i),
					})
				}
			}
		}
	}()

	body, _ := json.Marshal(CreateResponseRequest{Input: "multi", Stream: true})
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

	// Should contain 3 output_text.delta events.
	count := strings.Count(bodyStr, "response.output_text.delta")
	if count < 3 {
		t.Errorf("expected at least 3 output_text.delta events, got %d", count)
	}

	// Should contain all 3 parts.
	for i := 1; i <= 3; i++ {
		if !strings.Contains(bodyStr, fmt.Sprintf("Part %d", i)) {
			t.Errorf("missing Part %d in SSE stream", i)
		}
	}

	if !strings.Contains(bodyStr, "event: response.completed") {
		t.Error("missing response.completed event")
	}
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Error("missing [DONE] terminator")
	}
}

func TestServeHTTPStreamTimeout(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	ch.config.RequestTimeout = 1 // 1 second timeout

	body, _ := json.Marshal(CreateResponseRequest{Input: "slow", Stream: true})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Should still get a completed response (empty content) after timeout.
	bodyStr := rr.Body.String()
	if !strings.Contains(bodyStr, "event: response.completed") {
		t.Error("missing response.completed event on timeout")
	}
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Error("missing [DONE] terminator on timeout")
	}
}

func TestErrorResponseTypeField(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	body, _ := json.Marshal(CreateResponseRequest{Input: "   "})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	var resp struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if resp.Error.Type != "invalid_request" {
		t.Errorf("expected type 'invalid_request', got %q", resp.Error.Type)
	}
	if resp.Error.Code != "invalid_request" {
		t.Errorf("expected code 'invalid_request', got %q", resp.Error.Code)
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

func TestWriteJSONResponseWithStream(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	stream.push(streamEvent{kind: eventKindText, content: "Test"})
	stream.close()

	rr := httptest.NewRecorder()
	ch.writeJSONResponseWithStream(rr, stream, "resp_1", "msg_1", "conv_1", "")

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

func TestJSONResponseHasUsage(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	stream.push(streamEvent{kind: eventKindText, content: "Hello"})
	stream.close()

	rr := httptest.NewRecorder()
	ch.writeJSONResponseWithStream(rr, stream, "resp_1", "msg_1", "conv_1", "")

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Usage.InputTokens != 0 {
		t.Errorf("expected input_tokens 0, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 0 {
		t.Errorf("expected output_tokens 0, got %d", resp.Usage.OutputTokens)
	}
}

func TestSSEOutputItemAddedHasEmptyContent(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	stream.push(streamEvent{kind: eventKindText, content: "Hello world"})
	stream.close()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ch.writeSSEResponseStream(rr, req, stream, "resp_1", "msg_1", "conv_1", "")

	body := rr.Body.String()

	// Extract the output_item.added event data.
	var addedEventData string
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.Contains(lines[i], "event: response.output_item.added") && i+1 < len(lines) {
			addedEventData = strings.TrimPrefix(lines[i+1], "data: ")
			break
		}
	}
	if addedEventData == "" {
		t.Fatalf("missing response.output_item.added event data")
	}
	var addedEvent ResponseEvent
	if err := json.Unmarshal([]byte(addedEventData), &addedEvent); err != nil {
		t.Fatalf("failed to unmarshal added event: %v", err)
	}
	if len(addedEvent.Item.Content) != 0 {
		t.Errorf("expected output_item.added content to be empty, got %v", addedEvent.Item.Content)
	}

	// Extract the output_item.done event data.
	var doneEventData string
	for i := 0; i < len(lines); i++ {
		if strings.Contains(lines[i], "event: response.output_item.done") && i+1 < len(lines) {
			doneEventData = strings.TrimPrefix(lines[i+1], "data: ")
			break
		}
	}
	if doneEventData == "" {
		t.Fatalf("missing response.output_item.done event data")
	}
	var doneEvent ResponseEvent
	if err := json.Unmarshal([]byte(doneEventData), &doneEvent); err != nil {
		t.Fatalf("failed to unmarshal done event: %v", err)
	}
	if len(doneEvent.Item.Content) != 1 || doneEvent.Item.Content[0].Text != "Hello world" {
		t.Errorf("expected output_item.done content to have full text, got %v", doneEvent.Item.Content)
	}
}

func TestWriteSSEResponseStream(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	stream.push(streamEvent{kind: eventKindText, content: "SSE test"})
	stream.close()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ch.writeSSEResponseStream(rr, req, stream, "resp_1", "msg_1", "conv_1", "")

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	required := []string{
		"event: response.in_progress",
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

func TestWriteJSONResponseWithStream_Reasoning(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	stream.push(streamEvent{kind: eventKindText, content: "Final answer"})
	stream.push(streamEvent{kind: eventKindReasoning, content: "Let me reason..."})
	stream.close()

	rr := httptest.NewRecorder()
	ch.writeJSONResponseWithStream(rr, stream, "resp_1", "msg_1", "conv_1", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(resp.Output) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "message" {
		t.Errorf("expected first item type 'message', got %s", resp.Output[0].Type)
	}
	if resp.Output[0].Content[0].Text != "Final answer" {
		t.Errorf("unexpected first content: %s", resp.Output[0].Content[0].Text)
	}
	if resp.Output[1].Type != "reasoning" {
		t.Errorf("expected second item type 'reasoning', got %s", resp.Output[1].Type)
	}
	if resp.Output[1].Content[0].Type != "reasoning_text" {
		t.Errorf("expected reasoning_text, got %s", resp.Output[1].Content[0].Type)
	}
	if resp.Output[1].Content[0].Text != "Let me reason..." {
		t.Errorf("unexpected reasoning content: %s", resp.Output[1].Content[0].Text)
	}
}

func TestWriteSSEResponseStream_Reasoning(t *testing.T) {
	ch, _ := newTestChannel(t, "secret")
	defer ch.Stop(context.Background())

	stream := newPendingStream(10)
	stream.push(streamEvent{kind: eventKindReasoning, content: "Thinking..."})
	stream.close()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ch.writeSSEResponseStream(rr, req, stream, "resp_1", "msg_1", "conv_1", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()

	required := []string{
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.reasoning_text.delta",
		"event: response.reasoning_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
		"data: [DONE]",
		"Thinking...",
	}
	for _, s := range required {
		if !strings.Contains(body, s) {
			t.Errorf("SSE response missing: %s", s)
		}
	}

	// Verify output_item.added has type "reasoning".
	var addedEventData string
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.Contains(lines[i], "event: response.output_item.added") && i+1 < len(lines) {
			addedEventData = strings.TrimPrefix(lines[i+1], "data: ")
			break
		}
	}
	if addedEventData == "" {
		t.Fatal("missing response.output_item.added event data")
	}
	var addedEvent ResponseEvent
	if err := json.Unmarshal([]byte(addedEventData), &addedEvent); err != nil {
		t.Fatalf("failed to unmarshal added event: %v", err)
	}
	if addedEvent.Item.Type != "reasoning" {
		t.Errorf("expected output_item.added type 'reasoning', got %s", addedEvent.Item.Type)
	}

	// Verify output_item.done has type "reasoning" and reasoning_text content.
	var doneEventData string
	for i := 0; i < len(lines); i++ {
		if strings.Contains(lines[i], "event: response.output_item.done") && i+1 < len(lines) {
			doneEventData = strings.TrimPrefix(lines[i+1], "data: ")
			break
		}
	}
	if doneEventData == "" {
		t.Fatal("missing response.output_item.done event data")
	}
	var doneEvent ResponseEvent
	if err := json.Unmarshal([]byte(doneEventData), &doneEvent); err != nil {
		t.Fatalf("failed to unmarshal done event: %v", err)
	}
	if doneEvent.Item.Type != "reasoning" {
		t.Errorf("expected output_item.done type 'reasoning', got %s", doneEvent.Item.Type)
	}
	if len(doneEvent.Item.Content) != 1 || doneEvent.Item.Content[0].Type != "reasoning_text" {
		t.Errorf("expected reasoning_text content, got %v", doneEvent.Item.Content)
	}
	if doneEvent.Item.Content[0].Text != "Thinking..." {
		t.Errorf("expected 'Thinking...', got %s", doneEvent.Item.Content[0].Text)
	}
}

func TestServeHTTPJSONResponseWithImage(t *testing.T) {
	ch, msgBus, store := newTestChannelWithStore(t, "secret")
	defer ch.Stop(context.Background())

	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	_ = os.WriteFile(imgPath, []byte("fake image bytes"), 0644)
	ref, _ := store.Store(imgPath, media.MediaMeta{
		Filename:    "test.png",
		ContentType: "image/png",
	}, "test-scope")

	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				time.Sleep(20 * time.Millisecond)
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "Here is your image:",
				})
				ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Parts: []bus.MediaPart{
						{Type: "image", Ref: ref, Filename: "test.png", ContentType: "image/png"},
					},
				})
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "",
					Context: bus.InboundContext{Raw: map[string]string{"message_kind": "turn_end"}},
				})
			}
		}
	}()

	body, _ := json.Marshal(CreateResponseRequest{Input: "generate image"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Output) != 2 {
		t.Fatalf("expected 2 output items, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "message" || resp.Output[0].Content[0].Type != "output_text" {
		t.Errorf("expected first item text message, got %+v", resp.Output[0])
	}
	if resp.Output[1].Type != "message" || resp.Output[1].Content[0].Type != "output_image" {
		t.Errorf("expected second item image message, got %+v", resp.Output[1])
	}
	if !strings.HasPrefix(resp.Output[1].Content[0].ImageURL, "data:image/png;base64,") {
		t.Errorf("expected image data URL, got %s", resp.Output[1].Content[0].ImageURL)
	}
}

func TestServeHTTPSSEStreamWithImage(t *testing.T) {
	ch, msgBus, store := newTestChannelWithStore(t, "secret")
	defer ch.Stop(context.Background())

	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	_ = os.WriteFile(imgPath, []byte("fake image bytes"), 0644)
	ref, _ := store.Store(imgPath, media.MediaMeta{
		Filename:    "test.png",
		ContentType: "image/png",
	}, "test-scope")

	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				time.Sleep(20 * time.Millisecond)
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "Image follows:",
				})
				ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Parts: []bus.MediaPart{
						{Type: "image", Ref: ref, Filename: "test.png", ContentType: "image/png"},
					},
				})
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "",
					Context: bus.InboundContext{Raw: map[string]string{"message_kind": "turn_end"}},
				})
			}
		}
	}()

	body, _ := json.Marshal(CreateResponseRequest{Input: "show me", Stream: true})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	bodyStr := rr.Body.String()

	// Must contain standard SSE framing.
	if !strings.Contains(bodyStr, "event: response.in_progress") {
		t.Error("missing response.in_progress")
	}
	if !strings.Contains(bodyStr, "event: response.completed") {
		t.Error("missing response.completed")
	}
	if !strings.Contains(bodyStr, "data: [DONE]") {
		t.Error("missing [DONE] terminator")
	}

	// Must contain the text item.
	if !strings.Contains(bodyStr, "Image follows:") {
		t.Error("missing text content")
	}

	// Must contain image-specific SSE events.
	if !strings.Contains(bodyStr, `"type":"output_image"`) {
		t.Error("missing output_image content part")
	}
	if !strings.Contains(bodyStr, "data:image/png;base64,") {
		t.Error("missing image data URL in SSE stream")
	}

	// Image events should NOT contain delta events.
	if strings.Contains(bodyStr, "output_image.delta") {
		t.Error("output_image should not produce delta events")
	}
}

func TestServeHTTPJSONResponseTextThenImageThenText(t *testing.T) {
	ch, msgBus, store := newTestChannelWithStore(t, "secret")
	defer ch.Stop(context.Background())

	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "test.png")
	_ = os.WriteFile(imgPath, []byte("img"), 0644)
	ref, _ := store.Store(imgPath, media.MediaMeta{
		Filename:    "test.png",
		ContentType: "image/png",
	}, "test-scope")

	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				time.Sleep(20 * time.Millisecond)
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "Before",
				})
				ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Parts:   []bus.MediaPart{{Type: "image", Ref: ref}},
				})
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "After",
				})
				ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "",
					Context: bus.InboundContext{Raw: map[string]string{"message_kind": "turn_end"}},
				})
			}
		}
	}()

	body, _ := json.Marshal(CreateResponseRequest{Input: "mixed"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	var resp Response
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Output) != 3 {
		t.Fatalf("expected 3 output items, got %d", len(resp.Output))
	}
	if resp.Output[0].Content[0].Text != "Before" {
		t.Errorf("expected 'Before', got %s", resp.Output[0].Content[0].Text)
	}
	if resp.Output[1].Content[0].Type != "output_image" {
		t.Errorf("expected image at index 1, got %s", resp.Output[1].Content[0].Type)
	}
	if resp.Output[2].Content[0].Text != "After" {
		t.Errorf("expected 'After', got %s", resp.Output[2].Content[0].Text)
	}
}
