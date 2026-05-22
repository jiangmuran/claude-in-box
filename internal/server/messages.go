package server

import (
	"encoding/json"
	"net/http"

	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// /api/sessions/{id}/messages returns the session's transcript already
// reduced to the high-level chat-shape downstream clients want — same
// model claude-code-webui's UnifiedMessageProcessor produces, just
// pre-aggregated server-side so callers don't have to walk the raw
// frame stream.
//
// Shape:
//   {
//     "session": "<uuid>",
//     "last_seq": 42,
//     "messages": [
//       { "type": "text",  "role": "user"|"assistant", "text": "..." },
//       { "type": "thinking", "text": "..." },
//       { "type": "tool",  "tool": "Bash", "input": {...}, "output": ..., "is_error": false, "tool_use_id": "..." },
//       { "type": "todo",  "items": [...] },
//       { "type": "askq",  "questions": [...] },
//       { "type": "stop",  "reason": "end_turn", "duration_ms": 1234, "total_cost_usd": 0 },
//       { "type": "usage", "input": N, "output": M, "cache_read": K }
//     ]
//   }
//
// Same code path as the Web UI's ChatView.aggregate — kept in one place
// so the wire format and the rendered UI stay synchronised.
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	all := sess.Snapshot()
	msgs := aggregateMessages(all)
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  sess.ID,
		"last_seq": sess.LastSeq(),
		"messages": msgs,
	})
}

// aggregateMessages collapses a frame slice into chat-shaped messages.
// Same rules as web/src/components/ChatView.svelte's `aggregate`.
func aggregateMessages(frames []stream.Frame) []map[string]any {
	out := make([]map[string]any, 0, len(frames))
	toolIdx := map[string]int{} // tool_use_id → index in out

	flushIfText := func(last *map[string]any) { _ = last }

	for _, f := range frames {
		switch f.Kind {
		case stream.KindTextDelta:
			var d struct {
				Text string `json:"text"`
				Role string `json:"role"`
			}
			_ = json.Unmarshal(f.Data, &d)
			role := d.Role
			if role == "" {
				role = "assistant"
			}
			// Merge into the previous text bubble if same role.
			if len(out) > 0 {
				last := out[len(out)-1]
				if last["type"] == "text" && last["role"] == role {
					last["text"] = last["text"].(string) + d.Text
					continue
				}
			}
			out = append(out, map[string]any{"type": "text", "role": role, "text": d.Text, "seq": f.Seq})
			flushIfText(nil)

		case "thinking":
			var d struct{ Text string `json:"text"` }
			_ = json.Unmarshal(f.Data, &d)
			out = append(out, map[string]any{"type": "thinking", "text": d.Text, "seq": f.Seq})

		case stream.KindToolUseStart:
			var d struct {
				Tool      string          `json:"tool"`
				Input     json.RawMessage `json:"input"`
				ToolUseID string          `json:"tool_use_id"`
			}
			_ = json.Unmarshal(f.Data, &d)
			// AskUserQuestion gets its own shape; the result is the user's
			// answer typed back through the input API.
			if d.Tool == "AskUserQuestion" {
				var inp struct {
					Questions []map[string]any `json:"questions"`
				}
				_ = json.Unmarshal(d.Input, &inp)
				out = append(out, map[string]any{"type": "askq", "questions": inp.Questions, "seq": f.Seq})
				if d.ToolUseID != "" {
					toolIdx[d.ToolUseID] = -1
				}
				continue
			}
			b := map[string]any{
				"type":        "tool",
				"tool":        d.Tool,
				"input":       d.Input,
				"tool_use_id": d.ToolUseID,
				"seq":         f.Seq,
			}
			out = append(out, b)
			if d.ToolUseID != "" {
				toolIdx[d.ToolUseID] = len(out) - 1
			}

		case stream.KindToolUseResult:
			var d struct {
				Tool       string          `json:"tool"`
				Output     json.RawMessage `json:"output"`
				IsError    bool            `json:"is_error"`
				DurationMs int64           `json:"duration_ms"`
				ToolUseID  string          `json:"tool_use_id"`
			}
			_ = json.Unmarshal(f.Data, &d)
			if d.ToolUseID != "" {
				if at, ok := toolIdx[d.ToolUseID]; ok {
					if at == -1 {
						continue // AskUserQuestion suppressed
					}
					out[at]["output"] = d.Output
					out[at]["is_error"] = d.IsError
					out[at]["duration_ms"] = d.DurationMs
					continue
				}
			}
			out = append(out, map[string]any{
				"type":        "tool",
				"tool":        d.Tool,
				"output":      d.Output,
				"is_error":    d.IsError,
				"duration_ms": d.DurationMs,
				"tool_use_id": d.ToolUseID,
				"seq":         f.Seq,
			})

		case stream.KindTodoUpdate:
			var d struct{ Items []map[string]any `json:"items"` }
			_ = json.Unmarshal(f.Data, &d)
			out = append(out, map[string]any{"type": "todo", "items": d.Items, "seq": f.Seq})

		case stream.KindUsage:
			var d struct {
				Input      int `json:"input"`
				Output     int `json:"output"`
				CacheRead  int `json:"cache_read"`
				CacheWrite int `json:"cache_write"`
			}
			_ = json.Unmarshal(f.Data, &d)
			out = append(out, map[string]any{"type": "usage", "input": d.Input, "output": d.Output, "cache_read": d.CacheRead, "cache_write": d.CacheWrite, "seq": f.Seq})

		case stream.KindStop:
			var d struct {
				Reason       string  `json:"reason"`
				DurationMs   int64   `json:"duration_ms"`
				TotalCostUSD float64 `json:"total_cost_usd"`
				IsError      bool    `json:"is_error"`
			}
			_ = json.Unmarshal(f.Data, &d)
			out = append(out, map[string]any{"type": "stop", "reason": d.Reason, "duration_ms": d.DurationMs, "total_cost_usd": d.TotalCostUSD, "is_error": d.IsError, "seq": f.Seq})

		case stream.KindMeta:
			var d map[string]any
			_ = json.Unmarshal(f.Data, &d)
			out = append(out, map[string]any{"type": "meta", "meta": d, "seq": f.Seq})
		}
	}
	return out
}
