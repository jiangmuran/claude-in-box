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
	// The first transcript line that carries a sessionId (last-prompt
	// or permission-mode) fires BEFORE claude has finished painting the
	// welcome banner and accepting keystrokes on the prompt line.
	// Without this settle, the write below races and the prompt is
	// swallowed. Empirical: 2s suffices on the test box; 3s is the
	// safety margin.
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return nil, http.StatusGatewayTimeout, fmt.Errorf("ready: ctx cancelled")
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

// anthropicMessagesStream runs a turn AND streams each text.delta frame
// as a `content_block_delta` SSE event the moment it lands — token-by-
// token incremental rendering, the way Anthropic's own API streams.
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

	emit := func(event string, data any) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, marshalJSON(data))
		flusher.Flush()
	}
	emitErr := func(msg string) {
		emit("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": msg},
		})
	}

	prompt, err := renderMessagesAsPrompt(req.System, req.Messages)
	if err != nil {
		emitErr(err.Error())
		return
	}
	authMode := ""
	if s.cfg.Prefs != nil {
		authMode = s.cfg.Prefs.Get().DefaultAuthMode
	}
	if authMode == "" {
		authMode = "subscription"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	sess, err := s.cfg.Sessions.Spawn(ctx, session.SpawnOptions{
		Workdir:           "/workspace",
		Model:             req.Model,
		AuthMode:          authMode,
		BypassPermissions: true,
	})
	if err != nil {
		emitErr(fmt.Sprintf("spawn: %v", err))
		return
	}
	defer func() { _ = sess.Kill(syscall.SIGTERM) }()

	if err := waitForClaudeReady(ctx, sess, 30*time.Second); err != nil {
		emitErr(fmt.Sprintf("ready: %v", err))
		return
	}
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		emitErr("ready: ctx cancelled")
		return
	}

	id := "msg_" + randHex(12)
	model := req.Model
	if sess.Model != "" {
		model = sess.Model
	}

	// Open Anthropic's preamble events.
	emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant",
			"model": model, "content": []any{},
			"usage": map[string]any{"input_tokens": approxTokens(prompt), "output_tokens": 0},
		},
	})
	emit("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})

	// Close-events helper. Once message_start has been emitted, SDK
	// iterators will hang forever if we don't also emit content_block_stop
	// + message_delta + message_stop — even on error paths.
	streamOpen := true
	closeStream := func(stopReason string) {
		if !streamOpen {
			return
		}
		streamOpen = false
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 0},
		})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}
	defer func() { closeStream("end_turn") }()

	// Subscribe BEFORE writing so we don't miss the first text.delta.
	startSeq := sess.LastSeq()
	sub := sess.Bus().Subscribe(ctx, startSeq, 512)
	defer sub.Cancel()
	if _, werr := sess.Write([]byte(normalizePromptForCR(prompt))); werr != nil {
		emitErr(fmt.Sprintf("write: %v", werr))
		closeStream("error")
		return
	}

	var assistantText strings.Builder
	turnDone := false
	for !turnDone {
		select {
		case <-ctx.Done():
			emitErr("turn timeout")
			closeStream("error")
			return
		case f, ok := <-sub.Frames():
			if !ok {
				turnDone = true
				break
			}
			switch f.Kind {
			case stream.KindTextDelta:
				var d struct {
					Role string `json:"role"`
					Text string `json:"text"`
				}
				if json.Unmarshal(f.Data, &d) != nil {
					continue
				}
				if d.Role != "assistant" || d.Text == "" {
					continue
				}
				assistantText.WriteString(d.Text)
				emit("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": d.Text},
				})
			case stream.KindStop:
				// 400ms drain so trailing text.delta (cctranscript lag) lands.
				drain := time.NewTimer(400 * time.Millisecond)
				for draining := true; draining; {
					select {
					case <-drain.C:
						draining = false
					case f2, ok2 := <-sub.Frames():
						if !ok2 {
							draining = false
							break
						}
						if f2.Kind == stream.KindTextDelta {
							var d struct {
								Role string `json:"role"`
								Text string `json:"text"`
							}
							if json.Unmarshal(f2.Data, &d) == nil && d.Role == "assistant" && d.Text != "" {
								assistantText.WriteString(d.Text)
								emit("content_block_delta", map[string]any{
									"type":  "content_block_delta",
									"index": 0,
									"delta": map[string]any{"type": "text_delta", "text": d.Text},
								})
							}
						}
					case <-ctx.Done():
						draining = false
					}
				}
				drain.Stop()
				turnDone = true
			}
		}
	}

	// Anthropic's close events. Output token count uses the actual
	// accumulated text so the SDK sees a reasonable usage value.
	streamOpen = false
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": approxTokens(assistantText.String())},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

// waitForClaudeReady blocks until claude's REPL is up — defined as
// "the cctranscript watcher has emitted a meta frame carrying a
// session_id field" (i.e. it saw the first JSONL line claude wrote).
//
// The plain Session.Spawn-emitted meta frame ("session started") is
// NOT enough on its own — it fires synchronously during Spawn() before
// claude has opened its PTY for input. We discriminate by looking for
// session_id (or model) in the meta payload, which only cctranscript
// populates.
func waitForClaudeReady(parent context.Context, sess sessionForSend, timeout time.Duration) error {
	isReadyMeta := func(f stream.Frame) bool {
		if f.Kind != stream.KindMeta {
			return false
		}
		var d map[string]any
		if json.Unmarshal(f.Data, &d) != nil {
			return false
		}
		if sid, _ := d["session_id"].(string); sid != "" {
			return true
		}
		if m, _ := d["model"].(string); m != "" {
			return true
		}
		return false
	}
	// Check what's already on the bus.
	lastSeen := uint64(0)
	for _, f := range sess.Snapshot() {
		if isReadyMeta(f) {
			return nil
		}
		if f.Seq > lastSeen {
			lastSeen = f.Seq
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	sub := sess.Bus().Subscribe(ctx, lastSeen, 64)
	defer sub.Cancel()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for claude ready")
		case f, ok := <-sub.Frames():
			if !ok {
				return fmt.Errorf("session ended before ready")
			}
			if isReadyMeta(f) {
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
