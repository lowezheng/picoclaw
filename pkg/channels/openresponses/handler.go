package openresponses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
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
	content, media := extractRequestContent(req)

	// Separate images (sent to LLM vision) from non-image files (saved to temp).
	var imageMedia []string
	var filePaths []string
	for _, m := range media {
		if isImageDataURL(m) {
			imageMedia = append(imageMedia, m)
			continue
		}
		if path, err := saveDataURLToTemp(m); err == nil {
			filePaths = append(filePaths, path)
		} else {
			logger.WarnCF("openresponses", "Failed to save uploaded file to temp", map[string]any{
				"error": err.Error(),
			})
		}
	}

	// Inject file paths into the user message so the AI can read them via tools.
	if len(filePaths) > 0 {
		var sb strings.Builder
		if content != "" {
			sb.WriteString(content)
			sb.WriteString("\n\n")
		}
		sb.WriteString("用户上传了以下文件，如需读取请使用 read_file 工具：\n")
		for _, p := range filePaths {
			sb.WriteString("- ")
			sb.WriteString(p)
			sb.WriteString("\n")
		}
		content = sb.String()
	}

	if strings.TrimSpace(content) == "" && len(imageMedia) == 0 {
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

	stream, queued, err := c.dispatch(ctx, conversationID, content, imageMedia)
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

	// textBuf accumulates deltas from streaming mode.
	var textBuf string
	flushTextBuf := func() {
		if textBuf == "" {
			return
		}
		itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
		msgSeq++
		outputItems = append(outputItems, ResponseItem{
			Type:   "message",
			ID:     itemID,
			Status: "completed",
			Role:   "assistant",
			Content: []Content{
				{Type: "output_text", Text: textBuf},
			},
		})
		textBuf = ""
	}

	for ev := range stream.events {
		logger.DebugCF("openresponses", "writeJSON event", map[string]any{
			"event_kind": ev.kind,
		})
		switch ev.kind {
		case eventKindTextDelta:
			// Accumulate streaming deltas for later flush.
			textBuf += ev.content
		case eventKindTurnEnd:
			// Streaming turn finished: flush accumulated text.
			flushTextBuf()
		case eventKindText:
			// Non-streaming full-text event.
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
			flushTextBuf()
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
		case eventKindImage:
			flushTextBuf()
			itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
			msgSeq++
			outputItems = append(outputItems, ResponseItem{
				Type:   "message",
				ID:     itemID,
				Status: "completed",
				Role:   "assistant",
				Content: []Content{
					{Type: "output_image", ImageURL: ev.imageURL},
				},
			})
		case eventKindFunctionCall:
			flushTextBuf()
			itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
			msgSeq++
			outputItems = append(outputItems, ResponseItem{
				Type:      "function_call",
				ID:        itemID,
				Status:    "completed",
				CallID:    ev.callID,
				Name:      ev.name,
				Arguments: ev.arguments,
			})
		}
	}

	// Flush any remaining buffered text after the channel closes.
	flushTextBuf()

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
	var (
		hasActiveTextItem        bool
		activeTextItemID         string
		activeTextItemIndex      int
		lastTextContent          string
		hasActiveReasoningItem   bool
		activeReasoningItemID    string
		activeReasoningItemIndex int
		lastReasoningContent     string
	)

	// closeActiveTextItem finalizes an in-progress streaming text item.
	closeActiveTextItem := func() {
		if !hasActiveTextItem {
			return
		}
		// response.output_text.done
		writeSSEEvent(w, "response.output_text.done", ResponseEvent{
			Type:           "response.output_text.done",
			SequenceNumber: seq,
			ItemID:         activeTextItemID,
			OutputIndex:    activeTextItemIndex,
			ContentIndex:   0,
		})
		seq++
		flushAndExtend()

		// response.content_part.done
		writeSSEEvent(w, "response.content_part.done", ResponseEvent{
			Type:           "response.content_part.done",
			SequenceNumber: seq,
			ItemID:         activeTextItemID,
			OutputIndex:    activeTextItemIndex,
			ContentIndex:   0,
			Part:           Content{Type: "output_text", Text: lastTextContent},
		})
		seq++
		flushAndExtend()

		// response.output_item.done
		doneItem := ResponseItem{
			Type:    "message",
			ID:      activeTextItemID,
			Status:  "completed",
			Role:    "assistant",
			Content: []Content{{Type: "output_text", Text: lastTextContent}},
		}
		writeSSEEvent(w, "response.output_item.done", ResponseEvent{
			Type:           "response.output_item.done",
			SequenceNumber: seq,
			OutputIndex:    activeTextItemIndex,
			Item:           doneItem,
		})
		seq++
		flushAndExtend()

		outputItems = append(outputItems, doneItem)
		hasActiveTextItem = false
	}

	// closeActiveReasoningItem finalizes an in-progress streaming reasoning item.
	closeActiveReasoningItem := func() {
		if !hasActiveReasoningItem {
			return
		}
		// response.reasoning_text.done
		writeSSEEvent(w, "response.reasoning_text.done", ResponseEvent{
			Type:           "response.reasoning_text.done",
			SequenceNumber: seq,
			ItemID:         activeReasoningItemID,
			OutputIndex:    activeReasoningItemIndex,
			ContentIndex:   0,
		})
		seq++
		flushAndExtend()

		// response.content_part.done
		writeSSEEvent(w, "response.content_part.done", ResponseEvent{
			Type:           "response.content_part.done",
			SequenceNumber: seq,
			ItemID:         activeReasoningItemID,
			OutputIndex:    activeReasoningItemIndex,
			ContentIndex:   0,
			Part:           Content{Type: "reasoning_text", Text: lastReasoningContent},
		})
		seq++
		flushAndExtend()

		// response.output_item.done
		doneItem := ResponseItem{
			Type:    "reasoning",
			ID:      activeReasoningItemID,
			Status:  "completed",
			Content: []Content{{Type: "reasoning_text", Text: lastReasoningContent}},
		}
		writeSSEEvent(w, "response.output_item.done", ResponseEvent{
			Type:           "response.output_item.done",
			SequenceNumber: seq,
			OutputIndex:    activeReasoningItemIndex,
			Item:           doneItem,
		})
		seq++
		flushAndExtend()

		outputItems = append(outputItems, doneItem)
		hasActiveReasoningItem = false
	}

	for ev := range stream.events {
		logger.DebugCF("openresponses", "writeSSE event", map[string]any{
			"event_kind": ev.kind,
		})
		switch ev.kind {
		case eventKindTurnEnd:
			closeActiveTextItem()
			closeActiveReasoningItem()
			continue

		case eventKindTextDelta:
			closeActiveReasoningItem()
			if !hasActiveTextItem {
				activeTextItemID = fmt.Sprintf("%s_%d", msgID, msgSeq)
				activeTextItemIndex = len(outputItems)
				msgSeq++
				hasActiveTextItem = true
				lastTextContent = ""

				addedItem := ResponseItem{
					Type:    "message",
					ID:      activeTextItemID,
					Status:  "in_progress",
					Role:    "assistant",
					Content: []Content{},
				}
				writeSSEEvent(w, "response.output_item.added", ResponseEvent{
					Type:           "response.output_item.added",
					SequenceNumber: seq,
					OutputIndex:    activeTextItemIndex,
					Item:           addedItem,
				})
				seq++
				flushAndExtend()

				writeSSEEvent(w, "response.content_part.added", ResponseEvent{
					Type:           "response.content_part.added",
					SequenceNumber: seq,
					ItemID:         activeTextItemID,
					OutputIndex:    activeTextItemIndex,
					ContentIndex:   0,
					Part:           Content{Type: "output_text", Text: ""},
				})
				seq++
				flushAndExtend()
			}

			lastTextContent += ev.content
			writeSSEEvent(w, "response.output_text.delta", ResponseEvent{
				Type:           "response.output_text.delta",
				SequenceNumber: seq,
				ItemID:         activeTextItemID,
				OutputIndex:    activeTextItemIndex,
				ContentIndex:   0,
				Delta:          ev.content,
			})
			seq++
			flushAndExtend()
			continue

		case eventKindText:
			closeActiveTextItem()
			closeActiveReasoningItem()

			itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
			msgSeq++

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

			writeSSEEvent(w, "response.output_text.done", ResponseEvent{
				Type:           "response.output_text.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
			})
			seq++
			flushAndExtend()

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
			closeActiveTextItem()
			if !hasActiveReasoningItem {
				activeReasoningItemID = fmt.Sprintf("%s_%d", msgID, msgSeq)
				activeReasoningItemIndex = len(outputItems)
				msgSeq++
				hasActiveReasoningItem = true
				lastReasoningContent = ""

				addedItem := ResponseItem{
					Type:    "reasoning",
					ID:      activeReasoningItemID,
					Status:  "in_progress",
					Content: []Content{},
				}
				writeSSEEvent(w, "response.output_item.added", ResponseEvent{
					Type:           "response.output_item.added",
					SequenceNumber: seq,
					OutputIndex:    activeReasoningItemIndex,
					Item:           addedItem,
				})
				seq++
				flushAndExtend()

				writeSSEEvent(w, "response.content_part.added", ResponseEvent{
					Type:           "response.content_part.added",
					SequenceNumber: seq,
					ItemID:         activeReasoningItemID,
					OutputIndex:    activeReasoningItemIndex,
					ContentIndex:   0,
					Part:           Content{Type: "reasoning_text", Text: ""},
				})
				seq++
				flushAndExtend()
			}

			lastReasoningContent += ev.content
			writeSSEEvent(w, "response.reasoning_text.delta", ResponseEvent{
				Type:           "response.reasoning_text.delta",
				SequenceNumber: seq,
				ItemID:         activeReasoningItemID,
				OutputIndex:    activeReasoningItemIndex,
				ContentIndex:   0,
				Delta:          ev.content,
			})
			seq++
			flushAndExtend()
			continue

		case eventKindImage:
			closeActiveTextItem()
			closeActiveReasoningItem()

			itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
			msgSeq++

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

			writeSSEEvent(w, "response.content_part.added", ResponseEvent{
				Type:           "response.content_part.added",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "output_image", ImageURL: ""},
			})
			seq++
			flushAndExtend()

			writeSSEEvent(w, "response.content_part.done", ResponseEvent{
				Type:           "response.content_part.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "output_image", ImageURL: ev.imageURL},
			})
			seq++
			flushAndExtend()

			doneItem := ResponseItem{
				Type:    "message",
				ID:      itemID,
				Status:  "completed",
				Role:    "assistant",
				Content: []Content{{Type: "output_image", ImageURL: ev.imageURL}},
			}
			writeSSEEvent(w, "response.output_item.done", ResponseEvent{
				Type:           "response.output_item.done",
				SequenceNumber: seq,
				OutputIndex:    len(outputItems),
				Item:           stripImageURLsFromItem(doneItem),
			})
			seq++
			flushAndExtend()

			outputItems = append(outputItems, doneItem)

		case eventKindFunctionCall:
			closeActiveTextItem()
			closeActiveReasoningItem()

			itemID := fmt.Sprintf("%s_%d", msgID, msgSeq)
			msgSeq++

			addedItem := ResponseItem{
				Type:   "function_call",
				ID:     itemID,
				Status: "in_progress",
				CallID: ev.callID,
				Name:   ev.name,
			}
			writeSSEEvent(w, "response.output_item.added", ResponseEvent{
				Type:           "response.output_item.added",
				SequenceNumber: seq,
				OutputIndex:    len(outputItems),
				Item:           addedItem,
			})
			seq++
			flushAndExtend()

			writeSSEEvent(w, "response.content_part.added", ResponseEvent{
				Type:           "response.content_part.added",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "function_call_arguments", Arguments: ""},
			})
			seq++
			flushAndExtend()

			writeSSEEvent(w, "response.function_call_arguments.delta", ResponseEvent{
				Type:           "response.function_call_arguments.delta",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Delta:          ev.arguments,
			})
			seq++
			flushAndExtend()

			writeSSEEvent(w, "response.function_call_arguments.done", ResponseEvent{
				Type:           "response.function_call_arguments.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
			})
			seq++
			flushAndExtend()

			writeSSEEvent(w, "response.content_part.done", ResponseEvent{
				Type:           "response.content_part.done",
				SequenceNumber: seq,
				ItemID:         itemID,
				OutputIndex:    len(outputItems),
				ContentIndex:   0,
				Part:           Content{Type: "function_call_arguments", Arguments: ev.arguments},
			})
			seq++
			flushAndExtend()

			doneItem := ResponseItem{
				Type:      "function_call",
				ID:        itemID,
				Status:    "completed",
				CallID:    ev.callID,
				Name:      ev.name,
				Arguments: ev.arguments,
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

	// Close any lingering streaming text item before final response.
	closeActiveTextItem()
	closeActiveReasoningItem()

	// Final response.completed with accumulated output.
	resp.Status = "completed"
	resp.Usage = Usage{InputTokens: 0, OutputTokens: 0}
	if len(outputItems) > 0 {
		resp.Output = stripImageURLs(outputItems)
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

// extractRequestContent extracts text and media from the request.
// It prefers req.Content array, falling back to req.Input for backward compatibility.
func extractRequestContent(req *CreateResponseRequest) (string, []string) {
	if len(req.Content) > 0 {
		var textParts []string
		var media []string
		for _, part := range req.Content {
			switch part.Type {
			case "input_text":
				if strings.TrimSpace(part.Content) != "" {
					textParts = append(textParts, part.Content)
				}
			case "input_image", "input_file":
				if strings.TrimSpace(part.Content) != "" {
					media = append(media, part.Content)
				}
			}
		}
		return strings.Join(textParts, "\n"), media
	}

	// Fallback to legacy Input field
	return normalizeInput(req.Input), nil
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

// stripImageURLs removes ImageURL from output_image content parts to save
// bandwidth in SSE events where the full data URL was already sent via
// response.content_part.done.
func stripImageURLs(items []ResponseItem) []ResponseItem {
	stripped := make([]ResponseItem, len(items))
	for i, item := range items {
		stripped[i] = stripImageURLsFromItem(item)
	}
	return stripped
}

func stripImageURLsFromItem(item ResponseItem) ResponseItem {
	if len(item.Content) == 0 {
		return item
	}
	newContent := make([]Content, len(item.Content))
	for j, c := range item.Content {
		newContent[j] = c
		if c.Type == "output_image" {
			newContent[j].ImageURL = ""
		}
	}
	item.Content = newContent
	return item
}

// isImageDataURL returns true if the data URL has an image MIME type.
func isImageDataURL(url string) bool {
	return strings.HasPrefix(url, "data:image/")
}

// saveDataURLToTemp decodes a base64 data URL and writes it to the media temp
// directory. Returns the absolute path of the saved file.
func saveDataURLToTemp(dataURL string) (string, error) {
	if !strings.HasPrefix(dataURL, "data:") {
		return "", fmt.Errorf("not a data URL")
	}

	payload := strings.TrimPrefix(dataURL, "data:")
	meta, dataBase64, found := strings.Cut(payload, ",")
	if !found {
		return "", fmt.Errorf("invalid data URL format")
	}

	mime, _, _ := strings.Cut(meta, ";")
	mime = strings.TrimSpace(mime)

	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(dataBase64))
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	ext := extFromMime(mime)
	tmpDir := media.TempDir()
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	filename := fmt.Sprintf("upload_%s%s", uuid.New().String(), ext)
	path := filepath.Join(tmpDir, filename)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	return path, nil
}

// extFromMime returns a file extension for a MIME type.
func extFromMime(mime string) string {
	switch strings.ToLower(mime) {
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/markdown":
		return ".md"
	case "application/json":
		return ".json"
	case "application/xml", "text/xml":
		return ".xml"
	case "application/javascript", "text/javascript":
		return ".js"
	case "text/css":
		return ".css"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		if idx := strings.LastIndex(mime, "/"); idx != -1 && idx < len(mime)-1 {
			return "." + strings.TrimPrefix(mime[idx+1:], "x-")
		}
		return ".bin"
	}
}
