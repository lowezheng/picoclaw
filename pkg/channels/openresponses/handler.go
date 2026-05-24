package openresponses

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (c *OpenResponsesChannel) WebhookPath() string {
	path := c.cfg.EndpointPath
	if path == "" {
		path = "/v1/responses/"
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

func (c *OpenResponsesChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		writeError(w, http.StatusServiceUnavailable, "server_error", "", "Channel not running")
		return
	}

	base := c.WebhookPath()                      // "/v1/responses/"
	baseNoSlash := strings.TrimSuffix(base, "/") // "/v1/responses"
	chatPath := baseNoSlash + "/chat"            // "/v1/responses/chat"
	sessionsBase := baseNoSlash + "/sessions"    // "/v1/responses/sessions"
	sessionsBaseSlash := sessionsBase + "/"      // "/v1/responses/sessions/"
	path := r.URL.Path

	switch {
	case path == chatPath:
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "invalid_request", "", "Method not allowed")
			return
		}
		c.serveCreateResponse(w, r)
	case path == sessionsBase || path == sessionsBaseSlash:
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "invalid_request", "", "Method not allowed")
			return
		}
		c.handleListSessions(w, r)
	case strings.HasPrefix(path, sessionsBaseSlash):
		id := strings.TrimPrefix(path, sessionsBaseSlash)
		c.handleSessionDetail(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "not_found", "", "Endpoint not found")
	}
}

func (c *OpenResponsesChannel) checkAuth(r *http.Request) bool {
	token := c.cfg.Token.String()
	if token == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	return strings.TrimPrefix(auth, "Bearer ") == token
}

func writeError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{
			Type:    errType,
			Code:    code,
			Message: message,
		},
	})
}

func (c *OpenResponsesChannel) serveCreateResponse(w http.ResponseWriter, r *http.Request) {
	if !c.checkAuth(r) {
		writeError(w, http.StatusUnauthorized, "invalid_request", "", "Invalid token")
		return
	}

	maxBody := c.cfg.MaxBodySize
	if maxBody == 0 {
		maxBody = 1024 * 1024
	}

	if r.Header.Get("Content-Type") != "application/json" {
		writeError(w, http.StatusBadRequest, "invalid_request", "", "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var req CreateResponseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "", "Invalid JSON: "+err.Error())
		return
	}

	content, mediaParts, err := extractRequestContent(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "", err.Error())
		return
	}

	// Handle file uploads
	var imageMedia []string
	var filePaths []string
	for _, m := range mediaParts {
		if isImageDataURL(m) {
			imageMedia = append(imageMedia, m)
		} else {
			path, err := saveDataURLToTemp(m)
			if err == nil {
				filePaths = append(filePaths, path)
			}
		}
	}
	if len(filePaths) > 0 {
		content += "\n\n用户上传了以下文件，如需读取请使用 read_file 工具：\n"
		for _, p := range filePaths {
			content += "- " + p + "\n"
		}
	}

	if strings.TrimSpace(content) == "" && len(imageMedia) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "", "Input is empty")
		return
	}

	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		conversationID = "conv_" + uuid.New().String()
	}

	ctx := r.Context()
	stream, queued, err := c.dispatch(ctx, conversationID, content, imageMedia)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "", err.Error())
		return
	}

	if queued {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, ": queue-%s\n\n", time.Now().Format("2006-01-02"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	if req.Stream {
		c.serveStream(w, r, stream, conversationID, req)
	} else {
		c.serveJSON(w, r, stream, conversationID, req)
	}
}

// serveJSON waits for the turn to complete and returns a single JSON Response.
func (c *OpenResponsesChannel) serveJSON(w http.ResponseWriter, r *http.Request, stream *pendingStream, conversationID string, req CreateResponseRequest) {
	<-stream.done

	resp := c.buildResponse(stream, conversationID, req)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// buildResponse drains the pendingStream and assembles a Response.
// This is used for both streaming (final) and non-streaming responses.
func (c *OpenResponsesChannel) buildResponse(stream *pendingStream, conversationID string, req CreateResponseRequest) Response {
	respID := "resp_" + conversationID
	msgID := "msg_" + conversationID
	msgSeq := 0

	var outputItems []ResponseItem
	var textBuf string
	var hasActiveTextItem bool
	var hasActiveReasoningItem bool

	for ev := range stream.events {
		switch ev.kind {
		case eventKindTextDelta:
			if !hasActiveTextItem {
				if hasActiveReasoningItem {
					hasActiveReasoningItem = false
				}
				hasActiveTextItem = true
			}
			textBuf += ev.content

		case eventKindText:
			if hasActiveTextItem {
				item := ResponseItem{
					ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
					Type:    "message",
					Status:  "completed",
					Role:    "assistant",
					Content: []ContentOutput{{Type: "output_text", Text: textBuf}},
				}
				outputItems = append(outputItems, item)
				textBuf = ""
				hasActiveTextItem = false
				msgSeq++
			}
			if hasActiveReasoningItem {
				hasActiveReasoningItem = false
			}
			item := ResponseItem{
				ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:    "message",
				Status:  "completed",
				Role:    "assistant",
				Content: []ContentOutput{{Type: "output_text", Text: ev.content}},
			}
			outputItems = append(outputItems, item)
			msgSeq++

		case eventKindReasoning:
			if hasActiveTextItem {
				item := ResponseItem{
					ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
					Type:    "message",
					Status:  "completed",
					Role:    "assistant",
					Content: []ContentOutput{{Type: "output_text", Text: textBuf}},
				}
				outputItems = append(outputItems, item)
				textBuf = ""
				hasActiveTextItem = false
				msgSeq++
			}
			if !hasActiveReasoningItem {
				hasActiveReasoningItem = true
			}
			item := ResponseItem{
				ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:    "reasoning",
				Status:  "completed",
				Content: []ContentOutput{{Type: "reasoning_text", Text: ev.content}},
			}
			outputItems = append(outputItems, item)
			msgSeq++
			hasActiveReasoningItem = false

		case eventKindImage:
			if hasActiveTextItem {
				item := ResponseItem{
					ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
					Type:    "message",
					Status:  "completed",
					Role:    "assistant",
					Content: []ContentOutput{{Type: "output_text", Text: textBuf}},
				}
				outputItems = append(outputItems, item)
				textBuf = ""
				hasActiveTextItem = false
				msgSeq++
			}
			if hasActiveReasoningItem {
				hasActiveReasoningItem = false
			}
			item := ResponseItem{
				ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:    "message",
				Status:  "completed",
				Role:    "assistant",
				Content: []ContentOutput{{Type: "output_image", Text: ev.imageURL}},
			}
			outputItems = append(outputItems, item)
			msgSeq++

		case eventKindFunctionCall:
			if hasActiveTextItem {
				item := ResponseItem{
					ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
					Type:    "message",
					Status:  "completed",
					Role:    "assistant",
					Content: []ContentOutput{{Type: "output_text", Text: textBuf}},
				}
				outputItems = append(outputItems, item)
				textBuf = ""
				hasActiveTextItem = false
				msgSeq++
			}
			if hasActiveReasoningItem {
				hasActiveReasoningItem = false
			}
			item := ResponseItem{
				ID:     fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:   "function_call",
				Status: "completed",
				Content: []ContentOutput{{
					Type: "function_call_arguments",
					Text: ev.arguments,
				}},
			}
			outputItems = append(outputItems, item)
			msgSeq++

		case eventKindTurnEnd:
			if hasActiveTextItem {
				item := ResponseItem{
					ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
					Type:    "message",
					Status:  "completed",
					Role:    "assistant",
					Content: []ContentOutput{{Type: "output_text", Text: textBuf}},
				}
				outputItems = append(outputItems, item)
				textBuf = ""
				hasActiveTextItem = false
				msgSeq++
			}
			if hasActiveReasoningItem {
				hasActiveReasoningItem = false
			}
		}
	}

	// Flush any remaining text that was streamed as deltas but never finalized
	// with a turn_end (e.g. if the stream was closed prematurely).
	if hasActiveTextItem {
		item := ResponseItem{
			ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
			Type:    "message",
			Status:  "completed",
			Role:    "assistant",
			Content: []ContentOutput{{Type: "output_text", Text: textBuf}},
		}
		outputItems = append(outputItems, item)
		msgSeq++
	}
	if hasActiveReasoningItem {
		hasActiveReasoningItem = false
	}

	if len(outputItems) == 0 {
		outputItems = append(outputItems, ResponseItem{
			ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
			Type:    "message",
			Status:  "completed",
			Role:    "assistant",
			Content: []ContentOutput{{Type: "output_text", Text: ""}},
		})
	}

	return Response{
		ID:                 respID,
		Object:             "response",
		CreatedAt:          time.Now().Unix(),
		Status:             "completed",
		Model:              req.Model,
		Output:             outputItems,
		ConversationID:     conversationID,
		PreviousResponseID: req.PreviousResponseID,
		Usage:              Usage{0, 0},
	}
}

// serveStream sends SSE events as they arrive from the pendingStream.
func (c *OpenResponsesChannel) serveStream(w http.ResponseWriter, r *http.Request, stream *pendingStream, conversationID string, req CreateResponseRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Clear SSE headers before downgrading to JSON
		w.Header().Del("Content-Type")
		w.Header().Del("Cache-Control")
		w.Header().Del("Connection")
		c.serveJSON(w, r, stream, conversationID, req)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	respID := "resp_" + conversationID
	msgID := "msg_" + conversationID
	msgSeq := 0
	seqNum := 0

	// Send response.in_progress
	inProgress := Response{
		ID:             respID,
		Object:         "response",
		CreatedAt:      time.Now().Unix(),
		Status:         "in_progress",
		Output:         []ResponseItem{},
		ConversationID: conversationID,
		Usage:          Usage{0, 0},
	}
	writeSSEEvent(w, "response.in_progress", map[string]any{
		"type":            "response.in_progress",
		"sequence_number": seqNum,
		"response":        inProgress,
	})
	seqNum++
	flusher.Flush()

	var hasActiveTextItem bool
	var hasActiveReasoningItem bool
	var currentTextItemSeq int
	var currentReasoningItemSeq int

	var textStart, reasoningStart, funcCallStart time.Time

	closeTextItem := func() {
		if !hasActiveTextItem {
			return
		}
		writeSSEEvent(w, "response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"sequence_number": seqNum,
			"item_id":         fmt.Sprintf("%s_%d", msgID, currentTextItemSeq),
			"output_index":    currentTextItemSeq,
			"content_index":   0,
		})
		seqNum++
		writeSSEEvent(w, "response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"sequence_number": seqNum,
			"item_id":         fmt.Sprintf("%s_%d", msgID, currentTextItemSeq),
			"output_index":    currentTextItemSeq,
			"content_index":   0,
			"part":            map[string]string{"type": "output_text"},
		})
		seqNum++
		writeSSEEvent(w, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": seqNum,
			"output_index":    currentTextItemSeq,
			"item": map[string]any{
				"id":     fmt.Sprintf("%s_%d", msgID, currentTextItemSeq),
				"type":   "message",
				"status": "completed",
				"role":   "assistant",
			},
		})
		seqNum++
		hasActiveTextItem = false

		if !textStart.IsZero() {
			dur := time.Since(textStart)
			textStart = time.Time{}
			durSeq := msgSeq
			msgSeq++
			writeSSEEvent(w, "response.output_item.added", map[string]any{
				"type":            "response.output_item.added",
				"sequence_number": seqNum,
				"output_index":    durSeq,
				"item": map[string]any{
					"id":      fmt.Sprintf("%s_%d", msgID, durSeq),
					"type":    "message",
					"status":  "in_progress",
					"role":    "assistant",
					"content": []map[string]string{},
				},
			})
			seqNum++
			writeSSEEvent(w, "response.content_part.added", map[string]any{
				"type":            "response.content_part.added",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
				"part":            map[string]string{"type": "output_text", "text": ""},
			})
			seqNum++
			writeSSEEvent(w, "response.output_text.delta", map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
				"delta":           fmt.Sprintf("\n\n⏱️ LLM推理耗时 %s", formatDuration(dur)),
			})
			seqNum++
			writeSSEEvent(w, "response.output_text.done", map[string]any{
				"type":            "response.output_text.done",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
			})
			seqNum++
			writeSSEEvent(w, "response.content_part.done", map[string]any{
				"type":            "response.content_part.done",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
				"part":            map[string]string{"type": "output_text"},
			})
			seqNum++
			writeSSEEvent(w, "response.output_item.done", map[string]any{
				"type":            "response.output_item.done",
				"sequence_number": seqNum,
				"output_index":    durSeq,
				"item": map[string]any{
					"id":     fmt.Sprintf("%s_%d", msgID, durSeq),
					"type":   "message",
					"status": "completed",
					"role":   "assistant",
				},
			})
			seqNum++
		}
	}

	closeReasoningItem := func() {
		if !hasActiveReasoningItem {
			return
		}
		writeSSEEvent(w, "response.reasoning_text.done", map[string]any{
			"type":            "response.reasoning_text.done",
			"sequence_number": seqNum,
			"item_id":         fmt.Sprintf("%s_%d", msgID, currentReasoningItemSeq),
			"output_index":    currentReasoningItemSeq,
			"content_index":   0,
		})
		seqNum++
		writeSSEEvent(w, "response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"sequence_number": seqNum,
			"item_id":         fmt.Sprintf("%s_%d", msgID, currentReasoningItemSeq),
			"output_index":    currentReasoningItemSeq,
			"content_index":   0,
			"part":            map[string]string{"type": "reasoning_text"},
		})
		seqNum++
		writeSSEEvent(w, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": seqNum,
			"output_index":    currentReasoningItemSeq,
			"item": map[string]any{
				"id":     fmt.Sprintf("%s_%d", msgID, currentReasoningItemSeq),
				"type":   "reasoning",
				"status": "completed",
			},
		})
		seqNum++
		hasActiveReasoningItem = false

		if !reasoningStart.IsZero() {
			dur := time.Since(reasoningStart)
			reasoningStart = time.Time{}
			durSeq := msgSeq
			msgSeq++
			writeSSEEvent(w, "response.output_item.added", map[string]any{
				"type":            "response.output_item.added",
				"sequence_number": seqNum,
				"output_index":    durSeq,
				"item": map[string]any{
					"id":      fmt.Sprintf("%s_%d", msgID, durSeq),
					"type":    "message",
					"status":  "in_progress",
					"role":    "assistant",
					"content": []map[string]string{},
				},
			})
			seqNum++
			writeSSEEvent(w, "response.content_part.added", map[string]any{
				"type":            "response.content_part.added",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
				"part":            map[string]string{"type": "output_text", "text": ""},
			})
			seqNum++
			writeSSEEvent(w, "response.output_text.delta", map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
				"delta":           fmt.Sprintf("\n\n⏱️ LLM思考耗时 %s", formatDuration(dur)),
			})
			seqNum++
			writeSSEEvent(w, "response.output_text.done", map[string]any{
				"type":            "response.output_text.done",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
			})
			seqNum++
			writeSSEEvent(w, "response.content_part.done", map[string]any{
				"type":            "response.content_part.done",
				"sequence_number": seqNum,
				"item_id":         fmt.Sprintf("%s_%d", msgID, durSeq),
				"output_index":    durSeq,
				"content_index":   0,
				"part":            map[string]string{"type": "output_text"},
			})
			seqNum++
			writeSSEEvent(w, "response.output_item.done", map[string]any{
				"type":            "response.output_item.done",
				"sequence_number": seqNum,
				"output_index":    durSeq,
				"item": map[string]any{
					"id":     fmt.Sprintf("%s_%d", msgID, durSeq),
					"type":   "message",
					"status": "completed",
					"role":   "assistant",
				},
			})
			seqNum++
		}
	}

	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat-%s\n\n", time.Now().Format("15:04:05"))
			flusher.Flush()
		case ev, more := <-stream.events:
			if !more {
				// Stream closed, send completed
				completed := c.buildResponse(stream, conversationID, req)
				completed.Status = "completed"
				for i := range completed.Output {
					completed.Output[i] = stripContentsFromItem(completed.Output[i])
				}
				writeSSEEvent(w, "response.completed", map[string]any{
					"type":            "response.completed",
					"sequence_number": seqNum,
					"response":        completed,
				})
				seqNum++
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}

			rc := http.NewResponseController(w)
			rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))

			switch ev.kind {
			case eventKindTextDelta:
				closeReasoningItem()
				if !hasActiveTextItem {
					hasActiveTextItem = true
					textStart = time.Now()
					currentTextItemSeq = msgSeq
					writeSSEEvent(w, "response.output_item.added", map[string]any{
						"type":            "response.output_item.added",
						"sequence_number": seqNum,
						"output_index":    msgSeq,
						"item": map[string]any{
							"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
							"type":    "message",
							"status":  "in_progress",
							"role":    "assistant",
							"content": []map[string]string{},
						},
					})
					seqNum++
					writeSSEEvent(w, "response.content_part.added", map[string]any{
						"type":            "response.content_part.added",
						"sequence_number": seqNum,
						"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
						"output_index":    msgSeq,
						"content_index":   0,
						"part":            map[string]string{"type": "output_text", "text": ""},
					})
					seqNum++
					msgSeq++
				}
				writeSSEEvent(w, "response.output_text.delta", map[string]any{
					"type":            "response.output_text.delta",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, currentTextItemSeq),
					"output_index":    currentTextItemSeq,
					"content_index":   0,
					"delta":           ev.content,
				})
				seqNum++

			case eventKindReasoning:
				closeTextItem()
				if !hasActiveReasoningItem {
					hasActiveReasoningItem = true
					reasoningStart = time.Now()
					currentReasoningItemSeq = msgSeq
					writeSSEEvent(w, "response.output_item.added", map[string]any{
						"type":            "response.output_item.added",
						"sequence_number": seqNum,
						"output_index":    msgSeq,
						"item": map[string]any{
							"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
							"type":    "reasoning",
							"status":  "in_progress",
							"content": []map[string]string{},
						},
					})
					seqNum++
					writeSSEEvent(w, "response.content_part.added", map[string]any{
						"type":            "response.content_part.added",
						"sequence_number": seqNum,
						"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
						"output_index":    msgSeq,
						"content_index":   0,
						"part":            map[string]string{"type": "reasoning_text", "text": ""},
					})
					seqNum++
					msgSeq++
				}
				writeSSEEvent(w, "response.reasoning_text.delta", map[string]any{
					"type":            "response.reasoning_text.delta",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, currentReasoningItemSeq),
					"output_index":    currentReasoningItemSeq,
					"content_index":   0,
					"delta":           ev.content,
				})
				seqNum++

			case eventKindImage:
				closeTextItem()
				closeReasoningItem()
				// Image: add item, part, part.done, item.done (no delta)
				writeSSEEvent(w, "response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": seqNum,
					"output_index":    msgSeq,
					"item": map[string]any{
						"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
						"type":    "message",
						"status":  "in_progress",
						"role":    "assistant",
						"content": []map[string]string{},
					},
				})
				seqNum++
				writeSSEEvent(w, "response.content_part.added", map[string]any{
					"type":            "response.content_part.added",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
					"output_index":    msgSeq,
					"content_index":   0,
					"part":            map[string]string{"type": "output_image"},
				})
				seqNum++
				writeSSEEvent(w, "response.content_part.done", map[string]any{
					"type":            "response.content_part.done",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
					"output_index":    msgSeq,
					"content_index":   0,
					"part":            map[string]string{"type": "output_image", "url": ev.imageURL},
				})
				seqNum++
				writeSSEEvent(w, "response.output_item.done", map[string]any{
					"type":            "response.output_item.done",
					"sequence_number": seqNum,
					"output_index":    msgSeq,
					"item": map[string]any{
						"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
						"type":    "message",
						"status":  "completed",
						"role":    "assistant",
						"content": []map[string]string{{"type": "output_image", "url": ev.imageURL}},
					},
				})
				seqNum++
				msgSeq++

			case eventKindFunctionCall:
				funcCallStart = time.Now()
				closeTextItem()
				closeReasoningItem()
				// Function call: full sequence
				writeSSEEvent(w, "response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": seqNum,
					"output_index":    msgSeq,
					"item": map[string]any{
						"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
						"type":    "function_call",
						"status":  "in_progress",
						"call_id": ev.callID,
						"name":    ev.name,
					},
				})
				seqNum++
				writeSSEEvent(w, "response.content_part.added", map[string]any{
					"type":            "response.content_part.added",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
					"output_index":    msgSeq,
					"content_index":   0,
					"part":            map[string]string{"type": "function_call_arguments", "text": ""},
				})
				seqNum++
				writeSSEEvent(w, "response.function_call_arguments.delta", map[string]any{
					"type":            "response.function_call_arguments.delta",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
					"output_index":    msgSeq,
					"content_index":   0,
					"delta":           ev.arguments,
				})
				seqNum++
				writeSSEEvent(w, "response.function_call_arguments.done", map[string]any{
					"type":            "response.function_call_arguments.done",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
					"output_index":    msgSeq,
					"content_index":   0,
				})
				seqNum++
				writeSSEEvent(w, "response.content_part.done", map[string]any{
					"type":            "response.content_part.done",
					"sequence_number": seqNum,
					"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
					"output_index":    msgSeq,
					"content_index":   0,
					"part":            map[string]string{"type": "function_call_arguments", "text": ev.arguments},
				})
				seqNum++
				writeSSEEvent(w, "response.output_item.done", map[string]any{
					"type":            "response.output_item.done",
					"sequence_number": seqNum,
					"output_index":    msgSeq,
					"item": map[string]any{
						"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
						"type":    "function_call",
						"status":  "completed",
						"call_id": ev.callID,
						"name":    ev.name,
					},
				})
				seqNum++
				// Emit a text item with the function call duration so the frontend can display it
				if !funcCallStart.IsZero() {
					durMs := time.Since(funcCallStart).Milliseconds()
					funcCallStart = time.Time{}
					fcDurSeq := msgSeq
					msgSeq++
					writeSSEEvent(w, "response.output_item.added", map[string]any{
						"type":            "response.output_item.added",
						"sequence_number": seqNum,
						"output_index":    fcDurSeq,
						"item": map[string]any{
							"id":      fmt.Sprintf("%s_%d", msgID, fcDurSeq),
							"type":    "message",
							"status":  "in_progress",
							"role":    "assistant",
							"content": []map[string]string{},
						},
					})
					seqNum++
					writeSSEEvent(w, "response.content_part.added", map[string]any{
						"type":            "response.content_part.added",
						"sequence_number": seqNum,
						"item_id":         fmt.Sprintf("%s_%d", msgID, fcDurSeq),
						"output_index":    fcDurSeq,
						"content_index":   0,
						"part":            map[string]string{"type": "output_text", "text": ""},
					})
					seqNum++
					writeSSEEvent(w, "response.output_text.delta", map[string]any{
						"type":            "response.output_text.delta",
						"sequence_number": seqNum,
						"item_id":         fmt.Sprintf("%s_%d", msgID, fcDurSeq),
						"output_index":    fcDurSeq,
						"content_index":   0,
						"delta":           fmt.Sprintf("\n\n⏱️ Tool调用耗时 %s", formatDuration(time.Duration(durMs)*time.Millisecond)),
					})
					seqNum++
					writeSSEEvent(w, "response.output_text.done", map[string]any{
						"type":            "response.output_text.done",
						"sequence_number": seqNum,
						"item_id":         fmt.Sprintf("%s_%d", msgID, fcDurSeq),
						"output_index":    fcDurSeq,
						"content_index":   0,
					})
					seqNum++
					writeSSEEvent(w, "response.content_part.done", map[string]any{
						"type":            "response.content_part.done",
						"sequence_number": seqNum,
						"item_id":         fmt.Sprintf("%s_%d", msgID, fcDurSeq),
						"output_index":    fcDurSeq,
						"content_index":   0,
						"part":            map[string]string{"type": "output_text"},
					})
					seqNum++
					writeSSEEvent(w, "response.output_item.done", map[string]any{
						"type":            "response.output_item.done",
						"sequence_number": seqNum,
						"output_index":    fcDurSeq,
						"item": map[string]any{
							"id":     fmt.Sprintf("%s_%d", msgID, fcDurSeq),
							"type":   "message",
							"status": "completed",
							"role":   "assistant",
						},
					})
					seqNum++
				} else {
					msgSeq++
				}

			case eventKindTurnEnd:
				closeTextItem()
				closeReasoningItem()
			}

			flusher.Flush()
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, eventType string, payload map[string]any) {
	fmt.Fprintf(w, ": timestamp-%s\n\n", time.Now().Format("15:04:05"))
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(data))
}

func formatDuration(d time.Duration) string {
	ms := d.Milliseconds()
	if ms >= 60000 {
		return fmt.Sprintf("%.1fmin", float64(ms)/60000.0)
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
	}
	return fmt.Sprintf("%dms", ms)
}

func stripContentsFromItem(item ResponseItem) ResponseItem {
	for i := range item.Content {
		if item.Content[i].Type == "output_image" {
			item.Content[i].Text = ""
		}
	}
	return item
}
