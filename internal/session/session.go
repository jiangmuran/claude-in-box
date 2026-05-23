// Package session manages PTY-backed Claude Code child processes and exposes
// each one as a long-lived object with a structured event stream, an input
// channel, and lifecycle controls.
//
// A Session wraps:
//   - the *exec.Cmd running `claude` (or, in tests, a substitute command),
//   - the PTY master file descriptor that backs CC's controlling TTY,
//   - a stream.Bus that fans typed frames out to subscribers,
//   - a stream.Parser that converts CC's --output-format stream-json output
//     into frames on that bus.
//
// All sessions are managed by a *Manager (see manager.go).
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// Session is a single running (or stopped) Claude Code instance.
type Session struct {
	ID       string `json:"id"`
	Workdir  string `json:"workdir"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	AuthMode string `json:"auth_mode,omitempty"`
	// ClaudeSessionID is claude's OWN internal session id, captured from
	// the system.init line in the transcript JSONL. We need it for
	// --resume because claude indexes its transcripts by this, not by
	// cib's UUID.
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	StoppedAt       time.Time `json:"stopped_at,omitempty"`
	ExitCode        int       `json:"exit_code,omitempty"`
	BaseDir         string    `json:"-"` // sessions/<id>/

	// User-editable display metadata, set via SetTitle / SetGoal.
	// Persisted into meta.json so they survive cib restarts. These
	// fields exist so embedded clients (Cardputer + similar) can show
	// a session list with human labels without keeping their own
	// catalog. Title defaults to the first user prompt's prefix once
	// available; Goal stays empty unless the client sets it.
	Title string `json:"title,omitempty"`
	Goal  string `json:"goal,omitempty"`

	// Running token + cost totals — updated whenever the transcript
	// watcher publishes a `usage` frame. Exposed via Usage().
	tokensIn   atomic.Uint64
	tokensOut  atomic.Uint64
	cacheRead  atomic.Uint64
	cacheWrite atomic.Uint64

	// HookToken is the per-session shared secret used to authenticate hook
	// callbacks the child process makes back to /internal/hooks/<id>. Never
	// serialized to disk and not exposed through any public API surface.
	hookToken string

	cmd    *exec.Cmd
	pty    *os.File
	bus    *stream.Bus
	parser *stream.Parser

	state atomic.Value // stream.State
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex

	// transcriptStop, when non-nil, cancels the cctranscript watcher.
	transcriptStop func()
}

// Bus returns the session's event bus. Exposed so other packages (e.g. hook
// receiver wiring) can publish frames without going through Session methods.
func (s *Session) Bus() *stream.Bus { return s.bus }

// HookToken returns the session's hook callback token.
func (s *Session) HookToken() string { return s.hookToken }

// Status is an immutable snapshot of a session's externally visible state.
// Use this from API handlers; direct field reads on Session race with the
// reaper goroutine which writes StoppedAt / ExitCode at process exit.
type Status struct {
	ID              string       `json:"id"`
	Workdir         string       `json:"workdir"`
	Model           string       `json:"model,omitempty"`
	Effort          string       `json:"effort,omitempty"`
	AuthMode        string       `json:"auth_mode,omitempty"`
	ClaudeSessionID string       `json:"claude_session_id,omitempty"`
	Title           string       `json:"title,omitempty"`
	Goal            string       `json:"goal,omitempty"`
	State           stream.State `json:"state"`
	CreatedAt       time.Time    `json:"created_at"`
	StartedAt       time.Time    `json:"started_at,omitempty"`
	StoppedAt       time.Time    `json:"stopped_at,omitempty"`
	ExitCode        int          `json:"exit_code,omitempty"`
	LastSeq         uint64       `json:"last_seq"`
	Usage           Usage        `json:"usage"`
}

// Usage is the running token + cache totals for a session, exposed via
// Session.Usage() and inlined into Status.Usage. Sums are accumulated
// across the lifetime of the session; the cctranscript watcher feeds
// them by snooping `usage` frames.
type Usage struct {
	Input      uint64 `json:"input"`
	Output     uint64 `json:"output"`
	CacheRead  uint64 `json:"cache_read"`
	CacheWrite uint64 `json:"cache_write"`
}

// Snapshot returns the session's current externally visible state in a
// race-free way.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		ID:              s.ID,
		Workdir:         s.Workdir,
		Model:           s.Model,
		Effort:          s.Effort,
		AuthMode:        s.AuthMode,
		ClaudeSessionID: s.ClaudeSessionID,
		Title:           s.Title,
		Goal:            s.Goal,
		State:           s.State(),
		CreatedAt:       s.CreatedAt,
		StartedAt:       s.StartedAt,
		StoppedAt:       s.StoppedAt,
		ExitCode:        s.ExitCode,
		LastSeq:         s.bus.LastSeq(),
		Usage:           s.usageSnapshotLocked(),
	}
}

// Usage returns the running token totals for the session.
func (s *Session) Usage() Usage {
	return Usage{
		Input:      s.tokensIn.Load(),
		Output:     s.tokensOut.Load(),
		CacheRead:  s.cacheRead.Load(),
		CacheWrite: s.cacheWrite.Load(),
	}
}

func (s *Session) usageSnapshotLocked() Usage { return s.Usage() }

// AddUsage accumulates a usage delta into the session's running totals
// and rewrites meta.json so cib restarts preserve the count. Callers
// (the cctranscript watcher) invoke this whenever Claude emits a usage
// frame.
func (s *Session) AddUsage(in, out, cacheRead, cacheWrite uint64) {
	if in > 0 {
		s.tokensIn.Add(in)
	}
	if out > 0 {
		s.tokensOut.Add(out)
	}
	if cacheRead > 0 {
		s.cacheRead.Add(cacheRead)
	}
	if cacheWrite > 0 {
		s.cacheWrite.Add(cacheWrite)
	}
	_ = s.writeMeta()
}

// SetTitle sets the session's display title (user-editable) and
// persists it to meta.json. Empty input is allowed (clears the field).
func (s *Session) SetTitle(title string) error {
	s.mu.Lock()
	s.Title = title
	s.mu.Unlock()
	return s.writeMeta()
}

// SetGoal sets the session's goal string (a one-line summary of what
// the user is trying to do) and persists it.
func (s *Session) SetGoal(goal string) error {
	s.mu.Lock()
	s.Goal = goal
	s.mu.Unlock()
	return s.writeMeta()
}

// State returns the current high-level session state.
func (s *Session) State() stream.State {
	if v := s.state.Load(); v != nil {
		return v.(stream.State)
	}
	return stream.StateStarting
}

// SetState updates the session state and publishes a `status` frame.
func (s *Session) SetState(st stream.State) {
	s.state.Store(st)
	_, _ = s.bus.Publish(stream.KindStatus, stream.StatusData{
		State:     st,
		ElapsedMs: s.elapsedMs(),
	})
}

func (s *Session) elapsedMs() int64 {
	if s.StartedAt.IsZero() {
		return 0
	}
	return time.Since(s.StartedAt).Milliseconds()
}

// Subscribe attaches a subscriber to this session's frame stream starting
// from fromSeq (exclusive). The subscription ends when ctx is cancelled or
// Cancel() is called.
func (s *Session) Subscribe(ctx context.Context, fromSeq uint64) *stream.Subscription {
	return s.bus.Subscribe(ctx, fromSeq, 256)
}

// LastSeq returns the most recently published sequence number.
func (s *Session) LastSeq() uint64 { return s.bus.LastSeq() }

// Snapshot returns a copy of all buffered frames (oldest first).
func (s *Session) Snapshot() []stream.Frame { return s.bus.Snapshot() }

// Write sends bytes to the session's PTY (i.e. Claude Code's stdin).
func (s *Session) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pty == nil {
		return 0, errors.New("session: pty closed")
	}
	return s.pty.Write(b)
}

// WriteString sends a UTF-8 string to the PTY.
func (s *Session) WriteString(text string) error {
	_, err := s.Write([]byte(text))
	return err
}

// Interrupt sends Ctrl-C (ETX) into the PTY, which delivers SIGINT to the
// foreground process group on the slave side.
func (s *Session) Interrupt() error {
	return s.WriteString("\x03")
}

// SetModel writes "/model <name>\n" into the PTY and publishes a meta frame.
func (s *Session) SetModel(model string) error {
	if err := s.WriteString(fmt.Sprintf("/model %s\n", model)); err != nil {
		return err
	}
	s.mu.Lock()
	s.Model = model
	s.mu.Unlock()
	_, _ = s.bus.Publish(stream.KindMeta, stream.MetaData{Model: model})
	return nil
}

// Kill sends a signal to the child process. Use os.Interrupt or syscall.SIGTERM
// for graceful shutdown; os.Kill for forceful termination.
func (s *Session) Kill(sig os.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return errors.New("session: not started")
	}
	return s.cmd.Process.Signal(sig)
}

// Done is closed when the underlying process has exited.
func (s *Session) Done() <-chan struct{} { return s.done }

// closeBus shuts down subscriptions; safe to call multiple times.
func (s *Session) closeBus() {
	s.once.Do(func() {
		s.bus.CloseAll()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.pty != nil {
			_ = s.pty.Close()
			s.pty = nil
		}
	})
}

func (s *Session) writeMeta() error {
	if s.BaseDir == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.BaseDir, "meta.json"), b, 0o644)
}
