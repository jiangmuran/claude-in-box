package clauth

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stub(t *testing.T) (*Manager, func()) {
	t.Helper()
	// Resolve the stub script to an absolute path so PATH doesn't matter.
	abs, err := filepath.Abs("testdata/claude-stub.sh")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	stateDir := t.TempDir()
	t.Setenv("STUB_STATE_DIR", stateDir)
	return NewManager(abs), func() { /* TempDir is auto-cleaned by t */ }
}

func TestStatus_LoggedOutThenIn(t *testing.T) {
	m, cleanup := stub(t)
	defer cleanup()

	ctx := context.Background()
	s, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.LoggedIn {
		t.Fatalf("expected loggedIn=false initially, got %+v", s)
	}

	// Fake a logged-in state by completing a flow.
	f, err := m.Start(ctx, StartOptions{URLTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.Snapshot().State != StateAwaitingCode {
		t.Fatalf("state = %q, want awaiting_code", f.Snapshot().State)
	}
	if !strings.Contains(f.Snapshot().AuthURL, "claude.com/cai/oauth/authorize") {
		t.Fatalf("auth url not captured: %q", f.Snapshot().AuthURL)
	}

	if err := f.SubmitCode(ctx, "good-12345"); err != nil {
		t.Fatalf("SubmitCode: %v", err)
	}
	if f.Snapshot().State != StateDone {
		t.Fatalf("state after submit = %q, want done", f.Snapshot().State)
	}

	s, err = m.Status(ctx)
	if err != nil {
		t.Fatalf("Status after login: %v", err)
	}
	if !s.LoggedIn || s.Email != "test@example.com" || s.SubscriptionType != "max" {
		t.Fatalf("status post-login = %+v", s)
	}

	// Logout flips it back.
	if err := m.Logout(ctx); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	s, _ = m.Status(ctx)
	if s.LoggedIn {
		t.Fatalf("expected logged out, got %+v", s)
	}
}

func TestStart_BadCodeFails(t *testing.T) {
	m, _ := stub(t)
	ctx := context.Background()
	f, err := m.Start(ctx, StartOptions{URLTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	err = f.SubmitCode(ctx, "wrong")
	if err == nil {
		t.Fatal("expected SubmitCode failure for bad code")
	}
	if f.Snapshot().State != StateFailed {
		t.Fatalf("state = %q, want failed", f.Snapshot().State)
	}
}

func TestStart_RefusesSecondFlow(t *testing.T) {
	m, _ := stub(t)
	ctx := context.Background()
	f1, err := m.Start(ctx, StartOptions{URLTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	defer f1.Cancel()
	_, err = m.Start(ctx, StartOptions{URLTimeout: 1 * time.Second})
	if err != ErrAlreadyInFlight {
		t.Fatalf("err = %v, want ErrAlreadyInFlight", err)
	}
}

func TestStart_ResolvesActive(t *testing.T) {
	m, _ := stub(t)
	ctx := context.Background()
	f, err := m.Start(ctx, StartOptions{URLTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Cancel()

	if got := m.Active(); got == nil || got.ID != f.ID {
		t.Fatalf("Active() = %v", got)
	}
	if got := m.GetFlow(f.ID); got == nil || got.ID != f.ID {
		t.Fatalf("GetFlow() = %v", got)
	}
}

func TestStart_Cancel(t *testing.T) {
	m, _ := stub(t)
	ctx := context.Background()
	f, err := m.Start(ctx, StartOptions{URLTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Cancel()

	// Wait briefly for reap.
	deadline := time.After(2 * time.Second)
	for {
		if f.Snapshot().State == StateCancelled || f.Snapshot().State == StateFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("flow did not reach terminal state, current=%q", f.Snapshot().State)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if m.Active() != nil {
		t.Fatal("Active() should be nil after Cancel")
	}
}

func TestStart_IdleTimeoutFails(t *testing.T) {
	m, _ := stub(t)
	ctx := context.Background()
	f, err := m.Start(ctx, StartOptions{URLTimeout: 3 * time.Second, IdleTimeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		st := f.Snapshot().State
		if st == StateTimedOut {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("expected timed_out, got %q", st)
		case <-time.After(25 * time.Millisecond):
		}
	}
}
