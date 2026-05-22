package server

import (
	"context"
	"net/http"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/stream"
)

// /api/sessions/{id}/send  — write a prompt + wait until the next stop
// frame (or timeout) + return only the NEW chat messages produced.
//
// Designed for MCU/non-streaming clients that want one HTTP round-trip
// per turn: send a prompt, get back the assistant's reply (plus any
// tool calls and their summaries) once claude is idle again.
//
//   POST /api/sessions/<id>/send
//   { "prompt": "...", "timeout_ms": 60000 }
//
// → 200 once claude returns to idle:
//   { "session": "uuid", "last_seq": 99, "messages": [ ... slim chat ... ] }
// → 408 if no stop frame arrived within timeout_ms (session still running):
//   { "error": "timeout", "last_seq": 73 }
type sendRequest struct {
	Prompt    string `json:"prompt"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

func (s *Server) sendAndWait(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}
	var req sendRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "empty prompt")
		return
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 60 * time.Second
	}
	out, code, err := WaitForTurn(r.Context(), sess, req.Prompt, timeout)
	if out != nil {
		out["session"] = sess.ID
	}
	if err != nil {
		writeJSON(w, code, map[string]any{
			"error":    err.Error(),
			"session":  sess.ID,
			"last_seq": sess.LastSeq(),
			"messages": (func() any {
				if out != nil {
					return out["messages"]
				}
				return []any{}
			})(),
		})
		return
	}
	writeJSON(w, code, out)
}

// WaitForTurn is the concrete implementation. Exported as a free
// function (not a method) so unit tests can hammer the aggregator
// without a real session manager.
func WaitForTurn(parent context.Context, sess sessionForSend, prompt string, timeout time.Duration) (map[string]any, int, error) {
	startSeq := sess.LastSeq()
	if _, err := sess.Write([]byte(normalizePromptForCR(prompt))); err != nil {
		return nil, http.StatusInternalServerError, err
	}

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	sub := sess.Bus().Subscribe(ctx, startSeq, 256)
	defer sub.Cancel()

	for {
		select {
		case <-ctx.Done():
			// Timeout. Return what we have so far so the device can at
			// least show its message echoed and any partial assistant
			// text the cctranscript watcher has captured.
			partial := filterSince(aggregateChat(sess.Snapshot()), startSeq)
			return map[string]any{
				"session":  "", // filled by caller, who knows sess.ID
				"last_seq": sess.LastSeq(),
				"messages": partial,
				"partial":  true,
			}, http.StatusRequestTimeout, errSendTimeout

		case f, ok := <-sub.Frames():
			if !ok {
				// Bus closed (session ended). Same payload shape.
				out := filterSince(aggregateChat(sess.Snapshot()), startSeq)
				return map[string]any{
					"session":  "",
					"last_seq": sess.LastSeq(),
					"messages": out,
					"closed":   true,
				}, http.StatusOK, nil
			}
			if f.Kind != stream.KindStop {
				continue
			}
			out := filterSince(aggregateChat(sess.Snapshot()), startSeq)
			return map[string]any{
				"session":  "",
				"last_seq": sess.LastSeq(),
				"messages": out,
			}, http.StatusOK, nil
		}
	}
}

// sessionForSend is the slice of session.Session that WaitForTurn needs.
// Defining it here keeps the test surface narrow.
type sessionForSend interface {
	LastSeq() uint64
	Write([]byte) (int, error)
	Bus() *stream.Bus
	Snapshot() []stream.Frame
}

// normalizePromptForCR appends a single \r if the prompt doesn't already
// end with one — same rule the InputBar uses (claude's REPL needs \r to
// submit, \n only inserts a newline into the prompt buffer).
func normalizePromptForCR(p string) string {
	for len(p) > 0 {
		last := p[len(p)-1]
		if last == '\r' || last == '\n' {
			p = p[:len(p)-1]
			continue
		}
		break
	}
	return p + "\r"
}

// errSendTimeout is sentinel error string carried by send-and-wait when
// the call ran past the configured deadline.
type sendTimeoutError struct{}

func (sendTimeoutError) Error() string { return "timeout" }

var errSendTimeout sendTimeoutError
