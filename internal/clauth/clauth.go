// Package clauth drives `claude auth login --claudeai` inside the container
// as a PTY-backed flow. The Web UI presents the URL the CLI prints, the user
// authorises in their browser (which redirects to platform.claude.com and
// shows a code), and the UI hands that code back via this package so the CLI
// can finish exchanging the code for credentials.
//
// Once a flow completes successfully, Claude Code's standard credentials are
// written to the container's $CLAUDE_CONFIG_DIR (defaulting to /home/coder/
// .claude). New sessions can then declare auth_mode="subscription" with no
// further env vars and they consume the user's interactive quota — which is
// the **only** subscription-billed path after Anthropic's 2026-06-15 split
// that moved setup-token-issued tokens onto a separate Agent SDK quota.
package clauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// State enumerates Flow lifecycle states.
type State string

const (
	StateStarting     State = "starting"
	StateAwaitingCode State = "awaiting_code"
	StateVerifying    State = "verifying"
	StateDone         State = "done"
	StateFailed       State = "failed"
	StateCancelled    State = "cancelled"
	StateTimedOut     State = "timed_out"
)

// StatusJSON mirrors `claude auth status --json`.
type StatusJSON struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod,omitempty"`
	APIProvider      string `json:"apiProvider,omitempty"`
	Email            string `json:"email,omitempty"`
	OrgID            string `json:"orgId,omitempty"`
	OrgName          string `json:"orgName,omitempty"`
	SubscriptionType string `json:"subscriptionType,omitempty"`
}

// Manager owns the Claude binary path and the (at most one) in-flight login
// flow. A second concurrent flow is rejected so two browsers cannot fight
// over the same on-disk credentials.
type Manager struct {
	ClaudeBin string // empty → look up on PATH
	mu        sync.Mutex
	flow      *Flow
}

// NewManager builds a Manager. If claudeBin is empty the binary is resolved
// from PATH at flow start.
func NewManager(claudeBin string) *Manager {
	return &Manager{ClaudeBin: claudeBin}
}

// resolveBin returns the path to the claude binary.
func (m *Manager) resolveBin() (string, error) {
	bin := m.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	if path, err := exec.LookPath(bin); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("clauth: claude binary %q not on PATH", bin)
}

// Status returns the current authentication state.
//
// `claude auth status --json` exits with code 1 when not logged in but
// still prints valid `{"loggedIn":false,...}` JSON on stdout. We must
// parse the body regardless of exit code and only return an error if the
// body itself is not parseable.
func (m *Manager) Status(ctx context.Context) (StatusJSON, error) {
	bin, err := m.resolveBin()
	if err != nil {
		return StatusJSON{}, err
	}
	cmd := exec.CommandContext(ctx, bin, "auth", "status", "--json")
	out, runErr := cmd.Output()
	// runErr is non-nil when the child exited non-zero (logged-out case).
	// `out` still contains the stdout JSON though, so parse it first.
	if len(out) > 0 {
		var s StatusJSON
		if jerr := json.Unmarshal(out, &s); jerr == nil {
			return s, nil
		}
	}
	if runErr != nil {
		return StatusJSON{}, fmt.Errorf("clauth: auth status: %w", runErr)
	}
	return StatusJSON{}, fmt.Errorf("clauth: auth status returned no parseable JSON")
}

// Logout invalidates current credentials.
func (m *Manager) Logout(ctx context.Context) error {
	bin, err := m.resolveBin()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "auth", "logout")
	return cmd.Run()
}

// ErrAlreadyInFlight is returned by Start if another flow is already running.
var ErrAlreadyInFlight = errors.New("clauth: a login flow is already running")

// StartOptions configures a Start call.
type StartOptions struct {
	// SSO forces the --sso flag.
	SSO bool
	// Console picks the API-key flow (--console) instead of the subscription one.
	Console bool
	// Email pre-populates the login email.
	Email string
	// URLTimeout caps how long we wait for the CLI to print the auth URL.
	URLTimeout time.Duration
	// IdleTimeout reaps a flow that sat in awaiting_code too long without
	// receiving a code.
	IdleTimeout time.Duration
}

// Start spawns `claude auth login` and waits for the auth URL to appear in
// its output. It returns the URL and a flow handle. The flow is paused in
// `awaiting_code` until SubmitCode is called.
func (m *Manager) Start(ctx context.Context, opts StartOptions) (*Flow, error) {
	m.mu.Lock()
	if m.flow != nil && !m.flow.terminal() {
		m.mu.Unlock()
		return nil, ErrAlreadyInFlight
	}
	m.mu.Unlock()

	bin, err := m.resolveBin()
	if err != nil {
		return nil, err
	}

	args := []string{"auth", "login"}
	switch {
	case opts.Console:
		args = append(args, "--console")
	default:
		args = append(args, "--claudeai")
	}
	if opts.SSO {
		args = append(args, "--sso")
	}
	if opts.Email != "" {
		args = append(args, "--email", opts.Email)
	}

	if opts.URLTimeout <= 0 {
		opts.URLTimeout = 30 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}

	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("clauth: pty.Start: %w", err)
	}

	f := &Flow{
		ID:        uuid.NewString(),
		StartedAt: time.Now().UTC(),
		cmd:       cmd,
		pty:       master,
		done:      make(chan struct{}),
		urlReady:  make(chan struct{}),
	}
	f.state.Store(string(StateStarting))

	go f.reader()
	go f.reap()

	m.mu.Lock()
	m.flow = f
	m.mu.Unlock()

	// Wait for the URL or for the process to exit (failure path).
	select {
	case <-f.urlReady:
		// State should now be awaiting_code.
		// Arm an idle reaper.
		go f.armIdleTimeout(opts.IdleTimeout)
		return f, nil
	case <-time.After(opts.URLTimeout):
		f.fail(StateTimedOut, "auth URL did not appear within "+opts.URLTimeout.String())
		return f, errors.New("clauth: auth URL did not appear in time")
	case <-f.done:
		err := f.errorString()
		if err == "" {
			err = "claude auth login exited before printing the auth URL"
		}
		return f, errors.New("clauth: " + err)
	}
}

// Active returns the in-flight flow (if any).
func (m *Manager) Active() *Flow {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow == nil || m.flow.terminal() {
		return nil
	}
	return m.flow
}

// GetFlow returns a flow by ID (active or terminal).
func (m *Manager) GetFlow(id string) *Flow {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.flow != nil && m.flow.ID == id {
		return m.flow
	}
	return nil
}

// ----- Flow ----------------------------------------------------------------

// Flow is a single login attempt.
type Flow struct {
	ID        string
	StartedAt time.Time

	cmd     *exec.Cmd
	pty     *os.File
	state   atomic.Value // string of State
	errMsg  atomic.Value // string
	authURL atomic.Value // string
	output  bytes.Buffer
	mu      sync.Mutex

	urlReady chan struct{}
	urlOnce  sync.Once
	done     chan struct{}
	doneOnce sync.Once
}

// urlRe matches the long https://claude.com/cai/oauth/authorize?... URL the
// CLI prints. Tolerates either claude.com or claude.ai hostnames in case
// Anthropic shifts the path.
var urlRe = regexp.MustCompile(`https://claude\.(?:com|ai)/[^\s\x1b]*authorize[^\s\x1b]*`)

func (f *Flow) reader() {
	buf := make([]byte, 4096)
	for {
		n, err := f.pty.Read(buf)
		if n > 0 {
			f.mu.Lock()
			f.output.Write(buf[:n])
			currentOut := f.output.String()
			f.mu.Unlock()

			if f.authURL.Load() == nil {
				if m := urlRe.FindString(currentOut); m != "" {
					f.authURL.Store(m)
					f.state.Store(string(StateAwaitingCode))
					f.urlOnce.Do(func() { close(f.urlReady) })
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (f *Flow) reap() {
	err := f.cmd.Wait()
	_ = f.pty.Close()
	f.doneOnce.Do(func() { close(f.done) })

	// If we already moved to a terminal state via Cancel/fail, leave it.
	switch State(f.state.Load().(string)) {
	case StateCancelled, StateFailed, StateTimedOut:
		return
	}

	if err == nil {
		f.state.Store(string(StateDone))
		return
	}
	// Exit != 0: claude auth login printed a reason somewhere in output.
	f.state.Store(string(StateFailed))
	f.errMsg.Store(strings.TrimSpace(f.tail()))
	_ = err // kept to mute the linter; reason already captured from output
}

func (f *Flow) tail() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	const cap = 512
	s := f.output.String()
	if len(s) > cap {
		s = s[len(s)-cap:]
	}
	return s
}

// SubmitCode writes the code to the PTY's stdin and waits for the claude
// process to exit. Returns nil on success (state goes Done), error otherwise
// (state is Failed or TimedOut).
func (f *Flow) SubmitCode(ctx context.Context, code string) error {
	state := State(f.state.Load().(string))
	if state != StateAwaitingCode {
		return fmt.Errorf("clauth: flow is in %q, not awaiting_code", state)
	}
	f.state.Store(string(StateVerifying))

	if _, err := f.pty.Write([]byte(strings.TrimSpace(code) + "\n")); err != nil {
		return fmt.Errorf("clauth: write code to pty: %w", err)
	}

	select {
	case <-f.done:
		final := State(f.state.Load().(string))
		if final == StateDone {
			return nil
		}
		if m := f.errMsg.Load(); m != nil {
			return errors.New("clauth: " + m.(string))
		}
		return fmt.Errorf("clauth: flow ended in %q", final)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancel kills the underlying claude process.
func (f *Flow) Cancel() {
	if f.terminal() {
		return
	}
	f.state.Store(string(StateCancelled))
	if f.cmd.Process != nil {
		_ = f.cmd.Process.Signal(os.Interrupt)
		// Give it a moment to exit gracefully.
		go func() {
			t := time.NewTimer(500 * time.Millisecond)
			defer t.Stop()
			select {
			case <-f.done:
			case <-t.C:
				_ = f.cmd.Process.Kill()
			}
		}()
	}
}

func (f *Flow) fail(s State, msg string) {
	f.state.Store(string(s))
	if msg != "" {
		f.errMsg.Store(msg)
	}
	_ = f.pty.Close()
	if f.cmd.Process != nil {
		_ = f.cmd.Process.Kill()
	}
}

func (f *Flow) armIdleTimeout(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-f.done:
		return
	case <-t.C:
		if State(f.state.Load().(string)) == StateAwaitingCode {
			f.fail(StateTimedOut, "user did not paste the code within "+d.String())
		}
	}
}

func (f *Flow) terminal() bool {
	switch State(f.state.Load().(string)) {
	case StateDone, StateFailed, StateCancelled, StateTimedOut:
		return true
	default:
		return false
	}
}

func (f *Flow) errorString() string {
	if v := f.errMsg.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// Snapshot is the public view of a flow's state.
func (f *Flow) Snapshot() Snapshot {
	return Snapshot{
		ID:      f.ID,
		State:   State(f.state.Load().(string)),
		AuthURL: stringValue(f.authURL.Load()),
		Started: f.StartedAt,
		Error:   f.errorString(),
	}
}

// Snapshot is the JSON-safe shape of a flow.
type Snapshot struct {
	ID      string    `json:"id"`
	State   State     `json:"state"`
	AuthURL string    `json:"auth_url,omitempty"`
	Started time.Time `json:"started_at"`
	Error   string    `json:"error,omitempty"`
}

func stringValue(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// drain is a small helper for tests that just want to flush PTY output.
func drain(r io.Reader) string {
	var b bytes.Buffer
	_, _ = io.Copy(&b, r)
	return b.String()
}

// Compile-time check.
var _ = drain
