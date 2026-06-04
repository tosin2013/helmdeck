// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"fmt"

	"github.com/tosin2013/helmdeck/internal/session"
)

// GenerateSilence creates a silent MP3 of the given duration at
// outPath via libmp3lame and validates the result is non-empty. Post-
// success validation is mandatory: ffmpeg can exit 0 yet produce a
// 0-byte file (mid-write SIGPIPE, temp-disk full, etc.) — without
// the check, the empty file would flow into a downstream concat /
// encode and surface as a confusing "no audio" error elsewhere.
//
// label names the caller so any error attributes blame correctly
// ("silence slide 7", "podcast silence turn 2").
func GenerateSilence(ctx context.Context, exec Executor, seconds float64, outPath, label string) error {
	cmd := fmt.Sprintf(
		"ffmpeg -y -f lavfi -i anullsrc=r=44100:cl=mono -t %.3f -acodec libmp3lame %s",
		seconds, shellQuote(outPath),
	)
	res, err := exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
	if err != nil {
		return transportError(label+": silence-gen", err, nil)
	}
	if res.ExitCode != 0 {
		return exitCodeError(label+": silence-gen", res.ExitCode, res.Stderr)
	}
	return RequireNonEmptyOutput(ctx, exec, outPath, MinSilenceMP3Bytes, label+": silence-gen output")
}

// ConcatAudioOpts configures audio-only concat. Bitrate is in kbps;
// 0 means "use the avenc default" (192k — matches the
// ElevenLabs Creator-tier source so the re-encode at concat boundaries
// has headroom for libmp3lame's psychoacoustic loss).
type ConcatAudioOpts struct {
	BitrateKbps int    // 0 → 192 (matches ElevenLabs Creator-tier source)
	Codec       string // "" → libmp3lame; "aac" for AAC concat output
}

// ConcatAudio joins the supplied MP3/AAC files into a single audio
// file at outPath via the ffmpeg concat demuxer with mandatory
// re-encode of the audio stream. Re-encode is intentional: PR #404
// proved that stream-copy across segments with different AAC frame
// boundaries produces audible mid-segment dropouts; the only safe
// concat is one that re-aligns frames at the boundary, which means
// re-encoding the audio stream.
//
// parts is the list of file paths inside the sidecar. The caller is
// responsible for writing the concat list file (`/tmp/X.txt`) and
// passing its PATH as listPath — keeping list management out of
// avenc lets each pack name its own scratch files. listPath must
// already exist and contain `file '/abs/path/a.mp3'\nfile '/abs/path/b.mp3'\n…`
// in the standard ffmpeg concat-demuxer format.
//
// label names the caller for error attribution.
func ConcatAudio(ctx context.Context, exec Executor, listPath, outPath string, opts ConcatAudioOpts, label string) error {
	codec := opts.Codec
	if codec == "" {
		codec = "libmp3lame"
	}
	bitrate := opts.BitrateKbps
	if bitrate == 0 {
		bitrate = 192
	}
	cmd := fmt.Sprintf(
		"ffmpeg -y -f concat -safe 0 -i %s -acodec %s -b:a %dk -ar 44100 %s",
		shellQuote(listPath), codec, bitrate, shellQuote(outPath),
	)
	res, err := exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
	if err != nil {
		return transportError(label+": ConcatAudio", err, nil)
	}
	if res.ExitCode != 0 {
		return exitCodeError(label+": ConcatAudio", res.ExitCode, res.Stderr)
	}
	return RequireNonEmptyOutput(ctx, exec, outPath, MinSilenceMP3Bytes, label+": ConcatAudio output")
}

// ConcatVideoMP4sOpts configures the video-with-audio concat case.
// AudioBitrateKbps controls the AAC re-encode; 0 → 192k (matches
// slides.narrate's per-segment baseline).
type ConcatVideoMP4sOpts struct {
	AudioBitrateKbps int // 0 → 192
}

// ConcatVideoMP4s joins MP4 segments into a single MP4 with VIDEO
// stream-copy AND AUDIO re-encode. This is the asymmetric concat
// PR #404 introduced for slides.narrate:
//
//   - Video stays stream-copy because per-segment h264 is identical
//     across segments (same encoder invocation, same params) and the
//     GOP structure aligns to keyframes at each segment start.
//   - Audio MUST be re-encoded because AAC frame boundaries
//     (1024 samples each) rarely align with TTS-driven segment
//     durations; stream-copy would splice at wrong-boundary frames
//     and produce audible mid-segment dropouts.
//
// The cost is a single AAC pass over the total audio (typically
// seconds, negligible vs. per-segment h264 encode time).
//
// listPath is the path to a pre-written concat-list file (same
// format as ConcatAudio). label names the caller.
func ConcatVideoMP4s(ctx context.Context, exec Executor, listPath, outPath string, opts ConcatVideoMP4sOpts, label string) error {
	bitrate := opts.AudioBitrateKbps
	if bitrate == 0 {
		bitrate = 192
	}
	// -movflags +faststart relocates the moov atom from the file
	// tail (mp4 muxer's default) to the head via a second-pass
	// rewrite. Without this flag, every streaming player — HTML5
	// <video>, browser embeds, the OpenClaw chat UI's inline
	// preview, and most mobile MP4 frameworks — must download the
	// entire file before they can begin playback because the seek
	// index lives at the end. In practice that manifests as "audio
	// plays for the buffered portion, then stops at a deterministic
	// timestamp on every replay" — even though the on-disk audio is
	// healthy and contiguous. ffprobe-diagnosed in PR (audio
	// playback dropouts) by sampling RMS at 30s intervals across the
	// full file: uniform -22 to -24 dB throughout, yet players cut
	// out partway. The fix is one flag; the bug had been there since
	// slides.narrate shipped.
	cmd := fmt.Sprintf(
		"ffmpeg -y -f concat -safe 0 -i %s -c:v copy -c:a aac -b:a %dk -ar 44100 -movflags +faststart %s",
		shellQuote(listPath), bitrate, shellQuote(outPath),
	)
	res, err := exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
	if err != nil {
		return transportError(label+": ConcatVideoMP4s", err, nil)
	}
	if res.ExitCode != 0 {
		return exitCodeError(label+": ConcatVideoMP4s", res.ExitCode, res.Stderr)
	}
	return RequireNonEmptyOutput(ctx, exec, outPath, MinEncodedSegmentBytes, label+": ConcatVideoMP4s output")
}

// PadAudioToMin appends silence to inPath so its total duration is
// at least minSec. Composes GenerateSilence + ConcatAudio: generate
// a silence segment of the deficit, write a concat list, run
// ConcatAudio to merge, then rename the merged file over the
// original.
//
// Returns nil (no-op) when currentDur is already at or above minSec
// (deficit ≤ 1ms). On any sub-step failure the function returns the
// underlying error WITHOUT rolling back partial state — the caller
// is expected to discard the slot if padding fails (which matches
// both slides.narrate and podcast.Concat's existing behaviour).
//
// scratchDir is where avenc puts intermediate files. The caller
// supplies the directory so per-run / per-session tmpdirs work
// naturally. slotID is a unique-within-scratchDir identifier
// (typically a 0-based slide index or turn number) used to name
// the intermediate files. label names the caller.
func PadAudioToMin(ctx context.Context, exec Executor, inPath, scratchDir, slotID string, currentDur, minSec float64, label string) error {
	const deficitEpsilon = 0.001 // 1 ms — below human-perceptible audio gap
	deficit := minSec - currentDur
	if deficit <= deficitEpsilon {
		return nil
	}
	padPath := fmt.Sprintf("%s/avenc-pad-%s.mp3", scratchDir, slotID)
	mergedPath := fmt.Sprintf("%s/avenc-padded-%s.mp3", scratchDir, slotID)
	listPath := fmt.Sprintf("%s/avenc-pad-%s.txt", scratchDir, slotID)

	if err := GenerateSilence(ctx, exec, deficit, padPath, label+": pad"); err != nil {
		return err
	}
	// Write the concat-list inline via printf — avoids requiring the
	// caller to manage a separate list-write step for a one-shot pad.
	writeList := fmt.Sprintf(`printf "file '%s'\nfile '%s'\n" > %s`,
		inPath, padPath, shellQuote(listPath))
	if res, err := exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", writeList},
	}); err != nil {
		return transportError(label+": pad write-list", err, nil)
	} else if res.ExitCode != 0 {
		return exitCodeError(label+": pad write-list", res.ExitCode, res.Stderr)
	}
	if err := ConcatAudio(ctx, exec, listPath, mergedPath, ConcatAudioOpts{}, label+": pad concat"); err != nil {
		return err
	}
	mv := fmt.Sprintf("mv %s %s", shellQuote(mergedPath), shellQuote(inPath))
	if res, err := exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", mv},
	}); err != nil {
		return transportError(label+": pad mv", err, nil)
	} else if res.ExitCode != 0 {
		return exitCodeError(label+": pad mv", res.ExitCode, res.Stderr)
	}
	return nil
}
