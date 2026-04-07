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
