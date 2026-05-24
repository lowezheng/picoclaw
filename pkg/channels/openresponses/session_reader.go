package openresponses

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

const maxSessionJSONLLineSize = 10 * 1024 * 1024

func (c *OpenResponsesChannel) sessionsDir() string {
	if c.workspace == "" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".picoclaw", "workspace", "sessions")
	}
	return filepath.Join(c.workspace, "sessions")
}

func sanitizeSessionKey(key string) string {
	key = strings.ReplaceAll(key, ":", "_")
	key = strings.ReplaceAll(key, "/", "_")
	key = strings.ReplaceAll(key, "\\", "_")
	return key
}

func (c *OpenResponsesChannel) readSessionMeta(path, sessionKey string) (memory.SessionMeta, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return memory.SessionMeta{Key: sessionKey}, nil
	}
	if err != nil {
		return memory.SessionMeta{}, err
	}
	var meta memory.SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return memory.SessionMeta{}, err
	}
	if meta.Key == "" {
		meta.Key = sessionKey
	}
	return meta, nil
}

func (c *OpenResponsesChannel) readSessionMessages(path string, skip int) ([]providers.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	msgs := make([]providers.Message, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSessionJSONLLineSize)

	seen := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		seen++
		if seen <= skip {
			continue
		}
		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func extractOpenResponsesSessionIDFromScope(scope session.SessionScope) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(scope.Channel), "openresponses") {
		return "", false
	}
	chatValue := strings.TrimSpace(scope.Values["chat"])
	if chatValue != "" {
		if idx := strings.Index(chatValue, ":"); idx >= 0 {
			sessionID := strings.TrimSpace(chatValue[idx+1:])
			if sessionID != "" {
				return sessionID, true
			}
		}
		return chatValue, true
	}
	senderID := strings.TrimSpace(scope.Values["sender"])
	if senderID != "" {
		return senderID, true
	}
	return "", false
}

type openResponsesSessionRef struct {
	ID  string
	Key string
}

func (c *OpenResponsesChannel) findOpenResponsesSessions(dir string) ([]openResponsesSessionRef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	refs := make([]openResponsesSessionRef, 0)
	seen := make(map[string]struct{})
	metaBackedBases := make(map[string]struct{})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		name := entry.Name()
		metaPath := filepath.Join(dir, name)
		meta, err := c.readSessionMeta(metaPath, "")
		if err != nil {
			continue
		}
		if len(meta.Scope) == 0 {
			continue
		}
		var scope session.SessionScope
		if err := json.Unmarshal(meta.Scope, &scope); err != nil {
			continue
		}
		sessionID, ok := extractOpenResponsesSessionIDFromScope(scope)
		if !ok || sessionID == "" || meta.Key == "" {
			continue
		}
		base := strings.TrimSuffix(name, ".meta.json")
		metaBackedBases[base] = struct{}{}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		refs = append(refs, openResponsesSessionRef{ID: sessionID, Key: meta.Key})
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		name := entry.Name()
		base := strings.TrimSuffix(name, ".jsonl")
		if _, ok := metaBackedBases[base]; ok {
			continue
		}
		if !session.IsOpaqueSessionKey(base) {
			continue
		}
		if _, exists := seen[base]; exists {
			continue
		}
		seen[base] = struct{}{}
		refs = append(refs, openResponsesSessionRef{ID: base, Key: base})
	}

	return refs, nil
}

func (c *OpenResponsesChannel) findOpenResponsesSession(dir, sessionID string) (openResponsesSessionRef, error) {
	refs, err := c.findOpenResponsesSessions(dir)
	if err != nil {
		return openResponsesSessionRef{}, err
	}
	for _, ref := range refs {
		if ref.ID == sessionID {
			return ref, nil
		}
	}
	return openResponsesSessionRef{}, os.ErrNotExist
}

func (c *OpenResponsesChannel) readJSONLSession(dir, sessionKey string) (struct {
	Key      string
	Messages []providers.Message
	Summary  string
	Created  time.Time
	Updated  time.Time
}, error) {
	base := filepath.Join(dir, sanitizeSessionKey(sessionKey))
	jsonlPath := base + ".jsonl"
	metaPath := base + ".meta.json"

	meta, err := c.readSessionMeta(metaPath, sessionKey)
	if err != nil {
		return struct {
			Key      string
			Messages []providers.Message
			Summary  string
			Created  time.Time
			Updated  time.Time
		}{}, err
	}

	messages, err := c.readSessionMessages(jsonlPath, meta.Skip)
	if err != nil {
		return struct {
			Key      string
			Messages []providers.Message
			Summary  string
			Created  time.Time
			Updated  time.Time
		}{}, err
	}

	updated := meta.UpdatedAt
	created := meta.CreatedAt
	if created.IsZero() || updated.IsZero() {
		if info, statErr := os.Stat(jsonlPath); statErr == nil {
			if created.IsZero() {
				created = info.ModTime()
			}
			if updated.IsZero() {
				updated = info.ModTime()
			}
		}
	}

	return struct {
		Key      string
		Messages []providers.Message
		Summary  string
		Created  time.Time
		Updated  time.Time
	}{
		Key:      meta.Key,
		Messages: messages,
		Summary:  meta.Summary,
		Created:  created,
		Updated:  updated,
	}, nil
}

func isEmptySession(messages []providers.Message, summary string) bool {
	return len(messages) == 0 && strings.TrimSpace(summary) == ""
}

func buildOpenResponsesSessionListItem(sessionID string, messages []providers.Message, summary string, created, updated time.Time) sessionListItem {
	visibleCount := 0
	preview := ""
	for _, msg := range messages {
		if msg.Role == "tool" {
			continue
		}
		visibleCount++
		if msg.Role == "user" && preview == "" {
			if msg.Content != "" {
				preview = truncatePreview(msg.Content)
			} else if len(msg.Media) > 0 {
				preview = "[image]"
			}
		}
	}
	if preview == "" {
		preview = "(empty)"
	}
	return sessionListItem{
		ID:           sessionID,
		Title:        preview,
		Preview:      preview,
		MessageCount: visibleCount,
		Created:      created.Format(time.RFC3339),
		Updated:      updated.Format(time.RFC3339),
	}
}

func (c *OpenResponsesChannel) listSessions(offset, limit int) []sessionListItem {
	dir := c.sessionsDir()
	refs, err := c.findOpenResponsesSessions(dir)
	if err != nil {
		return []sessionListItem{}
	}

	items := make([]sessionListItem, 0, len(refs))
	for _, ref := range refs {
		sess, err := c.readJSONLSession(dir, ref.Key)
		if err != nil || isEmptySession(sess.Messages, sess.Summary) {
			continue
		}
		items = append(items, buildOpenResponsesSessionListItem(ref.ID, sess.Messages, sess.Summary, sess.Created, sess.Updated))
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Updated > items[j].Updated
	})

	if offset > len(items) {
		return []sessionListItem{}
	}
	items = items[offset:]
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}

func (c *OpenResponsesChannel) getSession(id string) map[string]any {
	dir := c.sessionsDir()
	ref, err := c.findOpenResponsesSession(dir, id)
	if err != nil {
		return nil
	}
	sess, err := c.readJSONLSession(dir, ref.Key)
	if err != nil {
		return nil
	}

	messages := make([]map[string]any, 0)
	for _, msg := range sess.Messages {
		if msg.Role == "tool" {
			continue
		}
		attachments := make([]map[string]any, 0)
		for _, att := range msg.Attachments {
			url := att.URL
			if url == "" && strings.HasPrefix(att.Ref, "media://") {
				continue
			}
			if url == "" {
				url = att.Ref
			}
			if url == "" {
				continue
			}
			attachments = append(attachments, map[string]any{
				"type":         att.Type,
				"url":          url,
				"filename":     att.Filename,
				"content_type": att.ContentType,
			})
		}
		messages = append(messages, map[string]any{
			"role":        msg.Role,
			"content":     msg.Content,
			"media":       msg.Media,
			"attachments": attachments,
		})
	}

	return map[string]any{
		"id":            id,
		"title":         id,
		"preview":       "",
		"message_count": len(messages),
		"messages":      messages,
		"summary":       sess.Summary,
		"created":       sess.Created.Format(time.RFC3339),
		"updated":       sess.Updated.Format(time.RFC3339),
	}
}

func (c *OpenResponsesChannel) deleteSession(id string) bool {
	dir := c.sessionsDir()
	ref, err := c.findOpenResponsesSession(dir, id)
	if err != nil {
		return false
	}
	base := filepath.Join(dir, sanitizeSessionKey(ref.Key))
	removed := false
	for _, path := range []string{base + ".jsonl", base + ".meta.json"} {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false
		}
		removed = true
	}
	return removed
}
