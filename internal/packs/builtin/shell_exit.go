// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// shell_exit.go — translate exit codes from shelled-out tools
// (ffmpeg, imagemagick, playwright, etc.) into typed packs.ErrorCode
// values so the gateway classifier can route OS-side resource kills
// to FailureTransient instead of FailurePackBug.
//
// The motivating case: slides.narrate encodes 15+ ffmpeg segments in
// sequence at 1080p. Under memory pressure the kernel OOM killer
// terminates ffmpeg with SIGKILL, surfacing as exit 137. Before this
// helper, the per-segment handler returned a generic CodeHandlerFailed
// which classify.go (`internal/pipelines/classify.go`) mapped to
// FailurePackBug, minting a "file an issue" URL for what is actually
// a memory-budget problem. With the helper, exit 137 lifts to
// CodeResourceExhausted, classify.go routes to FailureTransient, and
// the operator sees an actionable "bump MemoryLimit" message instead
// of a bogus bug report.
//
// Scope kept small on purpose. Today only exit 137 (SIGKILL) is
// recognized — empirically that's the path the OOM killer takes, and
// it has no overlapping legitimate use in the shell-out packs this
// codebase ships. Other signals (143/SIGTERM, 124/timeout, …) stay
// generic; if a future pack needs them surfaced they get added here,
// not reinvented per-handler.

import "github.com/tosin2013/helmdeck/internal/packs"

// classifyShellExitCode returns the typed ErrorCode that best fits a
// non-zero exit from a shelled-out child process, plus an ok flag. ok
// is false when the exit code doesn't have a known typed meaning —
// callers should fall through to their existing CodeHandlerFailed
// path in that case.
//
// Today's mapping:
//
//	137 (SIGKILL)  → CodeResourceExhausted, true
//	(others)       → "", false
//
// Why 137 specifically: every shelled-out tool the helmdeck packs
// invoke (ffmpeg, ffprobe, marp, chromium via playwright, docling
// CLI, etc.) returns 137 when killed by SIGKILL from outside the
// process, and in our container environments the dominant cause of
// an external SIGKILL is the kernel OOM killer reaping a process
// whose RSS exceeded the cgroup memory limit. We deliberately don't
// try to disambiguate OOM-kill from manual-kill in the helper —
// every cause is "the OS terminated this for environmental
// reasons" which is exactly what FailureTransient captures.
func classifyShellExitCode(exitCode int) (packs.ErrorCode, bool) {
	switch exitCode {
	case 137:
		return packs.CodeResourceExhausted, true
	}
	return "", false
}
