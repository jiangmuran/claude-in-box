package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/session"
	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// /openai/v1/chat/completions — OpenAI Chat Completions compatibility.
//
// Same idea as /v1/messages but speaks OpenAI's wire shape. Lets an
// unmodified `openai` Python SDK or `openai-node` client point at cib
// and get subscription-backed Claude.
//
// Under the hood: we translate the OpenAI body to an Anthropic body,
// run anthropicRunTurn (which spawns a session, waits for ready, fires
// the prompt, waits for stop), then project the assistant text back
// into OpenAI's response shape.

type openAIMessage struct {
	Role    string          `json:"role"`           // "user" | "assistant" | "system"
	Content json.RawMessage `json:"content,omitempty"` // string or array of content_parts
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	// Tools / function_call are not yet plumbed through; the prompt
	// currently sees them as opaque JSON.
}

type openAIChatChoice struct {
	Index        int             `json:"index"`
	Message      openAIChoiceMsg `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type openAIChoiceMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []openAIChatChoice `json:"choices"`
	Usage   openAIUsage        `json:"usage"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (s *Server) openaiChat(w http.ResponseWriter, r *http.Request) {
	var req openAIChatRequest
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "invalid_request_error", "message": "messages: empty"},
		})
		return
	}

	antReq, _ := openAIToAnthropic(&req)
	if req.Stream {
		s.openaiChatStream(w, r, antReq)
		return
	}
	antReq.Stream = false

	out, code, err := s.anthropicRunTurn(r, antReq)
	if err != nil {
		writeJSON(w, code, map[string]any{
			"error": map[string]any{"type": "api_error", "message": err.Error()},
		})
		return
	}
	text := ""
	if len(out.Content) > 0 {
		text = out.Content[0].Text
	}
	resp := &openAIChatResponse{
		ID:      "chatcmpl-" + randHex(12),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   out.Model,
		Choices: []openAIChatChoice{{
			Index:        0,
			Message:      openAIChoiceMsg{Role: "assistant", Content: text},
			FinishReason: "stop",
		}},
		Usage: openAIUsage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
			TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// openaiChatStream runs a session turn and emits one
// chat.completion.chunk per assistant text.delta the moment it lands —
// token-by-block incremental streaming so SDK iterators yield as data
// arrives. Closes with `data: [DONE]\n\n`.
func (s *Server) openaiChatStream(w http.ResponseWriter, r *http.Request, antReq *anthropicRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	id := "chatcmpl-" + randHex(12)
	created := time.Now().Unix()

	emit := func(v any) {
		fmt.Fprintf(w, "data: %s\n\n", marshalJSON(v))
		flusher.Flush()
	}
	emitErr := func(msg string) {
		emit(map[string]any{
			"error": map[string]any{"type": "api_error", "message": msg},
		})
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != nil {
			choice["finish_reason"] = finish
		}
		return map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   antReq.Model,
			"choices": []any{choice},
		}
	}

	prompt, err := renderMessagesAsPrompt(antReq.System, antReq.Messages)
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
		Model:             antReq.Model,
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

	// Open with the role chunk OpenAI clients expect.
	emit(chunk(map[string]any{"role": "assistant"}, nil))

	// Close-events helper. Once the role chunk has been emitted, OpenAI
	// SDK iterators will hang forever if we don't also emit a finish
	// chunk + [DONE] terminator on every exit path, including errors.
	streamOpen := true
	closeStream := func(finishReason string) {
		if !streamOpen {
			return
		}
		streamOpen = false
		emit(chunk(map[string]any{}, finishReason))
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
	defer func() { closeStream("stop") }()

	startSeq := sess.LastSeq()
	sub := sess.Bus().Subscribe(ctx, startSeq, 512)
	defer sub.Cancel()
	if _, werr := sess.Write([]byte(normalizePromptForCR(prompt))); werr != nil {
		emitErr(fmt.Sprintf("write: %v", werr))
		closeStream("error")
		return
	}

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
				if json.Unmarshal(f.Data, &d) != nil || d.Role != "assistant" || d.Text == "" {
					continue
				}
				emit(chunk(map[string]any{"content": d.Text}, nil))
			case stream.KindStop:
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
								emit(chunk(map[string]any{"content": d.Text}, nil))
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

	// Normal-path close — the deferred closeStream("stop") would do this
	// too but emitting here keeps the happy-path in the source.
	streamOpen = false
	emit(chunk(map[string]any{}, "stop"))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// openAIToAnthropic projects an OpenAI Chat Completions request to an
// anthropicRequest. system-role messages get folded into the Anthropic
// `system` field (concatenated with newlines so multiple system msgs
// stack cleanly). Returns the request + extracted system text for
// debug/log purposes.
func openAIToAnthropic(req *openAIChatRequest) (*anthropicRequest, string) {
	var sys strings.Builder
	conv := make([]anthropicMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		text, _ := extractMessageText(m.Content)
		if m.Role == "system" {
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(text)
			continue
		}
		// Re-marshal content as a string so anthropicRequest's
		// extractMessageText sees a clean string variant.
		raw, _ := json.Marshal(text)
		conv = append(conv, anthropicMessage{Role: m.Role, Content: raw})
	}
	out := &anthropicRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Messages:    conv,
		Temperature: req.Temperature,
	}
	if sys.Len() > 0 {
		out.System, _ = json.Marshal(sys.String())
	}
	return out, sys.String()
}
