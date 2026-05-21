package hooks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubSink lets receiver tests assert what arrived without dragging in the
// session package (which would import hooks; cycle).
type stubSink struct {
	mu      sync.Mutex
	tokens  map[string]string // sessionID → token
	frames  []stubFrame
	emitErr error
}

type stubFrame struct {
	SessionID string
	Event     string
	Payload   json.RawMessage
}

func (s *stubSink) CheckHookToken(sessionID, provided string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[sessionID]
	return ok && ConstantTimeEqualString(tok, provided)
}

func (s *stubSink) EmitHookFrame(sessionID, event string, payload json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emitErr != nil {
		return s.emitErr
	}
	s.frames = append(s.frames, stubFrame{SessionID: sessionID, Event: event, Payload: payload})
	return nil
}

func newSink(t *testing.T, sess, tok string) *stubSink {
	t.Helper()
	return &stubSink{tokens: map[string]string{sess: tok}}
}

func TestReceiver_HappyPath(t *testing.T) {
	sink := newSink(t, "s1", "tok1")
	rec := New(sink)

	body := []byte(`{"tool":"Bash","input":{"command":"ls"}}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/s1?event=PreToolUse", bytes.NewReader(body))
	req.Header.Set(HeaderHookToken, "tok1")
	w := httptest.NewRecorder()

	rec.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d want 200; body=%s", w.Code, w.Body)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "{}" {
		t.Fatalf("body = %q want {}", got)
	}
	if len(sink.frames) != 1 {
		t.Fatalf("frames = %d want 1", len(sink.frames))
	}
	f := sink.frames[0]
	if f.SessionID != "s1" || f.Event != "PreToolUse" {
		t.Fatalf("frame meta = %+v", f)
	}
	if !strings.Contains(string(f.Payload), `"command":"ls"`) {
		t.Fatalf("payload = %s", f.Payload)
	}
}

func TestReceiver_RejectsBadMethod(t *testing.T) {
	rec := New(newSink(t, "s1", "tok1"))
	req := httptest.NewRequest(http.MethodGet, "/internal/hooks/s1?event=Stop", nil)
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d want 405", w.Code)
	}
}

func TestReceiver_RejectsMissingToken(t *testing.T) {
	rec := New(newSink(t, "s1", "tok1"))
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/s1?event=Stop", nil)
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401", w.Code)
	}
}

func TestReceiver_RejectsBadToken(t *testing.T) {
	rec := New(newSink(t, "s1", "tok1"))
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/s1?event=Stop", nil)
	req.Header.Set(HeaderHookToken, "nope")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401; body=%s", w.Code, w.Body)
	}
}

func TestReceiver_RejectsUnknownSession(t *testing.T) {
	rec := New(newSink(t, "s1", "tok1"))
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/other?event=Stop", nil)
	req.Header.Set(HeaderHookToken, "tok1")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401 (token check fails)", w.Code)
	}
}

func TestReceiver_RejectsMissingEvent(t *testing.T) {
	rec := New(newSink(t, "s1", "tok1"))
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/s1", nil)
	req.Header.Set(HeaderHookToken, "tok1")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", w.Code)
	}
}

func TestReceiver_RejectsMissingSessionID(t *testing.T) {
	rec := New(newSink(t, "s1", "tok1"))
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/?event=Stop", nil)
	req.Header.Set(HeaderHookToken, "tok1")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", w.Code)
	}
}

func TestReceiver_AcceptsEmptyBody(t *testing.T) {
	sink := newSink(t, "s1", "tok1")
	rec := New(sink)
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/s1?event=Stop", nil)
	req.Header.Set(HeaderHookToken, "tok1")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d want 200; body=%s", w.Code, w.Body)
	}
	if len(sink.frames) != 1 || sink.frames[0].Event != "Stop" {
		t.Fatalf("frames = %+v", sink.frames)
	}
	if sink.frames[0].Payload != nil {
		t.Fatalf("payload = %s want nil", sink.frames[0].Payload)
	}
}

func TestReceiver_TooLargeBodyRejected(t *testing.T) {
	rec := New(newSink(t, "s1", "tok1"))
	big := bytes.Repeat([]byte{'x'}, MaxBodyBytes+128)
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/s1?event=Stop", bytes.NewReader(big))
	req.Header.Set(HeaderHookToken, "tok1")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d want 413", w.Code)
	}
}

func TestReceiver_InvalidJSONBodyStillRecorded(t *testing.T) {
	sink := newSink(t, "s1", "tok1")
	rec := New(sink)
	req := httptest.NewRequest(http.MethodPost, "/internal/hooks/s1?event=Stop", strings.NewReader("not json"))
	req.Header.Set(HeaderHookToken, "tok1")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d want 200", w.Code)
	}
	if len(sink.frames) != 1 {
		t.Fatalf("frames = %d want 1", len(sink.frames))
	}
	if !strings.Contains(string(sink.frames[0].Payload), `"_raw":"not json"`) {
		t.Fatalf("payload = %s", sink.frames[0].Payload)
	}
}
