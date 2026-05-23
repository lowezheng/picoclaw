package openresponses

import (
	"path/filepath"

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
	// TODO: implement using pkg/session session store
	return []sessionListItem{}
}

func (c *OpenResponsesChannel) getSession(id string) map[string]any {
	// TODO: implement using pkg/session session store
	return nil
}

func (c *OpenResponsesChannel) deleteSession(id string) bool {
	// TODO: implement using pkg/session session store
	return false
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
