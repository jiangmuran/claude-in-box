// Package cctranscript tails the JSONL transcript file Claude Code writes
// under `~/.claude/projects/<encoded-cwd>/<session_id>.jsonl` and emits
// structured frames onto a session's bus — same shape the driver view
// already renders.
//
// This is the analog of claude-code-webui's UnifiedMessageProcessor.ts
// adapted for Go: the JSONL is the source of truth for chat history,
// streaming text, tool use, tool result, todos, usage, and lifecycle.
// Hooks alone do not carry the streaming text deltas; the transcript
// does. We tail it because the interactive REPL cannot emit stream-json
// to stdout.
package cctranscript

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Publisher is anything that can publish frames to a session bus.
// The return values are deliberately untyped so this package does not
// pull in internal/stream as a dependency; callers wrap their bus.
type Publisher interface {
	Publish(kind string, data any) (any, error)
}

// Watcher tails a single JSONL transcript file. Cancel ctx (or Stop()) to
// release the goroutine.
type Watcher struct {
	Path string
	Bus  Publisher
	Poll time.Duration // default 150 ms

	// OnInit is invoked once when we first see a `system.init` line in
	// the JSONL. Carries claude's internal session_id (different from
	// cib's session id; we need it for --resume) plus model + workdir.
	// Optional; nil = ignore.
	OnInit func(claudeSessionID, model, workdir string)

	// toolNames caches { tool_use_id -> tool_name } so we can join
	// tool_use blocks (from assistant turns) to their tool_result blocks
	// (which arrive later in user turns).
	toolNames sync.Map // map[string]string

	mu      sync.Mutex // protects cancel, started, off
	cancel  context.CancelFunc
	started bool
	off     int64

	done    chan struct{} // closed exactly once via doneOnce
	doneOnce sync.Once
	closed   atomic.Bool
}

// New creates a Watcher; call Start to begin tailing.
func New(path string, bus Publisher) *Watcher {
	return &Watcher{Path: path, Bus: bus, Poll: 150 * time.Millisecond, done: make(chan struct{})}
}

// Start tails the file in a goroutine. Safe to call multiple times —
// only the first call launches the goroutine.
func (w *Watcher) Start(ctx context.Context) {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	ctx, w.cancel = context.WithCancel(ctx)
	w.mu.Unlock()
	go w.run(ctx)
}

// Stop cancels the watcher's context. Safe to call before Start (in
// which case Done() returns a channel that is closed immediately) and
// safe to call multiple times.
func (w *Watcher) Stop() {
	if w.closed.Swap(true) {
		return
	}
	w.mu.Lock()
	cancel := w.cancel
	started := w.started
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !started {
		// Stop called before Start ever ran; nobody will close `done`.
		w.doneOnce.Do(func() { close(w.done) })
	}
}

// Done returns a channel closed when the watcher's goroutine exits (or
// immediately if Stop was called before Start).
func (w *Watcher) Done() <-chan struct{} { return w.done }

func (w *Watcher) run(ctx context.Context) {
	defer w.doneOnce.Do(func() { close(w.done) })

	// Wait for the file to appear. Claude writes it shortly after the
	// session starts; if it never shows up, the watcher just sits idle.
	for {
		if _, err := os.Stat(w.Path); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.Poll):
		}
	}

	scanBuf := make([]byte, 0, 64*1024)
	maxLine := 4 * 1024 * 1024
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Re-stat each tick; if file shrank (claude rewrote) reset offset.
		info, err := os.Stat(w.Path)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.Poll):
			}
			continue
		}
		if info.Size() < w.off {
			w.off = 0
		}
		if info.Size() == w.off {
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.Poll):
			}
			continue
		}
		f, err := os.Open(w.Path)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.Poll):
			}
			continue
		}
		if _, err := f.Seek(w.off, io.SeekStart); err != nil {
			f.Close()
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(scanBuf, maxLine)
		read := int64(0)
		for scanner.Scan() {
			line := scanner.Bytes()
			read += int64(len(line)) + 1 // +1 for the newline scanner ate
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			w.emit(line)
		}
		f.Close()
		w.off += read
	}
}

// HistoryReplay reads the entire transcript once (for late-joining
// subscribers / restart resume) and emits frames as if they were
// streaming. Does not affect a running watcher's offset.
func HistoryReplay(path string, bus Publisher) error {
	w := &Watcher{Path: path, Bus: bus}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cctranscript.HistoryReplay: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		w.emit(line)
	}
	return scanner.Err()
}

// ---- translation -----------------------------------------------------------

// rawEntry covers the union of claude transcript line shapes (the real
// on-disk schema in ~/.claude/projects/<hash>/<session>.jsonl — every
// field name is camelCase, NOT snake_case, and the line types are NOT
// the stream-json types: types observed include user, assistant,
// system, last-prompt, permission-mode, attachment, ai-title,
// file-history-snapshot).
type rawEntry struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model,omitempty"`
		Usage   *struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheReadInput      int `json:"cache_read_input_tokens,omitempty"`
			CacheCreationInput  int `json:"cache_creation_input_tokens,omitempty"`
		} `json:"usage,omitempty"`
	} `json:"message,omitempty"`

	// Every persistent-transcript line carries sessionId once claude
	// has decided what it is. Some lines also carry cwd / permissionMode.
	SessionID      string          `json:"sessionId,omitempty"`
	Cwd            string          `json:"cwd,omitempty"`
	PermissionMode string          `json:"permissionMode,omitempty"`
	Tools          json.RawMessage `json:"tools,omitempty"`

	// Result-style lines (legacy / --print mode); harmless on persistent
	// transcripts where they don't appear.
	Model        string  `json:"model,omitempty"`
	DurationMs   int64   `json:"duration_ms,omitempty"`
	TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
	IsError      bool    `json:"is_error,omitempty"`
	Usage        *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

type contentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Thinking   string          `json:"thinking,omitempty"`
	ID         string          `json:"id,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

func (w *Watcher) emit(line []byte) {
	var e rawEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	// Capture sessionId from the FIRST line of any type that carries it.
	// Persistent transcripts have no single "init" line — sessionId
	// appears on user/assistant/system/last-prompt/permission-mode etc.
	// alike. OnInit may fire once per watcher.
	if w.OnInit != nil && e.SessionID != "" {
		w.OnInit(e.SessionID, modelFromMessage(e), e.Cwd)
		w.OnInit = nil // one-shot
	}

	switch e.Type {
	case "system":
		// system.init carries model + cwd + tools + permissionMode.
		if e.Subtype == "init" {
			_, _ = w.Bus.Publish("meta", map[string]any{
				"model":          e.Model,
				"workdir":        e.Cwd,
				"session_id":     e.SessionID,
				"tools":          json.RawMessage(e.Tools),
				"permissionMode": e.PermissionMode,
				"note":           "claude session init",
			})
		}
	case "permission-mode":
		_, _ = w.Bus.Publish("meta", map[string]any{
			"permissionMode": e.PermissionMode,
			"session_id":     e.SessionID,
			"note":           "permission mode",
		})

	case "assistant":
		if e.Message == nil {
			return
		}
		w.emitContentBlocks(e.Message.Content, "assistant")
		if e.Message.Usage != nil {
			_, _ = w.Bus.Publish("usage", map[string]any{
				"input":       e.Message.Usage.InputTokens,
				"output":      e.Message.Usage.OutputTokens,
				"cache_read":  e.Message.Usage.CacheReadInput,
				"cache_write": e.Message.Usage.CacheCreationInput,
			})
		}

	case "user":
		if e.Message == nil {
			return
		}
		w.emitContentBlocks(e.Message.Content, "user")

	case "result":
		if e.Usage != nil {
			_, _ = w.Bus.Publish("usage", map[string]any{
				"input":  e.Usage.InputTokens,
				"output": e.Usage.OutputTokens,
			})
		}
		_, _ = w.Bus.Publish("stop", map[string]any{
			"reason":         e.Subtype,
			"duration_ms":    e.DurationMs,
			"total_cost_usd": e.TotalCostUSD,
			"is_error":       e.IsError,
		})
	}
}

func (w *Watcher) emitContentBlocks(raw json.RawMessage, role string) {
	// content can be a bare string OR an array of typed blocks.
	if len(raw) == 0 {
		return
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			_, _ = w.Bus.Publish("text.delta", map[string]any{"text": s, "role": role})
		}
		return
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			_, _ = w.Bus.Publish("text.delta", map[string]any{"text": b.Text, "role": role})

		case "thinking":
			if b.Thinking == "" {
				continue
			}
			_, _ = w.Bus.Publish("thinking", map[string]any{"text": b.Thinking})

		case "tool_use":
			// Cache the name so the tool_result later can self-identify.
			if b.ID != "" && b.Name != "" {
				w.toolNames.Store(b.ID, b.Name)
			}
			// Special-case TodoWrite: project the input list into a
			// todo.update frame the side rail already renders.
			if b.Name == "TodoWrite" {
				if items := parseTodos(b.Input); len(items) > 0 {
					_, _ = w.Bus.Publish("todo.update", map[string]any{"items": items})
				}
			}
			_, _ = w.Bus.Publish("tool.use.start", map[string]any{
				"tool_use_id": b.ID,
				"tool":        b.Name,
				"input":       json.RawMessage(b.Input),
			})

		case "tool_result":
			toolName := ""
			if v, ok := w.toolNames.Load(b.ToolUseID); ok {
				toolName, _ = v.(string)
			}
			_, _ = w.Bus.Publish("tool.use.result", map[string]any{
				"tool_use_id": b.ToolUseID,
				"tool":        toolName,
				"output":      json.RawMessage(b.Content),
				"is_error":    b.IsError,
			})
		}
	}
}

// modelFromMessage extracts a model name from an entry's message block
// (assistant messages on persistent transcripts carry `message.model`).
func modelFromMessage(e rawEntry) string {
	if e.Model != "" {
		return e.Model
	}
	if e.Message != nil {
		return e.Message.Model
	}
	return ""
}

func parseTodos(raw json.RawMessage) []map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var input struct {
		Todos []struct {
			Content    string `json:"content"`
			Subject    string `json:"subject"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
			ID         string `json:"id"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil
	}
	if len(input.Todos) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(input.Todos))
	for _, t := range input.Todos {
		subj := t.Content
		if subj == "" {
			subj = t.Subject
		}
		out = append(out, map[string]string{
			"id":         t.ID,
			"subject":    subj,
			"status":     strings.TrimSpace(t.Status),
			"activeForm": t.ActiveForm,
		})
	}
	return out
}
