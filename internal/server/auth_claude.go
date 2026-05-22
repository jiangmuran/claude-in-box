package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/clauth"
	"github.com/jiangmuran/claude-in-box/internal/prefs"
)

// prefsAuthSubscription / prefsAuthAPI are the pref deltas applied by
// the mutual-exclusion side of the auth flow.
var (
	prefsAuthSubscription = prefs.Prefs{DefaultAuthMode: "subscription"}
	prefsAuthAPI          = prefs.Prefs{DefaultAuthMode: "api_key"}
)

// --- handlers ---------------------------------------------------------------

func (s *Server) claudeAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ClaudeAuth == nil {
		writeErr(w, http.StatusServiceUnavailable, "claude auth not configured (no claude binary)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := s.cfg.ClaudeAuth.Status(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "claude auth status: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type startReq struct {
	SSO     bool   `json:"sso,omitempty"`
	Console bool   `json:"console,omitempty"`
	Email   string `json:"email,omitempty"`
}

func (s *Server) claudeAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ClaudeAuth == nil {
		writeErr(w, http.StatusServiceUnavailable, "claude auth not configured")
		return
	}
	var req startReq
	// Body is optional.
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	flow, err := s.cfg.ClaudeAuth.Start(ctx, clauth.StartOptions{
		SSO:         req.SSO,
		Console:     req.Console,
		Email:       req.Email,
		URLTimeout:  30 * time.Second,
		IdleTimeout: 5 * time.Minute,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, flow.Snapshot())
}

type codeReq struct {
	FlowID string `json:"flow_id"`
	Code   string `json:"code"`
}

func (s *Server) claudeAuthCode(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ClaudeAuth == nil {
		writeErr(w, http.StatusServiceUnavailable, "claude auth not configured")
		return
	}
	var req codeReq
	if !readJSON(w, r, &req) {
		return
	}
	flow := s.cfg.ClaudeAuth.GetFlow(req.FlowID)
	if flow == nil {
		writeErr(w, http.StatusNotFound, "no such flow")
		return
	}
	// Outer timeout is comfortably larger than SubmitCode's internal 30s
	// rejection deadline, so the inner ErrInvalidCode path always wins.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := flow.SubmitCode(ctx, req.Code); err != nil {
		// Invalid-code is a recoverable user error: state has been reset
		// to awaiting_code and the UI may post another code on the same
		// flow_id. Surface as 400 with retryable=true.
		retryable := errors.Is(err, clauth.ErrInvalidCode)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":     err.Error(),
			"retryable": retryable,
			"snapshot":  flow.Snapshot(),
		})
		return
	}
	// Mutual-exclusion: a successful subscription login wipes any
	// configured third-party API providers so the next session can't
	// accidentally bill against an API key. Best-effort — failure here
	// must not unwind the login itself.
	if s.cfg.Providers != nil {
		for _, p := range s.cfg.Providers.List() {
			_ = s.cfg.Providers.Delete(p.ID)
		}
	}
	if s.cfg.Prefs != nil {
		_ = s.cfg.Prefs.Patch(prefsAuthSubscription)
	}
	writeJSON(w, http.StatusOK, flow.Snapshot())
}

type cancelReq struct {
	FlowID string `json:"flow_id"`
}

func (s *Server) claudeAuthCancel(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ClaudeAuth == nil {
		writeErr(w, http.StatusServiceUnavailable, "claude auth not configured")
		return
	}
	var req cancelReq
	if !readJSON(w, r, &req) {
		return
	}
	flow := s.cfg.ClaudeAuth.GetFlow(req.FlowID)
	if flow == nil {
		writeErr(w, http.StatusNotFound, "no such flow")
		return
	}
	flow.Cancel()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) claudeAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ClaudeAuth == nil {
		writeErr(w, http.StatusServiceUnavailable, "claude auth not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.cfg.ClaudeAuth.Logout(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, "logout: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
