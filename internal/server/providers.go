package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/providers"
)

type providerReq struct {
	Label   string `json:"label"`
	APIHost string `json:"api_host"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model,omitempty"`
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Providers == nil {
		writeErr(w, http.StatusServiceUnavailable, "providers not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": s.cfg.Providers.List()})
}

func (s *Server) addProvider(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Providers == nil {
		writeErr(w, http.StatusServiceUnavailable, "providers not configured")
		return
	}
	var req providerReq
	if !readJSON(w, r, &req) {
		return
	}
	p, err := s.cfg.Providers.Add(req.Label, req.APIHost, req.APIKey, req.Model)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Mutual-exclusion: adding an api_key provider wipes the
	// container's claude.ai subscription credentials so the next
	// session can't accidentally use them. Idempotent if no creds.
	s.switchAuthToAPIKey(r.Context())
	// Return the public shape so the UI does not echo the key back.
	writeJSON(w, http.StatusCreated, p.Public())
}

// switchAuthToAPIKey runs Logout (best-effort) + flips
// prefs.DefaultAuthMode = "api_key". Used by the addProvider and
// replaceProvider handlers to enforce the user's "configuring an API
// key wipes the subscription" rule.
func (s *Server) switchAuthToAPIKey(ctx context.Context) {
	if s.cfg.ClaudeAuth != nil {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = s.cfg.ClaudeAuth.Logout(lctx)
		cancel()
	}
	if s.cfg.Prefs != nil {
		_ = s.cfg.Prefs.Patch(prefsAuthAPI)
	}
}

func (s *Server) replaceProvider(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Providers == nil {
		writeErr(w, http.StatusServiceUnavailable, "providers not configured")
		return
	}
	id := r.PathValue("id")
	var req providerReq
	if !readJSON(w, r, &req) {
		return
	}
	p, err := s.cfg.Providers.Replace(id, req.Label, req.APIHost, req.APIKey, req.Model)
	if err != nil {
		if errors.Is(err, providers.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.switchAuthToAPIKey(r.Context())
	writeJSON(w, http.StatusOK, p.Public())
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Providers == nil {
		writeErr(w, http.StatusServiceUnavailable, "providers not configured")
		return
	}
	id := r.PathValue("id")
	if err := s.cfg.Providers.Delete(id); err != nil {
		if errors.Is(err, providers.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// probeProviderRequest may carry a full Provider shape (so the UI can
// validate BEFORE creating one) OR an id (validate an existing record).
type probeReq struct {
	ID      string `json:"id,omitempty"`
	Label   string `json:"label,omitempty"`
	APIHost string `json:"api_host,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Model   string `json:"model,omitempty"`
}

func (s *Server) probeProvider(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Providers == nil {
		writeErr(w, http.StatusServiceUnavailable, "providers not configured")
		return
	}
	var req probeReq
	// Path-style probe: POST /api/providers/{id}/probe with empty body.
	idFromPath := r.PathValue("id")
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	if idFromPath != "" {
		req.ID = idFromPath
	}

	var p providers.Provider
	if req.ID != "" {
		got, ok := s.cfg.Providers.Get(req.ID)
		if !ok {
			writeErr(w, http.StatusNotFound, "no such provider")
			return
		}
		p = got
	} else {
		if req.APIHost == "" || req.APIKey == "" {
			writeErr(w, http.StatusBadRequest, "provide either id or { api_host + api_key }")
			return
		}
		p = providers.Provider{
			Label:   req.Label,
			APIHost: req.APIHost,
			APIKey:  req.APIKey,
			Model:   req.Model,
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := providers.ProbeProvider(ctx, p)

	// Marshal directly so we can include latency_ms (more JSON-friendly).
	out := map[string]any{
		"ok":         res.OK,
		"http":       res.HTTP,
		"endpoint":   res.Endpoint,
		"latency_ms": res.Latency.Milliseconds(),
		"detail":     res.Detail,
	}
	_ = json.NewEncoder(w)
	writeJSON(w, http.StatusOK, out)
}
