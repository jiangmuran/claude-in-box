// Package hooks installs Claude Code lifecycle hooks for each session and
// receives the resulting callbacks on a loopback HTTP route, turning them
// into hook frames on the session's event bus.
//
// Mechanism: at session spawn time we write a settings.json into a
// per-session $CLAUDE_CONFIG_DIR that registers `command`-type hooks. Each
// hook is a `curl` POST to `http://127.0.0.1:<port>/internal/hooks/<id>?event=<E>`
// with a per-session token in the X-CIB-Hook-Token header. The control plane's
// internal HTTP route (see Receiver) verifies the token, emits a `hook` frame
// on the session's bus, and (later) interprets the JSON response to mutate
// the event (block a tool, inject context, etc.).
package hooks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultEvents is the set of lifecycle events we register for. Unknown ones
// are simply ignored by Claude Code, so it is safe to over-register.
var DefaultEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"Notification",
	"Stop",
	"SubagentStop",
	"SessionEnd",
	"PreCompact",
}

// Settings is the minimal shape of a Claude Code settings.json we author.
// Contains only the `hooks` section — onboarding flags live in
// ~/.claude.json (global state) and are seeded by the entrypoint.
//
// The hooks shape Claude Code (v2.1.146+) expects is matcher-grouped, not
// a flat list:
//
//   { "hooks": { "PreToolUse": [
//       { "matcher": "Bash|Edit",
//         "hooks": [ {"type":"command","command":"…"} ] }
//   ]}}
//
// `matcher: ""` matches every event of that kind.
type Settings struct {
	Hooks map[string][]HookGroup `json:"hooks,omitempty"`
}

// HookGroup binds a matcher pattern to a list of HookEntry to run when it
// fires. Empty matcher = match-all.
type HookGroup struct {
	Matcher string      `json:"matcher"`
	Hooks   []HookEntry `json:"hooks"`
}

// HookEntry is one hook registration under a single event.
type HookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command,omitempty"`
}

// NewToken returns a fresh 32-byte random token, hex-encoded (64 chars).
// Used as the per-session shared secret in the X-CIB-Hook-Token header.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// WriteSessionSettings writes the per-session settings.json under configDir
// (the directory we point CLAUDE_CONFIG_DIR at). It returns the absolute path
// of the written file.
//
// `ctlAddr` is the host:port the hook scripts curl to. Inside the container
// this is loopback (127.0.0.1:<port>), regardless of the public listen
// address.
func WriteSessionSettings(configDir, sessionID, token, ctlAddr string, events []string) (string, error) {
	if events == nil {
		events = DefaultEvents
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", fmt.Errorf("hooks: mkdir configDir: %w", err)
	}

	settings := Settings{Hooks: make(map[string][]HookGroup, len(events))}
	for _, event := range events {
		// Single-quoted shell args: the token is hex, sessionID is uuid, and
		// event is from our whitelist, so none of them contain shell
		// metacharacters. ctlAddr is host:port. URL-encode is unnecessary at
		// these character sets.
		cmd := fmt.Sprintf(
			`curl --silent --show-error --max-time 10 `+
				`--data-binary @- `+
				`-H 'Content-Type: application/json' `+
				`-H 'X-CIB-Hook-Token: %s' `+
				`'http://%s/internal/hooks/%s?event=%s'`,
			token, ctlAddr, sessionID, event,
		)
		settings.Hooks[event] = []HookGroup{{
			Matcher: "",
			Hooks:   []HookEntry{{Type: "command", Command: cmd}},
		}}
	}

	out := filepath.Join(configDir, "settings.json")
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		return "", fmt.Errorf("hooks: write %s: %w", out, err)
	}
	return out, nil
}
