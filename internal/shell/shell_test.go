package shell

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSpawn_EchoBack(t *testing.T) {
	m := NewManager(t.TempDir())
	s, err := m.Spawn(SpawnOptions{Cmd: "bash", Args: []string{"-c", "echo hello-from-shell; exit 0"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Subscribe returns the scrollback buffer too — the shell command
	// (`echo …; exit 0`) often completes in <1ms and the bytes land in
	// the buffer BEFORE this goroutine subscribes. Both the scrollback
	// AND the live channel feed `buf` so the race is no longer flaky.
	id, ch, scrollback := s.Subscribe(8)
	defer s.Unsubscribe(id)

	var buf bytes.Buffer
	buf.Write(scrollback)

	if strings.Contains(buf.String(), "hello-from-shell") {
		goto done
	}

	{
		deadline := time.After(3 * time.Second)
		for {
			select {
			case chunk, ok := <-ch:
				if !ok {
					goto done
				}
				buf.Write(chunk)
			case <-deadline:
				t.Fatalf("timed out waiting for output; got %q", buf.String())
			}
			if strings.Contains(buf.String(), "hello-from-shell") {
				goto done
			}
		}
	}
done:
	if !strings.Contains(buf.String(), "hello-from-shell") {
		t.Fatalf("never saw expected output; got %q", buf.String())
	}

	<-s.Done()
	if s.ExitCode() != 0 {
		t.Fatalf("exit = %d want 0", s.ExitCode())
	}
}

func TestWriteAndResize(t *testing.T) {
	m := NewManager(t.TempDir())
	// `read` blocks waiting for stdin; we'll feed it via Write.
	s, err := m.Spawn(SpawnOptions{
		Cmd:  "bash",
		Args: []string{"-c", `read line; printf 'got=%s\n' "$line"`},
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	id, ch, _ := s.Subscribe(8)
	defer s.Unsubscribe(id)

	if err := s.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if _, err := s.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var buf bytes.Buffer
	deadline := time.After(3 * time.Second)
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				goto done
			}
			buf.Write(chunk)
		case <-deadline:
			t.Fatalf("timed out; got %q", buf.String())
		}
		if strings.Contains(buf.String(), "got=hello") {
			goto done
		}
	}
done:
	<-s.Done()
}

func TestManager_ListGetKillForget(t *testing.T) {
	m := NewManager(t.TempDir())
	s, err := m.Spawn(SpawnOptions{Cmd: "bash", Args: []string{"-c", "sleep 5"}})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	if got, ok := m.Get(s.ID); !ok || got.ID != s.ID {
		t.Fatalf("Get failed: %v %v", got, ok)
	}
	if len(m.List()) != 1 {
		t.Fatalf("List len = %d want 1", len(m.List()))
	}

	if err := m.Kill(s.ID, syscall.SIGTERM); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	<-s.Done()
	if !m.Forget(s.ID) {
		t.Fatalf("Forget returned false")
	}
	if _, ok := m.Get(s.ID); ok {
		t.Fatalf("Get after Forget should fail")
	}
}

func TestSubscribe_ScrollbackForLateJoiner(t *testing.T) {
	m := NewManager(t.TempDir())
	s, _ := m.Spawn(SpawnOptions{Cmd: "bash", Args: []string{"-c", "echo first; sleep 0.3; echo second"}})

	// Wait for the first line to appear in scrollback.
	id1, ch, _ := s.Subscribe(16)
	deadline := time.After(2 * time.Second)
	var have bytes.Buffer
	for !strings.Contains(have.String(), "first") {
		select {
		case chunk := <-ch:
			have.Write(chunk)
		case <-deadline:
			t.Fatalf("never saw first line; got %q", have.String())
		}
	}
	s.Unsubscribe(id1)

	// Late join — must see "first" in scrollback.
	_, _, sb := s.Subscribe(16)
	if !strings.Contains(string(sb), "first") {
		t.Fatalf("scrollback missing first line; got %q", sb)
	}
	<-s.Done()
}
