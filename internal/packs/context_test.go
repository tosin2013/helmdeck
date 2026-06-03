// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package packs

import (
	"context"
	"sync"
	"testing"
)

// context_test.go covers the exported context-extractor accessors
// (PR F of the v0.25.0 reliability arc). These are small but load-
// bearing — every audit row, every memory-namespace decision, every
// progress event passes through this seam.
//
// Coverage gaps before this file:
//   - WithProgress / ProgressFromContext (Engine wires the engine-
//     side callback through the ctx; pipelines.Runner reads it via
//     ProgressFromContext to surface to the run record)
//   - CallerFromContext (the bare-namespace key for memory writes;
//     audit attribution depends on it)
//   - WithRunID / RunIDFromContext were already covered by
//     pipelines/runs tests; not re-tested here.

// TestWithCaller_RoundTrip pins the WithCaller → CallerFromContext
// round trip. The accessor returns "unknown" when no value attached,
// the literal subject when one is, and "unknown" when an empty string
// was attached (so the namespace is always well-defined for the
// audit/memory layer).
func TestWithCaller_RoundTrip(t *testing.T) {
	if got := CallerFromContext(context.Background()); got != "unknown" {
		t.Errorf("bare context caller = %q; want unknown", got)
	}

	ctx := WithCaller(context.Background(), "alice@example.com")
	if got := CallerFromContext(ctx); got != "alice@example.com" {
		t.Errorf("attached caller = %q; want alice@example.com", got)
	}

	// Empty subject still resolves to "unknown" — the namespace
	// MUST be non-empty so memory writes can't be attributed to "".
	empty := WithCaller(context.Background(), "")
	if got := CallerFromContext(empty); got != "unknown" {
		t.Errorf("empty subject caller = %q; want unknown", got)
	}
}

// TestWithCaller_NestedChild — the child context inherits the parent's
// caller. Pin so a future refactor that changes the context-key
// strategy doesn't accidentally orphan caller-scoped operations.
func TestWithCaller_NestedChild(t *testing.T) {
	parent := WithCaller(context.Background(), "alice")
	child, cancel := context.WithCancel(parent)
	defer cancel()
	if got := CallerFromContext(child); got != "alice" {
		t.Errorf("child caller = %q; want alice (inherited)", got)
	}
}

// TestWithProgress_RoundTrip pins the WithProgress → ProgressFromContext
// round trip and the always-non-nil contract: a bare context returns
// a no-op callback the caller can invoke without checking. Without
// the non-nil guarantee, every handler invoking ec.Report would need
// a nil check.
func TestWithProgress_RoundTrip(t *testing.T) {
	// Bare context: returns a no-op (must not panic on call).
	cb := ProgressFromContext(context.Background())
	if cb == nil {
		t.Fatal("ProgressFromContext returned nil; contract is always-non-nil")
	}
	cb(0, "noop") // should not panic

	// Attached: round-trips.
	var (
		mu       sync.Mutex
		captured []float64
	)
	fn := ProgressFunc(func(pct float64, _ string) {
		mu.Lock()
		captured = append(captured, pct)
		mu.Unlock()
	})
	ctx := WithProgress(context.Background(), fn)
	got := ProgressFromContext(ctx)
	got(0.5, "halfway")
	got(1.0, "done")
	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 || captured[0] != 0.5 || captured[1] != 1.0 {
		t.Errorf("captured = %v; want [0.5 1.0]", captured)
	}
}

// TestWithProgress_NilClears — passing nil to WithProgress installs
// the no-op default rather than crashing. Useful for callers that
// want to clear an inherited callback before forwarding the context
// downstream.
func TestWithProgress_NilClears(t *testing.T) {
	ctx := WithProgress(context.Background(), nil)
	got := ProgressFromContext(ctx)
	if got == nil {
		t.Fatal("ProgressFromContext returned nil after WithProgress(nil); want no-op")
	}
	got(0.1, "still no-op")
}
