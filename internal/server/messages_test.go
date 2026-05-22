package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/stream"
)

func mkFrame(seq uint64, kind string, payload any) stream.Frame {
	b, _ := json.Marshal(payload)
	return stream.Frame{Session: "s", Seq: seq, TS: time.Now(), Kind: kind, Data: b}
}

func TestAggregateChat_TextMerging(t *testing.T) {
	frames := []stream.Frame{
		mkFrame(1, stream.KindTextDelta, map[string]any{"text": "Hello, ", "role": "assistant"}),
		mkFrame(2, stream.KindTextDelta, map[string]any{"text": "world.", "role": "assistant"}),
		mkFrame(3, stream.KindTextDelta, map[string]any{"text": "ok", "role": "user"}),
	}
	got := aggregateChat(frames)
	if len(got) != 2 {
		t.Fatalf("len = %d want 2 (one merged assistant + one user); got=%+v", len(got), got)
	}
	if got[0]["role"] != "assistant" || got[0]["text"] != "Hello, world." {
		t.Fatalf("assistant bubble wrong: %+v", got[0])
	}
	if got[1]["role"] != "user" || got[1]["text"] != "ok" {
		t.Fatalf("user bubble wrong: %+v", got[1])
	}
}

func TestAggregateChat_ToolStartThenResult(t *testing.T) {
	frames := []stream.Frame{
		mkFrame(10, stream.KindToolUseStart, map[string]any{"tool": "Bash", "tool_use_id": "t1"}),
		mkFrame(12, stream.KindToolUseResult, map[string]any{"tool_use_id": "t1", "is_error": false, "duration_ms": 23}),
	}
	got := aggregateChat(frames)
	if len(got) != 1 || got[0]["role"] != "tool" || got[0]["tool"] != "Bash" {
		t.Fatalf("expected one tool bubble: %+v", got)
	}
	if got[0]["summary"] != "ok · 23ms" {
		t.Fatalf("summary = %q", got[0]["summary"])
	}
	// seq must advance to the result frame so polling cursors work.
	if got[0]["seq"].(uint64) != 12 {
		t.Fatalf("seq = %v want 12", got[0]["seq"])
	}
}

func TestAggregateChat_ToolErrorSummary(t *testing.T) {
	frames := []stream.Frame{
		mkFrame(10, stream.KindToolUseStart, map[string]any{"tool": "Bash", "tool_use_id": "t1"}),
		mkFrame(12, stream.KindToolUseResult, map[string]any{"tool_use_id": "t1", "is_error": true, "duration_ms": 312}),
	}
	got := aggregateChat(frames)
	if got[0]["summary"] != "error · 312ms" {
		t.Fatalf("summary = %q", got[0]["summary"])
	}
}

func TestFilterSince_FreshSlice(t *testing.T) {
	msgs := []map[string]any{
		{"seq": uint64(1), "x": "a"},
		{"seq": uint64(3), "x": "b"},
		{"seq": uint64(5), "x": "c"},
	}
	out := filterSince(msgs, 2)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	// Slice must be a different backing array — the old in-place
	// `out := msgs[:0]` would have made out alias msgs.
	if len(out) > 0 && &out[0] == &msgs[1] {
		t.Fatal("filterSince re-used input slice backing array (aliasing footgun)")
	}
	// Appending to out must not corrupt msgs.
	out = append(out, map[string]any{"seq": uint64(99)})
	if msgs[2]["x"] != "c" {
		t.Fatalf("msgs corrupted by appending to filter result: %+v", msgs)
	}
}

func TestAggregateMessages_AllKinds(t *testing.T) {
	frames := []stream.Frame{
		mkFrame(1, stream.KindMeta, map[string]any{"model": "claude-opus-4-7"}),
		mkFrame(2, stream.KindTextDelta, map[string]any{"text": "go", "role": "user"}),
		mkFrame(3, "thinking", map[string]any{"text": "let me see"}),
		mkFrame(4, stream.KindToolUseStart, map[string]any{"tool": "Read", "tool_use_id": "u"}),
		mkFrame(5, stream.KindToolUseResult, map[string]any{"tool_use_id": "u", "is_error": false}),
		mkFrame(6, stream.KindTodoUpdate, map[string]any{"items": []map[string]any{{"subject": "x", "status": "pending"}}}),
		mkFrame(7, stream.KindTextDelta, map[string]any{"text": "done", "role": "assistant"}),
		mkFrame(8, stream.KindUsage, map[string]any{"input": 5, "output": 12}),
		mkFrame(9, stream.KindStop, map[string]any{"reason": "end_turn", "duration_ms": 100}),
	}
	got := aggregateMessages(frames)

	kinds := []string{}
	for _, m := range got {
		kinds = append(kinds, m["type"].(string))
	}
	want := []string{"meta", "text", "thinking", "tool", "todo", "text", "usage", "stop"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("kinds[%d] = %q want %q (full: %v)", i, kinds[i], k, kinds)
		}
	}
}

func TestAggregateChat_AskUserQuestionPlaceholder(t *testing.T) {
	// AskUserQuestion is a tool — slim chat just shows it as a tool
	// bubble like any other (the rich rendering is in the Web UI).
	frames := []stream.Frame{
		mkFrame(1, stream.KindToolUseStart, map[string]any{"tool": "AskUserQuestion", "tool_use_id": "q1"}),
	}
	got := aggregateChat(frames)
	if len(got) != 1 || got[0]["tool"] != "AskUserQuestion" {
		t.Fatalf("got = %+v", got)
	}
}
