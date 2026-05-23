// Package server wires the control-plane HTTP routes together: REST,
// WebSocket, SSE, the internal hook receiver, and (in default mode) a
// placeholder static index. Everything is multiplexed onto a single
// http.ServeMux so a container exposes exactly one TCP port.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	aespkg "github.com/jiangmuran/claude-in-box/internal/aes"
	"github.com/jiangmuran/claude-in-box/internal/auth"
	"github.com/jiangmuran/claude-in-box/internal/clauth"
	"github.com/jiangmuran/claude-in-box/internal/fsapi"
	"github.com/jiangmuran/claude-in-box/internal/hooks"
	"github.com/jiangmuran/claude-in-box/internal/ports"
	"github.com/jiangmuran/claude-in-box/internal/prefs"
	"github.com/jiangmuran/claude-in-box/internal/providers"
	"github.com/jiangmuran/claude-in-box/internal/session"
	"github.com/jiangmuran/claude-in-box/internal/shell"
)

// Config carries everything Server needs at boot.
type Config struct {
	// Mode is "default" (serve Web UI on /) or "headless" (only /api, /ws,
	// /sse, /aes, /internal).
	Mode string

	Sessions   *session.Manager
	Tokens     auth.Store
	ClaudeAuth *clauth.Manager  // may be nil — handlers return 503 in that case
	Shells     *shell.Manager   // may be nil — handlers return 503
	Files      *fsapi.Manager   // may be nil — handlers return 503
	Providers  *providers.Store // may be nil — handlers return 503
	Prefs      *prefs.Store     // may be nil — handlers return 503
	Ports      *ports.Manager   // may be nil — handlers return 503
	AESReplay  *aespkg.ReplayCache

	// Version, Commit are reported by /api/health and the placeholder index.
	Version string
	Commit  string
}

// Server is the control-plane HTTP wiring.
type Server struct {
	cfg Config
	mux *http.ServeMux

	// providerHost is a per-request scratch carrying the upstream
	// ANTHROPIC_BASE_URL when a session create routes through a stored
	// provider. Cleared in createSession after being consumed.
	providerHost string
}

// New builds a Server with all routes registered.
func New(cfg Config) *Server {
	if cfg.AESReplay == nil {
		cfg.AESReplay = aespkg.NewReplayCache()
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler returns the http.Handler suitable for embedding in an http.Server.
func (s *Server) Handler() http.Handler {
	return withLogging(s.mux)
}

// routes registers every endpoint. Protected handlers are wrapped in
// auth.Require with the appropriate scopes.
func (s *Server) routes() {
	mux := s.mux

	// Public.
	mux.HandleFunc("GET /api/health", s.health)

	// Sessions.
	mux.Handle("GET /api/sessions",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.listSessions)))
	mux.Handle("POST /api/sessions",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.createSession)))
	mux.Handle("GET /api/sessions/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.getSession)))
	mux.Handle("DELETE /api/sessions/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.killSession)))
	mux.Handle("POST /api/sessions/{id}/input",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsInput)(http.HandlerFunc(s.inputSession)))
	mux.Handle("POST /api/sessions/{id}/model",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.modelSession)))
	mux.Handle("POST /api/sessions/{id}/interrupt",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.interruptSession)))
	mux.Handle("GET /api/sessions/{id}/transcript",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.transcriptSession)))
	// Chat-shaped aggregate of the same data. Same bubble model the Web UI
	// renders; downstream non-Web clients can hit this instead of walking
	// the raw frame stream themselves.
	mux.Handle("GET /api/sessions/{id}/messages",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.listMessages)))
	// Embedded-friendly slim chat (user/assistant text + tool one-liners
	// only; supports ?since=<seq> for incremental polling). Sized for
	// MCU-class devices that can only afford a few hundred KB of heap.
	mux.Handle("GET /api/sessions/{id}/chat",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.chatMessages)))
	// One-shot send-and-wait: write prompt + block until next stop frame
	// (or timeout_ms) + return slim-chat delta. Designed for MCU clients
	// that want a single HTTP round-trip per turn.
	mux.Handle("POST /api/sessions/{id}/send",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsInput)(http.HandlerFunc(s.sendAndWait)))

	// Streams.
	mux.Handle("GET /ws/sessions/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.streamWS)))
	mux.Handle("GET /sse/sessions/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.streamSSE)))
	// MCU-friendly: slim-chat events over SSE. Line-by-line consumable on
	// any HTTP/1.1 client (no WS upgrade, no JSON aggregation work).
	mux.Handle("GET /sse/sessions/{id}/chat",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.chatStream)))

	// Tokens.
	mux.Handle("GET /api/tokens",
		auth.Require(s.cfg.Tokens, auth.ScopeTokensRead)(http.HandlerFunc(s.listTokens)))
	mux.Handle("POST /api/tokens",
		auth.Require(s.cfg.Tokens, auth.ScopeTokensWrite)(http.HandlerFunc(s.mintToken)))
	mux.Handle("DELETE /api/tokens/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeTokensWrite)(http.HandlerFunc(s.revokeToken)))

	// AES envelope transport (embedded clients). The cleartext bootstrap
	// endpoints are unauthenticated by design (matches docs/AES-TRANSPORT.md);
	// every other /aes/* route authenticates through the AES envelope itself
	// via the per-device key in Sec-CIB-KeyId.
	mux.HandleFunc("GET /aes/time", s.aesTime)
	mux.HandleFunc("GET /aes/keyinfo", s.aesKeyInfo)
	mux.HandleFunc("POST /aes/sessions/{id}/input", s.aesInput)
	mux.HandleFunc("POST /aes/sessions/{id}/events/stream", s.aesEventsStream)
	mux.HandleFunc("POST /aes/sessions/{id}/chat", s.aesChat)
	// Management surface (mirrors /api/sessions/* under the AES envelope).
	mux.HandleFunc("GET    /aes/sessions", s.aesSessionsList)
	mux.HandleFunc("POST   /aes/sessions", s.aesSessionsCreate)
	mux.HandleFunc("GET    /aes/sessions/{id}", s.aesSessionGet)
	mux.HandleFunc("DELETE /aes/sessions/{id}", s.aesSessionDelete)
	mux.HandleFunc("PUT    /aes/sessions/{id}/metadata", s.aesSessionMetadata)
	mux.HandleFunc("POST   /aes/sessions/{id}/model", s.aesSessionModel)
	mux.HandleFunc("POST   /aes/sessions/{id}/interrupt", s.aesSessionInterrupt)
	mux.HandleFunc("GET    /aes/sessions/{id}/usage", s.aesSessionUsage)

	// Shells (plain-bash PTYs alongside Claude Code sessions).
	mux.Handle("GET /api/shells",
		auth.Require(s.cfg.Tokens, auth.ScopeShellsRead)(http.HandlerFunc(s.listShells)))
	mux.Handle("POST /api/shells",
		auth.Require(s.cfg.Tokens, auth.ScopeShellsWrite)(http.HandlerFunc(s.createShell)))
	mux.Handle("GET /api/shells/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeShellsRead)(http.HandlerFunc(s.getShell)))
	mux.Handle("DELETE /api/shells/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeShellsWrite)(http.HandlerFunc(s.killShell)))
	mux.Handle("POST /api/shells/{id}/input",
		auth.Require(s.cfg.Tokens, auth.ScopeShellsInput)(http.HandlerFunc(s.inputShell)))
	mux.Handle("POST /api/shells/{id}/resize",
		auth.Require(s.cfg.Tokens, auth.ScopeShellsInput)(http.HandlerFunc(s.resizeShell)))
	mux.Handle("GET /ws/shells/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeShellsRead)(http.HandlerFunc(s.streamShellWS)))

	// Files (constrained file browser/editor).
	mux.Handle("GET /api/fs/roots",
		auth.Require(s.cfg.Tokens, auth.ScopeFSRead)(http.HandlerFunc(s.listFSRoots)))
	mux.Handle("GET /api/fs/list",
		auth.Require(s.cfg.Tokens, auth.ScopeFSRead)(http.HandlerFunc(s.fsList)))
	mux.Handle("GET /api/fs/read",
		auth.Require(s.cfg.Tokens, auth.ScopeFSRead)(http.HandlerFunc(s.fsRead)))
	mux.Handle("PUT /api/fs/write",
		auth.Require(s.cfg.Tokens, auth.ScopeFSWrite)(http.HandlerFunc(s.fsWrite)))
	mux.Handle("POST /api/fs/mkdir",
		auth.Require(s.cfg.Tokens, auth.ScopeFSWrite)(http.HandlerFunc(s.fsMkdir)))
	mux.Handle("DELETE /api/fs/delete",
		auth.Require(s.cfg.Tokens, auth.ScopeFSWrite)(http.HandlerFunc(s.fsDelete)))

	// Providers (third-party Anthropic-compatible endpoints).
	mux.Handle("GET /api/providers",
		auth.Require(s.cfg.Tokens, auth.ScopeProvidersRead)(http.HandlerFunc(s.listProviders)))
	mux.Handle("POST /api/providers",
		auth.Require(s.cfg.Tokens, auth.ScopeProvidersWrite)(http.HandlerFunc(s.addProvider)))
	mux.Handle("PUT /api/providers/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeProvidersWrite)(http.HandlerFunc(s.replaceProvider)))
	mux.Handle("DELETE /api/providers/{id}",
		auth.Require(s.cfg.Tokens, auth.ScopeProvidersWrite)(http.HandlerFunc(s.deleteProvider)))
	mux.Handle("POST /api/providers/probe",
		auth.Require(s.cfg.Tokens, auth.ScopeProvidersWrite)(http.HandlerFunc(s.probeProvider)))
	mux.Handle("POST /api/providers/{id}/probe",
		auth.Require(s.cfg.Tokens, auth.ScopeProvidersWrite)(http.HandlerFunc(s.probeProvider)))

	// Preferences (default auth mode, default provider, default model).
	mux.Handle("GET /api/prefs",
		auth.Require(s.cfg.Tokens, auth.ScopePrefsRead)(http.HandlerFunc(s.getPrefs)))
	mux.Handle("PATCH /api/prefs",
		auth.Require(s.cfg.Tokens, auth.ScopePrefsWrite)(http.HandlerFunc(s.patchPrefs)))

	// Port mapping — surface an in-container service on a host port from
	// the pre-allocated range (CIB_PORT_RANGE on docker run).
	mux.Handle("GET /api/ports",
		auth.Require(s.cfg.Tokens, auth.ScopePortsRead)(http.HandlerFunc(s.listPorts)))
	mux.Handle("POST /api/ports/expose",
		auth.Require(s.cfg.Tokens, auth.ScopePortsWrite)(http.HandlerFunc(s.exposePort)))
	mux.Handle("DELETE /api/ports/{host_port}",
		auth.Require(s.cfg.Tokens, auth.ScopePortsWrite)(http.HandlerFunc(s.unexposePort)))

	// Anthropic Messages API compatibility — accepts the upstream wire
	// shape so unmodified SDKs (`@anthropic-ai/sdk`, `anthropic` Python)
	// can point their base_url at cib.
	mux.Handle("POST /v1/messages",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsInput)(http.HandlerFunc(s.anthropicMessages)))
	// OpenAI Chat Completions API compatibility — same idea, OpenAI wire
	// shape on top of the same Anthropic pipeline.
	mux.Handle("POST /openai/v1/chat/completions",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsInput)(http.HandlerFunc(s.openaiChat)))

	// Claude auth (in-container `claude auth login --claudeai`).
	mux.Handle("GET /api/auth/claude/status",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsRead)(http.HandlerFunc(s.claudeAuthStatus)))
	mux.Handle("POST /api/auth/claude/start",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.claudeAuthStart)))
	mux.Handle("POST /api/auth/claude/code",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.claudeAuthCode)))
	mux.Handle("POST /api/auth/claude/cancel",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.claudeAuthCancel)))
	mux.Handle("POST /api/auth/claude/logout",
		auth.Require(s.cfg.Tokens, auth.ScopeSessionsWrite)(http.HandlerFunc(s.claudeAuthLogout)))

	// Internal hooks receiver. Authenticated by per-session HMAC, not by
	// the bearer middleware; see internal/hooks/receiver.go.
	if s.cfg.Sessions != nil {
		hooksReceiver := hooks.New(s.cfg.Sessions)
		mux.Handle("/internal/hooks/", hooksReceiver)
	}

	// Root + UI assets: serve the embedded Svelte build in default mode.
	// If the build is absent (e.g. `go run` without npm build), fall back
	// to the inline placeholder index so health checks still work.
	if s.cfg.Mode != "headless" {
		if ui := uiHandler(); ui != nil {
			mux.Handle("/", ui)
		} else {
			mux.HandleFunc("GET /{$}", s.placeholderIndex)
		}
	}
}

// ----- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v == nil {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// readJSON parses the request body as JSON into `dst`. Errors are surfaced
// to the client as 400.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body: "+err.Error())
		return false
	}
	return true
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController see through to the underlying writer
// for Hijack (WebSocket) and other unwrap-aware ops.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush forwards directly so direct `w.(http.Flusher)` type assertions in
// downstream handlers (notably SSE) still work without ResponseController.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func withLogging(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		if r.URL.Path == "/api/health" {
			return
		}
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"remote", clientIP(r),
		)
	})
}

func clientIP(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		// First IP in the list.
		if i := strings.IndexByte(h, ','); i >= 0 {
			return strings.TrimSpace(h[:i])
		}
		return strings.TrimSpace(h)
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		return h
	}
	return r.RemoteAddr
}
