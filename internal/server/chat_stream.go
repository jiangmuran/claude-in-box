package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

	// Snapshot first, then subscribe FROM the snapshot's seq high-water
	// mark — eliminates the replay-race where frames published between
	// the historical re-emit and the Subscribe call would be emitted
	// twice. Same resume model as /sse/sessions/{id}.
	historical := sess.Snapshot()
	var lastReplayed uint64
	for _, m := range filterSince(aggregateChat(historical), since) {
		if seq, ok := m["seq"].(uint64); ok && seq > lastReplayed {
			lastReplayed = seq
		}
		writeChatSSE(w, "chat", m)
	}
	flusher.Flush()

	// Seed the streaming aggregator with the historical state so the
	// next live text.delta on a still-open turn emits `event: update`
	// (continuation) rather than `event: chat` (new bubble), and so
	// tool-result frames can join their tool-use entry.
	agg := newChatAgg(func(kind string, m map[string]any) {
		writeChatSSE(w, kind, m)
		flusher.Flush()
	})
	agg.warmup(historical)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	subFrom := lastReplayed
	if subFrom == 0 {
		subFrom = since
	}
	sub := sess.Bus().Subscribe(ctx, subFrom, 256)
	defer sub.Cancel()

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

// newChatAgg constructs an agg with an empty tools map (so callers don't
// have to nil-check).
func newChatAgg(emit func(kind string, m map[string]any)) *chatAgg {
	return &chatAgg{emit: emit, tools: map[string]map[string]any{}}
}

// warmup replays a frame slice silently (no emit) so that a `?since=`
// resume picks up open turns / in-flight tool calls correctly. Same
// transitions step() does, but `emit` is suppressed.
func (a *chatAgg) warmup(frames []stream.Frame) {
	orig := a.emit
	a.emit = func(string, map[string]any) {} // swallow
	for _, f := range frames {
		a.step(f)
	}
	a.emit = orig
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
		// Close open bubbles; flush any tool entries that never received
		// their result so the device can render them as orphans rather
		// than leak them in our state.
		a.openUser = nil
		a.openAssistant = nil
		for id, entry := range a.tools {
			entry["summary"] = "orphan"
			entry["seq"] = uint64(f.Seq)
			a.emit("update", entry)
			delete(a.tools, id)
		}
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
