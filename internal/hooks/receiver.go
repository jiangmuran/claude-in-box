package hooks

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const (
	// MaxBodyBytes caps the JSON payload an individual hook callback may
	// deliver. Claude Code may send large transcripts on PreCompact;
	// 8 MiB is a comfortable bound for now.
	MaxBodyBytes = 8 << 20

	HeaderHookToken = "X-CIB-Hook-Token"
)

// Sink is the interface the receiver uses to verify tokens and forward
// hook events into the session manager. session.Manager satisfies this.
type Sink interface {
	// CheckHookToken returns true if `provided` matches the per-session
	// token issued at spawn time. Implementations should compare in
	// constant time.
	CheckHookToken(sessionID, provided string) bool

	// EmitHookFrame publishes a hook frame on the session's event bus.
	// Returns an error only if the session does not exist.
	EmitHookFrame(sessionID, event string, payload json.RawMessage) error
}

// Receiver implements http.Handler for /internal/hooks/<session_id>.
// The path prefix routing is the caller's job (mux.Handle("/internal/hooks/", ...)).
type Receiver struct {
	Sink Sink
}

// New builds a Receiver wired to the given Sink.
func New(sink Sink) *Receiver { return &Receiver{Sink: sink} }

// ServeHTTP handles a hook callback. Successful callbacks return 200 with
// an empty JSON object body, signaling "no mutation; continue normally".
func (rcv *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	sessionID := strings.TrimPrefix(r.URL.Path, "/internal/hooks/")
	// Strip any trailing slash or extra segments.
	if i := strings.IndexByte(sessionID, '/'); i >= 0 {
		sessionID = sessionID[:i]
	}
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "missing session id")
		return
	}

	provided := r.Header.Get(HeaderHookToken)
	if provided == "" {
		writeJSONError(w, http.StatusUnauthorized, "missing token")
		return
	}
	if !rcv.Sink.CheckHookToken(sessionID, provided) {
		writeJSONError(w, http.StatusUnauthorized, "bad token")
		return
	}

	event := r.URL.Query().Get("event")
	if event == "" {
		writeJSONError(w, http.StatusBadRequest, "missing event")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(body) > MaxBodyBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}

	// Empty body is acceptable (some events ship no payload).
	var payload json.RawMessage
	if len(body) > 0 {
		if !json.Valid(body) {
			// Still record it so observers can see something arrived.
			payload = json.RawMessage(`{"_raw":` + jsonString(string(body)) + `}`)
		} else {
			payload = body
		}
	}

	if err := rcv.Sink.EmitHookFrame(sessionID, event, payload); err != nil {
		if errors.Is(err, ErrUnknownSession) {
			writeJSONError(w, http.StatusNotFound, "unknown session")
			return
		}
		slog.Warn("hooks.receiver: emit failed", "session", sessionID, "event", event, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "emit failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

// ErrUnknownSession is returned by Sink.EmitHookFrame when the session
// has gone away (e.g. just stopped).
var ErrUnknownSession = errors.New("hooks: unknown session")

// ConstantTimeEqualString compares two strings in constant time.
func ConstantTimeEqualString(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":` + jsonString(msg) + `}`))
}

// jsonString returns s as a JSON-encoded string literal (with quotes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
