// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

import (
	"context"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/avenc"
	"github.com/tosin2013/helmdeck/internal/session"
)

const (
	defaultSilenceMs = 600
	concatBytesCap   = 256 << 20 // 256 MiB final-podcast cap (~3.5 hours of 128 kbps mp3)
	concatTempDir    = "/tmp/helmdeck-podcast"
)

// ConcatOptions controls how Concat stitches per-turn MP3 segments.
type ConcatOptions struct {
	// SilenceBetweenTurnsMs is the gap inserted between every adjacent
	// pair of turns (NOT before the first or after the last) so the
	// listener gets a beat between speakers. Default is defaultSilenceMs
	// (600ms). Pass 0 to use the default.
	SilenceBetweenTurnsMs int

	// MinTurnDurationS is the per-turn floor (#141). Each turn shorter
	// than this is padded with trailing anullsrc silence so its total
	// duration is at least MinTurnDurationS seconds. Use 0 (default)
	// to opt out and preserve raw TTS pacing for callers that want
	// natural pauses between speakers without enforced minimums.
	//
	// The floor exists because real TTS sometimes returns very short
	// audio (a 1-2s "agreed" turn cuts to the next speaker abruptly);
	// downstream video pipelines (slides.narrate, YouTube cuts) want
	// a stable per-segment minimum to avoid feeling rushed.
	MinTurnDurationS float64
}

// avencBind wraps a session.Executor + sessionID into the bound-Exec
// callback shape avenc helpers expect. The wrapping is intentional —
// avenc doesn't know about the session ID dispatch indirection, and
// adding that knowledge would put a sessionID parameter on every
// avenc API, which would be a worse trade than this thin closure.
func avencBind(ex session.Executor, sessionID string) avenc.Executor {
	return func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
		return ex.Exec(ctx, sessionID, req)
	}
}

// Concat takes a slice of per-turn MP3 byte buffers and produces a
// single MP3 with silence padding between turns. The work runs
// inside a session sidecar (ffmpeg + ffprobe must be installed
// there — they are in the helmdeck-sidecar image).
//
// Returns the final MP3 bytes plus the total duration in seconds
// (computed from per-turn ffprobe + the silence count).
//
// The session-side temp dir is /tmp/helmdeck-podcast — cleaned up at
// the end of the call (best-effort).
//
// The ffmpeg/ffprobe surface is delegated to internal/avenc/ (PR C of
// the avenc consolidation). The behaviour-relevant patterns avenc
// owns for this file: silence generation post-validates the produced
// file size, ffprobe-duration rejects NaN/Inf/non-positive AND pins
// LC_ALL=C for locale stability, concat re-encodes audio (PR #404's
// lesson — `-c copy` on AAC across mismatched frame boundaries
// produces audible dropouts).
func Concat(ctx context.Context, ex session.Executor, sessionID string, turns [][]byte, opts ConcatOptions) ([]byte, float64, error) {
	if len(turns) == 0 {
		return nil, 0, fmt.Errorf("podcast/concat: no turns to stitch")
	}
	silenceMs := opts.SilenceBetweenTurnsMs
	if silenceMs <= 0 {
		silenceMs = defaultSilenceMs
	}

	avencExec := avencBind(ex, sessionID)

	// 1. Make the temp dir.
	if _, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd: []string{"sh", "-c", "rm -rf " + concatTempDir + " && mkdir -p " + concatTempDir},
	}); err != nil {
		return nil, 0, fmt.Errorf("mkdir tempdir: %w", err)
	}

	// 2. Write each turn's MP3 bytes via Stdin. writeTurnFile stays
	// local — it streams bytes (Stdin) rather than running a typical
	// ffmpeg command, so the avenc shape doesn't fit naturally.
	for i, mp3 := range turns {
		if err := writeTurnFile(ctx, ex, sessionID, i, mp3); err != nil {
			return nil, 0, fmt.Errorf("write turn %d: %w", i, err)
		}
	}

	// 2b. Apply the per-turn duration floor (#141) when requested.
	// Done after-write rather than inline so the silent-turn engine
	// path (Synthesize falls back to SilenceTurn) is also covered:
	// any turn — TTS or silent — gets the same floor.
	if opts.MinTurnDurationS > 0 {
		for i := range turns {
			if err := padTurnToMin(ctx, ex, sessionID, i, opts.MinTurnDurationS); err != nil {
				return nil, 0, fmt.Errorf("pad turn %d to %.1fs: %w", i, opts.MinTurnDurationS, err)
			}
		}
	}

	// 3. Make a silence segment used between every pair of turns
	// (generated once, reused). Delegated to avenc which adds the
	// post-encode size-validation check.
	silenceSec := float64(silenceMs) / 1000.0
	silencePath := concatTempDir + "/silence.mp3"
	if err := avenc.GenerateSilence(ctx, avencExec, silenceSec, silencePath, "podcast between-turn"); err != nil {
		return nil, 0, fmt.Errorf("silence segment: %w", err)
	}

	// 4. Build a concat-demuxer list: turn-0, silence, turn-1, silence, ..., turn-N
	var listB strings.Builder
	for i := range turns {
		fmt.Fprintf(&listB, "file '%s/turn-%03d.mp3'\n", concatTempDir, i)
		if i < len(turns)-1 {
			fmt.Fprintf(&listB, "file '%s'\n", silencePath)
		}
	}
	listPath := concatTempDir + "/concat.txt"
	if _, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd:   []string{"sh", "-c", "cat > " + listPath},
		Stdin: []byte(listB.String()),
	}); err != nil {
		return nil, 0, fmt.Errorf("write concat list: %w", err)
	}

	// 5. Concat via avenc.ConcatAudio. Audio re-encode is mandatory:
	// the silence segment may have a slightly different frame-size
	// from ElevenLabs' MP3s and `-c copy` would splice at wrong
	// boundaries (PR #404's lesson for the video path applies here
	// too). Bitrate (192 kbps) and sample rate (44100 Hz) come from
	// avenc.ConcatAudio's defaults — matched to the ElevenLabs
	// Creator-tier source so the re-encode doesn't bottleneck below
	// the source quality.
	finalPath := concatTempDir + "/final.mp3"
	if err := avenc.ConcatAudio(ctx, avencExec, listPath, finalPath,
		avenc.ConcatAudioOpts{Codec: "libmp3lame"},
		"podcast concat"); err != nil {
		return nil, 0, fmt.Errorf("ffmpeg concat: %w", err)
	}

	// 6. ffprobe the final duration via avenc — same call, but
	// LC_ALL=C-pinned and NaN/Inf-rejected. Best-effort: if the probe
	// fails we still return the bytes; the caller falls back to a
	// duration of 0 for cost accounting, which is the historical
	// behavior (fmt.Sscanf silently returned 0 on garbage).
	duration, perr := avenc.ProbeAudioDuration(ctx, avencExec, finalPath)
	if perr != nil {
		duration = 0
	}

	// 7. Read the final MP3 bytes back. Cap at 256 MiB.
	readRes, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd: []string{"sh", "-c",
			fmt.Sprintf("dd if=%s bs=1M count=256 2>/dev/null", finalPath)},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("read final mp3: %w", err)
	}
	if len(readRes.Stdout) == 0 {
		return nil, 0, fmt.Errorf("final mp3 is empty")
	}
	if len(readRes.Stdout) > concatBytesCap {
		return nil, 0, fmt.Errorf("final mp3 %d bytes exceeds cap", len(readRes.Stdout))
	}

	// 8. Cleanup, best-effort.
	_, _ = ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd: []string{"sh", "-c", "rm -rf " + concatTempDir},
	})

	return readRes.Stdout, duration, nil
}

// writeTurnFile writes the per-turn MP3 bytes into the session temp
// dir at /tmp/helmdeck-podcast/turn-NNN.mp3. Uses Stdin to stream
// without base64-encoding overhead. Stays local — avenc doesn't have
// a streaming-write helper because the pattern isn't ffmpeg-specific.
func writeTurnFile(ctx context.Context, ex session.Executor, sessionID string, idx int, mp3 []byte) error {
	path := fmt.Sprintf("%s/turn-%03d.mp3", concatTempDir, idx)
	res, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd:   []string{"sh", "-c", "cat > " + path},
		Stdin: mp3,
	})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write turn %d exit %d: %s", idx, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// padTurnToMin composes avenc.ProbeAudioDuration + avenc.PadAudioToMin
// for the per-turn floor (#141). Best-effort: a failing ffprobe (e.g.
// corrupt MP3) skips the pad rather than aborting the whole concat —
// the listener gets the unpadded turn. Same behaviour as the pre-avenc
// shape; the implementation is now a 4-line composition instead of an
// inline 4-step ffmpeg pipeline.
func padTurnToMin(ctx context.Context, ex session.Executor, sessionID string, idx int, minSec float64) error {
	avencExec := avencBind(ex, sessionID)
	turnPath := fmt.Sprintf("%s/turn-%03d.mp3", concatTempDir, idx)
	dur, perr := avenc.ProbeAudioDuration(ctx, avencExec, turnPath)
	if perr != nil {
		return nil // best-effort: skip the pad if we can't probe
	}
	return avenc.PadAudioToMin(ctx, avencExec, turnPath, concatTempDir,
		fmt.Sprintf("turn-%03d", idx), dur, minSec, fmt.Sprintf("podcast turn %d", idx))
}

// SilenceTurn produces a silent MP3 segment of the given duration —
// used as the fallback when no API key is configured. Wraps
// avenc.GenerateSilence (which post-validates the file size, fixing
// the 0-byte-MP3 hole the original SilenceTurn had) and reads the
// bytes back so the caller sees the same byte-slice return shape.
func SilenceTurn(ctx context.Context, ex session.Executor, sessionID string, seconds float64) ([]byte, error) {
	avencExec := avencBind(ex, sessionID)
	silencePath := concatTempDir + "/silent-turn.mp3"
	if _, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd: []string{"sh", "-c", "mkdir -p " + concatTempDir},
	}); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	if err := avenc.GenerateSilence(ctx, avencExec, seconds, silencePath, "podcast silent turn"); err != nil {
		return nil, fmt.Errorf("silence ffmpeg: %w", err)
	}
	res, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd: []string{"sh", "-c", "cat " + silencePath},
	})
	if err != nil {
		return nil, fmt.Errorf("read silent turn: %w", err)
	}
	return res.Stdout, nil
}
