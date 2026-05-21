// Command cib is the claude-in-box control plane.
//
// M1.1 scaffold: brings up an HTTP server on the configured address, requires
// CIB_AUTH_TOKEN to be set (unless CIB_AUTH_DISABLED=1), exposes /api/health,
// and serves a placeholder index on / when CIB_MODE is unset (the default).
// The session manager, transports, hooks runtime, and Web UI bundle land in
// subsequent M1 sub-tasks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const defaultAddr = ":8080"

// Stamped at build time via -ldflags="-X main.version=... -X main.commit=...".
var (
	version = "0.0.1-dev"
	commit  = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	addr := flag.String("addr", defaultAddr, "listen address")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cib %s (%s)\n", version, commit)
		return
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if err := run(*addr); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	authToken := os.Getenv("CIB_AUTH_TOKEN")
	authDisabled := os.Getenv("CIB_AUTH_DISABLED") == "1"
	if authToken == "" && !authDisabled {
		return errors.New("CIB_AUTH_TOKEN is required (set CIB_AUTH_DISABLED=1 to override for local dev)")
	}

	mode := os.Getenv("CIB_MODE")
	if mode == "" {
		mode = "default"
	}
	switch mode {
	case "default", "headless":
	default:
		return fmt.Errorf("unknown CIB_MODE %q (want \"\" / \"default\" / \"headless\")", mode)
	}

	slog.Info("starting",
		"version", version,
		"commit", commit,
		"addr", addr,
		"mode", mode,
		"auth_disabled", authDisabled,
	)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":%q,"commit":%q,"mode":%q}`, version, commit, mode)
	})

	if mode == "default" {
		mux.HandleFunc("GET /{$}", placeholderIndex)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           withLogging(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		slog.Info("shutdown signal received")
		shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shCtx)
	}
}

func placeholderIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>claude-in-box</title>
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
<p>Source: <a href="https://github.com/jiangmuran/claude-in-box">github.com/jiangmuran/claude-in-box</a></p>`)
}

func withLogging(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/health") {
			return
		}
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(t).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
