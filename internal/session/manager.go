package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"

	"github.com/jiangmuran/claude-in-box/internal/cctranscript"
	"github.com/jiangmuran/claude-in-box/internal/hooks"
	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// Manager owns the set of live sessions and the disk layout under BaseDir.
type Manager struct {
	BaseDir   string // e.g. /var/lib/claude-in-box/sessions
	ClaudeBin string // path to the `claude` binary; defaults to looking it up on PATH

	// ControlAddr is the host:port the child's hook scripts should curl back
	// to. Inside the container this is loopback (127.0.0.1:8080 by default).
	// Empty disables hook installation.
	ControlAddr string

	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager creates a Manager and ensures the base directory exists.
//
// claudeBin may be empty; it will be resolved at spawn time. Pass an absolute
// path to pin a specific version of the claude CLI.
func NewManager(baseDir, claudeBin string) (*Manager, error) {
	if baseDir == "" {
		return nil, errors.New("session.NewManager: baseDir is required")
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("session.NewManager: mkdir %s: %w", baseDir, err)
	}
	return &Manager{
		BaseDir:   baseDir,
		ClaudeBin: claudeBin,
		sessions:  make(map[string]*Session),
	}, nil
}

// SpawnOptions describe a new session to start.
type SpawnOptions struct {
	Workdir           string
	Model             string
	AuthMode          string // "subscription" | "api_key"
	APIKey            string
	OAuthToken        string
	ResumeFrom        string
	BypassPermissions bool
	// Effort sets claude's thinking depth via the --effort flag.
	// One of: "low", "medium", "high", "xhigh", "max". Empty = claude default.
	Effort string

	// Extra environment to pass to the child, in `KEY=VALUE` form. Used by
	// hooks integration in M1.3 (e.g. CIB_HOOK_HMAC_SECRET) and tests.
	ExtraEnv []string

	// Test hooks. When OverrideArgs is non-nil, the command line is built from
	// OverrideArgs verbatim (CC flags are ignored). When OverrideBin is set,
	// it overrides the resolved `claude` binary.
	OverrideBin  string
	OverrideArgs []string
}

// Spawn launches a new Claude Code session under management. The returned
// Session is already running.
func (m *Manager) Spawn(ctx context.Context, opts SpawnOptions) (*Session, error) {
	id := uuid.NewString()
	dir := filepath.Join(m.BaseDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("spawn: mkdir session dir: %w", err)
	}

	bin, args, err := m.commandFor(opts)
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:        id,
		Workdir:   opts.Workdir,
		Model:     opts.Model,
		Effort:    opts.Effort,
		AuthMode:  opts.AuthMode,
		CreatedAt: time.Now().UTC(),
		BaseDir:   dir,
		bus:       stream.NewBus(id, 4096),
		done:      make(chan struct{}),
	}
	sess.state.Store(stream.StateStarting)
	sess.parser = stream.NewParser(sess.bus)

	// Install per-session hooks. We do NOT override CLAUDE_CONFIG_DIR here:
	// that would cut the session off from the user's on-disk credentials
	// (written by `claude auth login`) and force every session to depend
	// on CLAUDE_CODE_OAUTH_TOKEN. Instead we drop a settings.json under
	// <session_dir>/.claude/ that contains ONLY our HTTP hooks, and pass
	// it via --settings so Claude Code merges it with the user's existing
	// settings/credentials at ~/.claude/.
	var settingsFile string
	if m.ControlAddr != "" {
		token, err := hooks.NewToken()
		if err != nil {
			return nil, fmt.Errorf("spawn: gen hook token: %w", err)
		}
		configDir := filepath.Join(dir, ".claude")
		path, err := hooks.WriteSessionSettings(configDir, id, token, m.ControlAddr, nil)
		if err != nil {
			return nil, fmt.Errorf("spawn: write hook settings: %w", err)
		}
		sess.hookToken = token
		settingsFile = path
	}

	// Re-assemble the command args so --settings comes right after the
	// stream-json flags. Done here (not in commandFor) because the file
	// path is only known after the per-session hook generation above.
	if settingsFile != "" {
		args = append(args, "--settings", settingsFile)
	}

	// IMPORTANT: do NOT use exec.CommandContext with the *request* context
	// here. The HTTP handler that called Spawn returns within milliseconds
	// of pty.Start succeeding (after writing the 201 response). As soon as
	// the response body is flushed, net/http cancels the request context
	// (or the client disconnects), which exec.CommandContext interprets as
	// "kill the child". Claude is then SIGKILL'd within ~1ms — exactly the
	// "exit -1, no PTY output" symptom the user saw. The session lives for
	// as long as Kill() / Interrupt() / Wait() decide, NOT as long as the
	// HTTP request that created it.
	cmd := exec.Command(bin, args...)
	cmd.Dir = opts.Workdir
	cmd.Env = m.envFor(opts)
	sess.cmd = cmd
	_ = ctx // currently unused; kept in the signature for future cancel-on-shutdown wiring

	master, err := pty.Start(cmd)
	if err != nil {
		sess.SetState(stream.StateFailed)
		sess.closeBus()
		return nil, fmt.Errorf("spawn: pty.Start: %w", err)
	}
	sess.pty = master
	sess.StartedAt = time.Now().UTC()
	sess.SetState(stream.StateIdle)

	// Publish an initial meta frame so subscribers can see the model/workdir.
	_, _ = sess.bus.Publish(stream.KindMeta, stream.MetaData{
		Model:    opts.Model,
		Workdir:  opts.Workdir,
		AuthMode: opts.AuthMode,
		Note:     "session started",
	})

	if err := sess.writeMeta(); err != nil {
		// Non-fatal; log via cc.raw so it surfaces somewhere.
		_, _ = sess.bus.Publish(stream.KindMeta, stream.MetaData{Note: "meta write failed: " + err.Error()})
	}

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	// Goroutine: feed the PTY's output into the parser. Use RunRaw because
	// the spawned claude is an interactive REPL (no --print/stream-json),
	// so its stdout is ANSI/TUI bytes, not JSONL.
	go func() {
		_ = sess.parser.RunRaw(context.Background(), master)
	}()

	// Goroutine: wait for the process to exit; tear down.
	go m.reap(sess)

	return sess, nil
}

func (m *Manager) reap(s *Session) {
	err := s.cmd.Wait()
	exitCode := 0
	failed := false
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
			failed = exitCode != 0
		} else {
			failed = true
		}
	}

	s.mu.Lock()
	s.StoppedAt = time.Now().UTC()
	s.ExitCode = exitCode
	stopTranscript := s.transcriptStop
	s.transcriptStop = nil
	s.mu.Unlock()
	if stopTranscript != nil {
		stopTranscript()
	}

	reason := "exit"
	if failed {
		reason = fmt.Sprintf("exit %d", exitCode)
	}
	_, _ = s.bus.Publish(stream.KindStop, stream.StopData{Reason: reason})

	if failed {
		s.SetState(stream.StateFailed)
	} else {
		s.SetState(stream.StateStopped)
	}

	_ = s.writeMeta()
	s.closeBus()
	close(s.done)
}

// Get returns the session with the given id, if any.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List returns a snapshot of all sessions, sorted by creation time (oldest first).
func (m *Manager) List() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// commandFor builds the (binary, args) tuple for a new session.
func (m *Manager) commandFor(opts SpawnOptions) (string, []string, error) {
	if opts.OverrideArgs != nil {
		bin := opts.OverrideBin
		if bin == "" && len(opts.OverrideArgs) > 0 {
			bin = opts.OverrideArgs[0]
		}
		var args []string
		if len(opts.OverrideArgs) > 1 {
			args = opts.OverrideArgs[1:]
		}
		return bin, args, nil
	}

	bin := opts.OverrideBin
	if bin == "" {
		bin = m.ClaudeBin
	}
	if bin == "" {
		bin = "claude"
	}
	if filepath.IsAbs(bin) {
		if _, err := os.Stat(bin); err != nil {
			return "", nil, fmt.Errorf("commandFor: claude binary %q: %w", bin, err)
		}
	} else if _, err := exec.LookPath(bin); err != nil {
		return "", nil, fmt.Errorf("commandFor: claude binary %q not on PATH: %w", bin, err)
	}

	// IMPORTANT: --output-format / --include-hook-events / --include-partial-messages
	// are all `--print`-only per Claude Code's official help. Passing them to
	// interactive REPL mode makes claude refuse to start. We rely on hooks
	// (installed via --settings) for the structured event channel and the
	// raw PTY byte stream for the visible terminal view.
	var args []string
	if opts.BypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.ResumeFrom != "" {
		args = append(args, "--resume", opts.ResumeFrom)
	}
	return bin, args, nil
}

// envFor builds the child environment. It preserves the parent's env (so
// PATH/TERM/etc work) and layers per-session overrides on top.
func (m *Manager) envFor(opts SpawnOptions, additional ...string) []string {
	env := os.Environ()
	overrides := map[string]string{}
	// Decide which Anthropic auth env to inject. Per the Claude Code docs
	// (code.claude.com/docs/en/env-vars):
	//   ANTHROPIC_API_KEY   → X-Api-Key header — direct Anthropic console keys
	//   ANTHROPIC_AUTH_TOKEN → Authorization: Bearer <value> — gateways/proxies
	// When a third-party provider host is in ExtraEnv (ANTHROPIC_BASE_URL set),
	// we default to AUTH_TOKEN because most community gateways
	// (claude-code-router, OneAPI, etc.) speak Bearer auth.
	thirdParty := false
	for _, kv := range opts.ExtraEnv {
		if strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
			thirdParty = true
			break
		}
	}
	switch opts.AuthMode {
	case "api_key":
		if opts.APIKey != "" {
			if thirdParty {
				overrides["ANTHROPIC_AUTH_TOKEN"] = opts.APIKey
			} else {
				overrides["ANTHROPIC_API_KEY"] = opts.APIKey
			}
			delete(overrides, "CLAUDE_CODE_OAUTH_TOKEN")
		}
	case "subscription":
		if opts.OAuthToken != "" {
			overrides["CLAUDE_CODE_OAUTH_TOKEN"] = opts.OAuthToken
			delete(overrides, "ANTHROPIC_API_KEY")
			delete(overrides, "ANTHROPIC_AUTH_TOKEN")
		}
	}
	// Make CC quieter for our needs; the user can re-enable.
	if _, ok := overrides["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"]; !ok {
		overrides["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	}
	// Claude Code's interactive TUI (ink/yoga) needs a real TERM to
	// initialise — without one it bails out and the process exits before
	// showing anything, which our parser observes as a 3ms-then-failed
	// session. Set xterm-256color unless the caller already supplied one.
	if !hasEnvKV(env, "TERM=") && !hasEnvKV(opts.ExtraEnv, "TERM=") {
		overrides["TERM"] = "xterm-256color"
	}
	// COLUMNS / LINES help ink lay out its UI when no SIGWINCH has fired yet.
	if !hasEnvKV(env, "COLUMNS=") && !hasEnvKV(opts.ExtraEnv, "COLUMNS=") {
		overrides["COLUMNS"] = "120"
	}
	if !hasEnvKV(env, "LINES=") && !hasEnvKV(opts.ExtraEnv, "LINES=") {
		overrides["LINES"] = "32"
	}

	// Apply overrides + opts.ExtraEnv + manager-provided additions.
	env = applyEnv(env, overrides)
	allExtras := append([]string{}, opts.ExtraEnv...)
	allExtras = append(allExtras, additional...)
	if len(allExtras) > 0 {
		extra := map[string]string{}
		for _, kv := range allExtras {
			for i := 0; i < len(kv); i++ {
				if kv[i] == '=' {
					extra[kv[:i]] = kv[i+1:]
					break
				}
			}
		}
		env = applyEnv(env, extra)
	}
	return env
}

// hasEnvKV reports whether any entry in env starts with prefix (e.g. "TERM=").
func hasEnvKV(env []string, prefix string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// CheckHookToken implements hooks.Sink. Constant-time comparison.
func (m *Manager) CheckHookToken(sessionID, provided string) bool {
	s, ok := m.Get(sessionID)
	if !ok {
		return false
	}
	return hooks.ConstantTimeEqualString(s.hookToken, provided)
}

// EmitHookFrame implements hooks.Sink. Publishes a `hook` frame on the
// session's bus AND opportunistically translates well-known events into
// structured frames the Web UI's driver view already understands —
// text.delta, tool.use.start, tool.use.result, stop, status — so that
// the chat-style view has something to render even though interactive
// REPL claude does not emit stream-json itself.
func (m *Manager) EmitHookFrame(sessionID, event string, payload json.RawMessage) error {
	s, ok := m.Get(sessionID)
	if !ok {
		return hooks.ErrUnknownSession
	}
	bus := s.Bus()
	if _, err := bus.Publish(stream.KindHook, stream.HookData{
		Name:    event,
		Event:   event,
		Payload: payload,
	}); err != nil {
		return err
	}

	// Start the transcript watcher the first time we see a transcript_path
	// on any hook event — claude writes the live JSONL we want to tail.
	// Hooks fire before claude's transcript file finishes being created,
	// but the watcher waits for the file to appear.
	type transcriptCarrier struct {
		TranscriptPath string `json:"transcript_path"`
	}
	var tc transcriptCarrier
	if json.Unmarshal(payload, &tc) == nil && tc.TranscriptPath != "" {
		s.mu.Lock()
		if s.transcriptStop == nil {
			ctx, cancel := context.WithCancel(context.Background())
			w := cctranscript.New(tc.TranscriptPath, busAdapter{bus})
			w.Start(ctx)
			s.transcriptStop = cancel
		}
		s.mu.Unlock()
	}

	// Hook → frame translation is narrow: only status transitions. All
	// content frames (text.delta / tool.use.start / tool.use.result /
	// todo.update / usage) are emitted by the cctranscript watcher
	// reading claude's own JSONL — that path has full per-block fidelity
	// and avoids the duplicates we had when both paths spoke.
	switch event {
	case "UserPromptSubmit":
		s.SetState(stream.StateWorking)
		_, _ = bus.Publish(stream.KindStatus, stream.StatusData{State: stream.StateWorking})
	case "Stop", "SubagentStop":
		s.SetState(stream.StateIdle)
		_, _ = bus.Publish(stream.KindStatus, stream.StatusData{State: stream.StateIdle})
		// Interactive REPL never writes a `result` line to the transcript
		// JSONL (that's a --print-mode thing), so the cctranscript watcher
		// never emits a stop frame on its own. Synthesize one here from
		// the Stop hook so /send WaitForTurn and other end-of-turn
		// consumers can wake up. Reason is the bare hook name; full
		// duration/cost stats are not available from the hook payload.
		_, _ = bus.Publish(stream.KindStop, stream.StopData{Reason: "turn_end"})
	}

	return nil
}

// busAdapter satisfies cctranscript.Publisher around a *stream.Bus.
type busAdapter struct{ bus *stream.Bus }

func (a busAdapter) Publish(kind string, data any) (any, error) {
	f, err := a.bus.Publish(kind, data)
	return f, err
}

func applyEnv(env []string, overrides map[string]string) []string {
	seen := map[string]bool{}
	for i, kv := range env {
		for j := 0; j < len(kv); j++ {
			if kv[j] == '=' {
				key := kv[:j]
				if v, ok := overrides[key]; ok {
					env[i] = key + "=" + v
					seen[key] = true
				}
				break
			}
		}
	}
	for k, v := range overrides {
		if !seen[k] {
			env = append(env, k+"="+v)
		}
	}
	return env
}
