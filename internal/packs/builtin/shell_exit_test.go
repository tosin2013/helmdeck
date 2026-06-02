// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// TestClassifyShellExitCode_137 — the motivating case. Exit 137 is
// SIGKILL, which in helmdeck's sandboxed sessions overwhelmingly
// means the kernel OOM killer reaped a child process whose RSS
// exceeded the cgroup memory limit. classifyShellExitCode lifts that
// into CodeResourceExhausted so the gateway classifier routes the
// failure to FailureTransient instead of FailurePackBug.
func TestClassifyShellExitCode_137(t *testing.T) {
	code, ok := classifyShellExitCode(137)
	if !ok {
		t.Fatalf("exit 137 should be recognized")
	}
	if code != packs.CodeResourceExhausted {
		t.Errorf("exit 137 should map to CodeResourceExhausted; got %q", code)
	}
}

// TestClassifyShellExitCode_OtherCodes — every other exit code is
// deliberately NOT recognized today. The helper returns ok=false so
// callers know to fall through to their existing CodeHandlerFailed
// path. Adding new codes (143/SIGTERM, 124/timeout, etc.) is a
// future-PR decision — the helper exists so that decision happens
// in one place, not per-handler.
func TestClassifyShellExitCode_OtherCodes(t *testing.T) {
	cases := []int{0, 1, 2, 124, 130, 139, 143, 255}
	for _, ec := range cases {
		code, ok := classifyShellExitCode(ec)
		if ok {
			t.Errorf("exit %d should NOT be recognized today; got code=%q ok=%v", ec, code, ok)
		}
		if code != "" {
			t.Errorf("exit %d unrecognized but code is not empty: %q", ec, code)
		}
	}
}
