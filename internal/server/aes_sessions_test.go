package server

import (
	"context"
	"encoding/json"
	"syscall"
	"testing"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/session"
	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// All tests here drive the v2 record-stream envelope via the same
// aesClient + helpers defined in aes_test.go; we just exercise the new
// /aes/sessions/* management surface.

// helper: pull a stub session, send a one-shot RPC, decode the JSON
// response. Lifts the boilerplate out of every test below.
func aesDoJSON(t *testing.T, c *aesClient, method, route string, payload any) (int, map[string]any) {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
	} else {
		body = []byte("{}")
	}
	status, out := c.do(t, method, route, body)
	// Application-level non-200s (404/400/etc.) are envelope-wrapped
	// JSON too — decode them. Only protocol-level rejections
	// (BadEnvelope, BadTag, …) come back cleartext, in which case the
	// helper does its best to parse and falls back to {"raw": ...}.
	var dec map[string]any
	if err := json.Unmarshal(out, &dec); err != nil {
		return status, map[string]any{"raw": string(out)}
	}
	return status, dec
}

// ---- list -----------------------------------------------------------

func TestAES_SessionsList_EmptyThenPopulated(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)

	status, body := aesDoJSON(t, c, "GET", "/aes/sessions", nil)
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 0 {
		t.Fatalf("expected empty list, got %v", body)
	}

	// Spawn a stub session and re-list.
	_ = h.spawnStubSession(t)
	status, body = aesDoJSON(t, c, "GET", "/aes/sessions", nil)
	sessions, _ = body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	entry := sessions[0].(map[string]any)
	if entry["state"] == nil {
		t.Fatalf("entry missing state: %+v", entry)
	}
	if entry["id"] == "" {
		t.Fatalf("entry missing id: %+v", entry)
	}
}

// ---- metadata: title + goal ----------------------------------------

func TestAES_SessionMetadata_SetAndPersist(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	sess := h.spawnStubSession(t)

	// PUT metadata
	status, body := aesDoJSON(t, c, "PUT",
		"/aes/sessions/"+sess.ID+"/metadata",
		map[string]any{"title": "Refactor sweep", "goal": "kill 200ms p99"},
	)
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["title"] != "Refactor sweep" {
		t.Fatalf("title not set: %+v", body)
	}
	if body["goal"] != "kill 200ms p99" {
		t.Fatalf("goal not set: %+v", body)
	}

	// Title alone (with goal absent) doesn't clobber goal.
	_, body = aesDoJSON(t, c, "PUT",
		"/aes/sessions/"+sess.ID+"/metadata",
		map[string]any{"title": "Renamed"},
	)
	if body["title"] != "Renamed" || body["goal"] != "kill 200ms p99" {
		t.Fatalf("partial update bug: %+v", body)
	}

	// GET reflects the saved state.
	_, getBody := aesDoJSON(t, c, "GET", "/aes/sessions/"+sess.ID, nil)
	if getBody["title"] != "Renamed" || getBody["goal"] != "kill 200ms p99" {
		t.Fatalf("get drift: %+v", getBody)
	}
}

func TestAES_SessionMetadata_Explicit_EmptyClears(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	sess := h.spawnStubSession(t)
	_, _ = aesDoJSON(t, c, "PUT", "/aes/sessions/"+sess.ID+"/metadata",
		map[string]any{"title": "X", "goal": "Y"})
	_, body := aesDoJSON(t, c, "PUT", "/aes/sessions/"+sess.ID+"/metadata",
		map[string]any{"title": "", "goal": ""})
	if body["title"] != nil || body["goal"] != nil {
		// slim entry omits empty strings via `omitempty`, so both fields disappear from the JSON
		t.Fatalf("explicit empty should clear: %+v", body)
	}
}

// ---- usage ----------------------------------------------------------

func TestAES_SessionUsage_TracksFrames(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	// Spawn a long-running session so the bus stays open while we
	// inject usage frames. The PTY runs `sleep 5`; we tear it down
	// at the end of the test.
	sess, err := h.sessions.Spawn(context.Background(), session.SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", "sleep 5"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer func() { _ = sess.Kill(syscall.SIGKILL); <-sess.Done() }()

	// Inject two usage frames via the bus — the watcher goroutine
	// installed by Manager.Spawn should accumulate them into the
	// session's running totals.
	_, _ = sess.Bus().Publish(stream.KindUsage, stream.UsageData{Input: 12, Output: 34})
	_, _ = sess.Bus().Publish(stream.KindUsage, stream.UsageData{Input: 5, Output: 7, CacheRead: 2})

	// Wait up to ~1s for the watcher to drain the channel.
	for i := 0; i < 50; i++ {
		u := sess.Usage()
		if u.Input >= 17 && u.Output >= 41 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	status, body := aesDoJSON(t, c, "GET", "/aes/sessions/"+sess.ID+"/usage", nil)
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	usage, _ := body["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("missing usage field: %+v", body)
	}
	in, _ := usage["input"].(float64)
	out, _ := usage["output"].(float64)
	cacheRead, _ := usage["cache_read"].(float64)
	if int(in) != 17 || int(out) != 41 || int(cacheRead) != 2 {
		t.Fatalf("usage accumulation wrong: in=%v out=%v cache_read=%v (full=%+v)",
			in, out, cacheRead, usage)
	}
}

// Direct unit on session.AddUsage to keep the math obvious.
func TestSession_UsageAccumulates(t *testing.T) {
	// Spawn a no-op session via the manager so AddUsage has a real meta.json target.
	h := newHarness(t)
	sess, err := h.sessions.Spawn(context.Background(), session.SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", "exit 0"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-sess.Done()
	sess.AddUsage(10, 20, 1, 2)
	sess.AddUsage(5, 5, 0, 1)
	u := sess.Usage()
	if u.Input != 15 || u.Output != 25 || u.CacheRead != 1 || u.CacheWrite != 3 {
		t.Fatalf("accumulation wrong: %+v", u)
	}
}

// ---- create / delete / model / interrupt --------------------------

// Create + delete via the AES surface. We can't actually spawn a real
// claude process in tests, so we expect the create to either succeed
// (when CLAUDE_CODE_OAUTH_TOKEN is set in the test env — it usually
// isn't) or fail with a clean envelope-wrapped error. Either way the
// envelope itself should round-trip.
func TestAES_SessionCreate_NoAuthEnvReturnsCleanError(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	// Ensure no env fallback.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	status, body := aesDoJSON(t, c, "POST", "/aes/sessions", map[string]any{
		"workdir": t.TempDir(),
		"title":   "ignored — auth failure first",
	})
	if status != 400 {
		t.Fatalf("expected 400, got status=%d body=%v", status, body)
	}
	errStr, _ := body["error"].(string)
	if errStr == "" {
		t.Fatalf("expected error field, got %+v", body)
	}
}

func TestAES_SessionDelete_SigTERM(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	sess, err := h.sessions.Spawn(context.Background(), session.SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", "sleep 5"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer func() { _ = sess.Kill(syscall.SIGKILL); <-sess.Done() }()

	status, body := aesDoJSON(t, c, "DELETE",
		"/aes/sessions/"+sess.ID, map[string]any{"signal": "term"})
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["id"] != sess.ID {
		t.Fatalf("delete response missing id: %+v", body)
	}
	// Process should be reaped within a beat.
	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit within 2s of DELETE")
	}
}

func TestAES_SessionModel_RequiresField(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	sess := h.spawnStubSession(t)
	status, body := aesDoJSON(t, c, "POST",
		"/aes/sessions/"+sess.ID+"/model", map[string]any{})
	if status != 400 {
		t.Fatalf("status = %d body=%v", status, body)
	}
}

func TestAES_SessionInterrupt_OK(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	sess, err := h.sessions.Spawn(context.Background(), session.SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", "sleep 5"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer func() { _ = sess.Kill(syscall.SIGKILL); <-sess.Done() }()

	status, body := aesDoJSON(t, c, "POST",
		"/aes/sessions/"+sess.ID+"/interrupt", nil)
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["id"] != sess.ID {
		t.Fatalf("interrupt response missing id: %+v", body)
	}
}

// ---- 404 on unknown id --------------------------------------------

func TestAES_SessionGet_404(t *testing.T) {
	h := newHarness(t)
	c := newAESClient(t, h)
	status, body := aesDoJSON(t, c, "GET", "/aes/sessions/no-such-id", nil)
	if status != 404 {
		t.Fatalf("status = %d body=%v", status, body)
	}
}

// Sanity: KindUsage isn't a string we typo'd.
func TestStream_KindUsageMatches(t *testing.T) {
	if stream.KindUsage != "usage" {
		t.Fatalf("KindUsage = %q", stream.KindUsage)
	}
}
