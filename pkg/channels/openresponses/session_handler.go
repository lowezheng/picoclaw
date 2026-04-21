package openresponses

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
)

func (c *OpenResponsesChannel) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}

	dir := c.sessionsDir()
	if dir == "" {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to resolve sessions directory")
		return
	}

	store := newSessionStore(dir)
	items, err := store.listSessions(defaultToolFeedbackMaxArgsLength())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list sessions")
		return
	}

	items = paginateSessions(items, r.URL.Query().Get("offset"), r.URL.Query().Get("limit"))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func (c *OpenResponsesChannel) handleGetSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
		return
	}

	sessionID := extractPathParam(r.URL.Path, "/v1/responses/sessions/")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing session id")
		return
	}

	dir := c.sessionsDir()
	if dir == "" {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to resolve sessions directory")
		return
	}

	store := newSessionStore(dir)
	detail, err := store.getSession(sessionID, defaultToolFeedbackMaxArgsLength())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "server_error", "failed to get session")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":       detail.ID,
		"messages": detail.Messages,
		"summary":  detail.Summary,
		"created":  detail.Created,
		"updated":  detail.Updated,
	})
}

func (c *OpenResponsesChannel) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only DELETE is supported")
		return
	}

	sessionID := extractPathParam(r.URL.Path, "/v1/responses/sessions/")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing session id")
		return
	}

	dir := c.sessionsDir()
	if dir == "" {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to resolve sessions directory")
		return
	}

	store := newSessionStore(dir)
	if err := store.deleteSession(sessionID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
		} else {
			writeError(w, http.StatusInternalServerError, "server_error", "failed to delete session")
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func extractPathParam(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	param := strings.TrimPrefix(path, prefix)
	// Drop any trailing slash
	return strings.TrimSuffix(param, "/")
}
