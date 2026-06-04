// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// EncodeSegmentOpts configures a single still-image-plus-audio →
// .mp4 encode. The defaults match slides.narrate's per-segment
// invocation; callers override individual fields to adopt a
// different threading/quality profile.
type EncodeSegmentOpts struct {
	// Threads bounds libx264's per-encoder thread count. Empty
	// string falls back to "4" — the value PR #390 introduced to
	// cap encoder peak memory (~50–80 MB per thread × N threads at
	// 1080p). Operators can override via the
	// HELMDECK_SLIDES_NARRATE_FFMPEG_THREADS env var at the
	// caller layer; avenc just respects what's passed in.
	Threads string

	// Preset is libx264's speed/quality/memory tradeoff knob.
	// Empty string uses the default "medium"; the OOM-retry path
	// (see EncodeVideoSegment below) uses "veryfast" which cuts
	// encoder memory roughly in half at the cost of a measurable
	// but acceptable quality hit.
	Preset string

	// VideoFilter is the ffmpeg `-vf` argument (scale + pad + optional
	// fade). Caller composes this from its resolution/aspect_ratio
	// inputs; avenc passes it verbatim.
	VideoFilter string

	// AudioBitrateKbps controls the AAC re-encode of the audio
	// stream. 0 → 192k (matches slides.narrate's baseline; the
	// concat pass uses the same default so the concat input/output
	// agree).
	AudioBitrateKbps int
}

// EncodeVideoSegment runs the per-slide ffmpeg encode that produces
// one .mp4 segment per (still image + audio track) pair. The
// resulting segment is meant to feed ConcatVideoMP4s for the final
// stitch.
//
// The PR #390 adaptive-retry pattern is built in: a primary attempt
// with the supplied opts (default -threads 4, libx264's "medium"
// preset), and on exit 137 (OS-side kill, typically OOM) ONE retry
// with degraded encoder settings (-threads 1 -preset veryfast) that
// cuts the encoder's working set by ~3-4× at the cost of a minor
// quality hit. Both attempts use the same -vf and audio bitrate.
//
// If the primary fails for non-OOM reasons OR the retry also OOMs,
// the surfaced error preserves the OOM classification
// (CodeResourceExhausted) so classify.go's pipeline routing puts it
// in FailureTransient with the "bump SessionSpec.MemoryLimit"
// recovery hint.
//
// label is the operator-facing step name ("ffmpeg segment 4");
// logger is used for the WARN line that records when the degraded
// retry fires (operators reading post-mortem logs need to know).
func EncodeVideoSegment(
	ctx context.Context,
	exec Executor,
	imagePath, audioPath, outPath string,
	opts EncodeSegmentOpts,
	logger *slog.Logger,
	label string,
) error {
	primary := normalizeOpts(opts)
	res, err := runSegmentEncode(ctx, exec, imagePath, audioPath, outPath, primary)

	// OOM retry: only on a clean exit code of 137 from ffmpeg. A
	// transport error (err != nil) doesn't qualify; that's an infra
	// problem, not a memory pressure problem.
	if err == nil && res.ExitCode == 137 {
		if logger != nil {
			logger.Warn(label+": primary encode OOM-killed; retrying ONCE with degraded encoder settings",
				"primary_threads", primary.Threads, "primary_preset", primary.Preset,
				"retry_threads", "1", "retry_preset", "veryfast")
		}
		retry := primary
		retry.Threads = "1"
		retry.Preset = "veryfast"
		res, err = runSegmentEncode(ctx, exec, imagePath, audioPath, outPath, retry)
		if err == nil && res.ExitCode == 137 {
			// Both attempts OOM'd. Surface with the
			// CodeResourceExhausted classification and an
			// operator-actionable message naming the lift.
			return &packs.PackError{
				Code: packs.CodeResourceExhausted,
				Message: fmt.Sprintf("%s killed by the OS on exit %d after primary encode AND degraded-retry both OOM'd (likely OOM — bump SessionSpec.MemoryLimit, reduce slide count, or lower the encode resolution). stderr: %s",
					label, res.ExitCode, truncateForMessage(string(res.Stderr), 4096)),
			}
		}
	}
	if err != nil {
		return transportError(label, err, res.Stderr)
	}
	if res.ExitCode != 0 {
		return exitCodeError(label, res.ExitCode, res.Stderr)
	}
	return RequireNonEmptyOutput(ctx, exec, outPath, MinEncodedSegmentBytes, label+" output")
}

func normalizeOpts(in EncodeSegmentOpts) EncodeSegmentOpts {
	out := in
	if out.Threads == "" {
		out.Threads = "4"
	}
	if out.AudioBitrateKbps == 0 {
		out.AudioBitrateKbps = 192
	}
	return out
}

func runSegmentEncode(
	ctx context.Context,
	exec Executor,
	imagePath, audioPath, outPath string,
	opts EncodeSegmentOpts,
) (session.ExecResult, error) {
	presetFlag := ""
	if opts.Preset != "" {
		presetFlag = "-preset " + opts.Preset + " "
	}
	cmd := fmt.Sprintf(
		"ffmpeg -y -loop 1 -i %s -i %s -c:v libx264 -threads %s %s-tune stillimage "+
			"-c:a aac -b:a %dk -ar 44100 -vf '%s' -pix_fmt yuv420p -shortest %s",
		shellQuote(imagePath), shellQuote(audioPath), opts.Threads, presetFlag,
		opts.AudioBitrateKbps, opts.VideoFilter, shellQuote(outPath),
	)
	return exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
}
