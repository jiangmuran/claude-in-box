// Package shell manages plain-bash PTY shells inside the container —
// separate from session.Manager which manages Claude Code REPL sessions.
//
// A shell is a child `bash -l` attached to a PTY. The Web UI binds a
// WebSocket to each shell for bidirectional raw bytes: user keystrokes
// flow in, terminal output flows out. Multiple subscribers per shell are
// supported with a ring-buffer scrollback for late joiners.
package shell

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// Default scrollback (bytes) per shell. 64 KiB is enough to repaint a
// terminal a few times when a client reconnects without ballooning per-
// shell memory.
const DefaultScrollback = 64 * 1024

// Shell is one bash PTY child.
type Shell struct {
	ID        string    `json:"id"`
	CWD       string    `json:"cwd"`
	Cmd       string    `json:"cmd"`
	CreatedAt time.Time `json:"created_at"`

	cmd *exec.Cmd
	pty *os.File

	mu       sync.Mutex
	buf      []byte
	bufLimit int
	subID    atomic.Uint64
	subs     map[uint64]chan []byte

	done     chan struct{}
	doneOnce sync.Once
	exitCode int
}

// Manager owns the set of live shells. Shells are not persisted — they
// die with the container.
type Manager struct {
	BaseDir string // default cwd for new shells when none is requested

	mu     sync.RWMutex
	shells map[string]*Shell
}

// NewManager.
func NewManager(baseDir string) *Manager {
	if baseDir == "" {
		baseDir = "/workspace"
	}
	return &Manager{
		BaseDir: baseDir,
		shells:  make(map[string]*Shell),
	}
}

// SpawnOptions configures a new shell.
type SpawnOptions struct {
	// CWD; defaults to Manager.BaseDir when empty.
	CWD string
	// Cmd — defaults to "bash" with `-l` so /etc/profile + ~/.profile run.
	Cmd     string
	Args    []string
	Env     []string
	Cols    uint16
	Rows    uint16
}

// Spawn creates a new shell and starts the underlying process. The
// returned Shell is already running; subscribers will see future output.
func (m *Manager) Spawn(opts SpawnOptions) (*Shell, error) {
	if opts.CWD == "" {
		opts.CWD = m.BaseDir
	}
	if opts.Cmd == "" {
		opts.Cmd = "bash"
		if len(opts.Args) == 0 {
			opts.Args = []string{"-l"}
		}
	}
	if opts.Cols == 0 {
		opts.Cols = 120
	}
	if opts.Rows == 0 {
		opts.Rows = 32
	}

	cmd := exec.Command(opts.Cmd, opts.Args...)
	cmd.Dir = opts.CWD
	env := append([]string{}, os.Environ()...)
	if opts.Env != nil {
		env = append(env, opts.Env...)
	}
	// Force a sensible TERM so colour + line editing work in xterm.js.
	if !hasEnv(env, "TERM=") {
		env = append(env, "TERM=xterm-256color")
	}
	cmd.Env = env

	master, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: opts.Cols,
		Rows: opts.Rows,
	})
	if err != nil {
		return nil, fmt.Errorf("shell.Spawn: pty.Start: %w", err)
	}

	s := &Shell{
		ID:        uuid.NewString(),
		CWD:       opts.CWD,
		Cmd:       opts.Cmd,
		CreatedAt: time.Now().UTC(),
		cmd:       cmd,
		pty:       master,
		bufLimit:  DefaultScrollback,
		subs:      make(map[uint64]chan []byte),
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.shells[s.ID] = s
	m.mu.Unlock()

	go s.reader()
	go s.reap()

	return s, nil
}

// Get a shell by id.
func (m *Manager) Get(id string) (*Shell, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.shells[id]
	return s, ok
}

// List returns a snapshot of all shells.
func (m *Manager) List() []*Shell {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Shell, 0, len(m.shells))
	for _, s := range m.shells {
		out = append(out, s)
	}
	return out
}

// Kill signals the underlying process.
func (m *Manager) Kill(id string, sig os.Signal) error {
	s, ok := m.Get(id)
	if !ok {
		return errors.New("shell: no such shell")
	}
	if s.cmd.Process == nil {
		return errors.New("shell: not started")
	}
	return s.cmd.Process.Signal(sig)
}

// Forget removes a stopped shell from the map. Returns true if the entry
// existed and was removed.
func (m *Manager) Forget(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.shells[id]; !ok {
		return false
	}
	delete(m.shells, id)
	return true
}

// ----- Shell methods ------------------------------------------------------

// Write sends bytes to the PTY's stdin (i.e. keystrokes).
func (s *Shell) Write(b []byte) (int, error) {
	if s.pty == nil {
		return 0, errors.New("shell: pty closed")
	}
	return s.pty.Write(b)
}

// Resize updates the PTY winsize.
func (s *Shell) Resize(cols, rows uint16) error {
	if s.pty == nil {
		return errors.New("shell: pty closed")
	}
	return pty.Setsize(s.pty, &pty.Winsize{Cols: cols, Rows: rows})
}

// Subscribe returns a channel that receives PTY output frames from now on,
// plus a one-shot snapshot of buffered scrollback the caller can write to
// the terminal first.
func (s *Shell) Subscribe(chanCap int) (id uint64, ch <-chan []byte, scrollback []byte) {
	if chanCap <= 0 {
		chanCap = 64
	}
	id = s.subID.Add(1)
	out := make(chan []byte, chanCap)

	s.mu.Lock()
	scrollback = make([]byte, len(s.buf))
	copy(scrollback, s.buf)
	s.subs[id] = out
	s.mu.Unlock()

	return id, out, scrollback
}

// Unsubscribe detaches a subscriber and closes its channel.
func (s *Shell) Unsubscribe(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.subs[id]; ok {
		delete(s.subs, id)
		// Defensive close inside a recover to avoid panics if the channel
		// was already closed by the fanout loop dropping a slow subscriber.
		safeClose(ch)
	}
}

func safeClose(ch chan []byte) {
	defer func() { _ = recover() }()
	close(ch)
}

// Done is closed once the underlying process has exited.
func (s *Shell) Done() <-chan struct{} { return s.done }

// ExitCode is meaningful only after Done.
func (s *Shell) ExitCode() int { return s.exitCode }

// reader drains the PTY into the scrollback buffer and fan-outs to subs.
func (s *Shell) reader() {
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			s.mu.Lock()
			s.buf = append(s.buf, chunk...)
			if len(s.buf) > s.bufLimit {
				s.buf = s.buf[len(s.buf)-s.bufLimit:]
			}
			for id, ch := range s.subs {
				select {
				case ch <- chunk:
				default:
					delete(s.subs, id)
					safeClose(ch)
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// reap waits for the process and closes the done channel.
func (s *Shell) reap() {
	err := s.cmd.Wait()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			s.exitCode = ee.ExitCode()
		} else {
			s.exitCode = -1
		}
	}
	// Close PTY + subs so blocked readers wake up.
	s.mu.Lock()
	if s.pty != nil {
		_ = s.pty.Close()
		s.pty = nil
	}
	for id, ch := range s.subs {
		delete(s.subs, id)
		safeClose(ch)
	}
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}

// hasEnv reports whether `kv` (`KEY=`) prefix appears in any entry of env.
func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if len(e) >= len(kv) && e[:len(kv)] == kv {
			return true
		}
	}
	return false
}
