// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"context"
	"testing"
)

// #183: WithJobID + JobIDFromContext round-trip. Covers the three
// shapes Dispatch can encounter: no key set, empty-string passed in
// (treated as no-op), and a populated value that flows through.

func TestJobIDContext_NotSet_ReturnsEmpty(t *testing.T) {
	if got := JobIDFromContext(context.Background()); got != "" {
		t.Errorf("JobIDFromContext on bare ctx = %q, want empty", got)
	}
}

func TestJobIDContext_NilContext_ReturnsEmpty(t *testing.T) {
	if got := JobIDFromContext(nil); got != "" {
		t.Errorf("JobIDFromContext on nil ctx = %q, want empty", got)
	}
}

func TestJobIDContext_EmptyString_DoesNotSetKey(t *testing.T) {
	ctx := WithJobID(context.Background(), "")
	if got := JobIDFromContext(ctx); got != "" {
		t.Errorf("expected empty job id when WithJobID called with empty string, got %q", got)
	}
}

func TestJobIDContext_RoundTrip(t *testing.T) {
	ctx := WithJobID(context.Background(), "job-42")
	if got := JobIDFromContext(ctx); got != "job-42" {
		t.Errorf("JobIDFromContext = %q, want job-42", got)
	}
}

func TestJobIDContext_NestedOverrides(t *testing.T) {
	ctx := WithJobID(context.Background(), "outer")
	ctx = WithJobID(ctx, "inner")
	if got := JobIDFromContext(ctx); got != "inner" {
		t.Errorf("nested WithJobID = %q, want inner", got)
	}
}
