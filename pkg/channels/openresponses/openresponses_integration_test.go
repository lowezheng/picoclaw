package openresponses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

const testToken = "570694ff7910121aaf9feea5f42e6263"

// setupIntegration 创建并启动一个 OpenResponsesChannel，返回清理函数。
func setupIntegration(t *testing.T) (*OpenResponsesChannel, *bus.MessageBus, func()) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{}
	bc.SetName("openresponses")
	cfg := &config.OpenResponsesSettings{
		Token:          *config.NewSecureString(testToken),
		RequestTimeout: 5,
	}
	ch, err := NewOpenResponsesChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("failed to create channel: %v", err)
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("failed to start channel: %v", err)
	}
	return ch, msgBus, func() { _ = ch.Stop(context.Background()) }
}

// startMockAgent 启动一个 goroutine 模拟 agent：读取 InboundMessage，延迟后通过 Send() 回复。
func startMockAgent(t *testing.T, ch *OpenResponsesChannel, msgBus *bus.MessageBus) {
	t.Helper()
	go func() {
		for {
			select {
			case msg, ok := <-msgBus.InboundChan():
				if !ok {
					return
				}
				time.Sleep(20 * time.Millisecond)
				_, _ = ch.Send(context.Background(), bus.OutboundMessage{
					Channel: msg.Context.Channel,
					ChatID:  msg.Context.ChatID,
					Content: "Reply: " + msg.Content,
				})
			}
		}
	}()
}

// postJSON 发送一个 POST 请求并返回 httptest.ResponseRecorder。
func postJSON(t *testing.T, ch *OpenResponsesChannel, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)
	return rr
}

// parseSSEEvents 解析 SSE 响应体，返回 event -> data 的切片（保持顺序）。
func parseSSEEvents(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	var current sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if current.event != "" {
				events = append(events, current)
			}
			current = sseEvent{}
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			current.event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			current.data = strings.TrimPrefix(line, "data: ")
		}
	}
	if current.event != "" {
		events = append(events, current)
	}
	return events
}

type sseEvent struct {
	event string
	data  string
}

// --- 1. Basic non-streaming request ---

func TestIntegration_BasicJSONRequest(t *testing.T) {
	ch, msgBus, cleanup := setupIntegration(t)
	defer cleanup()
	startMockAgent(t, ch, msgBus)

	rr := postJSON(t, ch, map[string]any{
		"input": "Hello, how are you?",
	}, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON, got %s", ct)
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Object != "response" {
		t.Errorf("expected object=response, got %s", resp.Object)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status=completed, got %s", resp.Status)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output, got %d", len(resp.Output))
	}
	if resp.Output[0].Type != "message" {
		t.Errorf("expected type=message, got %s", resp.Output[0].Type)
	}
	if len(resp.Output[0].Content) != 1 {
		t.Fatalf("expected 1 content, got %d", len(resp.Output[0].Content))
	}
	wantText := "Reply: Hello, how are you?"
	if resp.Output[0].Content[0].Text != wantText {
		t.Errorf("expected text=%q, got %q", wantText, resp.Output[0].Content[0].Text)
	}
}

// --- 2. Request with conversation_id (session continuity) ---

func TestIntegration_ConversationIDSession(t *testing.T) {
	ch, msgBus, cleanup := setupIntegration(t)
	defer cleanup()
	startMockAgent(t, ch, msgBus)

	convID := "conv_test_42"
	rr := postJSON(t, ch, map[string]any{
		"input":           "What is the weather like?",
		"conversation_id": convID,
	}, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ConversationID != convID {
		t.Errorf("expected conversation_id=%q, got %q", convID, resp.ConversationID)
	}
	if !strings.HasPrefix(resp.ID, "resp_") {
		t.Errorf("expected id to start with 'resp_', got %s", resp.ID)
	}
}

// --- 3. SSE streaming request ---

func TestIntegration_SSEStreamEvents(t *testing.T) {
	ch, msgBus, cleanup := setupIntegration(t)
	defer cleanup()
	startMockAgent(t, ch, msgBus)

	rr := postJSON(t, ch, map[string]any{
		"input":  "Tell me a short story",
		"stream": true,
	}, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected SSE, got %s", ct)
	}

	events := parseSSEEvents(t, rr.Body.String())

	// Expected event sequence per README.
	expectedEvents := []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}

	if len(events) != len(expectedEvents) {
		t.Fatalf("expected %d events, got %d", len(expectedEvents), len(events))
	}

	for i, want := range expectedEvents {
		if events[i].event != want {
			t.Errorf("event[%d]: expected %q, got %q", i, want, events[i].event)
		}
	}

	// Verify delta contains actual content.
	deltaFound := false
	for _, ev := range events {
		if ev.event == "response.output_text.delta" {
			deltaFound = true
			if !strings.Contains(ev.data, "Reply: ") {
				t.Errorf("delta missing reply prefix: %s", ev.data)
			}
		}
	}
	if !deltaFound {
		t.Error("missing response.output_text.delta event")
	}

	// Verify the raw body contains the terminal [DONE] marker.
	if !strings.Contains(rr.Body.String(), "data: [DONE]") {
		t.Error("missing [DONE] terminator in SSE body")
	}
}

// --- 4. Array input format ---

func TestIntegration_ArrayInput(t *testing.T) {
	ch, msgBus, cleanup := setupIntegration(t)
	defer cleanup()
	startMockAgent(t, ch, msgBus)

	rr := postJSON(t, ch, map[string]any{
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": "Explain quantum computing"},
		},
		"conversation_id": "conv_456",
	}, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ConversationID != "conv_456" {
		t.Errorf("expected conversation_id=conv_456, got %s", resp.ConversationID)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output, got %d", len(resp.Output))
	}
	wantText := "Reply: Explain quantum computing"
	if resp.Output[0].Content[0].Text != wantText {
		t.Errorf("expected text=%q, got %q", wantText, resp.Output[0].Content[0].Text)
	}
}

// --- 5. Invalid token (should return 401) ---

func TestIntegration_InvalidToken(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]any{"input": "test"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-token")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
	var errResp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	errObj, ok := errResp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %T", errResp["error"])
	}
	if errObj["code"] != "unauthorized" {
		t.Errorf("expected code=unauthorized, got %v", errObj["code"])
	}
}

// --- 6. Empty input (should return 400) ---

func TestIntegration_EmptyInput(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()

	rr := postJSON(t, ch, map[string]any{"input": "   "}, nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// --- 7. Method not allowed ---

func TestIntegration_MethodNotAllowed(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

// --- 8. Bad Content-Type ---

func TestIntegration_BadContentType(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// --- 9. Payload too large ---

func TestIntegration_PayloadTooLarge(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()
	ch.config.MaxBodySize = 10

	body := []byte(`{"input":"this payload is definitely more than ten bytes"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rr.Code)
	}
}

// --- 10. Channel not running ---

func TestIntegration_ChannelNotRunning(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	cleanup() // stop immediately

	rr := postJSON(t, ch, map[string]any{"input": "hello"}, nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

// --- 11. Timeout fallback (empty content, still 200) ---

func TestIntegration_TimeoutFallback(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()
	ch.config.RequestTimeout = 1 // 1 second

	// Do NOT start mock agent, so request will time out.
	rr := postJSON(t, ch, map[string]any{"input": "hello"}, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on timeout fallback, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status=completed, got %s", resp.Status)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("expected 1 output, got %d", len(resp.Output))
	}
	if resp.Output[0].Content[0].Text != "" {
		t.Errorf("expected empty text on timeout, got %q", resp.Output[0].Content[0].Text)
	}
}

// --- 12. Concurrent requests ---

func TestIntegration_ConcurrentRequests(t *testing.T) {
	ch, msgBus, cleanup := setupIntegration(t)
	defer cleanup()
	startMockAgent(t, ch, msgBus)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			rr := postJSON(t, ch, map[string]any{
				"input":           fmt.Sprintf("request %d", idx),
				"conversation_id": fmt.Sprintf("conv_%d", idx),
			}, nil)
			if rr.Code != http.StatusOK {
				t.Errorf("request %d: expected 200, got %d: %s", idx, rr.Code, rr.Body.String())
				return
			}
			var resp Response
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Errorf("request %d: decode error: %v", idx, err)
				return
			}
			want := fmt.Sprintf("Reply: request %d", idx)
			if resp.Output[0].Content[0].Text != want {
				t.Errorf("request %d: expected %q, got %q", idx, want, resp.Output[0].Content[0].Text)
			}
			wantConv := fmt.Sprintf("conv_%d", idx)
			if resp.ConversationID != wantConv {
				t.Errorf("request %d: expected conv=%q, got %q", idx, wantConv, resp.ConversationID)
			}
		}(i)
	}

	wg.Wait()
}

// --- 13. Multiple requests with same conversation_id ---

func TestIntegration_ReuseConversationID(t *testing.T) {
	ch, msgBus, cleanup := setupIntegration(t)
	defer cleanup()
	startMockAgent(t, ch, msgBus)

	convID := "conv_reused"

	for i := 0; i < 3; i++ {
		rr := postJSON(t, ch, map[string]any{
			"input":           fmt.Sprintf("turn %d", i),
			"conversation_id": convID,
		}, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("turn %d: expected 200, got %d: %s", i, rr.Code, rr.Body.String())
		}
		var resp Response
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("turn %d: decode error: %v", i, err)
		}
		if resp.ConversationID != convID {
			t.Errorf("turn %d: expected conv=%q, got %q", i, convID, resp.ConversationID)
		}
		want := fmt.Sprintf("Reply: turn %d", i)
		if resp.Output[0].Content[0].Text != want {
			t.Errorf("turn %d: expected %q, got %q", i, want, resp.Output[0].Content[0].Text)
		}
	}
}

// --- 14. No Authorization header ---

func TestIntegration_MissingAuth(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]any{"input": "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// --- 15. Malformed JSON body ---

func TestIntegration_MalformedJSON(t *testing.T) {
	ch, _, cleanup := setupIntegration(t)
	defer cleanup()

	body := []byte(`{not valid json`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ch.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}
