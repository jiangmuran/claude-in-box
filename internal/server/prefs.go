package server

import (
	"net/http"

	"github.com/jiangmuran/claude-in-box/internal/prefs"
)

func (s *Server) getPrefs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Prefs == nil {
		writeErr(w, http.StatusServiceUnavailable, "prefs not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Prefs.Get())
}

// patchPrefs accepts a partial Prefs body and merges it. Pass "-" for a
// string field to clear it. PUT semantics over JSON, no DELETE needed.
func (s *Server) patchPrefs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Prefs == nil {
		writeErr(w, http.StatusServiceUnavailable, "prefs not configured")
		return
	}
	var req prefs.Prefs
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.cfg.Prefs.Patch(req); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Prefs.Get())
}
