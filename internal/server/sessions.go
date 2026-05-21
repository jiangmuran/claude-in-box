package server

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/session"
)

// createSessionRequest mirrors session.SpawnOptions but only the public fields
// (no test-mode overrides). All credential fields default to the corresponding
// container env vars when empty; this is what makes "single user, one
// container" feel zero-config.
type createSessionRequest struct {
	Workdir           string `json:"workdir,omitempty"`
	Model             string `json:"model,omitempty"`
	AuthMode          string `json:"auth_mode,omitempty"`
	APIKey            string `json:"api_key,omitempty"`
	OAuthToken        string `json:"oauth_token,omitempty"`
	ProviderID        string `json:"provider_id,omitempty"`
	ResumeFrom        string `json:"resume_from,omitempty"`
	BypassPermissions *bool  `json:"bypass_permissions,omitempty"`
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list := s.cfg.Sessions.List()
	out := make([]any, 0, len(list))
	for _, sess := range list {
		out = append(out, sess.Status())
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if !readJSON(w, r, &req) {
		return
	}

	if req.Workdir == "" {
		req.Workdir = "/workspace"
		if cwd, err := os.Getwd(); err == nil {
			req.Workdir = cwd
		}
	}
	bypass := true
	if req.BypassPermissions != nil {
		bypass = *req.BypassPermissions
	}

	// Fall back to container env credentials when caller did not provide.
	authMode := req.AuthMode
	apiKey := req.APIKey
	oauth := req.OAuthToken
	if authMode == "" {
		switch {
		case oauth != "" || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "":
			authMode = "subscription"
		case apiKey != "" || os.Getenv("ANTHROPIC_API_KEY") != "":
			authMode = "api_key"
		default:
			writeErr(w, http.StatusBadRequest, "no auth: set CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY, or pass oauth_token/api_key in the request")
			return
		}
	}
	switch authMode {
	case "subscription":
		if oauth == "" {
			oauth = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
		}
		// If still empty, the user might be logged in via the in-container
		// `claude auth login --claudeai` flow — credentials live on disk
		// under ~/.claude/.credentials.json and claude will read them
		// without any env var. Verify that and let it through.
		if oauth == "" && s.cfg.ClaudeAuth != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			st, err := s.cfg.ClaudeAuth.Status(ctx)
			cancel()
			if err == nil && st.LoggedIn {
				break
			}
			writeErr(w, http.StatusBadRequest,
				"subscription mode requires either an oauth_token in the request, a CLAUDE_CODE_OAUTH_TOKEN env var, or a `claude auth login` performed inside the container (top-right 'sign in with claude' button)")
			return
		}
		if oauth == "" {
			writeErr(w, http.StatusBadRequest,
				"subscription mode requires oauth_token, CLAUDE_CODE_OAUTH_TOKEN env, or an in-container claude login")
			return
		}
	case "api_key":
		// Three sources for credentials, in order:
		// 1. Explicit { api_key } in the request body.
		// 2. provider_id pointing at a stored third-party endpoint.
		// 3. ANTHROPIC_API_KEY env var on the container.
		var providerHost, providerModel string
		if req.ProviderID != "" && s.cfg.Providers != nil {
			p, ok := s.cfg.Providers.Get(req.ProviderID)
			if !ok {
				writeErr(w, http.StatusBadRequest, "provider_id not found")
				return
			}
			if apiKey == "" {
				apiKey = p.APIKey
			}
			providerHost = p.APIHost
			if req.Model == "" && p.Model != "" {
				providerModel = p.Model
			}
			s.cfg.Providers.MarkUsed(p.ID)
		}
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if apiKey == "" {
			writeErr(w, http.StatusBadRequest, "api_key mode requires api_key, provider_id, or ANTHROPIC_API_KEY env")
			return
		}
		// Stash the provider's host+model on the createSessionRequest so
		// the Spawn call below picks them up via opts.APIHost / opts.Model.
		req.APIKey = apiKey
		if providerHost != "" {
			s.providerHost = providerHost
		}
		if providerModel != "" && req.Model == "" {
			req.Model = providerModel
		}
	default:
		writeErr(w, http.StatusBadRequest, "auth_mode must be \"subscription\" or \"api_key\"")
		return
	}

	spawnOpts := session.SpawnOptions{
		Workdir:           req.Workdir,
		Model:             req.Model,
		AuthMode:          authMode,
		APIKey:            apiKey,
		OAuthToken:        oauth,
		ResumeFrom:        req.ResumeFrom,
		BypassPermissions: bypass,
	}
	if s.providerHost != "" {
		spawnOpts.ExtraEnv = append(spawnOpts.ExtraEnv,
			"ANTHROPIC_BASE_URL="+s.providerHost)
		s.providerHost = "" // one-shot
	}
	sess, err := s.cfg.Sessions.Spawn(r.Context(), spawnOpts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "spawn: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, sess.Status())
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	writeJSON(w, http.StatusOK, sess.Status())
}

func (s *Server) killSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	sig := os.Signal(syscall.SIGTERM)
	if r.URL.Query().Get("signal") == "kill" {
		sig = os.Kill
	}
	if err := sess.Kill(sig); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Best-effort wait for graceful exit; do not block the client forever.
	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
	}
	writeJSON(w, http.StatusOK, sess.Status())
}

type inputRequest struct {
	Data     string `json:"data"`
	Encoding string `json:"encoding,omitempty"` // "utf8" (default), "raw", future: "base64"
}

func (s *Server) inputSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	var req inputRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Data == "" {
		writeErr(w, http.StatusBadRequest, "empty data")
		return
	}
	if _, err := sess.Write([]byte(req.Data)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bytes": len(req.Data)})
}

type modelRequest struct {
	Model string `json:"model"`
}

func (s *Server) modelSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	var req modelRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Model == "" {
		writeErr(w, http.StatusBadRequest, "model is required")
		return
	}
	if err := sess.SetModel(req.Model); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": sess.ID, "model": req.Model})
}

func (s *Server) interruptSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	if err := sess.Interrupt(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": sess.ID})
}

func (s *Server) transcriptSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	var fromSeq uint64
	if q := r.URL.Query().Get("from"); q != "" {
		if n, err := strconv.ParseUint(q, 10, 64); err == nil {
			fromSeq = n
		}
	}
	all := sess.Snapshot()
	out := all
	if fromSeq > 0 {
		out = out[:0:0]
		for _, f := range all {
			if f.Seq > fromSeq {
				out = append(out, f)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       sess.ID,
		"last_seq": sess.LastSeq(),
		"frames":   out,
	})
}
