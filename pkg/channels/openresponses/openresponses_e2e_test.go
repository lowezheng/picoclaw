package openresponses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	e2eURL     = "http://localhost:18790/v1/responses"
	e2eToken   = "570694ff7910121aaf9feea5f42e6263"
	e2eTimeout = 60 * time.Second
)

// e2eConfig is the fixed endpoint configuration.
// Set OPENRESPONSES_SKIP_E2E=1 to skip all e2e tests (e.g. in CI).
type e2eConfig struct {
	URL     string
	Token   string
	Timeout time.Duration
	Skip    bool
}

func loadE2EConfig(t *testing.T) e2eConfig {
	if os.Getenv("OPENRESPONSES_SKIP_E2E") == "1" {
		return e2eConfig{Skip: true}
	}

	return e2eConfig{
		URL:     e2eURL,
		Token:   e2eToken,
		Timeout: e2eTimeout,
	}
}

func skipIfNotReady(t *testing.T, c e2eConfig) {
	if c.Skip {
		t.Skip("e2e tests skipped by OPENRESPONSES_SKIP_E2E=1")
	}
}

// postE2E 发送一个真实的 HTTP POST 请求到运行中的 PicoClaw 服务。
// 非流式请求使用 context 控制超时；流式（SSE）请求仅依赖 http.Client.Timeout，
// 避免函数返回时的 cancel() 中断正在读取的响应流。
func postE2E(t *testing.T, cfg e2eConfig, body any, stream bool) (*http.Response, error) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}

	var req *http.Request
	if stream {
		req, err = http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(b))
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(b))
	}
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	client := &http.Client{Timeout: cfg.Timeout}
	return client.Do(req)
}

// --- E2E Tests ---

// TestE2E_BasicJSON 对应 README 示例 1：基本非流式请求。
func TestE2E_BasicJSON(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	resp, err := postE2E(t, cfg, map[string]any{
		"input": "Hello, how are you?",
	}, false)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON, got %s", ct)
	}

	var result Response
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, string(body))
	}
	assertBasicResponse(t, result)
	if result.Output[0].Content[0].Text == "" {
		t.Error("expected non-empty AI response text")
	}
	t.Logf("AI response: %s", result.Output[0].Content[0].Text)
}

// TestE2E_ConversationID 对应 README 示例 2：带 conversation_id 的会话。
func TestE2E_ConversationID(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	convID := "e2e_conv_" + fmt.Sprintf("%d", time.Now().Unix())

	// First turn.
	resp1, err := postE2E(t, cfg, map[string]any{
		"input":           "My name is Alice.",
		"conversation_id": convID,
	}, false)
	if err != nil {
		t.Fatalf("turn 1 request failed: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("turn 1 expected 200, got %d: %s", resp1.StatusCode, string(body1))
	}
	var r1 Response
	if err := json.Unmarshal(body1, &r1); err != nil {
		t.Fatalf("turn 1 decode: %v", err)
	}
	if r1.ConversationID != convID {
		t.Errorf("turn 1: expected conversation_id=%q, got %q", convID, r1.ConversationID)
	}
	t.Logf("turn 1 AI response: %s", r1.Output[0].Content[0].Text)

	// Second turn with same conversation_id.
	resp2, err := postE2E(t, cfg, map[string]any{
		"input":           "What is my name?",
		"conversation_id": convID,
	}, false)
	if err != nil {
		t.Fatalf("turn 2 request failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("turn 2 expected 200, got %d: %s", resp2.StatusCode, string(body2))
	}
	var r2 Response
	if err := json.Unmarshal(body2, &r2); err != nil {
		t.Fatalf("turn 2 decode: %v", err)
	}
	if r2.ConversationID != convID {
		t.Errorf("turn 2: expected conversation_id=%q, got %q", convID, r2.ConversationID)
	}
	t.Logf("turn 2 AI response: %s", r2.Output[0].Content[0].Text)
	// The AI should mention "Alice" since it's the same conversation.
	if !strings.Contains(strings.ToLower(r2.Output[0].Content[0].Text), "alice") {
		t.Error("turn 2: AI did not recall the name 'Alice' from previous turn")
	}
}

// TestE2E_SSEStream 对应 README 示例 3：SSE 流式请求。
func TestE2E_SSEStream(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	resp, err := postE2E(t, cfg, map[string]any{
		"input":  "Tell me a short story in one sentence.",
		"stream": true,
	}, true)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected SSE, got %s", ct)
	}

	// Parse SSE events with a large buffer (some events embed full Response JSON).
	scanner := bufio.NewScanner(resp.Body)
	const maxScanTokenSize = 2 * 1024 * 1024 // 2 MB
	scanBuf := make([]byte, 0, 64*1024)
	scanner.Buffer(scanBuf, maxScanTokenSize)

	var events []sseEvent
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
	if err := scanner.Err(); err != nil {
		t.Fatalf("SSE scan error: %v", err)
	}

	// Debug: log received events.
	for i, ev := range events {
		preview := ev.data
		//if len(preview) > 120 {
		//	preview = preview[:120] + "..."
		//}
		t.Logf("SSE event[%d]: %s | data: %s", i, ev.event, preview)
	}

	// Validate event sequence.
	expected := []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(events) != len(expected) {
		t.Fatalf("expected %d events, got %d", len(expected), len(events))
	}
	for i, want := range expected {
		if events[i].event != want {
			t.Errorf("event[%d]: expected %q, got %q", i, want, events[i].event)
		}
	}

	// Delta should contain actual text.
	var deltaText string
	for _, ev := range events {
		if ev.event == "response.output_text.delta" {
			var evt ResponseEvent
			if err := json.Unmarshal([]byte(ev.data), &evt); err == nil {
				deltaText = evt.Delta
			}
		}
	}
	if deltaText == "" {
		t.Error("expected non-empty delta text")
	} else {
		t.Logf("SSE delta text: %s", deltaText)
	}

	// Final response.completed should be a valid Response.
	last := events[len(events)-1]
	if last.event == "response.completed" {
		var finalEvt ResponseEvent
		if err := json.Unmarshal([]byte(last.data), &finalEvt); err == nil {
			assertBasicResponse(t, finalEvt.Response)
		}
	}
}

// TestE2E_ArrayInput 对应 README 示例 4：数组输入格式。
func TestE2E_ArrayInput(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	resp, err := postE2E(t, cfg, map[string]any{
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": "Explain quantum computing in one sentence."},
		},
		"conversation_id": "e2e_array_" + fmt.Sprintf("%d", time.Now().Unix()),
	}, false)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var result Response
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertBasicResponse(t, result)
	if result.Output[0].Content[0].Text == "" {
		t.Error("expected non-empty AI response text")
	}
	t.Logf("AI response: %s", result.Output[0].Content[0].Text)
}

// TestE2E_InvalidToken 对应 README 示例 5：无效 token。
func TestE2E_InvalidToken(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	b, _ := json.Marshal(map[string]any{"input": "test"})
	req, _ := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-token")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 401, got %d: %s", resp.StatusCode, string(body))
	}
}

// TestE2E_EmptyInput 对应 README 示例 6：空输入。
func TestE2E_EmptyInput(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	resp, err := postE2E(t, cfg, map[string]any{"input": "   "}, false)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 400, got %d: %s", resp.StatusCode, string(body))
	}
}

// TestE2E_NoAuth 缺少 Authorization 头。
func TestE2E_NoAuth(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	b, _ := json.Marshal(map[string]any{"input": "hello"})
	req, _ := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 401, got %d: %s", resp.StatusCode, string(body))
	}
}

// TestE2E_MalformedJSON 畸形 JSON。
func TestE2E_MalformedJSON(t *testing.T) {
	cfg := loadE2EConfig(t)
	skipIfNotReady(t, cfg)

	req, _ := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader([]byte(`{not json`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 400, got %d: %s", resp.StatusCode, string(body))
	}
}

// --- Helpers ---

func assertBasicResponse(t *testing.T, r Response) {
	t.Helper()
	if r.Object != "response" {
		t.Errorf("expected object=response, got %s", r.Object)
	}
	if r.Status != "completed" {
		t.Errorf("expected status=completed, got %s", r.Status)
	}
	if r.ID == "" {
		t.Error("expected non-empty response id")
	}
	if r.CreatedAt == 0 {
		t.Error("expected non-zero created_at")
	}
	if len(r.Output) == 0 {
		t.Fatal("expected at least one output item")
	}
	item := r.Output[0]
	if item.Type != "message" {
		t.Errorf("expected output[0].type=message, got %s", item.Type)
	}
	if item.Status != "completed" {
		t.Errorf("expected output[0].status=completed, got %s", item.Status)
	}
	if item.Role != "assistant" {
		t.Errorf("expected output[0].role=assistant, got %s", item.Role)
	}
	if len(item.Content) == 0 {
		t.Fatal("expected at least one content block")
	}
	if item.Content[0].Type != "output_text" {
		t.Errorf("expected content[0].type=output_text, got %s", item.Content[0].Type)
	}
}
