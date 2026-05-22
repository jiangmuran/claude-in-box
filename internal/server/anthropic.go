package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/session"
	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// /v1/messages — Anthropic Messages API compatibility layer.
//
// Spawns a per-request session under cib's auth context, writes the
// caller's `messages` history (rendered to plain text) into the PTY,
// waits for the stop frame, projects the answer back to Anthropic's
// response shape, then reaps the session.
//
// Designed so an unmodified `@anthropic-ai/sdk` or `anthropic` Python
// SDK can point its base URL at cib and get subscription-backed Claude.
//
// Non-stream only in this first cut. `stream: true` falls back to a
// single message_start → content_block_delta → message_stop SSE sweep
// after the full turn completes; true incremental streaming is a
// follow-up. See docs/API.md.

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`              // "user" | "assistant"
	Content json.RawMessage `json:"content,omitempty"` // string OR []content_block
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	System      json.RawMessage    `json:"system,omitempty"` // string OR []content_block
	Stream      bool               `json:"stream,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []anthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   string                  `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (s *Server) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	var req anthropicRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages: empty")
		return
	}
	if req.Stream {
		s.anthropicMessagesStream(w, r, &req)
		return
	}
	out, code, err := s.anthropicRunTurn(r, &req)
	if err != nil {
		writeJSON(w, code, map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": err.Error()},
		})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// anthropicRunTurn spawns a per-request session, writes the rendered
// prompt + \r, waits for stop (with the standard 400 ms drain), and
// projects the new chat messages into an Anthropic response. The
// session is killed after the turn — we don't (yet) keep them warm.
func (s *Server) anthropicRunTurn(r *http.Request, req *anthropicRequest) (*anthropicResponse, int, error) {
	prompt, err := renderMessagesAsPrompt(req.System, req.Messages)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	// Auth mode + provider come from prefs, not the request — the
	// /v1/messages caller is not expected to know how cib stores creds.
	authMode := ""
	if s.cfg.Prefs != nil {
		authMode = s.cfg.Prefs.Get().DefaultAuthMode
	}
	if authMode == "" {
		authMode = "subscription"
	}

	spawnOpts := session.SpawnOptions{
		Workdir:           "/workspace",
		Model:             req.Model,
		AuthMode:          authMode,
		BypassPermissions: true,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	sess, err := s.cfg.Sessions.Spawn(ctx, spawnOpts)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("spawn: %w", err)
	}
	// Reap on exit regardless of outcome. SIGTERM is what session.Manager
	// expects: reap() catches Wait() and tears down the bus.
	defer func() { _ = sess.Kill(syscall.SIGTERM) }()

	// Wait for claude to come up before writing the prompt. We watch for
	// the first `meta` frame the cctranscript watcher emits when it sees
	// the system.init line in the JSONL transcript — that's the earliest
	// reliable signal that claude is alive and ready for input. Without
	// this guard the spawn→write window races: claude is still rendering
	// the welcome screen when we write, the keystrokes get swallowed,
	// WaitForTurn then sits at its 2-minute cap waiting for a stop that
	// never comes.
	if err := waitForClaudeReady(ctx, sess, 30*time.Second); err != nil {
		return nil, http.StatusGatewayTimeout, fmt.Errorf("ready: %w", err)
	}

	out, _, runErr := WaitForTurn(ctx, sess, prompt, 2*time.Minute)
	if runErr != nil {
		return nil, http.StatusGatewayTimeout, runErr
	}
	msgs, _ := out["messages"].([]map[string]any)

	// Project to Anthropic shape — concatenate all assistant text blocks
	// produced this turn.
	var assistantText strings.Builder
	for _, m := range msgs {
		if m["role"] != "assistant" {
			continue
		}
		if t, ok := m["text"].(string); ok {
			assistantText.WriteString(t)
		}
	}

	model := req.Model
	if sess.Model != "" {
		model = sess.Model
	}

	resp := &anthropicResponse{
		ID:         "msg_" + randHex(12),
		Type:       "message",
		Role:       "assistant",
		Content:    []anthropicContentBlock{{Type: "text", Text: assistantText.String()}},
		Model:      model,
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:  approxTokens(prompt),
			OutputTokens: approxTokens(assistantText.String()),
		},
	}
	return resp, http.StatusOK, nil
}

// anthropicMessagesStream emits an SSE event stream that matches
// Anthropic's wire format closely enough for SDK consumers. We run the
// full turn synchronously, then emit a single content_block_delta with
// the whole text — incremental token-by-token streaming is on the
// roadmap (would require chunking text.delta frames live).
func (s *Server) anthropicMessagesStream(w http.ResponseWriter, r *http.Request, req *anthropicRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	resp, _, err := s.anthropicRunTurn(r, req)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", marshalJSON(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": err.Error()},
		}))
		flusher.Flush()
		return
	}
	emit := func(event string, data any) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, marshalJSON(data))
		flusher.Flush()
	}
	emit("message_start", map[string]any{
		"type":    "message_start",
		"message": map[string]any{"id": resp.ID, "type": "message", "role": "assistant", "model": resp.Model, "content": []any{}, "usage": resp.Usage},
	})
	emit("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	emit("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "text_delta", "text": resp.Content[0].Text},
	})
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": resp.StopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": resp.Usage.OutputTokens},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

// waitForClaudeReady blocks until either a `meta` frame appears on the
// session's bus (claude's transcript JSONL has been written, so the
// process is alive and the REPL is up) or `timeout` elapses.
func waitForClaudeReady(parent context.Context, sess sessionForSend, timeout time.Duration) error {
	if sess.LastSeq() > 0 {
		for _, f := range sess.Snapshot() {
			if f.Kind == stream.KindMeta {
				return nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	sub := sess.Bus().Subscribe(ctx, 0, 64)
	defer sub.Cancel()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for claude ready")
		case f, ok := <-sub.Frames():
			if !ok {
				return fmt.Errorf("session ended before ready")
			}
			if f.Kind == stream.KindMeta {
				return nil
			}
		}
	}
}

// renderMessagesAsPrompt flattens the Anthropic messages array (and
// optional system block) into a single string written to the PTY.
// Multi-turn history is rendered with `[user]:` / `[assistant]:`
// markers so claude sees the prior context — the alternative (replay
// via --resume) needs an existing transcript we don't have.
func renderMessagesAsPrompt(system json.RawMessage, msgs []anthropicMessage) (string, error) {
	var b strings.Builder
	if sysText := extractAnthropicText(system); sysText != "" {
		b.WriteString("[system]: ")
		b.WriteString(sysText)
		b.WriteString("\n\n")
	}
	for i, m := range msgs {
		text, err := extractMessageText(m.Content)
		if err != nil {
			return "", fmt.Errorf("messages[%d]: %w", i, err)
		}
		if i == len(msgs)-1 && m.Role == "user" {
			// Last user turn is the actual prompt; don't tag it.
			b.WriteString(text)
		} else {
			b.WriteByte('[')
			b.WriteString(m.Role)
			b.WriteString("]: ")
			b.WriteString(text)
			b.WriteString("\n\n")
		}
	}
	return b.String(), nil
}

func extractMessageText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	// content can be a bare string OR an array of content blocks.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range blocks {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), nil
}

func extractAnthropicText(raw json.RawMessage) string {
	s, _ := extractMessageText(raw)
	return s
}

// approxTokens is a rough whitespace-split count — good enough for the
// Usage field given we don't have a real tokenizer in-process and the
// claude transcript only carries token counts after the fact via its
// own usage events (which we don't surface here).
func approxTokens(s string) int {
	return len(strings.Fields(s))
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func marshalJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Forward-declared so we don't pull "context" into the imports above
// (it's already imported via session.SpawnOptions usage).
var _ = stream.KindStop
