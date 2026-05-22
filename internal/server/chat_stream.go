package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// /sse/sessions/{id}/chat[?since=N]
//
// Streams the slim chat shape (same as /api/sessions/{id}/chat) over
// Server-Sent Events. One message becomes available as the underlying
// frames roll in:
//
//   - assistant text turns are emitted on the FIRST frame the role
//     appears and then updated with `event: update` each time the
//     turn's accumulated text grows (so an MCU client can decide to
//     either re-render or just keep the latest);
//   - user text is one-shot;
//   - tool calls are emitted on tool.use.start with summary="running"
//     and re-emitted with `event: update` carrying the result summary
//     once tool.use.result lands.
//
// The wire shape per event:
//
//   event: chat
//   data:  {"seq":12,"role":"user","text":"hi"}
//
//   event: update
//   data:  {"seq":18,"role":"tool","tool":"Bash","summary":"ok · 17ms"}
//
//   event: stop
//   data:  {"reason":"end_turn"}
//
//   :heartbeat       (every 25 s; SSE comment line so the connection
//                    survives idle proxies without producing visible
//                    chat traffic)
func (s *Server) chatStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	since := parseSinceQuery(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // tell nginx not to buffer
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Re-emit any historical messages newer than `since` so a reconnecting
	// MCU client doesn't lose the gap.
	for _, m := range filterSince(aggregateChat(sess.Snapshot()), since) {
		writeChatSSE(w, "chat", m)
	}
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub := sess.Bus().Subscribe(ctx, sess.LastSeq(), 256)
	defer sub.Cancel()

	// Per-stream running aggregator state.
	agg := &chatAgg{
		emit: func(kind string, m map[string]any) {
			writeChatSSE(w, kind, m)
			flusher.Flush()
		},
	}

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ":heartbeat\n\n")
			flusher.Flush()
		case f, ok := <-sub.Frames():
			if !ok {
				_, _ = fmt.Fprint(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			agg.step(f)
		}
	}
}

func writeChatSSE(w http.ResponseWriter, event string, m map[string]any) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "id: %v\nevent: %s\ndata: %s\n\n", m["seq"], event, b)
}

// chatAgg is the streaming counterpart of aggregateChat — same rules,
// but emits incrementally as frames arrive.
type chatAgg struct {
	emit func(kind string, m map[string]any)

	// Last text bubble per role we're appending to. nil = no open bubble.
	openUser      map[string]any
	openAssistant map[string]any

	// Tool entries indexed by tool_use_id awaiting result.
	tools map[string]map[string]any
}

func (a *chatAgg) step(f stream.Frame) {
	switch f.Kind {
	case stream.KindTextDelta:
		var d struct {
			Text string `json:"text"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(f.Data, &d)
		if d.Text == "" {
			return
		}
		role := d.Role
		if role == "" {
			role = "assistant"
		}
		open := &a.openAssistant
		if role == "user" {
			open = &a.openUser
		}
		if *open != nil {
			(*open)["text"] = (*open)["text"].(string) + d.Text
			(*open)["seq"] = uint64(f.Seq)
			a.emit("update", *open)
			return
		}
		// Role switch / first message of role: close the other role,
		// open a new bubble.
		if role == "assistant" {
			a.openUser = nil
		} else {
			a.openAssistant = nil
		}
		m := map[string]any{
			"seq":  uint64(f.Seq),
			"role": role,
			"text": d.Text,
		}
		*open = m
		a.emit("chat", m)

	case stream.KindToolUseStart:
		// Any open text turn is done — flush local state so the next
		// text frame opens a fresh bubble.
		a.openUser = nil
		a.openAssistant = nil
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
		if a.tools == nil {
			a.tools = map[string]map[string]any{}
		}
		if d.ToolUseID != "" {
			a.tools[d.ToolUseID] = entry
		}
		a.emit("chat", entry)

	case stream.KindToolUseResult:
		var d struct {
			ToolUseID  string `json:"tool_use_id"`
			IsError    bool   `json:"is_error"`
			DurationMs int64  `json:"duration_ms"`
		}
		_ = json.Unmarshal(f.Data, &d)
		entry, ok := a.tools[d.ToolUseID]
		if !ok {
			return
		}
		sum := "ok"
		if d.IsError {
			sum = "error"
		}
		if d.DurationMs > 0 {
			sum += fmt.Sprintf(" · %dms", d.DurationMs)
		}
		entry["summary"] = sum
		entry["seq"] = uint64(f.Seq)
		a.emit("update", entry)
		delete(a.tools, d.ToolUseID)

	case stream.KindStop:
		// Close open bubbles; emit a stop event so MCU knows the turn is done.
		a.openUser = nil
		a.openAssistant = nil
		var d struct {
			Reason     string `json:"reason"`
			DurationMs int64  `json:"duration_ms"`
		}
		_ = json.Unmarshal(f.Data, &d)
		a.emit("stop", map[string]any{
			"seq":         uint64(f.Seq),
			"reason":      d.Reason,
			"duration_ms": d.DurationMs,
		})
	}
}

// Ensure parseSinceQuery / strconv stays referenced when these become
// the only consumers in this file.
var _ = strconv.ParseUint
