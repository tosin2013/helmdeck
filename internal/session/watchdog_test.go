package session_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/session/fake"
)

func TestWatchdogTerminatesExpired(t *testing.T) {
	rt := fake.New()
	rt.Inject(&session.Session{
		ID:        "expired",
		Status:    session.StatusRunning,
		CreatedAt: time.Now().Add(-10 * time.Minute),
		Spec:      session.Spec{Timeout: 5 * time.Minute},
	})
	rt.Inject(&session.Session{
		ID:        "fresh",
		Status:    session.StatusRunning,
		CreatedAt: time.Now(),
		Spec:      session.Spec{Timeout: 5 * time.Minute},
	})

	wd := session.NewWatchdog(rt, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Drive a single tick directly via Run+cancel; the public API is Run.
	go wd.Run(ctx)
	// Give the ticker one cycle.
	time.Sleep(1500 * time.Millisecond)
	cancel()

	if _, err := rt.Get(context.Background(), "expired"); err == nil {
		t.Fatal("expected expired session to be terminated")
	}
	if _, err := rt.Get(context.Background(), "fresh"); err != nil {
		t.Fatalf("fresh session should still exist: %v", err)
	}
}

// TestWatchdogRespectsExtendedTimeout is the regression guard for the
// shared-session pipeline bug — repo.fetch creates a session with a
// 5-minute spec, slides.narrate (with a 30-minute spec) reuses it via
// _session_id, the engine calls ExtendTimeout, and the watchdog must
// use the LONGER deadline. Without the fix the session would die at
// 5m1s + the watchdog interval and the slides.narrate handler's
// segment-7 docker-exec call returns ErrSessionNotFound.
func TestWatchdogRespectsExtendedTimeout(t *testing.T) {
	rt := fake.New()
	// Injected with CreatedAt 6 minutes ago. Original Timeout=5m would
	// have it killed; extended Timeout=30m must keep it alive.
	rt.Inject(&session.Session{
		ID:        "shared",
		Status:    session.StatusRunning,
		CreatedAt: time.Now().Add(-6 * time.Minute),
		Spec:      session.Spec{Timeout: 5 * time.Minute},
	})

	if err := rt.ExtendTimeout(context.Background(), "shared", 30*time.Minute); err != nil {
		t.Fatalf("ExtendTimeout: %v", err)
	}

	wd := session.NewWatchdog(rt, slog.New(slog.NewTextHandler(io.Discard, nil)), 500*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go wd.Run(ctx)
	time.Sleep(1200 * time.Millisecond)
	cancel()

	if _, err := rt.Get(context.Background(), "shared"); err != nil {
		t.Fatalf("session must survive — watchdog should use extended 30m, not original 5m: %v", err)
	}
}
