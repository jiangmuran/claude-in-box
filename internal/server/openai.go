package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
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

	// Translate to Anthropic shape, then run through the existing pipeline.
	antReq, sys := openAIToAnthropic(&req)
	antReq.Stream = false // we'll wrap the stream ourselves

	out, code, err := s.anthropicRunTurn(r, antReq)
	if err != nil {
		writeJSON(w, code, map[string]any{
			"error": map[string]any{"type": "api_error", "message": err.Error()},
		})
		return
	}
	_ = sys

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

	if req.Stream {
		s.openaiChatStream(w, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// openaiChatStream emits the full reply as one OpenAI-shaped SSE chunk
// plus a [DONE] terminator. Incremental token-by-token streaming is the
// same follow-up as Anthropic's.
func (s *Server) openaiChatStream(w http.ResponseWriter, resp *openAIChatResponse) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	emit := func(v any) {
		fmt.Fprintf(w, "data: %s\n\n", marshalJSON(v))
		flusher.Flush()
	}
	// Role chunk
	emit(map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion.chunk",
		"created": resp.Created,
		"model":   resp.Model,
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"role": "assistant"},
		}},
	})
	// Content chunk
	emit(map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion.chunk",
		"created": resp.Created,
		"model":   resp.Model,
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"content": resp.Choices[0].Message.Content},
		}},
	})
	// Finish chunk
	emit(map[string]any{
		"id":      resp.ID,
		"object":  "chat.completion.chunk",
		"created": resp.Created,
		"model":   resp.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
	})
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
