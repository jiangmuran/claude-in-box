package cctranscript

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type capture struct {
	mu     sync.Mutex
	frames []frame
}
type frame struct {
	kind string
	data any
}

func (c *capture) Publish(kind string, data any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, frame{kind, data})
	return uint64(len(c.frames)), nil
}

func TestHistoryReplay_BasicShapes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	body := `{"type":"system","subtype":"init","model":"claude-opus-4-7","session_id":"abc","cwd":"/workspace","permissionMode":"bypassPermissions"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file1\nfile2","is_error":false}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"TodoWrite","input":{"todos":[{"content":"do thing","status":"in_progress","activeForm":"doing thing"}]}}]}}
{"type":"result","subtype":"end_turn","duration_ms":1234,"usage":{"input_tokens":10,"output_tokens":20}}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cap := &capture{}
	if err := HistoryReplay(path, cap); err != nil {
		t.Fatalf("HistoryReplay: %v", err)
	}
	got := map[string]int{}
	for _, f := range cap.frames {
		got[f.kind]++
	}
	for _, want := range []string{"meta", "text.delta", "tool.use.start", "tool.use.result", "todo.update", "usage", "stop"} {
		if got[want] < 1 {
			t.Fatalf("missing kind %q in %+v", want, got)
		}
	}
}

func TestHistoryReplay_RealClaudeCamelCase(t *testing.T) {
	// Real ~/.claude/projects/<hash>/<sid>.jsonl uses camelCase fields and
	// type names that aren't in the SDK stream-json schema.
	path := filepath.Join(t.TempDir(), "real.jsonl")
	body := `{"type":"last-prompt","leafUuid":"a","sessionId":"sid-camel-1"}
{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"sid-camel-1"}
{"type":"user","message":{"role":"user","content":"hello"},"sessionId":"sid-camel-1","cwd":"/workspace"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-4-7"},"sessionId":"sid-camel-1"}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Direct OnInit capture (HistoryReplay doesn't expose it; use a
	// Watcher instance and walk lines through emit directly via a small
	// adapter).
	var capturedSID, capturedModel string
	w := &Watcher{Path: path, Bus: &capture{}}
	w.OnInit = func(sid, model, _ string) {
		if capturedSID == "" {
			capturedSID = sid
			capturedModel = model
		}
	}
	for _, line := range []string{
		`{"type":"last-prompt","leafUuid":"a","sessionId":"sid-camel-1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-opus-4-7"},"sessionId":"sid-camel-1"}`,
	} {
		w.emit([]byte(line))
	}
	if capturedSID != "sid-camel-1" {
		t.Fatalf("OnInit didn't capture sessionId; got %q", capturedSID)
	}
	if capturedModel == "" {
		t.Logf("note: model only known after assistant line; got %q (acceptable on first line)", capturedModel)
	}
}

func TestParseTodos_ContentVariant(t *testing.T) {
	items := parseTodos([]byte(`{"todos":[{"content":"task A","status":"pending","activeForm":"doing A"}]}`))
	if len(items) != 1 || items[0]["subject"] != "task A" || items[0]["status"] != "pending" {
		t.Fatalf("items = %+v", items)
	}
}
