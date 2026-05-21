package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewToken(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("token len = %d want 64", len(tok))
	}
	for _, c := range tok {
		ok := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !ok {
			t.Fatalf("token has non-hex char %q", c)
		}
	}
	tok2, _ := NewToken()
	if tok == tok2 {
		t.Fatal("NewToken returned same token twice")
	}
}

func TestWriteSessionSettings_ShapeAndContents(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, ".claude")

	out, err := WriteSessionSettings(configDir, "sess-1", "deadbeef", "127.0.0.1:8080", []string{"SessionStart", "Stop"})
	if err != nil {
		t.Fatalf("WriteSessionSettings: %v", err)
	}
	if out != filepath.Join(configDir, "settings.json") {
		t.Fatalf("path = %q", out)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, raw)
	}
	if len(s.Hooks) != 2 {
		t.Fatalf("hook map size = %d want 2", len(s.Hooks))
	}
	for _, ev := range []string{"SessionStart", "Stop"} {
		entries, ok := s.Hooks[ev]
		if !ok || len(entries) != 1 {
			t.Fatalf("hook[%s] missing", ev)
		}
		if entries[0].Type != "command" {
			t.Fatalf("hook[%s].type = %q", ev, entries[0].Type)
		}
		cmd := entries[0].Command
		for _, must := range []string{"curl", "deadbeef", "127.0.0.1:8080", "/internal/hooks/sess-1", "event=" + ev, HeaderHookToken} {
			if !strings.Contains(cmd, must) {
				t.Fatalf("hook[%s] missing %q in: %s", ev, must, cmd)
			}
		}
	}
}

func TestWriteSessionSettings_DefaultEvents(t *testing.T) {
	out, err := WriteSessionSettings(t.TempDir(), "s", "tok", "127.0.0.1:8080", nil)
	if err != nil {
		t.Fatalf("WriteSessionSettings: %v", err)
	}
	raw, _ := os.ReadFile(out)
	var s Settings
	_ = json.Unmarshal(raw, &s)
	if len(s.Hooks) != len(DefaultEvents) {
		t.Fatalf("registered %d events, want %d", len(s.Hooks), len(DefaultEvents))
	}
}

func TestWriteSessionSettings_FileModeIsRestrictive(t *testing.T) {
	out, _ := WriteSessionSettings(t.TempDir(), "s", "tok", "127.0.0.1:8080", []string{"SessionStart"})
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("settings file mode = %o, expected 0600-style", info.Mode().Perm())
	}
}
