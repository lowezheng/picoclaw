package openresponses

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// WebhookPath implements channels.WebhookHandler.
func (c *OpenResponsesChannel) WebhookPath() string {
	return c.endpointPath()
}

// ServeHTTP implements http.Handler.
func (c *OpenResponsesChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, `{"error":"channel not running"}`, http.StatusServiceUnavailable)
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is supported")
		return
	}

	if !c.authenticate(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or missing authorization token")
		return
	}

	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if !strings.HasPrefix(contentType, "application/json") {
		writeError(w, http.StatusBadRequest, "invalid_content_type", "Content-Type must be application/json")
		return
	}

	maxSize := c.maxBodySize()
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", fmt.Sprintf("Request body exceeds %d bytes", maxSize))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to read request body")
		return
	}

	var req CreateResponseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Failed to parse JSON request body")
		return
	}

	c.handleCreateResponse(w, r, &req)
}

// authenticate checks the Authorization header for a Bearer token.
func (c *OpenResponsesChannel) authenticate(r *http.Request) bool {
	token := c.config.Token.String()
	if token == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return strings.TrimSpace(after) == token
	}
	return false
}

// handleCreateResponse orchestrates the request through the PicoClaw agent.
func (c *OpenResponsesChannel) handleCreateResponse(w http.ResponseWriter, r *http.Request, req *CreateResponseRequest) {
	content := normalizeInput(req.Input)
	if strings.TrimSpace(content) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Input content is empty")
		return
	}

	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		conversationID = "conv_" + uuid.New().String()
	}

	ctx := r.Context()
	if c.ctx != nil {
		// Prefer the channel lifecycle context so shutdown cancels in-flight requests.
		ctx = c.ctx
	}

	p, requestID, err := c.dispatch(ctx, conversationID, content)
	if err != nil {
		logger.ErrorCF("openresponses", "Failed to dispatch request", map[string]any{
			"error": err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "server_error", "Failed to process request")
		return
	}

	// Drain the stream, collecting text events until done or cancelled.
	var contentParts []string
	drainLoop:
	for {
		select {
		case ev, ok := <-p.events:
			if !ok {
				break drainLoop
			}
			if ev.kind == eventKindText {
				contentParts = append(contentParts, ev.content)
			}
		case <-r.Context().Done():
			writeError(w, http.StatusServiceUnavailable, "server_error", "Request cancelled by client")
			return
		case <-c.ctx.Done():
			writeError(w, http.StatusServiceUnavailable, "server_error", "Channel is shutting down")
			return
		}
	}

	content = strings.Join(contentParts, "")

	respID := "resp_" + requestID[4:] // strip "req_" prefix, keep UUID
	msgID := "msg_" + requestID[4:]

	if req.Stream {
		c.writeSSEResponse(w, respID, msgID, conversationID, req.PreviousResponseID, content)
	} else {
		c.writeJSONResponse(w, respID, msgID, conversationID, req.PreviousResponseID, content)
	}
}

// writeJSONResponse writes a completed OpenResponses JSON response.
func (c *OpenResponsesChannel) writeJSONResponse(w http.ResponseWriter, respID, msgID, conversationID, previousResponseID, content string) {
	resp := buildResponse(respID, msgID, conversationID, previousResponseID, content)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.ErrorCF("openresponses", "Failed to encode JSON response", map[string]any{
			"error": err.Error(),
		})
	}
}

// writeSSEResponse writes a minimal but spec-compliant SSE stream.
func (c *OpenResponsesChannel) writeSSEResponse(w http.ResponseWriter, respID, msgID, conversationID, previousResponseID, content string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Fallback: write as single JSON event if streaming is not supported.
		c.writeJSONResponse(w, respID, msgID, conversationID, previousResponseID, content)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	resp := buildResponse(respID, msgID, conversationID, previousResponseID, content)
	seq := 0

	// 1. response.created
	writeSSEEvent(w, "response.created", ResponseEvent{
		Type:           "response.created",
		SequenceNumber: seq,
		Response:       resp,
	})
	seq++
	flusher.Flush()

	// 2. response.output_item.added
	item := resp.Output[0]
	writeSSEEvent(w, "response.output_item.added", ResponseEvent{
		Type:           "response.output_item.added",
		SequenceNumber: seq,
		OutputIndex:    0,
		Item:           item,
	})
	seq++
	flusher.Flush()

	// 3. response.content_part.added
	writeSSEEvent(w, "response.content_part.added", ResponseEvent{
		Type:           "response.content_part.added",
		SequenceNumber: seq,
		ItemID:         msgID,
		OutputIndex:    0,
		ContentIndex:   0,
	})
	seq++
	flusher.Flush()

	// 4. response.output_text.delta
	writeSSEEvent(w, "response.output_text.delta", ResponseEvent{
		Type:           "response.output_text.delta",
		SequenceNumber: seq,
		ItemID:         msgID,
		OutputIndex:    0,
		ContentIndex:   0,
		Delta:          content,
	})
	seq++
	flusher.Flush()

	// 5. response.output_text.done
	writeSSEEvent(w, "response.output_text.done", ResponseEvent{
		Type:           "response.output_text.done",
		SequenceNumber: seq,
		ItemID:         msgID,
		OutputIndex:    0,
		ContentIndex:   0,
	})
	seq++
	flusher.Flush()

	// 6. response.content_part.done
	writeSSEEvent(w, "response.content_part.done", ResponseEvent{
		Type:           "response.content_part.done",
		SequenceNumber: seq,
		ItemID:         msgID,
		OutputIndex:    0,
		ContentIndex:   0,
	})
	seq++
	flusher.Flush()

	// 7. response.output_item.done
	writeSSEEvent(w, "response.output_item.done", ResponseEvent{
		Type:           "response.output_item.done",
		SequenceNumber: seq,
		OutputIndex:    0,
		Item:           item,
	})
	seq++
	flusher.Flush()

	// 8. response.completed
	writeSSEEvent(w, "response.completed", ResponseEvent{
		Type:           "response.completed",
		SequenceNumber: seq,
		Response:       resp,
	})
	flusher.Flush()

	// Terminal marker
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func buildResponse(respID, msgID, conversationID, previousResponseID, content string) Response {
	return Response{
		ID:                 respID,
		Object:             "response",
		CreatedAt:          nowUnix(),
		Status:             "completed",
		ConversationID:     conversationID,
		PreviousResponseID: previousResponseID,
		Output: []ResponseItem{
			{
				Type:   "message",
				ID:     msgID,
				Status: "completed",
				Role:   "assistant",
				Content: []Content{
					{Type: "output_text", Text: content},
				},
			},
		},
	}
}

func writeSSEEvent(w http.ResponseWriter, eventType string, data any) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		logger.ErrorCF("openresponses", "Failed to marshal SSE event", map[string]any{
			"error": err.Error(),
		})
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(jsonBytes))
}

func writeError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":    "invalid_request_error",
			"code":    code,
			"message": message,
		},
	})
}
