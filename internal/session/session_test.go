package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// jsonlScript prints sample stream-json then exits 0. Used as a stand-in for
// `claude --output-format stream-json` so we can test the spawn + parse
// pipeline without depending on a real Claude Code install.
const jsonlScript = `cat <<'EOF'
{"type":"text_delta","text":"hi"}
{"type":"todo_update","items":[{"id":"1","subject":"do thing","status":"in_progress","activeForm":"doing thing"}]}
{"type":"usage","usage":{"input":10,"output":20}}
{"type":"message_stop","stop_reason":"end_turn"}
EOF`

func TestManager_SpawnAndParseFrames(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := m.Spawn(ctx, SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", jsonlScript},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Wait for the stub process to finish so all frames are buffered.
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to finish")
	}

	frames := sess.Snapshot()
	kinds := make([]string, 0, len(frames))
	for _, f := range frames {
		kinds = append(kinds, f.Kind)
	}

	requireKind(t, kinds, stream.KindTextDelta)
	requireKind(t, kinds, stream.KindTodoUpdate)
	requireKind(t, kinds, stream.KindUsage)
	requireKind(t, kinds, stream.KindStop)

	if sess.State() != stream.StateStopped {
		t.Fatalf("state = %q want stopped", sess.State())
	}
	if sess.ExitCode != 0 {
		t.Fatalf("exit = %d want 0", sess.ExitCode)
	}
}

func TestManager_ListReturnsSpawnedSession(t *testing.T) {
	m, _ := NewManager(t.TempDir(), "")

	ctx := context.Background()
	sess, err := m.Spawn(ctx, SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", "sleep 1"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	got, ok := m.Get(sess.ID)
	if !ok || got.ID != sess.ID {
		t.Fatalf("Get(%q) = %v, %v", sess.ID, got, ok)
	}

	list := m.List()
	if len(list) != 1 || list[0].ID != sess.ID {
		t.Fatalf("list = %v want one element with id %s", list, sess.ID)
	}

	_ = sess.Kill(stopSignal())
	<-sess.Done()
}

func TestSession_WriteAndInterrupt(t *testing.T) {
	m, _ := NewManager(t.TempDir(), "")

	// A tiny shell that echoes back what it reads from stdin, prefixed.
	// Wrap stdin handling in a trap so SIGINT terminates cleanly.
	script := `while IFS= read -r line; do printf 'got: %s\n' "$line"; done`

	sess, err := m.Spawn(context.Background(), SpawnOptions{
		Workdir:      t.TempDir(),
		OverrideArgs: []string{"bash", "-c", script},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if err := sess.WriteString("hello\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}

	// Wait briefly for the echo to come through the parser as pty.raw.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("never saw echoed pty.raw frame")
		default:
		}
		frames := sess.Snapshot()
		found := false
		for _, f := range frames {
			if f.Kind == stream.KindPTYRaw && strings.Contains(string(f.Data), "got:") {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Interrupt the bash loop; it should exit on EOF when we kill it.
	if err := sess.Kill(stopSignal()); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	<-sess.Done()
	if sess.State() == stream.StateIdle {
		t.Fatalf("state = %q want stopped or failed", sess.State())
	}
}

func TestSession_MetaFrameOnSpawn(t *testing.T) {
	m, _ := NewManager(t.TempDir(), "")
	sess, err := m.Spawn(context.Background(), SpawnOptions{
		Workdir:      t.TempDir(),
		Model:        "claude-sonnet-4-6",
		AuthMode:     "api_key",
		OverrideArgs: []string{"bash", "-c", "true"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-sess.Done()

	frames := sess.Snapshot()
	var sawMeta bool
	for _, f := range frames {
		if f.Kind == stream.KindMeta && strings.Contains(string(f.Data), "claude-sonnet-4-6") {
			sawMeta = true
			break
		}
	}
	if !sawMeta {
		t.Fatalf("no meta frame with model. frames=%v", frames)
	}
}

func TestManager_CommandForWithoutClaudeBinErrors(t *testing.T) {
	m, _ := NewManager(t.TempDir(), "definitely-not-a-real-binary-name-xyz")
	_, err := m.Spawn(context.Background(), SpawnOptions{Workdir: t.TempDir()})
	if err == nil {
		t.Fatalf("expected error when claude binary is missing")
	}
}

func requireKind(t *testing.T, kinds []string, want string) {
	t.Helper()
	for _, k := range kinds {
		if k == want {
			return
		}
	}
	t.Fatalf("kinds %v missing %q", kinds, want)
}
