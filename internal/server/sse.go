package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// streamSSE pushes every frame for a session as a Server-Sent Event. Clients
// reconnect with ?from=<last_seq> to resume.
func (s *Server) streamSSE(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// Disable nginx buffering when fronted by it.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var fromSeq uint64
	if q := r.URL.Query().Get("from"); q != "" {
		if n, err := strconv.ParseUint(q, 10, 64); err == nil {
			fromSeq = n
		}
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sub := sess.Subscribe(ctx, fromSeq)
	defer sub.Cancel()

	// Initial comment so the client's onopen fires immediately.
	_, _ = fmt.Fprintf(w, ":ok\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ":keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case frame, ok := <-sub.Frames():
			if !ok {
				_, _ = fmt.Fprintf(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			b, err := jsonEncode(frame)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", frame.Seq, frame.Kind, b); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
