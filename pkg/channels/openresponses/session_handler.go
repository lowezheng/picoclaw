package openresponses

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (c *OpenResponsesChannel) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !c.checkAuth(r) {
		writeError(w, http.StatusUnauthorized, "invalid_request", "", "Invalid token")
		return
	}

	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")
	offset := 0
	limit := 20
	if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
		offset = v
	}
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}

	items := c.listSessions(offset, limit)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(items)
}

func (c *OpenResponsesChannel) handleSessionDetail(w http.ResponseWriter, r *http.Request, id string) {
	if !c.checkAuth(r) {
		writeError(w, http.StatusUnauthorized, "invalid_request", "", "Invalid token")
		return
	}
	switch r.Method {
	case http.MethodGet:
		session := c.getSession(id)
		if session == nil {
			writeError(w, http.StatusNotFound, "not_found", "", "Session not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(session)
	case http.MethodDelete:
		if !c.deleteSession(id) {
			writeError(w, http.StatusNotFound, "not_found", "", "Session not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "invalid_request", "", "Method not allowed")
	}
}
