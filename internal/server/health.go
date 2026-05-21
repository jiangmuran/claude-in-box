package server

import "net/http"

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.cfg.Version,
		"commit":  s.cfg.Commit,
		"mode":    s.cfg.Mode,
	})
}

func (s *Server) placeholderIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>claude-in-box</title>
<style>
  body{font-family:system-ui,-apple-system,'Segoe UI',sans-serif;max-width:42rem;margin:5rem auto;padding:0 1rem;color:#4a3a2e;background:#f5f0e8;line-height:1.55}
  h1{color:#b85a3d;font-weight:700;font-size:2.5rem;letter-spacing:-.02em;margin-bottom:.25em}
  .tag{color:#7a6452;margin-top:0}
  code{background:#eadbcd;padding:.1rem .35rem;border-radius:.25rem;font-size:.9em}
  a{color:#b85a3d}
</style>
<h1>claude-in-box</h1>
<p class="tag">Portable Claude Code dev environment with sessions, hooks, and a web API.</p>
<p>The control plane is up. The full Web UI lands in M2.</p>
<p>Health: <code>GET /api/health</code></p>
<p>Source: <a href="https://github.com/jiangmuran/claude-in-box">github.com/jiangmuran/claude-in-box</a></p>`))
}
