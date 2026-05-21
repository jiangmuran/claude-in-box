package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Parser consumes Claude Code's `--output-format stream-json
// --include-hook-events` output (newline-delimited JSON objects on stdout) and
// publishes typed frames to a Bus.
//
// The translation is deliberately tolerant: well-known event shapes are
// mapped to typed frames; everything else passes through as a `cc.raw` frame
// carrying the original JSON so subscribers still see something. As we learn
// the exact stream-json schema from real Claude Code releases, we add more
// specific translations without breaking older clients.
type Parser struct {
	bus *Bus
}

// NewParser wires a parser to a bus.
func NewParser(bus *Bus) *Parser {
	return &Parser{bus: bus}
}

// Run reads JSONL from `r` line-by-line until EOF or ctx is cancelled.
// Returns the first non-EOF read error, if any. The reader is not closed.
func (p *Parser) Run(ctx context.Context, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		line := scanner.Bytes()
		line = trimTrailingNUL(line)
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' && line[0] != '[' {
			p.emitPTYRaw(line)
			continue
		}
		if err := p.handle(line); err != nil {
			p.emitRawString(line)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("parser scan: %w", err)
	}
	return nil
}

// RunRaw streams raw byte chunks from `r` as `pty.raw` frames — used by
// interactive REPL sessions whose output is ANSI/TUI, not JSONL. Returns
// the first non-EOF read error.
func (p *Parser) RunRaw(ctx context.Context, r io.Reader) error {
	buf := make([]byte, 4096)
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			p.emitPTYRaw(chunk)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("parser read: %w", err)
		}
	}
}

func trimTrailingNUL(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

// envelope captures the fields we need to discriminate the incoming event.
// Anthropic's stream-json schema uses "type" as the top-level discriminator,
// and may add nested "subtype" / "event" / "message" / "delta" fields. We
// keep this struct permissive.
type envelope struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	Event     json.RawMessage `json:"event,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Delta     json.RawMessage `json:"delta,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolID    string          `json:"tool_use_id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
	Usage     *UsageData      `json:"usage,omitempty"`
	HookName  string          `json:"hook_event_name,omitempty"`
	HookData  json.RawMessage `json:"hook_input,omitempty"`
	Text      string          `json:"text,omitempty"`
	StopReson string          `json:"stop_reason,omitempty"`
	Items     []TodoItem      `json:"items,omitempty"`
}

func (p *Parser) handle(line []byte) error {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return err
	}

	typ := strings.ToLower(env.Type)
	if typ == "" {
		typ = strings.ToLower(env.Subtype)
	}

	switch {
	case typ == "text_delta" || strings.Contains(typ, "text_delta") || (typ == "content_block_delta" && containsTextDelta(env.Delta)):
		text := env.Text
		if text == "" {
			text = pickText(env.Delta)
		}
		if text == "" {
			text = pickText(env.Message)
		}
		if text != "" {
			_, _ = p.bus.Publish(KindTextDelta, TextDeltaData{Text: text})
			return nil
		}
		return p.passThrough(line)

	case typ == "thinking" || strings.Contains(typ, "thinking"):
		text := env.Text
		if text == "" {
			text = pickText(env.Delta)
		}
		_, _ = p.bus.Publish(KindThinking, TextDeltaData{Text: text})
		return nil

	case typ == "tool_use" || typ == "tool_use_start" || strings.HasSuffix(typ, "tool_use"):
		_, _ = p.bus.Publish(KindToolUseStart, ToolUseStartData{
			ToolUseID: env.ToolID,
			Tool:      pickTool(env.Tool, env.ToolName),
			Input:     env.Input,
		})
		return nil

	case typ == "tool_result" || strings.HasSuffix(typ, "tool_result"):
		_, _ = p.bus.Publish(KindToolUseResult, ToolUseResultData{
			ToolUseID: env.ToolID,
			Tool:      pickTool(env.Tool, env.ToolName),
			Output:    env.Output,
			Error:     env.Error,
		})
		return nil

	case typ == "todo_update" || typ == "todo.update" || strings.Contains(typ, "todo"):
		if len(env.Items) > 0 {
			_, _ = p.bus.Publish(KindTodoUpdate, TodoUpdateData{Items: env.Items})
			return nil
		}
		return p.passThrough(line)

	case typ == "usage" || strings.HasSuffix(typ, "usage"):
		if env.Usage != nil {
			_, _ = p.bus.Publish(KindUsage, env.Usage)
			return nil
		}
		return p.passThrough(line)

	case typ == "stop" || typ == "message_stop" || typ == "session_end" || strings.HasSuffix(typ, "stop"):
		reason := env.StopReson
		if reason == "" {
			reason = "stop"
		}
		_, _ = p.bus.Publish(KindStop, StopData{Reason: reason})
		return nil

	case typ == "hook_event" || env.HookName != "":
		_, _ = p.bus.Publish(KindHook, HookData{
			Name:    env.HookName,
			Event:   env.HookName,
			Payload: env.HookData,
		})
		return nil

	default:
		return p.passThrough(line)
	}
}

func containsTextDelta(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(probe.Type), "text_delta")
}

func pickText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var probe struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Text
}

func pickTool(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (p *Parser) emitPTYRaw(line []byte) {
	_, _ = p.bus.Publish(KindPTYRaw, PTYRawData{Text: string(line)})
}

func (p *Parser) emitRawString(line []byte) {
	_, _ = p.bus.Publish(KindCCRaw, CCRawData{Original: string(line)})
}

func (p *Parser) passThrough(line []byte) error {
	_, _ = p.bus.Publish(KindCCRaw, CCRawData{Original: string(line)})
	return nil
}

