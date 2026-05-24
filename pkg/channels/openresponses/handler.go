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

	base := c.WebhookPath()
	baseNoSlash := strings.TrimSuffix(base, "/")
	chatPath := baseNoSlash + "/chat"
	sessionsBase := baseNoSlash + "/sessions"
	sessionsBaseSlash := sessionsBase + "/"
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

func (c *OpenResponsesChannel) serveJSON(w http.ResponseWriter, r *http.Request, stream *pendingStream, conversationID string, req CreateResponseRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		<-stream.done
		resp := c.buildResponse(stream, conversationID, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": hb-%s\n\n", time.Now().Format("15:04:05"))
			flusher.Flush()
		case <-stream.done:
			resp := c.buildResponse(stream, conversationID, req)
			resp.Status = "completed"
			writeSSEEvent(w, "response.completed", map[string]any{
				"type":            "response.completed",
				"sequence_number": 0,
				"response":        resp,
			})
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
	}
}

func (c *OpenResponsesChannel) buildResponse(stream *pendingStream, conversationID string, req CreateResponseRequest) Response {
	respID := "resp_" + conversationID
	msgID := "msg_" + conversationID
	msgSeq := 0

	var outputItems []ResponseItem
	var textBuf string
	var hasActiveTextItem bool
	var hasActiveReasoningItem bool

	flushText := func() {
		if !hasActiveTextItem {
			return
		}
		outputItems = append(outputItems, ResponseItem{
			ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
			Type:    "message",
			Status:  "completed",
			Role:    "assistant",
			Content: []ContentOutput{{Type: "output_text", Text: textBuf}},
		})
		textBuf = ""
		hasActiveTextItem = false
		msgSeq++
	}
	clearReasoning := func() {
		hasActiveReasoningItem = false
	}

	for ev := range stream.events {
		switch ev.kind {
		case eventKindTextDelta:
			if hasActiveReasoningItem {
				clearReasoning()
			}
			if !hasActiveTextItem {
				hasActiveTextItem = true
			}
			textBuf += ev.content

		case eventKindText:
			flushText()
			clearReasoning()
			outputItems = append(outputItems, ResponseItem{
				ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:    "message",
				Status:  "completed",
				Role:    "assistant",
				Content: []ContentOutput{{Type: "output_text", Text: ev.content}},
			})
			msgSeq++

		case eventKindReasoning:
			flushText()
			outputItems = append(outputItems, ResponseItem{
				ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:    "reasoning",
				Status:  "completed",
				Content: []ContentOutput{{Type: "reasoning_text", Text: ev.content}},
			})
			msgSeq++
			hasActiveReasoningItem = false

		case eventKindImage:
			flushText()
			clearReasoning()
			outputItems = append(outputItems, ResponseItem{
				ID:      fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:    "message",
				Status:  "completed",
				Role:    "assistant",
				Content: []ContentOutput{{Type: "output_image", Text: ev.imageURL}},
			})
			msgSeq++

		case eventKindFunctionCall:
			flushText()
			clearReasoning()
			outputItems = append(outputItems, ResponseItem{
				ID:     fmt.Sprintf("%s_%d", msgID, msgSeq),
				Type:   "function_call",
				Status: "completed",
				Content: []ContentOutput{{
					Type: "function_call_arguments",
					Text: ev.arguments,
				}},
			})
			msgSeq++

		case eventKindTurnEnd:
			flushText()
			clearReasoning()
		}
	}

	if hasActiveTextItem {
		flushText()
	}
	clearReasoning()

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

func (c *OpenResponsesChannel) serveStream(w http.ResponseWriter, r *http.Request, stream *pendingStream, conversationID string, req CreateResponseRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
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

	emitSSE(w, &seqNum, "response.in_progress", map[string]any{
		"type":            "response.in_progress",
		"sequence_number": seqNum,
		"response": Response{
			ID:             respID,
			Object:         "response",
			CreatedAt:      time.Now().Unix(),
			Status:         "in_progress",
			Output:         []ResponseItem{},
			ConversationID: conversationID,
			Usage:          Usage{0, 0},
		},
	})
	flusher.Flush()

	var hasActiveTextItem bool
	var hasActiveReasoningItem bool
	var currentTextItemSeq int
	var currentReasoningItemSeq int
	var textStart, reasoningStart, funcCallStart time.Time

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
				completed := c.buildResponse(stream, conversationID, req)
				completed.Status = "completed"
				for i := range completed.Output {
					completed.Output[i] = stripContentsFromItem(completed.Output[i])
				}
				emitSSE(w, &seqNum, "response.completed", map[string]any{
					"type":            "response.completed",
					"sequence_number": seqNum,
					"response":        completed,
				})
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}

			rc := http.NewResponseController(w)
			rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))

			switch ev.kind {
			case eventKindTextDelta:
				if hasActiveReasoningItem {
					emitReasoningItemEnd(w, flusher, msgID, &seqNum, currentReasoningItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &reasoningStart, "LLM思考")
					hasActiveReasoningItem = false
				}
				if !hasActiveTextItem {
					hasActiveTextItem = true
					textStart = time.Now()
					currentTextItemSeq = msgSeq
					emitTextItemStart(w, flusher, msgID, &seqNum, &msgSeq)
				}
				emitTextItemDelta(w, flusher, msgID, &seqNum, currentTextItemSeq, ev.content)

			case eventKindReasoning:
				if hasActiveTextItem {
					emitTextItemEnd(w, flusher, msgID, &seqNum, currentTextItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &textStart, "LLM推理")
					hasActiveTextItem = false
				}
				if !hasActiveReasoningItem {
					hasActiveReasoningItem = true
					reasoningStart = time.Now()
					currentReasoningItemSeq = msgSeq
					emitReasoningItemStart(w, flusher, msgID, &seqNum, &msgSeq)
				}
				emitReasoningItemDelta(w, flusher, msgID, &seqNum, currentReasoningItemSeq, ev.content)

			case eventKindImage:
				if hasActiveTextItem {
					emitTextItemEnd(w, flusher, msgID, &seqNum, currentTextItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &textStart, "LLM推理")
					hasActiveTextItem = false
				}
				if hasActiveReasoningItem {
					emitReasoningItemEnd(w, flusher, msgID, &seqNum, currentReasoningItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &reasoningStart, "LLM思考")
					hasActiveReasoningItem = false
				}
				emitImageItem(w, flusher, msgID, &seqNum, &msgSeq, ev.imageURL)

			case eventKindFunctionCall:
				funcCallStart = time.Now()
				if hasActiveTextItem {
					emitTextItemEnd(w, flusher, msgID, &seqNum, currentTextItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &textStart, "LLM推理")
					hasActiveTextItem = false
				}
				if hasActiveReasoningItem {
					emitReasoningItemEnd(w, flusher, msgID, &seqNum, currentReasoningItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &reasoningStart, "LLM思考")
					hasActiveReasoningItem = false
				}
				emitFunctionCallItem(w, flusher, msgID, &seqNum, msgSeq, ev)
				if !funcCallStart.IsZero() {
					durMs := time.Since(funcCallStart).Milliseconds()
					funcCallStart = time.Time{}
					emitDurationItemWithMS(w, flusher, msgID, &seqNum, &msgSeq, durMs, "Tool调用")
				} else {
					msgSeq++
				}

			case eventKindTurnEnd:
				if hasActiveTextItem {
					emitTextItemEnd(w, flusher, msgID, &seqNum, currentTextItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &textStart, "LLM推理")
					hasActiveTextItem = false
				}
				if hasActiveReasoningItem {
					emitReasoningItemEnd(w, flusher, msgID, &seqNum, currentReasoningItemSeq)
					emitDurationItem(w, flusher, msgID, &seqNum, &msgSeq, &reasoningStart, "LLM思考")
					hasActiveReasoningItem = false
				}
			}

			flusher.Flush()
		}
	}
}

func emitSSE(w http.ResponseWriter, seqNum *int, eventType string, payload map[string]any) {
	writeSSEEvent(w, eventType, payload)
	*seqNum++
}

func emitTextItemStart(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum, msgSeq *int) {
	idx := *msgSeq
	emitSSE(w, seqNum, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, idx),
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []map[string]string{},
		},
	})
	emitSSE(w, seqNum, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "output_text", "text": ""},
	})
	*msgSeq++
}

func emitTextItemDelta(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum *int, currentSeq int, delta string) {
	emitSSE(w, seqNum, "response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
		"output_index":    currentSeq,
		"content_index":   0,
		"delta":           delta,
	})
}

func emitTextItemEnd(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum *int, currentSeq int) {
	emitSSE(w, seqNum, "response.output_text.done", map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
		"output_index":    currentSeq,
		"content_index":   0,
	})
	emitSSE(w, seqNum, "response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
		"output_index":    currentSeq,
		"content_index":   0,
		"part":            map[string]string{"type": "output_text"},
	})
	emitSSE(w, seqNum, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": *seqNum,
		"output_index":    currentSeq,
		"item": map[string]any{
			"id":     fmt.Sprintf("%s_%d", msgID, currentSeq),
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
		},
	})
}

func emitReasoningItemStart(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum, msgSeq *int) {
	idx := *msgSeq
	emitSSE(w, seqNum, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, idx),
			"type":    "reasoning",
			"status":  "in_progress",
			"content": []map[string]string{},
		},
	})
	emitSSE(w, seqNum, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "reasoning_text", "text": ""},
	})
	*msgSeq++
}

func emitReasoningItemDelta(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum *int, currentSeq int, delta string) {
	emitSSE(w, seqNum, "response.reasoning_text.delta", map[string]any{
		"type":            "response.reasoning_text.delta",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
		"output_index":    currentSeq,
		"content_index":   0,
		"delta":           delta,
	})
}

func emitReasoningItemEnd(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum *int, currentSeq int) {
	emitSSE(w, seqNum, "response.reasoning_text.done", map[string]any{
		"type":            "response.reasoning_text.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
		"output_index":    currentSeq,
		"content_index":   0,
	})
	emitSSE(w, seqNum, "response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
		"output_index":    currentSeq,
		"content_index":   0,
		"part":            map[string]string{"type": "reasoning_text"},
	})
	emitSSE(w, seqNum, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": *seqNum,
		"output_index":    currentSeq,
		"item": map[string]any{
			"id":     fmt.Sprintf("%s_%d", msgID, currentSeq),
			"type":   "reasoning",
			"status": "completed",
		},
	})
}

func emitImageItem(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum, msgSeq *int, imageURL string) {
	idx := *msgSeq
	emitSSE(w, seqNum, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, idx),
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []map[string]string{},
		},
	})
	emitSSE(w, seqNum, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "output_image"},
	})
	emitSSE(w, seqNum, "response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "output_image", "url": imageURL},
	})
	emitSSE(w, seqNum, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, idx),
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": []map[string]string{{"type": "output_image", "url": imageURL}},
		},
	})
	*msgSeq++
}

func emitFunctionCallItem(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum *int, msgSeq int, ev streamEvent) {
	emitSSE(w, seqNum, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": *seqNum,
		"output_index":    msgSeq,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
			"type":    "function_call",
			"status":  "in_progress",
			"call_id": ev.callID,
			"name":    ev.name,
		},
	})
	emitSSE(w, seqNum, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
		"output_index":    msgSeq,
		"content_index":   0,
		"part":            map[string]string{"type": "function_call_arguments", "text": ""},
	})
	emitSSE(w, seqNum, "response.function_call_arguments.delta", map[string]any{
		"type":            "response.function_call_arguments.delta",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
		"output_index":    msgSeq,
		"content_index":   0,
		"delta":           ev.arguments,
	})
	emitSSE(w, seqNum, "response.function_call_arguments.done", map[string]any{
		"type":            "response.function_call_arguments.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
		"output_index":    msgSeq,
		"content_index":   0,
	})
	emitSSE(w, seqNum, "response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, msgSeq),
		"output_index":    msgSeq,
		"content_index":   0,
		"part":            map[string]string{"type": "function_call_arguments", "text": ev.arguments},
	})
	emitSSE(w, seqNum, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": *seqNum,
		"output_index":    msgSeq,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, msgSeq),
			"type":    "function_call",
			"status":  "completed",
			"call_id": ev.callID,
			"name":    ev.name,
		},
	})
}

func emitTextItemEndWithDuration(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum *int, currentSeq int, startTime *time.Time) {
	if startTime != nil && !(*startTime).IsZero() {
		dur := time.Since(*startTime)
		*startTime = time.Time{}
		emitSSE(w, seqNum, "response.output_text.delta", map[string]any{
			"type":            "response.output_text.delta",
			"sequence_number": *seqNum,
			"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
			"output_index":    currentSeq,
			"content_index":   0,
			"delta":           fmt.Sprintf("\n\n⏱️ LLM推理耗时 %s", formatDuration(dur)),
		})
	}
	emitTextItemEnd(w, flusher, msgID, seqNum, currentSeq)
}

func emitReasoningItemEndWithDuration(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum *int, currentSeq int, startTime *time.Time) {
	if startTime != nil && !(*startTime).IsZero() {
		dur := time.Since(*startTime)
		*startTime = time.Time{}
		emitSSE(w, seqNum, "response.reasoning_text.delta", map[string]any{
			"type":            "response.reasoning_text.delta",
			"sequence_number": *seqNum,
			"item_id":         fmt.Sprintf("%s_%d", msgID, currentSeq),
			"output_index":    currentSeq,
			"content_index":   0,
			"delta":           fmt.Sprintf("\n\n⏱️ LLM思考耗时 %s", formatDuration(dur)),
		})
	}
	emitReasoningItemEnd(w, flusher, msgID, seqNum, currentSeq)
}

func emitDurationItem(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum, msgSeq *int, startTime *time.Time, label string) {
	if startTime == nil || (*startTime).IsZero() {
		return
	}
	dur := time.Since(*startTime)
	*startTime = time.Time{}
	idx := *msgSeq
	*msgSeq++

	emitSSE(w, seqNum, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, idx),
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []map[string]string{},
		},
	})
	emitSSE(w, seqNum, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "output_text", "text": ""},
	})
	emitSSE(w, seqNum, "response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"delta":           fmt.Sprintf("\n\n⏱️ %s耗时 %s", label, formatDuration(dur)),
	})
	emitSSE(w, seqNum, "response.output_text.done", map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
	})
	emitSSE(w, seqNum, "response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "output_text"},
	})
	emitSSE(w, seqNum, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":     fmt.Sprintf("%s_%d", msgID, idx),
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
		},
	})
}

func emitDurationItemWithMS(w http.ResponseWriter, flusher http.Flusher, msgID string, seqNum, msgSeq *int, durMs int64, label string) {
	idx := *msgSeq
	*msgSeq++
	emitSSE(w, seqNum, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":      fmt.Sprintf("%s_%d", msgID, idx),
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []map[string]string{},
		},
	})
	emitSSE(w, seqNum, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "output_text", "text": ""},
	})
	emitSSE(w, seqNum, "response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"delta":           fmt.Sprintf("\n\n⏱️ %s耗时 %s", label, formatDuration(time.Duration(durMs)*time.Millisecond)),
	})
	emitSSE(w, seqNum, "response.output_text.done", map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
	})
	emitSSE(w, seqNum, "response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": *seqNum,
		"item_id":         fmt.Sprintf("%s_%d", msgID, idx),
		"output_index":    idx,
		"content_index":   0,
		"part":            map[string]string{"type": "output_text"},
	})
	emitSSE(w, seqNum, "response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": *seqNum,
		"output_index":    idx,
		"item": map[string]any{
			"id":     fmt.Sprintf("%s_%d", msgID, idx),
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
		},
	})
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
