package openresponses

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests require a running picoclaw server with openresponses channel configured.
// Token is read from ~/.picoclaw/config.json (openresponses.settings.token).
// Server must be listening on localhost:8080 before running these tests.

const testBaseURL = "http://localhost:18790/v1/responses"

func loadTokenFromConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".picoclaw", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		ChannelList map[string]struct {
			Settings struct {
				Token string `json:"token"`
			} `json:"settings"`
		} `json:"channel_list"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	ch := cfg.ChannelList["openresponses"]
	return ch.Settings.Token
}

func testToken() string {
	if tok := os.Getenv("OPENRESPONSES_TOKEN"); tok != "" {
		return tok
	}
	if tok := loadTokenFromConfig(); tok != "" {
		return tok
	}
	return "test-token-123"
}

func dumpRequest(t *testing.T, req *http.Request, body string) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s %s %s\n", req.Method, req.URL.RequestURI(), req.Proto))
	b.WriteString(fmt.Sprintf("Host: %s\n", req.URL.Host))
	for k, v := range req.Header {
		b.WriteString(fmt.Sprintf("%s: %s\n", k, strings.Join(v, ", ")))
	}
	if body != "" {
		b.WriteString("\n" + body)
	}
	t.Logf("\n=== REQUEST ===\n%s\n=== END REQUEST ===", b.String())
}

func dumpResponse(t *testing.T, resp *http.Response, body string) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s %s\n", resp.Proto, resp.Status))
	for k, v := range resp.Header {
		b.WriteString(fmt.Sprintf("%s: %s\n", k, strings.Join(v, ", ")))
	}
	if body != "" {
		b.WriteString("\n" + body)
	}
	t.Logf("\n=== RESPONSE ===\n%s\n=== END RESPONSE ===", b.String())
}

func doPost(t *testing.T, path, body string, withAuth bool) *http.Response {
	t.Helper()
	url := testBaseURL
	if path != "" {
		url = testBaseURL + path
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+testToken())
	}
	dumpRequest(t, req, body)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	respBody := readBody(t, resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(respBody))
	dumpResponse(t, resp, respBody)
	return resp
}

func doGet(t *testing.T, path string) *http.Response {
	t.Helper()
	url := testBaseURL
	if path != "" {
		url = testBaseURL + path
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken())
	dumpRequest(t, req, "")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	respBody := readBody(t, resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(respBody))
	dumpResponse(t, resp, respBody)
	return resp
}

func readBody(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func readSSE(t *testing.T, body io.Reader, timeout time.Duration) []sseEvent {
	t.Helper()

	// Read all raw SSE text first so we can print it
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read SSE body: %v", err)
	}

	var events []sseEvent
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
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

// -- Auth & validation --

func TestIntegration_AuthFailure(t *testing.T) {
	resp := doPost(t, "", `{"input":"hello"}`, false)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Type != "invalid_request" {
		t.Errorf("expected error type invalid_request, got %q", errResp.Error.Type)
	}
}

func TestIntegration_EmptyInput(t *testing.T) {
	resp := doPost(t, "", `{"input":""}`, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Type != "invalid_request" {
		t.Errorf("expected error type invalid_request, got %q", errResp.Error.Type)
	}
}

func TestIntegration_MethodNotAllowed(t *testing.T) {
	resp := doGet(t, "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

// -- Non-streaming response --

func TestIntegration_NonStreaming_Text(t *testing.T) {
	convID := "conv_integ_text_" + time.Now().Format("150405")
	body := `{"input":"Say exactly: Hello from integration test","conversation_id":"` + convID + `"}`
	resp := doPost(t, "", body, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyStr := readBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, bodyStr)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status completed, got %q", result.Status)
	}
	if result.ConversationID == "" {
		t.Error("expected conversation_id")
	}
	if len(result.Output) == 0 {
		t.Fatal("expected at least 1 output item")
	}
	if result.Output[0].Type != "message" {
		t.Errorf("expected message type, got %q", result.Output[0].Type)
	}
	if len(result.Output[0].Content) == 0 {
		t.Fatal("expected content")
	}
}

func loadTestPDFDataURL(t *testing.T) string {
	t.Helper()
	pdfPath := filepath.Join("testdata", "test_upload.pdf")
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("read test pdf: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return "data:application/pdf;base64," + encoded
}

func TestIntegration_NonStreaming_MultiPartInput(t *testing.T) {
	convID := "conv_integ_multi_" + time.Now().Format("150405")
	pdfDataURL := loadTestPDFDataURL(t)
	input := []map[string]any{
		{"type": "input_text", "content": "请总结上传的PDF文件中的关键信息，用一句话概括"},
		{"type": "input_file", "content": pdfDataURL},
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	body := `{"input":` + string(inputJSON) + `,"conversation_id":"` + convID + `"}`
	resp := doPost(t, "", body, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyStr := readBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, bodyStr)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("expected status completed, got %q", result.Status)
	}
	if len(result.Output) == 0 {
		t.Fatal("expected output items")
	}
	if result.Output[0].Type != "message" {
		t.Errorf("expected message type, got %q", result.Output[0].Type)
	}
	if len(result.Output[0].Content) == 0 {
		t.Fatal("expected content")
	}
	// Collect all text across all output items (non-streaming may include tool calls as items)
	var allText strings.Builder
	for _, item := range result.Output {
		if item.Type == "message" {
			for _, c := range item.Content {
				if c.Type == "output_text" {
					allText.WriteString(c.Text)
				}
			}
		}
	}
	lower := strings.ToLower(allText.String())
	if !strings.Contains(lower, "sky") && !strings.Contains(lower, "blue") && !strings.Contains(lower, "water") && !strings.Contains(lower, "boil") && !strings.Contains(lower, "openresponses") {
		t.Errorf("response does not reference PDF content; got: %q", allText.String())
	}
}

// -- Streaming SSE response --

func TestIntegration_Streaming_Text(t *testing.T) {
	convID := "conv_integ_stream_" + time.Now().Format("150405")
	body := `{"input":"Say exactly: Hello from SSE stream","stream":true,"conversation_id":"` + convID + `"}`
	resp := doPost(t, "", body, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyStr := readBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, bodyStr)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	events := readSSE(t, resp.Body, 30*time.Second)
	if len(events) == 0 {
		t.Fatal("expected SSE events, got none")
	}

	// Must start with response.in_progress
	if events[0].Event != "response.in_progress" {
		t.Errorf("first event should be response.in_progress, got %q", events[0].Event)
	}

	// Must end with response.completed and [DONE]
	last := events[len(events)-1]
	if last.Event == "response.completed" {
		// response.completed is second-to-last, [DONE] is last
		if len(events) < 2 {
			t.Fatal("expected at least 2 events including [DONE]")
		}
		if events[len(events)-2].Event != "response.completed" {
			t.Errorf("expected response.completed before [DONE]")
		}
	} else if last.Data != "[DONE]" {
		t.Errorf("expected [DONE] at end, got event=%q data=%q", last.Event, last.Data)
	}

	// Verify text delta events exist
	var hasTextDelta bool
	for _, ev := range events {
		if ev.Event == "response.output_text.delta" {
			hasTextDelta = true
			break
		}
	}
	if !hasTextDelta {
		t.Error("expected at least one response.output_text.delta event")
	}

	// Verify output_item.added exists
	var hasOutputItem bool
	for _, ev := range events {
		if ev.Event == "response.output_item.added" {
			hasOutputItem = true
			break
		}
	}
	if !hasOutputItem {
		t.Error("expected response.output_item.added event")
	}
}

func TestIntegration_Streaming_EventSequence(t *testing.T) {
	convID := "conv_integ_seq_" + time.Now().Format("150405")
	body := `{"input":"Reply with exactly the word: OK","stream":true,"conversation_id":"` + convID + `"}`
	resp := doPost(t, "", body, true)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyStr := readBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, bodyStr)
	}

	events := readSSE(t, resp.Body, 30*time.Second)

	// Verify exact first event sequence for a text response
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(events))
	}
	expectedPrefix := []string{
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
	}
	for i, exp := range expectedPrefix {
		if events[i].Event != exp {
			t.Errorf("event %d: expected %q, got %q", i, exp, events[i].Event)
		}
	}

	// Verify the last events are response.completed and [DONE]
	if len(events) >= 2 {
		secondLast := events[len(events)-2]
		last := events[len(events)-1]
		if secondLast.Event != "response.completed" {
			t.Errorf("expected response.completed as second-last, got %q", secondLast.Event)
		}
		if last.Data != "[DONE]" {
			t.Errorf("expected [DONE] as last, got event=%q data=%q", last.Event, last.Data)
		}
	}
}

// -- Session API --

func TestIntegration_SessionList(t *testing.T) {
	resp := doGet(t, "/sessions")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyStr := readBody(t, resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, bodyStr)
	}

	// Response should be a JSON array (even if empty)
	bodyStr := readBody(t, resp.Body)
	if !strings.HasPrefix(bodyStr, "[") {
		t.Errorf("expected JSON array, got %q", bodyStr)
	}
}

func TestIntegration_SessionDetail_NotFound(t *testing.T) {
	resp := doGet(t, "/sessions/nonexistent_session_12345")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error.Type != "not_found" {
		t.Errorf("expected error type not_found, got %q", errResp.Error.Type)
	}
}
