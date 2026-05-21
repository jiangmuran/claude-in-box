package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/claude-in-box/internal/auth"
	"github.com/jiangmuran/claude-in-box/internal/session"
)

const stubScript = `cat <<'EOF'
{"type":"text_delta","text":"hello "}
{"type":"text_delta","text":"world"}
{"type":"usage","usage":{"input":12,"output":34}}
{"type":"message_stop","stop_reason":"end_turn"}
EOF`

type harness struct {
	srv      *httptest.Server
	tokens   auth.Store
	sessions *session.Manager
	master   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewFileStore(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("token store: %v", err)
	}
	if err := store.SetMaster("master-secret"); err != nil {
		t.Fatalf("set master: %v", err)
	}
	mgr, err := session.NewManager(filepath.Join(dir, "sessions"), "")
	if err != nil {
		t.Fatalf("session mgr: %v", err)
	}

	s := New(Config{
		Mode:     "default",
		Sessions: mgr,
		Tokens:   store,
		Version:  "test",
		Commit:   "test",
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &harness{srv: ts, tokens: store, sessions: mgr, master: "master-secret"}
}

func (h *harness) req(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rdr)
	req.Header.Set("Authorization", "Bearer "+h.master)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

func decode(t *testing.T, res *http.Response, into any) {
	t.Helper()
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// spawnStubSession bypasses the public POST /api/sessions path (which would
// require ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN to be set) and goes
// straight to session.Manager.Spawn with a deterministic stub command.
func (h *harness) spawnStubSession(t *testing.T) *session.Session {
	t.Helper()
	sess, err := h.sessions.Spawn(context.Background(), session.SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", stubScript},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return sess
}

// -------- tests --------

func TestHealthIsPublic(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
}

func TestProtectedRouteRequiresAuth(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("no auth → %d want 401", res.StatusCode)
	}
}

func TestSessionsListEmpty(t *testing.T) {
	h := newHarness(t)
	res := h.req(t, "GET", "/api/sessions", nil)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var out struct{ Sessions []any }
	decode(t, res, &out)
	if len(out.Sessions) != 0 {
		t.Fatalf("expected empty list, got %d", len(out.Sessions))
	}
}

func TestCreateSession_NoAuthInfoReturns400(t *testing.T) {
	h := newHarness(t)
	res := h.req(t, "POST", "/api/sessions", map[string]any{})
	if res.StatusCode != 400 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d want 400 (no creds); body=%s", res.StatusCode, body)
	}
}

func TestGetAndKillStubSession(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)

	// GET /api/sessions/:id
	res := h.req(t, "GET", "/api/sessions/"+sess.ID, nil)
	if res.StatusCode != 200 {
		t.Fatalf("get session status = %d", res.StatusCode)
	}
	var got map[string]any
	decode(t, res, &got)
	if got["id"] != sess.ID {
		t.Fatalf("id = %v want %s", got["id"], sess.ID)
	}

	// Wait for the stub process to exit on its own (the cat script does).
	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("stub session did not finish")
	}

	res = h.req(t, "GET", "/api/sessions/"+sess.ID+"/transcript", nil)
	defer res.Body.Close()
	var trans struct {
		LastSeq uint64 `json:"last_seq"`
		Frames  []any  `json:"frames"`
	}
	json.NewDecoder(res.Body).Decode(&trans)
	if trans.LastSeq == 0 || len(trans.Frames) < 3 {
		t.Fatalf("transcript thin: last_seq=%d frames=%d", trans.LastSeq, len(trans.Frames))
	}
}

func TestTranscriptFromSeqFilters(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)
	<-sess.Done()

	res := h.req(t, "GET", "/api/sessions/"+sess.ID+"/transcript?from=2", nil)
	defer res.Body.Close()
	var trans struct {
		Frames []map[string]any `json:"frames"`
	}
	json.NewDecoder(res.Body).Decode(&trans)
	for _, f := range trans.Frames {
		seq := uint64(f["seq"].(float64))
		if seq <= 2 {
			t.Fatalf("got frame with seq=%d but asked from=2", seq)
		}
	}
}

func TestSSEStreamsFrames(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)

	req, _ := http.NewRequest("GET", h.srv.URL+"/sse/sessions/"+sess.ID, nil)
	req.Header.Set("Authorization", "Bearer "+h.master)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("sse status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("ct = %q", ct)
	}

	// Read a bounded number of bytes; we should see "event: text.delta" and
	// "event: usage" by then.
	// Run the SSE read on a goroutine so we can bound it with a timeout.
	type result struct{ text, usage bool }
	done := make(chan result, 1)
	go func() {
		rd := bufio.NewReader(res.Body)
		var r result
		for !(r.text && r.usage) {
			line, err := rd.ReadString('\n')
			if err != nil {
				done <- r
				return
			}
			if strings.Contains(line, "event: text.delta") {
				r.text = true
			}
			if strings.Contains(line, "event: usage") {
				r.usage = true
			}
		}
		done <- r
	}()
	var seenText, seenUsage bool
	select {
	case r := <-done:
		seenText, seenUsage = r.text, r.usage
	case <-time.After(3 * time.Second):
	}
	if !seenText || !seenUsage {
		t.Fatalf("sse did not yield expected events: text=%v usage=%v", seenText, seenUsage)
	}
}

func TestWSStreamsFrames(t *testing.T) {
	h := newHarness(t)
	sess := h.spawnStubSession(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws://" + strings.TrimPrefix(h.srv.URL, "http://") + "/ws/sessions/" + sess.ID
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"bearer." + h.master, "json"},
	})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer c.CloseNow()

	deadline := time.Now().Add(3 * time.Second)
	var seenStop bool
	for time.Now().Before(deadline) && !seenStop {
		typ, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		var frame map[string]any
		_ = json.Unmarshal(data, &frame)
		if frame["kind"] == "stop" {
			seenStop = true
		}
	}
	if !seenStop {
		t.Fatal("ws did not deliver a stop frame")
	}
}

func TestTokenLifecycle(t *testing.T) {
	h := newHarness(t)

	// mint
	res := h.req(t, "POST", "/api/tokens", map[string]any{
		"label":     "phone",
		"scopes":    []string{auth.ScopeSessionsRead},
		"ttl_hours": 0,
	})
	if res.StatusCode != 201 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("mint status = %d; body=%s", res.StatusCode, body)
	}
	var minted struct {
		Token     auth.PublicToken `json:"token"`
		Plaintext string           `json:"plaintext"`
	}
	decode(t, res, &minted)
	if minted.Plaintext == "" || minted.Token.ID == "" {
		t.Fatalf("mint output odd: %+v", minted)
	}

	// list (must include both master and the new one)
	res = h.req(t, "GET", "/api/tokens", nil)
	var list struct {
		Tokens []auth.PublicToken `json:"tokens"`
	}
	decode(t, res, &list)
	if len(list.Tokens) < 2 {
		t.Fatalf("tokens = %v", list.Tokens)
	}

	// revoke
	res = h.req(t, "DELETE", "/api/tokens/"+minted.Token.ID, nil)
	if res.StatusCode != 204 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("revoke status = %d; body=%s", res.StatusCode, body)
	}

	// cannot revoke master
	res = h.req(t, "DELETE", "/api/tokens/"+auth.MasterTokenID, nil)
	if res.StatusCode != 403 {
		t.Fatalf("revoke master status = %d want 403", res.StatusCode)
	}
}

func TestScopeEnforcement(t *testing.T) {
	h := newHarness(t)
	// mint a read-only token
	res := h.req(t, "POST", "/api/tokens", map[string]any{
		"label":  "ro",
		"scopes": []string{auth.ScopeSessionsRead},
	})
	defer res.Body.Close()
	var minted struct {
		Plaintext string `json:"plaintext"`
	}
	json.NewDecoder(res.Body).Decode(&minted)

	// GET should pass with sessions:read
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+minted.Plaintext)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res2.StatusCode != 200 {
		t.Fatalf("get sessions ro = %d", res2.StatusCode)
	}
	res2.Body.Close()

	// POST /api/sessions should be 403 (no sessions:write)
	req, _ = http.NewRequest("POST", h.srv.URL+"/api/sessions", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+minted.Plaintext)
	req.Header.Set("Content-Type", "application/json")
	res2, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if res2.StatusCode != 403 {
		body, _ := io.ReadAll(res2.Body)
		t.Fatalf("post sessions ro = %d want 403; body=%s", res2.StatusCode, body)
	}
}

func TestHealthBodyShape(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	for _, k := range []string{"status", "version", "commit", "mode"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("health missing key %q: %+v", k, body)
		}
	}
}

// Smoke: the placeholder index served on / only in default mode.
func TestPlaceholderIndex(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("/ status = %d", res.StatusCode)
	}
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "claude-in-box") {
		t.Fatalf("/ body did not contain banner: %s", b)
	}
}
