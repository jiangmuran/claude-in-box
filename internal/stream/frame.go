// Package stream defines the structured event frames the control plane fans
// out to subscribers, the in-memory bus that does the fan-out, and the parser
// that translates Claude Code's stream-json stdout into frames.
package stream

import (
	"encoding/json"
	"time"
)

// Kind enumerates the well-known frame kinds. Clients may also encounter
// `cc.raw` (an unrecognised Claude Code event we passed through verbatim) and
// in future versions, new kinds — they should be tolerated.
const (
	KindTextDelta     = "text.delta"
	KindThinking      = "thinking"
	KindToolUseStart  = "tool.use.start"
	KindToolUseResult = "tool.use.result"
	KindTodoUpdate    = "todo.update"
	KindAskQuestion   = "ask.question"
	KindUsage         = "usage"
	KindStatus        = "status"
	KindStop          = "stop"
	KindMeta          = "meta"
	KindHook          = "hook"
	KindPTYRaw        = "pty.raw"
	KindCCRaw         = "cc.raw"
)

// State is the high-level state of a session, exposed via `status` frames.
type State string

const (
	StateStarting        State = "starting"
	StateIdle            State = "idle"
	StateWorking         State = "working"
	StateWaitingForInput State = "waiting_for_input"
	StateStopped         State = "stopped"
	StateFailed          State = "failed"
)

// Frame is the on-the-wire representation of one event. It is JSON-encoded
// when sent to subscribers, and is also what tests assert against.
type Frame struct {
	Session string          `json:"session"`
	Seq     uint64          `json:"seq"`
	TS      time.Time       `json:"ts"`
	Kind    string          `json:"kind"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// NewFrame builds a frame with `data` JSON-encoded once and stored as the
// RawMessage; callers pass any struct.
func NewFrame(session string, seq uint64, kind string, data any) (Frame, error) {
	f := Frame{
		Session: session,
		Seq:     seq,
		TS:      time.Now().UTC(),
		Kind:    kind,
	}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return Frame{}, err
		}
		f.Data = b
	}
	return f, nil
}

// ----- Data payloads --------------------------------------------------------

type TextDeltaData struct {
	Text string `json:"text"`
}

type ToolUseStartData struct {
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type ToolUseResultData struct {
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	Tool       string          `json:"tool"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMs int64           `json:"duration_ms,omitempty"`
}

type TodoItem struct {
	ID         string `json:"id,omitempty"`
	Subject    string `json:"subject"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

type TodoUpdateData struct {
	Items []TodoItem `json:"items"`
}

type AskQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type AskQuestionData struct {
	Prompt      string              `json:"prompt"`
	Options     []AskQuestionOption `json:"options"`
	MultiSelect bool                `json:"multi_select"`
}

type UsageData struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cache_read,omitempty"`
	CacheWrite  int `json:"cache_write,omitempty"`
}

type StatusData struct {
	State     State `json:"state"`
	ElapsedMs int64 `json:"elapsed_ms,omitempty"`
}

type StopData struct {
	Reason string `json:"reason"`
}

type MetaData struct {
	Model    string `json:"model,omitempty"`
	Workdir  string `json:"workdir,omitempty"`
	AuthMode string `json:"auth_mode,omitempty"`
	Note     string `json:"note,omitempty"`
}

type HookData struct {
	Name    string          `json:"name"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type PTYRawData struct {
	// Text is the raw line content. ANSI escape sequences and control bytes
	// pass through as JSON-escaped (`[31m...`); invalid UTF-8 is
	// replaced with U+FFFD. CC outputs UTF-8 in practice, so this is fine
	// and produces a human-readable wire shape for terminal-style clients.
	Text string `json:"text"`
}

type CCRawData struct {
	// Original holds the unmodified source line verbatim. Encoded as a JSON
	// string so it round-trips even when the line itself is not valid JSON
	// (e.g., truncated stream-json or PTY noise that slipped into stdout).
	Original string `json:"original"`
}
