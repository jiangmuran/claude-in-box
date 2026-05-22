package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

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
	since := parseSinceQuery(r)
	all := sess.Snapshot()
	msgs := aggregateMessages(all)
	if since > 0 {
		msgs = filterSince(msgs, since)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  sess.ID,
		"last_seq": sess.LastSeq(),
		"messages": msgs,
	})
}

// /api/sessions/{id}/chat — embedded-friendly slim chat list. Same data
// as /messages but trimmed to fields tiny devices (a few hundred KB RAM,
// HTTP/1.1 only) can fit on the heap:
//
//   {
//     "session":  "<uuid>",
//     "last_seq": 42,
//     "messages": [
//       { "seq": 12, "role": "user",      "text": "hi" },
//       { "seq": 18, "role": "tool",      "tool": "Bash", "summary": "ls completed" },
//       { "seq": 24, "role": "assistant", "text": "hello" }
//     ]
//   }
//
// Tool input/output bodies, todos, thinking blocks, meta and usage are
// all dropped. Use /messages for the full shape, /transcript for the raw
// frame stream. Supports `?since=<seq>` for incremental polling so a
// device only re-downloads what it hasn't seen.
func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	since := parseSinceQuery(r)
	all := sess.Snapshot()
	msgs := aggregateChat(all)
	if since > 0 {
		msgs = filterSince(msgs, since)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  sess.ID,
		"last_seq": sess.LastSeq(),
		"messages": msgs,
	})
}

func parseSinceQuery(r *http.Request) uint64 {
	q := r.URL.Query().Get("since")
	if q == "" {
		return 0
	}
	n, err := strconv.ParseUint(q, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// filterSince returns a freshly-allocated slice of messages whose `seq`
// field is strictly greater than `since`. Never reuses the input
// backing array — callers can keep `msgs` after this returns.
func filterSince(msgs []map[string]any, since uint64) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		seq, _ := m["seq"].(uint64)
		if seq == 0 {
			if n, ok := m["seq"].(float64); ok {
				seq = uint64(n)
			} else if n, ok := m["seq"].(int); ok {
				seq = uint64(n)
			}
		}
		if seq > since {
			out = append(out, m)
		}
	}
	return out
}

// aggregateChat returns the slim chat-only projection used by
// /api/sessions/{id}/chat. It keeps:
//   - user text turns
//   - assistant text turns (joined across same-role deltas)
//   - tool calls as a single line: {role:tool, tool, summary}
//     where summary is a short success/fail/duration tag derived
//     from tool.use.result without including the raw output payload
// Drops thinking, todo, meta, usage, stop — embedded devices typically
// don't need them and they balloon the payload.
func aggregateChat(frames []stream.Frame) []map[string]any {
	out := make([]map[string]any, 0, len(frames)/2)
	tools := map[string]map[string]any{} // tool_use_id → in-progress tool entry

	for _, f := range frames {
		switch f.Kind {
		case stream.KindTextDelta:
			var d struct {
				Text string `json:"text"`
				Role string `json:"role"`
			}
			_ = json.Unmarshal(f.Data, &d)
			if d.Text == "" {
				continue
			}
			role := d.Role
			if role == "" {
				role = "assistant"
			}
			if len(out) > 0 {
				last := out[len(out)-1]
				if last["role"] == role {
					last["text"] = last["text"].(string) + d.Text
					last["seq"] = uint64(f.Seq) // bump to most recent
					continue
				}
			}
			out = append(out, map[string]any{
				"seq":  uint64(f.Seq),
				"role": role,
				"text": d.Text,
			})

		case stream.KindToolUseStart:
			var d struct {
				Tool      string `json:"tool"`
				ToolUseID string `json:"tool_use_id"`
			}
			_ = json.Unmarshal(f.Data, &d)
			entry := map[string]any{
				"seq":     uint64(f.Seq),
				"role":    "tool",
				"tool":    d.Tool,
				"summary": "running",
			}
			out = append(out, entry)
			if d.ToolUseID != "" {
				tools[d.ToolUseID] = entry
			}

		case stream.KindToolUseResult:
			var d struct {
				ToolUseID  string `json:"tool_use_id"`
				IsError    bool   `json:"is_error"`
				DurationMs int64  `json:"duration_ms"`
			}
			_ = json.Unmarshal(f.Data, &d)
			if entry, ok := tools[d.ToolUseID]; ok {
				sum := "ok"
				if d.IsError {
					sum = "error"
				}
				if d.DurationMs > 0 {
					sum += fmt.Sprintf(" · %dms", d.DurationMs)
				}
				entry["summary"] = sum
				entry["seq"] = uint64(f.Seq)
				delete(tools, d.ToolUseID)
			}
		}
	}
	return out
}

// aggregateMessages collapses a frame slice into chat-shaped messages.
// Same rules as web/src/components/ChatView.svelte's `aggregate`.
func aggregateMessages(frames []stream.Frame) []map[string]any {
	out := make([]map[string]any, 0, len(frames))
	toolIdx := map[string]int{} // tool_use_id → index in out

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
			out = append(out, map[string]any{"type": "text", "role": role, "text": d.Text, "seq": uint64(f.Seq)})

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
