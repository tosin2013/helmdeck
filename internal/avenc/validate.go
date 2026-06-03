// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// RequireNonEmptyOutput stats a file via `wc -c < PATH` and returns
// a typed *packs.PackError if the file is missing, the stat call
// fails at the transport layer, or the file is below minBytes. The
// canonical post-ffmpeg-success check: ffmpeg can exit 0 yet produce
// 0-byte or truncated output (PR #400's lesson), so callers that
// produce a file MUST follow with this check. The label is a
// human-readable identifier ("ffmpeg segment 4", "concat", "silence
// slide 7") so the surfaced error names the actual step.
//
// Same wc-c pattern fs.read uses (internal/packs/builtin/fs_packs.go) —
// portable across sidecars and standard busybox.
func RequireNonEmptyOutput(ctx context.Context, exec Executor, path string, minBytes int64, label string) error {
	res, err := exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", "wc -c < " + shellQuote(path)},
	})
	if err != nil {
		return &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: stat output %s (transport error): %v", label, path, err),
			Cause:   err,
		}
	}
	if res.ExitCode != 0 {
		return &packs.PackError{
			Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: produced no output (expected %s; wc-c stat exited %d: %s). The upstream binary returned exit 0 but the output file is missing — typically a libavformat silent failure on a malformed input.",
				label, path, res.ExitCode, strings.TrimSpace(string(res.Stderr))),
		}
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(string(res.Stdout)), 10, 64)
	if size < minBytes {
		return &packs.PackError{
			Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: produced only %d bytes (below the %d-byte floor) at %s. The upstream binary returned exit 0 but the output is too small to be a valid encoded artifact — typically a libavformat silent failure on a malformed input.",
				label, size, minBytes, path),
		}
	}
	return nil
}

// LooksLikeMP3 reports whether b starts with a valid MP3 file
// signature — either an MPEG frame sync (first 11 bits all set,
// encoded as 0xFF 0xE0..0xFF, restricted to Layer III variants
// MPEG-1 0xFB/0xFA and MPEG-2 0xF3/0xF2) or an ID3v2 tag header
// ("ID3"). Used to detect TTS providers wrapping an error response
// as JSON / HTML inside an HTTP 200 body — without this check the
// caller would treat the bytes as audio, ffmpeg would silently
// produce no audio, and the narrated video would be silent
// (PR #400 documents an ElevenLabs case).
func LooksLikeMP3(b []byte) bool {
	if len(b) < 3 {
		return false
	}
	if string(b[:3]) == "ID3" {
		return true
	}
	if b[0] != 0xFF {
		return false
	}
	switch b[1] {
	case 0xFB, 0xFA, 0xF3, 0xF2:
		return true
	}
	return false
}

// ValidateMP3Body rejects HTTP-response bodies that are too small or
// don't start with an MP3 sync word / ID3v2 tag. Combines the size
// floor and LooksLikeMP3 check; returns nil for a body that looks
// like real MP3 audio. Used by every TTS HTTP path so providers
// (ElevenLabs, OpenAI, future engines) that wrap an error envelope
// in HTTP 200 surface a typed failure instead of producing silent
// videos. Lifted verbatim from PR #400's validateElevenLabsBody —
// the helper has always been provider-agnostic.
func ValidateMP3Body(body []byte) error {
	if len(body) < MinTTSResponseBytes || !LooksLikeMP3(body) {
		return fmt.Errorf("TTS HTTP 200 body is not valid MP3 audio (%d bytes, prefix=%q) — likely an error envelope wrapped as 200",
			len(body), truncateForMessage(string(body), 64))
	}
	return nil
}

// ValidateMP4Streams runs `LC_ALL=C ffprobe -show_streams` on the
// produced .mp4 and verifies the file contains at least the streams
// the caller expected (typically 1 video + 1 audio). Catches the
// "ffmpeg exit 0 + non-zero output size + but the mp4 has no moov
// atom" silent-failure mode that RequireNonEmptyOutput's byte-floor
// check misses — see the external-research note in the plan file
// (vidcutter issue #32) for the canonical production-pipeline shape.
//
// wantVideo and wantAudio are independent: pass true for whichever
// streams the caller produced, false to skip. A muxed mp4 missing
// the requested stream type returns CodeHandlerFailed naming the
// step. Transport errors and ffprobe exit codes surface through the
// shared helpers; this function only contributes the post-success
// stream-presence check.
func ValidateMP4Streams(ctx context.Context, exec Executor, path string, wantVideo, wantAudio bool, label string) error {
	res, err := exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", LocalePrefix + "ffprobe -v error -show_entries stream=codec_type -of csv=p=0 " + shellQuote(path)},
	})
	if err != nil {
		return transportError(label+": ffprobe stream check", err, nil)
	}
	if res.ExitCode != 0 {
		return exitCodeError(label+": ffprobe stream check", res.ExitCode, res.Stderr)
	}
	// ffprobe stdout shape: one "audio" or "video" per line, in stream order.
	lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	haveVideo, haveAudio := false, false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "video":
			haveVideo = true
		case "audio":
			haveAudio = true
		}
	}
	if wantVideo && !haveVideo {
		return &packs.PackError{
			Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: ffmpeg exit 0 produced %s with NO video stream — typically a libavformat silent failure that bypasses the byte-floor check. ffprobe streams: %q",
				label, path, strings.TrimSpace(string(res.Stdout))),
		}
	}
	if wantAudio && !haveAudio {
		return &packs.PackError{
			Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: ffmpeg exit 0 produced %s with NO audio stream — typically a libavformat silent failure that bypasses the byte-floor check. ffprobe streams: %q",
				label, path, strings.TrimSpace(string(res.Stdout))),
		}
	}
	return nil
}
