package openresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
		ctx = c.ctx
	}

	stream, requestID, err := c.dispatch(ctx, conversationID, content)
	if err != nil {
		logger.ErrorCF("openresponses", "Failed to dispatch request", map[string]any{
			"error": err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "server_error", "Failed to process request")
		return
	}

	respID := "resp_" + requestID[4:] // strip "req_" prefix, keep UUID
	msgID := "msg_" + requestID[4:]

	if req.Stream {
		c.writeSSEResponseStream(w, r, stream, respID, msgID, conversationID, req.PreviousResponseID)
	} else {
		c.writeJSONResponseWithStream(w, stream, respID, msgID, conversationID, req.PreviousResponseID)
	}

	// Clean up the pending entry after handler finishes.
	c.pendingMu.Lock()
	delete(c.pending, requestID)
	c.pendingMu.Unlock()
	stream.close()
}

// writeJSONResponseWithStream collects all messages from the stream and
// writes a single completed OpenResponses JSON response.
func (c *OpenResponsesChannel) writeJSONResponseWithStream(
	w http.ResponseWriter,
	stream *pendingStream,
	respID, msgID, conversationID, previousResponseID string,
) {
	var parts []string

	for ev := range stream.events {
		if ev.kind == eventKindText && ev.content != "" {
			parts = append(parts, ev.content)
		}
	}

	content := strings.Join(parts, "\n")
	resp := buildResponse(respID, msgID, conversationID, previousResponseID, content)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.ErrorCF("openresponses", "Failed to encode JSON response", map[string]any{
			"error": err.Error(),
		})
	}
}

// writeSSEResponseStream reads messages from the stream in real time and
// writes them as SSE events. A heartbeat goroutine sends keep-alive comments
// every 5 seconds to prevent proxy timeout during long tool execution.
func (c *OpenResponsesChannel) writeSSEResponseStream(
	w http.ResponseWriter,
	r *http.Request,
	stream *pendingStream,
	respID, msgID, conversationID, previousResponseID string,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		c.writeJSONResponseWithStream(w, stream, respID, msgID, conversationID, previousResponseID)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	seq := 0
	var outputItems []ResponseItem

	// 1. response.created
	resp := buildResponse(respID, msgID, conversationID, previousResponseID, "")
	writeSSEEvent(w, "response.created", ResponseEvent{
		Type:           "response.created",
		SequenceNumber: seq,
		Response:       resp,
	})
	seq++
	flusher.Flush()

	// Start heartbeat goroutine.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				fmt.Fprint(w, ": heartbeat\n\n")
				flusher.Flush()
			case <-ctx.Done():
				return
			case <-stream.done:
				return
			}
		}
	}()

	// Read stream events and emit SSE events.
	msgSeq := 0
	for ev := range stream.events {
		if ev.kind != eventKindText || ev.content == "" {
			continue
		}

		itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
		msgSeq++

		item := ResponseItem{
			Type:    "message",
			ID:      itemID,
			Status:  "completed",
			Role:    "assistant",
			Content: []Content{{Type: "output_text", Text: ev.content}},
		}

		// response.output_item.added
		writeSSEEvent(w, "response.output_item.added", ResponseEvent{
			Type:           "response.output_item.added",
			SequenceNumber: seq,
			OutputIndex:    len(outputItems),
			Item:           item,
		})
		seq++
		flusher.Flush()

		// response.content_part.added
		writeSSEEvent(w, "response.content_part.added", ResponseEvent{
			Type:           "response.content_part.added",
			SequenceNumber: seq,
			ItemID:         itemID,
			OutputIndex:    len(outputItems),
			ContentIndex:   0,
		})
		seq++
		flusher.Flush()

		// response.output_text.delta
		writeSSEEvent(w, "response.output_text.delta", ResponseEvent{
			Type:           "response.output_text.delta",
			SequenceNumber: seq,
			ItemID:         itemID,
			OutputIndex:    len(outputItems),
			ContentIndex:   0,
			Delta:          ev.content,
		})
		seq++
		flusher.Flush()

		// response.output_text.done
		writeSSEEvent(w, "response.output_text.done", ResponseEvent{
			Type:           "response.output_text.done",
			SequenceNumber: seq,
			ItemID:         itemID,
			OutputIndex:    len(outputItems),
			ContentIndex:   0,
		})
		seq++
		flusher.Flush()

		// response.content_part.done
		writeSSEEvent(w, "response.content_part.done", ResponseEvent{
			Type:           "response.content_part.done",
			SequenceNumber: seq,
			ItemID:         itemID,
			OutputIndex:    len(outputItems),
			ContentIndex:   0,
		})
		seq++
		flusher.Flush()

		// response.output_item.done
		writeSSEEvent(w, "response.output_item.done", ResponseEvent{
			Type:           "response.output_item.done",
			SequenceNumber: seq,
			OutputIndex:    len(outputItems),
			Item:           item,
		})
		seq++
		flusher.Flush()

		outputItems = append(outputItems, item)
	}

	// Wait for heartbeat to finish.
	cancel()
	<-heartbeatDone

	// Final response.completed with accumulated output.
	if len(outputItems) > 0 {
		resp.Output = outputItems
	}
	writeSSEEvent(w, "response.completed", ResponseEvent{
		Type:           "response.completed",
		SequenceNumber: seq,
		Response:       resp,
	})
	flusher.Flush()

	// Terminal marker.
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
