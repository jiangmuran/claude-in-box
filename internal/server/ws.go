package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func (s *Server) streamWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.cfg.Sessions.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such session")
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// We let the client suggest a content-type subprotocol like "json"
		// or "msgpack" alongside bearer.<token>; the bearer entry was
		// already consumed by auth.Require. The server MUST echo at least
		// one of the client's offered subprotocols (RFC 6455 §4.1) or
		// browsers fail the handshake — list every non-secret label the UI
		// might offer.
		OriginPatterns: []string{"*"},
		Subprotocols:   []string{"json"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

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

	// Periodic pings keep proxies from idle-closing the connection.
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// One goroutine reads inbound messages (and discards them, for now —
	// the canonical input path is POST /api/sessions/:id/input). Bidirectional
	// input over the WS will land in a later milestone if needed.
	go func() {
		defer cancel()
		for {
			_, _, err := c.Read(ctx)
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = c.Close(websocket.StatusNormalClosure, "context done")
			return
		case <-pingTicker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pingCtx)
			pingCancel()
			if err != nil {
				return
			}
		case frame, ok := <-sub.Frames():
			if !ok {
				_ = c.Close(websocket.StatusNormalClosure, "session ended")
				return
			}
			if err := wsjson.Write(ctx, c, frame); err != nil {
				return
			}
		}
	}
}

// jsonEncode is a tiny helper for the rare manual encode case.
var jsonEncode = func(v any) ([]byte, error) { return json.Marshal(v) }
