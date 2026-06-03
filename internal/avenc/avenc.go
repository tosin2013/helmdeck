// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package avenc is helmdeck's shared audio/video encoding helper. It
// consolidates the ffmpeg / ffprobe / TTS-validation patterns that
// were duplicated across slides.narrate and internal/podcast.Concat
// before this package landed, and concentrates the lessons from
// PRs #390, #399, #400, #401, #404, #405 into ONE place with a
// concentrated test surface (target ≥ 90% line coverage).
//
// Design goals:
//
//  1. ALL ffmpeg / ffprobe invocations relevant to multi-pack audio
//     and video workflows live here. Future packs (tiktok.shorts,
//     audiobook.generate, …) import avenc and inherit every
//     battle-tested pattern automatically.
//
//  2. Every helper is parameterised over an Executor callback that
//     matches the shape of `packs.ExecutionContext.Exec`, so avenc
//     stays decoupled from the `packs` package internals — both
//     production callers (engine-wired Exec) and tests (a scripted
//     mock) plug in the same way.
//
//  3. Every helper distinguishes the THREE failure modes uniformly:
//     - transport error (err != nil from Executor) — surface honestly
//     as "<step>: docker-exec transport error" with the underlying
//     err wrapped via Cause, never as "exit 0" (PR #400's lesson).
//     - non-zero exit code — surface stderr + classify OS-side kills
//     via packs.classifyShellExitCode (137 → CodeResourceExhausted).
//     - silent success — ffmpeg / ffprobe can exit 0 yet produce
//     garbage (0-byte mp4, NaN duration, error-as-JSON wrapped in
//     HTTP 200). Every helper that writes a file validates it.
//
// What avenc does NOT do:
//
//   - Marp-specific PNG validation (stays in slides.narrate).
//   - Per-pack input/output schemas, vault lookups, orchestration.
//   - Real-ffmpeg integration testing — avenc is mocked at the
//     Executor boundary; integration tests against a real ffmpeg
//     binary live in a separate Docker harness.
//   - Anything Chromium / Marp / mmdc related.
//
// See /root/.claude/plans/i-would-like-to-elegant-kahan.md (the
// approved plan for this work) for the full taxonomy of failure
// modes addressed.
package avenc

import (
	"context"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// Executor is the shape callers plug into avenc helpers. It matches
// `packs.ExecutionContext.Exec` exactly so a pack handler can pass
// `ec.Exec` verbatim. Defined here (instead of importing the type from
// internal/packs) so a future test or non-pack caller can construct
// its own Executor without dragging in the engine.
type Executor func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error)

// Default byte-size floors. See the field comments below for why each
// value was chosen and which PR encoded the lesson.
const (
	// MinEncodedSegmentBytes is the smallest per-segment .mp4 (or the
	// final concatenated .mp4) we accept as a successful ffmpeg
	// encode. ffmpeg can exit 0 yet produce a 0-byte or truncated
	// output when the input is malformed in a way libavformat does
	// not surface as a non-zero exit. 1 KB is below any sensible
	// real .mp4 (a single-frame h264 mdat + a sane moov header is at
	// minimum a few KB) and well above zero — safe in both directions.
	// PR #400 introduced the floor; PR #404 confirmed it on the
	// concat path.
	MinEncodedSegmentBytes int64 = 1024

	// MinSilenceMP3Bytes is the smallest silence MP3 we accept from
	// GenerateSilence. libmp3lame's overhead per file (ID3v2 header
	// plus at minimum a single MPEG frame) is at least a few hundred
	// bytes even for very short durations. 256 is well above zero and
	// well below any real silence track. PR #400 added this check
	// after observing mid-write SIGPIPE produce 0-byte files.
	MinSilenceMP3Bytes int64 = 256

	// MinTTSResponseBytes is the smallest body we accept from a
	// successful TTS HTTP 200 response. A 1-second mp3 at the
	// 128 kbps slides.narrate baseline is ≈ 16 KB; even very short
	// speech is comfortably above 512 bytes. A body smaller than this
	// is almost certainly a JSON error envelope wrapped in HTTP 200,
	// which providers occasionally do (PR #400 documents an
	// ElevenLabs case). 512 is generous: false-positives on real
	// audio are effectively impossible.
	MinTTSResponseBytes int = 512

	// LocalePrefix is prepended to every ffprobe (and any other
	// numeric-output) invocation so the C locale governs decimal
	// formatting. Without this, a sidecar with LC_NUMERIC=de_DE
	// would have ffprobe emit "5,123" instead of "5.123" and
	// strconv.ParseFloat would silently return 0. Recommended by the
	// authoritative ffprobe duration guide referenced in the plan
	// file. Single source so every avenc helper applies it uniformly.
	LocalePrefix = "LC_ALL=C "
)

// IsOOMExitCode reports whether an OS-signalled kill (typically
// SIGKILL from the cgroup OOM killer) is responsible for a non-zero
// exit. Today only 137 (SIGKILL) is recognised — the same conclusion
// the per-pack classifyShellExitCode helper reaches. Kept here as a
// thin wrapper so future callers ask "was this an OOM?" via avenc
// rather than re-implementing the check. Returning a bool keeps the
// API stable if more codes get added (e.g. 143 = SIGTERM, 124 =
// GNU `timeout` external watchdog).
func IsOOMExitCode(exitCode int) bool {
	return exitCode == 137
}

// transportError builds the honest-error-message form for the
// (err != nil, ExitCode == 0) case. The label names the step so an
// operator sees "ffmpeg segment 4: docker-exec transport error" rather
// than the misleading "exit 0" the old per-pack message format
// produced. PR #400 was the original fix; avenc generalises it.
func transportError(label string, err error, stderr []byte) error {
	stderrTail := ""
	if len(stderr) > 0 {
		stderrTail = " stderr: " + truncateForMessage(strings.TrimSpace(string(stderr)), 4096)
	}
	return &packs.PackError{
		Code: packs.CodeHandlerFailed,
		Message: fmt.Sprintf("%s: docker-exec transport error (the underlying binary did NOT return a real exit code): %v.%s",
			label, err, stderrTail),
		Cause: err,
	}
}

// exitCodeError builds the failure message for the (err == nil,
// ExitCode != 0) case. It applies the OOM lift — exit 137 surfaces
// as CodeResourceExhausted so classify.go routes it to
// FailureTransient with the "bump SessionSpec.MemoryLimit" recovery
// message — and otherwise returns CodeHandlerFailed with the stderr.
func exitCodeError(label string, exitCode int, stderr []byte) error {
	stderrStr := strings.TrimSpace(string(stderr))
	if IsOOMExitCode(exitCode) {
		return &packs.PackError{
			Code: packs.CodeResourceExhausted,
			Message: fmt.Sprintf("%s killed by the OS on exit %d (likely OOM — bump SessionSpec.MemoryLimit, reduce job size, or re-run on a host with more memory). stderr: %s",
				label, exitCode, truncateForMessage(stderrStr, 4096)),
		}
	}
	return &packs.PackError{
		Code: packs.CodeHandlerFailed,
		Message: fmt.Sprintf("%s failed (exit %d): %s",
			label, exitCode, truncateForMessage(stderrStr, 4096)),
	}
}

// truncateForMessage clips a string to max runes for inclusion in
// operator-facing error messages. Kept tiny so a misbehaving binary
// dumping multi-MB stderr can't blow up the audit log. Same shape as
// internal/vault.truncateForMessage; duplicated here so avenc doesn't
// import vault for one helper.
func truncateForMessage(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// shellQuote single-quotes a path for safe inclusion in `sh -c`
// strings, mirroring slides.narrate / shell_exit's existing
// shellQuote. Kept here so avenc doesn't import internal/packs/builtin
// (which would invert the dependency direction). The duplication is
// 6 lines and intentional.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
