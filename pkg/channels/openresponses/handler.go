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

	stream, queued, err := c.dispatch(ctx, conversationID, content)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "request_in_progress", err.Error())
		return
	}

	if queued {
		logger.InfoCF("openresponses", "Sending queued response", map[string]any{
			"conversation_id": conversationID,
		})
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, ": queue-%s\n\n", time.Now().Format("2006-01-02"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}

	respID := "resp_" + conversationID
	msgID := "msg_" + conversationID

	if req.Stream {
		c.writeSSEResponseStream(w, r, stream, respID, msgID, conversationID, req.PreviousResponseID)
	} else {
		c.writeJSONResponseWithStream(w, stream, respID, msgID, conversationID, req.PreviousResponseID)
	}
}

// writeJSONResponseWithStream collects all messages from the stream and
// writes a single completed OpenResponses JSON response.
func (c *OpenResponsesChannel) writeJSONResponseWithStream(
	w http.ResponseWriter,
	stream *pendingStream,
	respID, msgID, conversationID, previousResponseID string,
) {
	var outputItems []ResponseItem
	msgSeq := 0

	for ev := range stream.events {
		switch ev.kind {
		case eventKindText:
			itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
			msgSeq++
			outputItems = append(outputItems, ResponseItem{
				Type:   "message",
				ID:     itemID,
				Status: "completed",
				Role:   "assistant",
				Content: []Content{
					{Type: "output_text", Text: ev.content},
				},
			})
		case eventKindReasoning:
			itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
			msgSeq++
			outputItems = append(outputItems, ResponseItem{
				Type:   "reasoning",
				ID:     itemID,
				Status: "completed",
				Content: []Content{
					{Type: "reasoning_text", Text: ev.content},
				},
			})
		}
	}

	resp := Response{
		ID:                 respID,
		Object:             "response",
		CreatedAt:          nowUnix(),
		Status:             "completed",
		ConversationID:     conversationID,
		PreviousResponseID: previousResponseID,
		Usage:              Usage{InputTokens: 0, OutputTokens: 0},
	}
	if len(outputItems) > 0 {
		resp.Output = outputItems
	} else {
		resp.Output = []ResponseItem{
			{
				Type:   "message",
				ID:     msgID,
				Status: "completed",
				Role:   "assistant",
				Content: []Content{
					{Type: "output_text", Text: ""},
				},
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.ErrorCF("openresponses", "Failed to encode JSON response", map[string]any{
			"error": err.Error(),
		})
	}
}

// writeSSEResponseStream reads messages from the stream in real time and
// writes them as SSE events following the OpenResponses streaming protocol.
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

	// Extend write deadline on every flush so the 30s gateway WriteTimeout
	// behaves like an idle timeout rather than a hard request limit.
	rc := http.NewResponseController(w)
	flushAndExtend := func() {
		flusher.Flush()
		rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}

	seq := 0
	var outputItems []ResponseItem

	resp := Response{
		ID:                 respID,
		Object:             "response",
		CreatedAt:          nowUnix(),
		Status:             "in_progress",
		ConversationID:     conversationID,
		PreviousResponseID: previousResponseID,
		Output:             []ResponseItem{},
	}

	// response.in_progress
	writeSSEEvent(w, "response.in_progress", ResponseEvent{
		Type:           "response.in_progress",
		SequenceNumber: seq,
		Response:       resp,
	})
	seq++
	flushAndExtend()

	// Start heartbeat goroutine to prevent gateway/proxy idle timeout.
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
				fmt.Fprintf(w, ": heartbeat-%s\n\n", time.Now().Format("15:04:05"))
				flushAndExtend()
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
		if ev.kind == eventKindTurnEnd {
			continue
		}

		itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
		msgSeq++

		switch ev.kind {
		case eventKindText:
			// --- text item sequence ---
			addedItem := ResponseItem{
				Type:    "message",
				ID:      itemID,
				Status:  "in_progress",
				Role:    "assistant",
				Content: []Content{},
			}
			writeSSEEvent(w, "response.output_item.added", ResponseEvent{
				Type:           "response.output_item.added",
				SequenceNumber: seq,
				OutputIndex:    len(outputItems),
				Item:           addedItem,
			})
			seq++
			flushAndExtend()

			// response.content_part.added
			writeSSEEvent(w, "response.content_part.added", ResponseEvent{
				Type:           "response.content_part.added",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "output_text", Text: ""},
			})
			seq++
			flushAndExtend()

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
			flushAndExtend()

			// response.output_text.done
			writeSSEEvent(w, "response.output_text.done", ResponseEvent{
				Type:           "response.output_text.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
			})
			seq++
			flushAndExtend()

			// response.content_part.done
			writeSSEEvent(w, "response.content_part.done", ResponseEvent{
				Type:           "response.content_part.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "output_text", Text: ev.content},
			})
			seq++
			flushAndExtend()

			// response.output_item.done
			doneItem := ResponseItem{
				Type:    "message",
				ID:      itemID,
				Status:  "completed",
				Role:    "assistant",
				Content: []Content{{Type: "output_text", Text: ev.content}},
			}
			writeSSEEvent(w, "response.output_item.done", ResponseEvent{
				Type:           "response.output_item.done",
				SequenceNumber: seq,
				OutputIndex:    len(outputItems),
				Item:           doneItem,
			})
			seq++
			flushAndExtend()

			outputItems = append(outputItems, doneItem)

		case eventKindReasoning:
			// --- reasoning item sequence ---
			addedItem := ResponseItem{
				Type:    "reasoning",
				ID:      itemID,
				Status:  "in_progress",
				Content: []Content{},
			}
			writeSSEEvent(w, "response.output_item.added", ResponseEvent{
				Type:           "response.output_item.added",
				SequenceNumber: seq,
				OutputIndex:    len(outputItems),
				Item:           addedItem,
			})
			seq++
			flushAndExtend()

			// response.content_part.added
			writeSSEEvent(w, "response.content_part.added", ResponseEvent{
				Type:           "response.content_part.added",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "reasoning_text", Text: ""},
			})
			seq++
			flushAndExtend()

			// response.reasoning_text.delta
			writeSSEEvent(w, "response.reasoning_text.delta", ResponseEvent{
				Type:           "response.reasoning_text.delta",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Delta:          ev.content,
			})
			seq++
			flushAndExtend()

			// response.reasoning_text.done
			writeSSEEvent(w, "response.reasoning_text.done", ResponseEvent{
				Type:           "response.reasoning_text.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
			})
			seq++
			flushAndExtend()

			// response.content_part.done
			writeSSEEvent(w, "response.content_part.done", ResponseEvent{
				Type:           "response.content_part.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "reasoning_text", Text: ev.content},
			})
			seq++
			flushAndExtend()

			// response.output_item.done
			doneItem := ResponseItem{
				Type:    "reasoning",
				ID:      itemID,
				Status:  "completed",
				Content: []Content{{Type: "reasoning_text", Text: ev.content}},
			}
			writeSSEEvent(w, "response.output_item.done", ResponseEvent{
				Type:           "response.output_item.done",
				SequenceNumber: seq,
				OutputIndex:    len(outputItems),
				Item:           doneItem,
			})
			seq++
			flushAndExtend()

			outputItems = append(outputItems, doneItem)
		}
	}

	// Wait for heartbeat to finish before sending final events.
	cancel()
	<-heartbeatDone

	// Final response.completed with accumulated output.
	resp.Status = "completed"
	resp.Usage = Usage{InputTokens: 0, OutputTokens: 0}
	if len(outputItems) > 0 {
		resp.Output = outputItems
	}
	writeSSEEvent(w, "response.completed", ResponseEvent{
		Type:           "response.completed",
		SequenceNumber: seq,
		Response:       resp,
	})
	flushAndExtend()

	// Terminal marker.
	fmt.Fprint(w, "data: [DONE]\n\n")
	flushAndExtend()
}

func buildResponse(respID, msgID, conversationID, previousResponseID, content string) Response {
	return Response{
		ID:                 respID,
		Object:             "response",
		CreatedAt:          nowUnix(),
		Status:             "completed",
		ConversationID:     conversationID,
		PreviousResponseID: previousResponseID,
		Usage:              Usage{InputTokens: 0, OutputTokens: 0},
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
	var errType string
	switch {
	case statusCode >= 500:
		errType = "server_error"
	case statusCode == http.StatusTooManyRequests:
		errType = "rate_limit_exceeded"
	case statusCode == http.StatusNotFound:
		errType = "not_found"
	default:
		errType = "invalid_request"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":    errType,
			"code":    code,
			"message": message,
		},
	})
}
