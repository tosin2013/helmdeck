// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// slides_captions.go — pure-function SRT generation from the per-slide
// narrator notes + per-slide audio durations slides.narrate already
// has finalized at the end of its audio-generation loop. Kept out of
// slides_narrate.go to match the slides_notes.go separation (pure
// functions live in their own files).
//
// SRT output is consumed two ways from the same byte stream:
//   1. Persisted as a sidecar artifact ("captions.srt") that
//      YouTube/Vimeo auto-import as the CC track (the research-cited
//      ~12-13% view boost path).
//   2. Optionally written to a sidecar path under /tmp and burned
//      into every video frame via ffmpeg's libass `subtitles=`
//      filter when captions_burn_in:true is requested.

import (
	"fmt"
	"strings"
)

// buildSRT renders cumulative-timed SRT cues from per-slide notes
// and per-slide audio durations. Cues are 1-based per the SRT spec.
// Empty-notes slides get a single-space cue rather than being
// dropped — keeping cue numbering aligned with slide indices is
// the cheapest way to make a misordered narration easy to spot
// during manual review of the .srt.
//
// The two slices MUST be the same length; mismatches are not the
// caller's fault on the happy path (the handler builds both from
// the same iteration), so this function trusts the contract and
// will index up to min(len(slides), len(durations)).
func buildSRT(slides []slideContent, durations []float64) []byte {
	n := len(slides)
	if d := len(durations); d < n {
		n = d
	}
	var b strings.Builder
	cumulative := 0.0
	for i := 0; i < n; i++ {
		dur := durations[i]
		start := cumulative
		end := cumulative + dur
		cumulative = end

		body := normalizeSRTCue(slides[i].Notes)
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, formatSRTTimestamp(start), formatSRTTimestamp(end), body)
	}
	return []byte(b.String())
}

// formatSRTTimestamp renders seconds as HH:MM:SS,mmm — the SRT spec
// requires the wider field AND a COMMA decimal separator (vs the
// M:SS / dot-period format formatTimestamp emits for YouTube
// chapter markers). Distinct helper rather than reusing
// formatTimestamp because the requirements diverge in two places at
// once (width + separator) and a parameterized helper would obscure
// intent at every call site.
func formatSRTTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	// Round to nearest millisecond. int conversion truncates, so we
	// add 0.5 to round half-up — keeps the timeline non-decreasing
	// across cues even when float arithmetic drifts a hair.
	totalMs := int(seconds*1000 + 0.5)
	ms := totalMs % 1000
	totalSec := totalMs / 1000
	s := totalSec % 60
	totalMin := totalSec / 60
	m := totalMin % 60
	h := totalMin / 60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// normalizeSRTCue collapses line-ending variants, strips per-cue
// leading/trailing whitespace, and substitutes a single space for
// empty input so cue numbering doesn't drift.
func normalizeSRTCue(notes string) string {
	// CRLF / CR → LF so the SRT parser sees consistent line breaks.
	s := strings.ReplaceAll(notes, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return " "
	}
	return s
}
