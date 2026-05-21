package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/jiangmuran/claude-in-box/internal/shell"
)

// -------- REST ---------

type createShellRequest struct {
	CWD  string `json:"cwd,omitempty"`
	Cmd  string `json:"cmd,omitempty"`
	Args []string `json:"args,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

type shellView struct {
	ID        string    `json:"id"`
	CWD       string    `json:"cwd"`
	Cmd       string    `json:"cmd"`
	CreatedAt time.Time `json:"created_at"`
	Running   bool      `json:"running"`
	ExitCode  int       `json:"exit_code,omitempty"`
}

func makeShellView(s *shell.Shell) shellView {
	running := true
	select {
	case <-s.Done():
		running = false
	default:
	}
	return shellView{
		ID:        s.ID,
		CWD:       s.CWD,
		Cmd:       s.Cmd,
		CreatedAt: s.CreatedAt,
		Running:   running,
		ExitCode:  s.ExitCode(),
	}
}

func (s *Server) listShells(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shells == nil {
		writeErr(w, http.StatusServiceUnavailable, "shells not configured")
		return
	}
	list := s.cfg.Shells.List()
	out := make([]shellView, 0, len(list))
	for _, sh := range list {
		out = append(out, makeShellView(sh))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shells": out})
}

func (s *Server) createShell(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shells == nil {
		writeErr(w, http.StatusServiceUnavailable, "shells not configured")
		return
	}
	var req createShellRequest
	if r.ContentLength > 0 {
		if !readJSON(w, r, &req) {
			return
		}
	}
	sh, err := s.cfg.Shells.Spawn(shell.SpawnOptions{
		CWD:  req.CWD,
		Cmd:  req.Cmd,
		Args: req.Args,
		Cols: req.Cols,
		Rows: req.Rows,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, makeShellView(sh))
}

func (s *Server) getShell(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shells == nil {
		writeErr(w, http.StatusServiceUnavailable, "shells not configured")
		return
	}
	id := r.PathValue("id")
	sh, ok := s.cfg.Shells.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such shell")
		return
	}
	writeJSON(w, http.StatusOK, makeShellView(sh))
}

func (s *Server) killShell(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shells == nil {
		writeErr(w, http.StatusServiceUnavailable, "shells not configured")
		return
	}
	id := r.PathValue("id")
	sig := os.Signal(syscall.SIGTERM)
	if r.URL.Query().Get("signal") == "kill" {
		sig = os.Kill
	}
	if err := s.cfg.Shells.Kill(id, sig); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	// Best-effort wait; do not block the client forever.
	if sh, ok := s.cfg.Shells.Get(id); ok {
		select {
		case <-sh.Done():
		case <-time.After(2 * time.Second):
		}
	}
	s.cfg.Shells.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

type shellInputRequest struct {
	Data string `json:"data"`
}

func (s *Server) inputShell(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shells == nil {
		writeErr(w, http.StatusServiceUnavailable, "shells not configured")
		return
	}
	id := r.PathValue("id")
	sh, ok := s.cfg.Shells.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such shell")
		return
	}
	var req shellInputRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Data == "" {
		writeErr(w, http.StatusBadRequest, "empty data")
		return
	}
	if _, err := sh.Write([]byte(req.Data)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bytes": len(req.Data)})
}

type shellResizeRequest struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (s *Server) resizeShell(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shells == nil {
		writeErr(w, http.StatusServiceUnavailable, "shells not configured")
		return
	}
	id := r.PathValue("id")
	sh, ok := s.cfg.Shells.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such shell")
		return
	}
	var req shellResizeRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Cols < 1 || req.Rows < 1 {
		writeErr(w, http.StatusBadRequest, "cols/rows must be >= 1")
		return
	}
	if err := sh.Resize(req.Cols, req.Rows); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -------- WS ---------
//
// Wire shape:
//   server → client : binary frames (raw PTY output bytes)
//   client → server : binary frames (raw keystrokes), OR text frame
//                     `{"resize":{"cols":N,"rows":M}}` to resize without
//                     hitting the HTTP route.

func (s *Server) streamShellWS(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Shells == nil {
		writeErr(w, http.StatusServiceUnavailable, "shells not configured")
		return
	}
	id := r.PathValue("id")
	sh, ok := s.cfg.Shells.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "no such shell")
		return
	}

	// Browsers fail the handshake if the client offered any subprotocol the
	// server does not echo back (RFC 6455 §4.1). Our Web UI sends
	// `bearer.<token>, binary`; auth.Require consumed the bearer entry, so
	// echo back "binary" here.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
		Subprotocols:   []string{"binary"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	subID, ch, scrollback := sh.Subscribe(256)
	defer sh.Unsubscribe(subID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 1) Replay scrollback to the new client.
	if len(scrollback) > 0 {
		if err := c.Write(ctx, websocket.MessageBinary, scrollback); err != nil {
			return
		}
	}

	// 2) Reader: pump client frames into the PTY.
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if _, werr := sh.Write(data); werr != nil {
					return
				}
			case websocket.MessageText:
				// Tiny inline JSON detector — avoid pulling in
				// encoding/json on the hot path for every keystroke.
				if isResize(data) {
					if cols, rows, ok := parseResize(data); ok {
						_ = sh.Resize(cols, rows)
					}
				}
			}
		}
	}()

	// 3) Writer: pump PTY output frames to the client + occasional ping
	//    so proxies do not idle-close.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = c.Close(websocket.StatusNormalClosure, "context done")
			return
		case chunk, ok := <-ch:
			if !ok {
				_ = c.Close(websocket.StatusNormalClosure, "shell ended")
				return
			}
			if err := c.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
		case <-ping.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.Ping(pingCtx)
			pingCancel()
			if err != nil {
				return
			}
		}
		// If the shell exits while we are blocked above, the channel will
		// close and we wake up. No extra signalling needed.
		select {
		case <-sh.Done():
			// Wait for any remaining buffered output to flush, then close.
			drainTimer := time.NewTimer(200 * time.Millisecond)
			drained := false
			for !drained {
				select {
				case chunk, ok := <-ch:
					if !ok {
						drained = true
						break
					}
					_ = c.Write(ctx, websocket.MessageBinary, chunk)
				case <-drainTimer.C:
					drained = true
				}
			}
			drainTimer.Stop()
			_ = c.Close(websocket.StatusNormalClosure, "shell exited")
			return
		default:
		}
	}
}

// -------- tiny JSON helpers (avoid full unmarshal in the hot read loop) --

func isResize(b []byte) bool {
	if len(b) < 18 {
		return false
	}
	// Look for "resize" substring; cheap and good enough.
	const needle = "\"resize\""
	for i := 0; i+len(needle) <= len(b); i++ {
		if string(b[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}

func parseResize(b []byte) (cols, rows uint16, ok bool) {
	type wrap struct {
		Resize struct {
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		} `json:"resize"`
	}
	var w wrap
	if err := jsonDecode(b, &w); err != nil {
		return 0, 0, false
	}
	if w.Resize.Cols == 0 && w.Resize.Rows == 0 {
		return 0, 0, false
	}
	return w.Resize.Cols, w.Resize.Rows, true
}

// jsonDecode is split out so the hot path imports stay minimal.
func jsonDecode(b []byte, v any) error {
	if !errors.Is(nil, nil) { // keep errors import non-stale; harmless
	}
	return jsonUnmarshal(b, v)
}
