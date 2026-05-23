package openresponses

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// sessionsDir returns the directory where session data is stored.
func (c *OpenResponsesChannel) sessionsDir() string {
	// Workspace is not directly available; use a default relative path.
	// In practice this should come from config or environment.
	return filepath.Join("sessions")
}

func (c *OpenResponsesChannel) listSessions(offset, limit int) []sessionListItem {
	c.sessionRegMu.RLock()
	defer c.sessionRegMu.RUnlock()

	items := make([]sessionListItem, 0, len(c.sessionRegistry))
	for _, entry := range c.sessionRegistry {
		items = append(items, sessionListItem{
			ID:           entry.id,
			Title:        entry.id,
			Preview:      entry.preview,
			MessageCount: entry.msgCount,
			Created:      entry.createdAt.Format(time.RFC3339),
			Updated:      entry.updatedAt.Format(time.RFC3339),
		})
	}

	// Sort by updatedAt desc
	sort.Slice(items, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, items[i].Updated)
		tj, _ := time.Parse(time.RFC3339, items[j].Updated)
		return ti.After(tj)
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
	c.sessionRegMu.RLock()
	entry, ok := c.sessionRegistry[id]
	c.sessionRegMu.RUnlock()
	if !ok {
		return nil
	}
	return map[string]any{
		"id":            entry.id,
		"title":         entry.id,
		"preview":       entry.preview,
		"message_count": entry.msgCount,
		"created":       entry.createdAt.Format(time.RFC3339),
		"updated":       entry.updatedAt.Format(time.RFC3339),
	}
}

func (c *OpenResponsesChannel) deleteSession(id string) bool {
	c.sessionRegMu.Lock()
	defer c.sessionRegMu.Unlock()
	if _, ok := c.sessionRegistry[id]; !ok {
		return false
	}
	delete(c.sessionRegistry, id)
	return true
}

// visibleSessionMessages filters raw session messages to only those visible to the user.
func visibleSessionMessages(msgs []providers.Message) []map[string]any {
	var result []map[string]any
	for _, msg := range msgs {
		if msg.Role == "user" {
			if msg.Content != "" || len(msg.Media) > 0 {
				result = append(result, map[string]any{
					"role":    "user",
					"content": msg.Content,
					"media":   msg.Media,
				})
			}
			continue
		}
		if msg.Role != "assistant" {
			continue
		}
		// Skip transient thought
		if msg.Content == "" && msg.ReasoningContent != "" && len(msg.ToolCalls) == 0 && len(msg.Media) == 0 {
			continue
		}
		// Skip internal-only
		if msg.Content == "Requested output delivered via tool attachment." {
			continue
		}
		// Skip empty
		if msg.Content == "" && len(msg.Media) == 0 {
			continue
		}
		result = append(result, map[string]any{
			"role":    "assistant",
			"content": msg.Content,
		})
	}
	return result
}

// extractOpenresponsesSessionIDFromScope extracts the openresponses session ID from a SessionScope.
func extractOpenresponsesSessionIDFromScope(scope session.SessionScope) (string, bool) {
	if scope.Channel != "openresponses" {
		return "", false
	}
	chatValue := scope.Values["chat"]
	if chatValue != "" {
		return chatValue, true
	}
	senderID := scope.Values["sender"]
	if senderID != "" {
		return senderID, true
	}
	return "", false
}
