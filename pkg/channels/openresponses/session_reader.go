package openresponses

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	maxSessionJSONLLineSize          = 10 * 1024 * 1024
	maxSessionTitleRunes             = 60
	handledToolResponseSummaryText   = "Requested output delivered via tool attachment."
)

// --- Types ---

type sessionListItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
	Created      string `json:"created"`
	Updated      string `json:"updated"`
}

type sessionChatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Media   []string `json:"media,omitempty"`
}

type sessionDetail struct {
	ID       string             `json:"id"`
	Messages []sessionChatMessage `json:"messages"`
	Summary  string             `json:"summary"`
	Created  string             `json:"created"`
	Updated  string             `json:"updated"`
}

type sessionFile struct {
	Key      string              `json:"key"`
	Messages []providers.Message `json:"messages"`
	Summary  string              `json:"summary,omitempty"`
	Created  time.Time           `json:"created"`
	Updated  time.Time           `json:"updated"`
}

type openresponsesSessionRef struct {
	ID  string
	Key string
}

// --- Session Store ---

type sessionStore struct {
	dir string
}

func newSessionStore(dir string) *sessionStore {
	return &sessionStore{dir: dir}
}

func (s *sessionStore) listSessions(toolFeedbackMaxArgsLength int) ([]sessionListItem, error) {
	if _, err := os.ReadDir(s.dir); err != nil {
		return []sessionListItem{}, nil
	}

	store, err := memory.NewJSONLStore(s.dir)
	if err != nil {
		return nil, err
	}

	keys := store.ListSessions()
	var items []sessionListItem
	seen := make(map[string]struct{})

	for _, key := range keys {
		meta, err := store.GetSessionMeta(context.Background(), key)
		if err != nil {
			continue
		}

		ref, ok := sessionRefFromMeta(meta)
		if !ok || ref.ID == "" {
			continue
		}
		if _, exists := seen[ref.ID]; exists {
			continue
		}
		seen[ref.ID] = struct{}{}

		msgs, err := store.GetHistory(context.Background(), key)
		if err != nil {
			continue
		}

		sess := sessionFile{
			Key:      key,
			Messages: msgs,
			Summary:  meta.Summary,
			Created:  meta.CreatedAt,
			Updated:  meta.UpdatedAt,
		}
		if isEmptySession(sess) {
			continue
		}

		items = append(items, buildSessionListItem(ref.ID, sess, toolFeedbackMaxArgsLength))
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Updated > items[j].Updated
	})

	return items, nil
}

func (s *sessionStore) getSession(sessionID string, toolFeedbackMaxArgsLength int) (sessionDetail, error) {
	store, err := memory.NewJSONLStore(s.dir)
	if err != nil {
		return sessionDetail{}, err
	}

	keys := store.ListSessions()
	var targetKey string
	var targetMeta memory.SessionMeta
	for _, key := range keys {
		meta, err := store.GetSessionMeta(context.Background(), key)
		if err != nil {
			continue
		}
		ref, ok := sessionRefFromMeta(meta)
		if !ok || ref.ID != sessionID {
			continue
		}
		targetKey = key
		targetMeta = meta
		break
	}

	if targetKey == "" {
		return sessionDetail{}, os.ErrNotExist
	}

	msgs, err := store.GetHistory(context.Background(), targetKey)
	if err != nil {
		return sessionDetail{}, err
	}

	sess := sessionFile{
		Key:      targetKey,
		Messages: msgs,
		Summary:  targetMeta.Summary,
		Created:  targetMeta.CreatedAt,
		Updated:  targetMeta.UpdatedAt,
	}

	workspace := resolveWorkspaceFromSessionsDir(s.dir)
	visibleMsgs := visibleSessionMessages(sess.Messages, toolFeedbackMaxArgsLength, workspace)

	return sessionDetail{
		ID:       sessionID,
		Messages: visibleMsgs,
		Summary:  sess.Summary,
		Created:  sess.Created.Format(time.RFC3339),
		Updated:  sess.Updated.Format(time.RFC3339),
	}, nil
}

func (s *sessionStore) deleteSession(sessionID string) error {
	store, err := memory.NewJSONLStore(s.dir)
	if err != nil {
		return err
	}

	keys := store.ListSessions()
	var targetKey string
	for _, key := range keys {
		meta, err := store.GetSessionMeta(context.Background(), key)
		if err != nil {
			continue
		}
		ref, ok := sessionRefFromMeta(meta)
		if !ok || ref.ID != sessionID {
			continue
		}
		targetKey = key
		break
	}

	if targetKey == "" {
		return os.ErrNotExist
	}

	base := filepath.Join(s.dir, sanitizeSessionKey(targetKey))
	for _, path := range []string{base + ".jsonl", base + ".meta.json"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// --- Scope / Ref Extraction ---

func extractOpenresponsesSessionIDFromScope(scope session.SessionScope) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(scope.Channel), "openresponses") {
		return "", false
	}

	chatValue := strings.TrimSpace(scope.Values["chat"])
	if strings.HasPrefix(chatValue, "direct:") {
		sessionID := strings.TrimPrefix(chatValue, "direct:")
		if sessionID != "" {
			return sessionID, true
		}
	}

	senderID := strings.TrimSpace(scope.Values["sender"])
	if senderID != "" {
		return senderID, true
	}

	return "", false
}

func sessionRefFromMeta(meta memory.SessionMeta) (openresponsesSessionRef, bool) {
	if len(meta.Scope) > 0 {
		var scope session.SessionScope
		if err := json.Unmarshal(meta.Scope, &scope); err != nil {
			return openresponsesSessionRef{}, false
		}
		sessionID, ok := extractOpenresponsesSessionIDFromScope(scope)
		if ok {
			return openresponsesSessionRef{ID: sessionID, Key: meta.Key}, true
		}
	}

	// Fallback: legacy aliases that contain openresponses channel info
	for _, alias := range meta.Aliases {
		if id, ok := extractOpenresponsesSessionIDFromLegacyAlias(alias); ok {
			return openresponsesSessionRef{ID: id, Key: meta.Key}, true
		}
	}

	return openresponsesSessionRef{}, false
}

func extractOpenresponsesSessionIDFromLegacyAlias(alias string) (string, bool) {
	parts := strings.Split(alias, ":")
	if len(parts) < 4 {
		return "", false
	}
	for i, part := range parts {
		if strings.EqualFold(part, "openresponses") && i+2 < len(parts) {
			if strings.EqualFold(parts[i+1], "direct") {
				return parts[i+2], true
			}
		}
	}
	for i, part := range parts {
		if strings.EqualFold(part, "direct") && i+1 < len(parts) {
			return parts[i+1], true
		}
	}
	return "", false
}

// --- List Item Builder ---

func buildSessionListItem(sessionID string, sess sessionFile, toolFeedbackMaxArgsLength int) sessionListItem {
	preview := ""
	for _, msg := range sess.Messages {
		if msg.Role == "user" {
			preview = sessionMessagePreview(msg)
		}
		if preview != "" {
			break
		}
	}
	preview = truncateRunes(preview, maxSessionTitleRunes)

	if preview == "" {
		preview = "(empty)"
	}
	title := preview

	validMessageCount := len(visibleSessionMessages(sess.Messages, toolFeedbackMaxArgsLength, ""))

	return sessionListItem{
		ID:           sessionID,
		Title:        title,
		Preview:      preview,
		MessageCount: validMessageCount,
		Created:      sess.Created.Format(time.RFC3339),
		Updated:      sess.Updated.Format(time.RFC3339),
	}
}

func isEmptySession(sess sessionFile) bool {
	return len(sess.Messages) == 0 && strings.TrimSpace(sess.Summary) == ""
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "..."
}

// --- Visible Message Filtering ---

func sessionMessageVisible(msg providers.Message) bool {
	return strings.TrimSpace(msg.Content) != "" || len(msg.Media) > 0
}

func sessionMessagePreview(msg providers.Message) string {
	if content := strings.TrimSpace(msg.Content); content != "" {
		return content
	}
	if len(msg.Media) > 0 {
		return "[image]"
	}
	return ""
}

func visibleSessionMessages(messages []providers.Message, toolFeedbackMaxArgsLength int, workspace string) []sessionChatMessage {
	transcript := make([]sessionChatMessage, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if sessionMessageVisible(msg) {
				transcript = append(transcript, sessionChatMessage{
					Role:    "user",
					Content: msg.Content,
					Media:   append([]string(nil), msg.Media...),
				})
			}

		case "assistant":
			if assistantMessageTransientThought(msg) {
				continue
			}

			toolSummaryMessages := visibleAssistantToolSummaryMessages(msg.ToolCalls, toolFeedbackMaxArgsLength, workspace)
			if len(toolSummaryMessages) > 0 {
				transcript = append(transcript, toolSummaryMessages...)
			}

			visibleToolMessages := visibleAssistantToolMessages(msg.ToolCalls)
			if len(visibleToolMessages) > 0 {
				transcript = append(transcript, visibleToolMessages...)
			}

			if !sessionMessageVisible(msg) || assistantMessageInternalOnly(msg) {
				continue
			}

			transcript = append(transcript, sessionChatMessage{
				Role:    "assistant",
				Content: msg.Content,
				Media:   append([]string(nil), msg.Media...),
			})
		}
	}

	return transcript
}

func assistantMessageTransientThought(msg providers.Message) bool {
	return strings.TrimSpace(msg.Content) == "" &&
		strings.TrimSpace(msg.ReasoningContent) != "" &&
		len(msg.ToolCalls) == 0 &&
		len(msg.Media) == 0
}

func assistantMessageInternalOnly(msg providers.Message) bool {
	return strings.TrimSpace(msg.Content) == handledToolResponseSummaryText
}

// --- Tool Summary Helpers ---

func visibleAssistantToolSummaryMessages(
	toolCalls []providers.ToolCall,
	toolFeedbackMaxArgsLength int,
	workspace string,
) []sessionChatMessage {
	if len(toolCalls) == 0 {
		return nil
	}
	if toolFeedbackMaxArgsLength <= 0 {
		toolFeedbackMaxArgsLength = defaultToolFeedbackMaxArgsLength()
	}

	messages := make([]sessionChatMessage, 0, len(toolCalls))
	for _, tc := range toolCalls {
		name := tc.Name
		argsJSON := ""
		if tc.Function != nil {
			if name == "" {
				name = tc.Function.Name
			}
			argsJSON = tc.Function.Arguments
		}

		if strings.TrimSpace(name) == "" {
			continue
		}

		if strings.TrimSpace(argsJSON) == "" && len(tc.Arguments) > 0 {
			if encodedArgs, err := json.Marshal(tc.Arguments); err == nil {
				argsJSON = string(encodedArgs)
			}
		}

		argsPreview := strings.TrimSpace(argsJSON)
		if argsPreview == "" {
			argsPreview = "{}"
		}

		var media []string
		if name == "send_file" && workspace != "" {
			if dataURL := encodeSendFileImage(workspace, argsJSON); dataURL != "" {
				media = append(media, dataURL)
			}
		}

		messages = append(messages, sessionChatMessage{
			Role:    "assistant",
			Content: utils.FormatToolFeedbackMessage(name, utils.Truncate(argsPreview, toolFeedbackMaxArgsLength)),
			Media:   media,
		})
	}

	return messages
}

func visibleAssistantToolMessages(toolCalls []providers.ToolCall) []sessionChatMessage {
	if len(toolCalls) == 0 {
		return nil
	}

	messages := make([]sessionChatMessage, 0, len(toolCalls))
	for _, tc := range toolCalls {
		name := tc.Name
		argsJSON := ""
		if tc.Function != nil {
			if name == "" {
				name = tc.Function.Name
			}
			argsJSON = tc.Function.Arguments
		}

		switch name {
		case "message":
			var args struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				continue
			}
			if strings.TrimSpace(args.Content) == "" {
				continue
			}
			messages = append(messages, sessionChatMessage{
				Role:    "assistant",
				Content: args.Content,
			})
		}
	}

	return messages
}

func defaultToolFeedbackMaxArgsLength() int {
	defaults := config.AgentDefaults{}
	return defaults.GetToolFeedbackMaxArgsLength()
}

// --- File/Image Helpers ---

func resolveWorkspaceFromSessionsDir(dir string) string {
	ws := filepath.Dir(dir)
	if len(ws) > 0 && ws[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(ws) > 1 && ws[1] == '/' {
			ws = home + ws[1:]
		} else {
			ws = home
		}
	}
	abs, _ := filepath.Abs(ws)
	return abs
}

func resolveSendFilePath(workspace, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if workspace == "" {
		home, _ := os.UserHomeDir()
		workspace = filepath.Join(home, ".picoclaw", "workspace")
	}
	absWorkspace, _ := filepath.Abs(workspace)
	absPath, _ := filepath.Abs(filepath.Join(absWorkspace, path))
	return absPath
}

func imageMimeFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

func encodeSendFileImage(workspace, argsJSON string) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	if strings.TrimSpace(args.Path) == "" {
		return ""
	}

	resolved := resolveSendFilePath(workspace, args.Path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ""
	}

	mime := imageMimeFromExt(resolved)
	if mime == "" {
		return ""
	}

	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func resolveSessionsDir(workspace string) string {
	if workspace == "" {
		home, _ := os.UserHomeDir()
		workspace = filepath.Join(home, ".picoclaw", "workspace")
	}
	if len(workspace) > 0 && workspace[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(workspace) > 1 && workspace[1] == '/' {
			workspace = home + workspace[1:]
		} else {
			workspace = home
		}
	}
	return filepath.Join(workspace, "sessions")
}

func sanitizeSessionKey(key string) string {
	key = strings.ReplaceAll(key, ":", "_")
	key = strings.ReplaceAll(key, "/", "_")
	key = strings.ReplaceAll(key, "\\", "_")
	return key
}

// --- Pagination Helper ---

func paginateSessions(items []sessionListItem, offsetStr, limitStr string) []sessionListItem {
	offset := 0
	limit := 20
	if val, err := strconv.Atoi(offsetStr); err == nil && val >= 0 {
		offset = val
	}
	if val, err := strconv.Atoi(limitStr); err == nil && val > 0 {
		limit = val
	}

	totalItems := len(items)
	if offset >= totalItems {
		return []sessionListItem{}
	}
	end := offset + limit
	if end > totalItems {
		end = totalItems
	}
	return items[offset:end]
}
