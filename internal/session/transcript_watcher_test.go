package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// TestStartTranscriptWatcher_FindsAndTails verifies the auto-discovery
// path: when a claude-style JSONL transcript appears under
// ~/.claude/projects/<encoded-workdir>/ AFTER the spawn time, the
// watcher attaches to it and frames it publishes land on the
// session's bus.
//
// We point HOME at a temp dir so the test doesn't touch the user's
// real ~/.claude.
func TestStartTranscriptWatcher_FindsAndTails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workdir := t.TempDir()
	encoded := strings.ReplaceAll(filepath.Clean(workdir), "/", "-")
	projDir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Synthetic session — startTranscriptWatcher only touches
	// sess.done / sess.mu / sess.bus / sess.transcriptStop /
	// sess.writeMeta (no-op since BaseDir is empty).
	sess := &Session{
		ID:      "sess-test",
		Workdir: workdir,
		BaseDir: t.TempDir(),
		done:    make(chan struct{}),
		bus:     stream.NewBus("sess-test", 64),
	}

	since := time.Now().Add(-1 * time.Second)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		startTranscriptWatcher(sess, workdir, since)
	}()

	// Wait briefly, then drop a real-looking transcript file.
	time.Sleep(400 * time.Millisecond)
	tpath := filepath.Join(projDir, "abc123.jsonl")
	body := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"claude-sid-1","model":"claude-sonnet-4-6","cwd":"` + workdir + `"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello from claude"}]}}`,
		``,
	}, "\n")
	if err := os.WriteFile(tpath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Give the watcher time to discover the file (300ms poll) and
	// publish frames from it.
	deadline := time.Now().Add(5 * time.Second)
	var sawText bool
	for time.Now().Before(deadline) {
		for _, f := range sess.bus.Snapshot() {
			if f.Kind == stream.KindTextDelta &&
				strings.Contains(string(f.Data), "hello from claude") {
				sawText = true
				break
			}
		}
		if sawText {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	close(sess.done)
	if sess.transcriptStop != nil {
		sess.transcriptStop()
	}
	sess.bus.CloseAll()
	wg.Wait()

	if !sawText {
		t.Fatalf("watcher never published text.delta containing claude's message; bus kinds: %v", kindList(sess.bus.Snapshot()))
	}
	if sess.transcriptStop == nil {
		t.Fatalf("transcriptStop never installed — auto-discovery path didn't fire")
	}
}

// TestStartTranscriptWatcher_RespectsExistingHookStart guarantees the
// goroutine bails out when the hook-driven path already set
// transcriptStop, so we don't run two watchers on the same file.
func TestStartTranscriptWatcher_RespectsExistingHookStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workdir := t.TempDir()
	encoded := strings.ReplaceAll(filepath.Clean(workdir), "/", "-")
	projDir := filepath.Join(home, ".claude", "projects", encoded)
	_ = os.MkdirAll(projDir, 0o755)
	tpath := filepath.Join(projDir, "x.jsonl")
	_ = os.WriteFile(tpath, []byte(`{"type":"system","subtype":"init"}`+"\n"), 0o644)

	sess := &Session{
		ID: "sess-x", Workdir: workdir, BaseDir: t.TempDir(),
		done: make(chan struct{}), bus: stream.NewBus("sess-x", 16),
	}

	// Pretend the hook path already installed a stop func.
	hookCancelled := false
	sess.transcriptStop = func() { hookCancelled = true }

	startTranscriptWatcher(sess, workdir, time.Now().Add(-1*time.Second))

	if hookCancelled {
		t.Fatalf("auto-discovery clobbered the hook-installed transcriptStop")
	}
	close(sess.done)
}

func kindList(fs []stream.Frame) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}
