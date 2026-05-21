// Command cib is the claude-in-box control plane.
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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jiangmuran/claude-in-box/internal/auth"
	"github.com/jiangmuran/claude-in-box/internal/clauth"
	"github.com/jiangmuran/claude-in-box/internal/server"
	"github.com/jiangmuran/claude-in-box/internal/session"
)

const (
	defaultAddr    = ":8080"
	defaultDataDir = "/var/lib/claude-in-box"
)

// Stamped at build time via -ldflags="-X main.version=... -X main.commit=...".
var (
	version = "0.0.1-dev"
	commit  = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	addr := flag.String("addr", defaultAddr, "listen address")
	dataDir := flag.String("data-dir", defaultDataDir, "directory for tokens.json, sessions/, etc.")
	claudeBin := flag.String("claude-bin", "", "absolute path to the claude binary (default: PATH lookup)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cib %s (%s)\n", version, commit)
		return
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if err := run(*addr, *dataDir, *claudeBin); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, dataDir, claudeBin string) error {
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

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("mkdir data-dir: %w", err)
	}

	tokenStore, err := auth.NewFileStore(filepath.Join(dataDir, "tokens.json"))
	if err != nil {
		return fmt.Errorf("init token store: %w", err)
	}
	if authToken != "" {
		if err := tokenStore.SetMaster(authToken); err != nil {
			return fmt.Errorf("register master token: %w", err)
		}
	}

	sessionMgr, err := session.NewManager(filepath.Join(dataDir, "sessions"), claudeBin)
	if err != nil {
		return fmt.Errorf("init session manager: %w", err)
	}
	sessionMgr.ControlAddr = loopbackAddr(addr)

	claudeAuth := clauth.NewManager(claudeBin)

	srv := server.New(server.Config{
		Mode:       mode,
		Sessions:   sessionMgr,
		Tokens:     tokenStore,
		ClaudeAuth: claudeAuth,
		Version:    version,
		Commit:     commit,
	})

	slog.Info("starting",
		"version", version,
		"commit", commit,
		"addr", addr,
		"data_dir", dataDir,
		"mode", mode,
		"auth_disabled", authDisabled,
		"hook_loopback", sessionMgr.ControlAddr,
	)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()

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
		return httpSrv.Shutdown(shCtx)
	}
}

// loopbackAddr converts the listen address into the host:port form a process
// inside the same container should use to call us back over loopback. ":8080"
// or "0.0.0.0:8080" → "127.0.0.1:8080"; otherwise unchanged.
func loopbackAddr(addr string) string {
	switch {
	case strings.HasPrefix(addr, ":"):
		return "127.0.0.1" + addr
	case strings.HasPrefix(addr, "0.0.0.0:"):
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	case strings.HasPrefix(addr, "[::]:"):
		return "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	default:
		return addr
	}
}
