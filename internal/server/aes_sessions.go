package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/session"
)

// /aes/sessions/* surface — the embedded-friendly mirror of the
// bearer-authenticated /api/sessions/* routes.  Same business logic,
// same authorization model (token-scoped), but wrapped in the v2 AES
// record-stream envelope so an MCU client can use ONE credential (the
// 32-byte AES master) instead of also having to carry a bearer for
// management calls.
//
// Routes added here:
//
//   GET    /aes/sessions                       — list, one-shot
//   POST   /aes/sessions                       — create, one-shot
//   GET    /aes/sessions/{id}                  — status snapshot
//   DELETE /aes/sessions/{id}                  — kill (SIGTERM by default)
//   PUT    /aes/sessions/{id}/metadata         — set title / goal
//   POST   /aes/sessions/{id}/model            — switch model
//   POST   /aes/sessions/{id}/interrupt        — Ctrl-C into the PTY
//   GET    /aes/sessions/{id}/usage            — token totals
//
// The data-plane /aes/sessions/{id}/{input,chat,events/stream} live in
// aes.go and are unchanged here.

// ---- list -----------------------------------------------------------

// aesSessionListEntry is the slim per-session shape served by /aes/
// sessions list — same fields as session.Status but trimmed of the
// credentials-ish ones (auth_mode, claude_session_id) since the device
// has no use for them and they bloat the embedded response.
type aesSessionListEntry struct {
	ID        string        `json:"id"`
	Title     string        `json:"title,omitempty"`
	Goal      string        `json:"goal,omitempty"`
	Model     string        `json:"model,omitempty"`
	Workdir   string        `json:"workdir,omitempty"`
	State     string        `json:"state"`
	CreatedAt time.Time     `json:"created_at"`
	StartedAt time.Time     `json:"started_at,omitempty"`
	LastSeq   uint64        `json:"last_seq"`
	Usage     session.Usage `json:"usage"`
}

func slimEntry(s *session.Session) aesSessionListEntry {
	st := s.Status()
	return aesSessionListEntry{
		ID:        st.ID,
		Title:     st.Title,
		Goal:      st.Goal,
		Model:     st.Model,
		Workdir:   st.Workdir,
		State:     string(st.State),
		CreatedAt: st.CreatedAt,
		StartedAt: st.StartedAt,
		LastSeq:   st.LastSeq,
		Usage:     st.Usage,
	}
}

func (s *Server) aesSessionsList(w http.ResponseWriter, r *http.Request) {
	_, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	list := s.cfg.Sessions.List()
	out := make([]aesSessionListEntry, 0, len(list))
	for _, sess := range list {
		out = append(out, slimEntry(sess))
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, map[string]any{
		"sessions": out,
		"count":    len(out),
	})
}

// ---- create ---------------------------------------------------------

// aesCreateSessionRequest is the *slim* subset of createSessionRequest:
// no api_key / oauth / provider_id / auth_mode fields. Embedded
// callers can't paste long upstream keys easily and rarely need to —
// the box reads CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY from its
// own env or its in-container `claude auth login` credentials. If a
// client really needs to pass an upstream key it can still do so via
// the regular REST /api/sessions endpoint.
type aesCreateSessionRequest struct {
	Workdir           string `json:"workdir,omitempty"`
	Model             string `json:"model,omitempty"`
	Effort            string `json:"effort,omitempty"`
	Title             string `json:"title,omitempty"`
	Goal              string `json:"goal,omitempty"`
	ResumeFrom        string `json:"resume_from,omitempty"`
	BypassPermissions *bool  `json:"bypass_permissions,omitempty"`
}

func (s *Server) aesSessionsCreate(w http.ResponseWriter, r *http.Request) {
	plaintext, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	var req aesCreateSessionRequest
	if len(plaintext) > 0 {
		if jerr := json.Unmarshal(plaintext, &req); jerr != nil {
			s.writeEnvelopeJSON(w, r, ec, http.StatusBadRequest, map[string]any{"error": "invalid json: " + jerr.Error()})
			return
		}
	}
	if req.Workdir == "" {
		req.Workdir = "/workspace"
		if cwd, werr := os.Getwd(); werr == nil {
			req.Workdir = cwd
		}
	}
	bypass := true
	if req.BypassPermissions != nil {
		bypass = *req.BypassPermissions
	}

	// Resolve auth from env (same fallback chain as REST createSession,
	// minus the request-body knobs).
	authMode, apiKey, oauth, errResp := s.resolveAuthFromEnv()
	if errResp != "" {
		s.writeEnvelopeJSON(w, r, ec, http.StatusBadRequest, map[string]any{"error": errResp})
		return
	}

	resumeFrom := req.ResumeFrom
	if resumeFrom != "" {
		if prior, ok := s.cfg.Sessions.Get(resumeFrom); ok && prior.ClaudeSessionID != "" {
			resumeFrom = prior.ClaudeSessionID
		}
	}

	sess, err2 := s.cfg.Sessions.Spawn(r.Context(), session.SpawnOptions{
		Workdir:           req.Workdir,
		Model:             req.Model,
		Effort:            req.Effort,
		AuthMode:          authMode,
		APIKey:            apiKey,
		OAuthToken:        oauth,
		ResumeFrom:        resumeFrom,
		BypassPermissions: bypass,
	})
	if err2 != nil {
		s.writeEnvelopeJSON(w, r, ec, http.StatusInternalServerError, map[string]any{"error": "spawn: " + err2.Error()})
		return
	}
	if req.Title != "" {
		_ = sess.SetTitle(req.Title)
	}
	if req.Goal != "" {
		_ = sess.SetGoal(req.Goal)
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusCreated, slimEntry(sess))
}

// resolveAuthFromEnv picks an auth mode + credentials from the box's
// environment alone (no request-body overrides). Returns
// (mode, apiKey, oauth, errMsg). errMsg empty means success.
func (s *Server) resolveAuthFromEnv() (mode, apiKey, oauth, errMsg string) {
	oauth = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	apiKey = os.Getenv("ANTHROPIC_API_KEY")
	switch {
	case oauth != "":
		return "subscription", "", oauth, ""
	case apiKey != "":
		return "api_key", apiKey, "", ""
	default:
		// `claude auth login` on-disk credentials are picked up
		// silently when subscription mode is selected with no token —
		// see REST createSession for the ClaudeAuth.Status() check.
		if s.cfg.ClaudeAuth != nil {
			return "subscription", "", "", ""
		}
		return "", "", "", "no auth: set CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY on the box, or `claude auth login` from inside the container"
	}
}

// ---- get / delete / metadata / model / interrupt -------------------

func (s *Server) aesSessionGet(w http.ResponseWriter, r *http.Request) {
	_, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, slimEntry(sess))
}

func (s *Server) aesSessionDelete(w http.ResponseWriter, r *http.Request) {
	plaintext, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}
	sig := os.Signal(syscall.SIGTERM)
	if len(plaintext) > 0 {
		var req struct {
			Signal string `json:"signal"`
		}
		_ = json.Unmarshal(plaintext, &req)
		if strings.EqualFold(req.Signal, "kill") {
			sig = os.Kill
		}
	}
	if err := sess.Kill(sig); err != nil {
		s.writeEnvelopeJSON(w, r, ec, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Best-effort wait, then status snapshot.
	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, slimEntry(sess))
}

type aesMetadataRequest struct {
	Title *string `json:"title,omitempty"`
	Goal  *string `json:"goal,omitempty"`
}

func (s *Server) aesSessionMetadata(w http.ResponseWriter, r *http.Request) {
	plaintext, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}
	var req aesMetadataRequest
	if len(plaintext) > 0 {
		if jerr := json.Unmarshal(plaintext, &req); jerr != nil {
			s.writeEnvelopeJSON(w, r, ec, http.StatusBadRequest, map[string]any{"error": "invalid json: " + jerr.Error()})
			return
		}
	}
	if req.Title != nil {
		if err := sess.SetTitle(*req.Title); err != nil {
			s.writeEnvelopeJSON(w, r, ec, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	if req.Goal != nil {
		if err := sess.SetGoal(*req.Goal); err != nil {
			s.writeEnvelopeJSON(w, r, ec, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, slimEntry(sess))
}

func (s *Server) aesSessionModel(w http.ResponseWriter, r *http.Request) {
	plaintext, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if jerr := json.Unmarshal(plaintext, &req); jerr != nil || req.Model == "" {
		s.writeEnvelopeJSON(w, r, ec, http.StatusBadRequest, map[string]any{"error": "model is required"})
		return
	}
	if err := sess.SetModel(req.Model); err != nil {
		s.writeEnvelopeJSON(w, r, ec, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, map[string]any{"id": sess.ID, "model": req.Model})
}

func (s *Server) aesSessionInterrupt(w http.ResponseWriter, r *http.Request) {
	_, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}
	if err := sess.Interrupt(); err != nil {
		s.writeEnvelopeJSON(w, r, ec, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, map[string]any{"id": sess.ID})
}

func (s *Server) aesSessionUsage(w http.ResponseWriter, r *http.Request) {
	_, ec, err := s.readEnvelope1(r)
	if err != nil {
		writeAESErr(w, err)
		return
	}
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		s.writeEnvelopeJSON(w, r, ec, http.StatusNotFound, map[string]any{"error": "no such session"})
		return
	}
	s.writeEnvelopeJSON(w, r, ec, http.StatusOK, map[string]any{
		"id":    sess.ID,
		"usage": sess.Usage(),
	})
}

// ---- shared error tail ---------------------------------------------

// writeAESErr collapses the (aeError vs generic error) dance every
// handler does into one helper.
func writeAESErr(w http.ResponseWriter, err error) {
	var ae *aesError
	if errors.As(err, &ae) {
		writeAESError(w, ae.Status, ae.Code, ae.Detail)
		return
	}
	writeAESError(w, http.StatusBadRequest, "BadEnvelope", err.Error())
}
