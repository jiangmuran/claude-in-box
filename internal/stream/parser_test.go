package stream

import (
	"context"
	"strings"
	"testing"
)

func TestParser_TextDeltaPassesThrough(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)

	input := strings.NewReader(`{"type":"text_delta","text":"hello"}` + "\n" +
		`{"type":"text_delta","text":" world"}` + "\n")

	if err := p.Run(context.Background(), input); err != nil {
		t.Fatalf("parser run: %v", err)
	}

	snap := bus.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snap len = %d want 2", len(snap))
	}
	for i, f := range snap {
		if f.Kind != KindTextDelta {
			t.Fatalf("snap[%d].kind = %q want %q", i, f.Kind, KindTextDelta)
		}
	}
}

func TestParser_NestedDeltaTextDelta(t *testing.T) {
	// Mimics the Anthropic content_block_delta shape with an inner text_delta.
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hey"}}` + "\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 1 || snap[0].Kind != KindTextDelta {
		t.Fatalf("snap = %+v want one text.delta", snap)
	}
	if !strings.Contains(string(snap[0].Data), `"hey"`) {
		t.Fatalf("data = %s want hey", snap[0].Data)
	}
}

func TestParser_ToolUseStartAndResult(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader(
		`{"type":"tool_use","tool":"Bash","tool_use_id":"tu1","input":{"command":"ls"}}` + "\n" +
			`{"type":"tool_result","tool":"Bash","tool_use_id":"tu1","output":{"stdout":"hi"}}` + "\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d want 2", len(snap))
	}
	if snap[0].Kind != KindToolUseStart {
		t.Fatalf("snap[0].kind = %q want %q", snap[0].Kind, KindToolUseStart)
	}
	if snap[1].Kind != KindToolUseResult {
		t.Fatalf("snap[1].kind = %q want %q", snap[1].Kind, KindToolUseResult)
	}
}

func TestParser_TodoUpdate(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader(`{"type":"todo_update","items":[{"id":"1","subject":"do it","status":"in_progress","activeForm":"doing it"}]}` + "\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 1 || snap[0].Kind != KindTodoUpdate {
		t.Fatalf("snap = %+v want todo.update", snap)
	}
	if !strings.Contains(string(snap[0].Data), `"doing it"`) {
		t.Fatalf("data = %s missing activeForm", snap[0].Data)
	}
}

func TestParser_UsageAndStop(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader(
		`{"type":"usage","usage":{"input":12,"output":34,"cache_read":7}}` + "\n" +
			`{"type":"message_stop","stop_reason":"end_turn"}` + "\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d want 2", len(snap))
	}
	if snap[0].Kind != KindUsage || !strings.Contains(string(snap[0].Data), `"input":12`) {
		t.Fatalf("usage frame off: %+v", snap[0])
	}
	if snap[1].Kind != KindStop || !strings.Contains(string(snap[1].Data), `"end_turn"`) {
		t.Fatalf("stop frame off: %+v", snap[1])
	}
}

func TestParser_HookEvent(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader(`{"type":"hook_event","hook_event_name":"PreToolUse","hook_input":{"tool":"Bash"}}` + "\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 1 || snap[0].Kind != KindHook {
		t.Fatalf("snap = %+v want hook", snap)
	}
	if !strings.Contains(string(snap[0].Data), `"PreToolUse"`) {
		t.Fatalf("data = %s missing event name", snap[0].Data)
	}
}

func TestParser_UnknownTypeFallsThrough(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader(`{"type":"future_event","payload":{"x":1}}` + "\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 1 || snap[0].Kind != KindCCRaw {
		t.Fatalf("snap = %+v want cc.raw", snap)
	}
}

func TestParser_NonJSONLineEmitsPTYRaw(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader("plain terminal banner\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 1 || snap[0].Kind != KindPTYRaw {
		t.Fatalf("snap = %+v want pty.raw", snap)
	}
}

func TestParser_MalformedJSONDoesNotKillParser(t *testing.T) {
	bus := NewBus("s1", 64)
	p := NewParser(bus)
	in := strings.NewReader("{not json\n" +
		`{"type":"text_delta","text":"after"}` + "\n")
	if err := p.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	snap := bus.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len = %d want 2", len(snap))
	}
	if snap[1].Kind != KindTextDelta {
		t.Fatalf("snap[1].kind = %q want text.delta", snap[1].Kind)
	}
}
