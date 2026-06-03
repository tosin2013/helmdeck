// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/session/fake"
)

// TestFakeRuntime_ExtendTimeout_GrowsDeadline pins the basic contract:
// a longer requested timeout replaces the current value.
func TestFakeRuntime_ExtendTimeout_GrowsDeadline(t *testing.T) {
	rt := fake.New()
	rt.Inject(&session.Session{
		ID:        "s1",
		Status:    session.StatusRunning,
		CreatedAt: time.Now(),
		Spec:      session.Spec{Timeout: 5 * time.Minute},
	})

	if err := rt.ExtendTimeout(context.Background(), "s1", 30*time.Minute); err != nil {
		t.Fatalf("ExtendTimeout: %v", err)
	}
	s, err := rt.Get(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Spec.Timeout != 30*time.Minute {
		t.Errorf("Spec.Timeout = %v, want 30m", s.Spec.Timeout)
	}
}

// TestFakeRuntime_ExtendTimeout_NeverShrinks pins the no-shrink invariant:
// a shorter or equal newTimeout is a no-op. Critical so a later pack in a
// pipeline that needs a shorter timeout cannot accidentally pull an earlier
// long-deadline session's lifetime down.
func TestFakeRuntime_ExtendTimeout_NeverShrinks(t *testing.T) {
	rt := fake.New()
	rt.Inject(&session.Session{
		ID:        "s1",
		Status:    session.StatusRunning,
		CreatedAt: time.Now(),
		Spec:      session.Spec{Timeout: 30 * time.Minute},
	})

	for _, newT := range []time.Duration{30 * time.Minute, 5 * time.Minute, 0} {
		if err := rt.ExtendTimeout(context.Background(), "s1", newT); err != nil {
			t.Errorf("ExtendTimeout(%v): %v", newT, err)
		}
		s, _ := rt.Get(context.Background(), "s1")
		if s.Spec.Timeout != 30*time.Minute {
			t.Errorf("Spec.Timeout after ExtendTimeout(%v) = %v, want unchanged 30m",
				newT, s.Spec.Timeout)
		}
	}
}

// TestFakeRuntime_ExtendTimeout_UnknownSession returns ErrSessionNotFound
// so callers (the engine) can distinguish a missing session from a
// no-op extension.
func TestFakeRuntime_ExtendTimeout_UnknownSession(t *testing.T) {
	rt := fake.New()
	err := rt.ExtendTimeout(context.Background(), "nope", 10*time.Minute)
	if err != session.ErrSessionNotFound {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}
