// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  packs.ErrorCode
		wantClass string
	}{
		{"bad input is caller-fixable", &packs.PackError{Code: packs.CodeInvalidInput, Message: "model unavailable"}, packs.CodeInvalidInput, FailureCallerFixable},
		{"handler failure is a pack bug", &packs.PackError{Code: packs.CodeHandlerFailed, Message: "boom"}, packs.CodeHandlerFailed, FailurePackBug},
		{"invalid output is a pack bug", &packs.PackError{Code: packs.CodeInvalidOutput, Message: "schema"}, packs.CodeInvalidOutput, FailurePackBug},
		{"internal is a pack bug", &packs.PackError{Code: packs.CodeInternal, Message: "invariant"}, packs.CodeInternal, FailurePackBug},
		{"timeout is transient", &packs.PackError{Code: packs.CodeTimeout}, packs.CodeTimeout, FailureTransient},
		{"session unavailable is transient", &packs.PackError{Code: packs.CodeSessionUnavailable}, packs.CodeSessionUnavailable, FailureTransient},
		{"resource exhausted (OOM kill) is transient, NOT a pack bug", &packs.PackError{Code: packs.CodeResourceExhausted, Message: "ffmpeg segment 9 killed by the OS on exit 137"}, packs.CodeResourceExhausted, FailureTransient},
		{"schema mismatch is state-changed", &packs.PackError{Code: packs.CodeSchemaMismatch}, packs.CodeSchemaMismatch, FailureStateChanged},
		{"non-PackError is a pipeline definition problem", errors.New("unresolved reference"), "", FailureCallerFixable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, class, reason := classify(tc.err, "slides.render")
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
			if class != tc.wantClass {
				t.Errorf("class = %q, want %q", class, tc.wantClass)
			}
			if reason == "" {
				t.Error("reason must not be empty")
			}
			// A pack bug must point the user at a helmdeck issue link.
			if class == FailurePackBug {
				if !strings.Contains(reason, "github.com/tosin2013/helmdeck/issues/new") {
					t.Errorf("pack_bug reason should link a prefilled issue; got %q", reason)
				}
				if !strings.Contains(reason, "slides.render") {
					t.Errorf("pack_bug reason should name the pack; got %q", reason)
				}
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	retryable := []packs.ErrorCode{
		packs.CodeTimeout,
		packs.CodeSessionUnavailable,
		packs.CodeArtifactFailed,
		// CodeResourceExhausted (OOM kills): re-running with a
		// bumped memory budget or smaller job size frequently
		// succeeds, so the retry loop should give it a shot
		// before surfacing.
		packs.CodeResourceExhausted,
	}
	for _, c := range retryable {
		if !isRetryable(c) {
			t.Errorf("%s should be retryable", c)
		}
	}
	for _, c := range []packs.ErrorCode{packs.CodeInvalidInput, packs.CodeHandlerFailed, packs.CodeSchemaMismatch, packs.CodeInternal} {
		if isRetryable(c) {
			t.Errorf("%s should NOT be retryable", c)
		}
	}
}
