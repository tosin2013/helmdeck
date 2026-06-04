// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"strings"
	"testing"
)

// TestBuildSRT_CueNumbering — N slides produces cues 1..N, each
// separated by a blank line. This is the most basic SRT spec compliance
// check and the regression guard for cue-number drift (the bug class
// where empty-notes slides accidentally get dropped and downstream
// cue 5 actually corresponds to slide 7).
func TestBuildSRT_CueNumbering(t *testing.T) {
	slides := []slideContent{
		{Index: 0, Notes: "First slide narration."},
		{Index: 1, Notes: "Second slide."},
		{Index: 2, Notes: "Third."},
	}
	durations := []float64{5.0, 3.0, 7.0}
	srt := string(buildSRT(slides, durations))

	// Each cue starts with its 1-based number on its own line.
	for i := 1; i <= 3; i++ {
		marker := "\n" + string(rune('0'+i)) + "\n"
		if i == 1 {
			marker = "1\n"
		}
		if !strings.Contains(srt, marker) {
			t.Errorf("cue %d marker %q not found in SRT: %q", i, marker, srt)
		}
	}
	// Three cues → three blank-line separators (trailing).
	if got := strings.Count(srt, "\n\n"); got != 3 {
		t.Errorf("expected 3 cue separators (blank lines); got %d in:\n%s", got, srt)
	}
}

// TestBuildSRT_TimestampFormat — pins the SRT spec's HH:MM:SS,mmm
// shape and the cumulative-timing arithmetic. The previous-engagement
// formatTimestamp helper uses M:SS with a period — using that helper
// would fail YouTube's SRT parser silently, which is precisely the
// class of bug this test is designed to catch.
func TestBuildSRT_TimestampFormat(t *testing.T) {
	slides := []slideContent{
		{Notes: "Cue 1"},
		{Notes: "Cue 2"},
		{Notes: "Cue 3"},
	}
	durations := []float64{7.432, 5.696, 12.0}
	srt := string(buildSRT(slides, durations))

	// First cue: 0.000 → 7.432
	if !strings.Contains(srt, "00:00:00,000 --> 00:00:07,432") {
		t.Errorf("first cue timestamp missing or wrong; got:\n%s", srt)
	}
	// Second cue: 7.432 → 13.128
	if !strings.Contains(srt, "00:00:07,432 --> 00:00:13,128") {
		t.Errorf("second cue timestamp missing or wrong; got:\n%s", srt)
	}
	// Third cue: 13.128 → 25.128
	if !strings.Contains(srt, "00:00:13,128 --> 00:00:25,128") {
		t.Errorf("third cue timestamp missing or wrong; got:\n%s", srt)
	}
	// Critical: comma decimal, NOT period. A period would parse as
	// hours in some libass builds and produce 7-hour-offset captions.
	if strings.Contains(srt, ".432") || strings.Contains(srt, ".128") {
		t.Errorf("SRT spec mandates COMMA decimal separator, found period; got:\n%s", srt)
	}
}

// TestBuildSRT_EmptyNotes_StillEmitsCue — slides with empty narrator
// notes (a silent placeholder slide) still get a cue so the cue
// numbering stays aligned with the slide indices. Operators
// reviewing the .srt by eye need cue N to correspond to slide N+1
// for sane debugging.
func TestBuildSRT_EmptyNotes_StillEmitsCue(t *testing.T) {
	slides := []slideContent{
		{Notes: "First"},
		{Notes: ""}, // silent placeholder
		{Notes: "Third"},
	}
	durations := []float64{5.0, 3.0, 7.0}
	srt := string(buildSRT(slides, durations))

	// Cue 2 must exist (numbering preserved).
	if !strings.Contains(srt, "\n2\n") {
		t.Errorf("cue 2 missing — empty-notes slide must not break numbering; got:\n%s", srt)
	}
	// Cue 3 must come after cue 2 in the byte stream.
	idx2 := strings.Index(srt, "\n2\n")
	idx3 := strings.Index(srt, "\n3\n")
	if idx3 < idx2 {
		t.Errorf("cue 3 appears before cue 2; numbering drifted")
	}
}

// TestBuildSRT_MultilineNotes_Normalized — CRLF/CR get normalized
// to LF and trailing whitespace stripped. Mixed line-endings show up
// when narrator notes are pasted from Word docs / DOS-line-ending
// editors; without normalization libass renders literal "\r" as
// box characters and YouTube rejects the file.
func TestBuildSRT_MultilineNotes_Normalized(t *testing.T) {
	slides := []slideContent{
		{Notes: "Line one\r\nLine two\rLine three   \n"},
	}
	durations := []float64{5.0}
	srt := string(buildSRT(slides, durations))

	if strings.Contains(srt, "\r") {
		t.Errorf("SRT must not contain CR after normalization; got:\n%q", srt)
	}
	if !strings.Contains(srt, "Line one\nLine two\nLine three") {
		t.Errorf("multiline notes not normalized correctly; got:\n%s", srt)
	}
	// No trailing whitespace inside the cue body (would render as
	// trailing space in libass).
	if strings.Contains(srt, "Line three   ") {
		t.Errorf("trailing whitespace not stripped from cue body; got:\n%s", srt)
	}
}

// TestFormatSRTTimestamp_Edges — boundary cases that exercise the
// hours-rollover, ms-rounding, and zero paths. Each is a one-off
// regression risk that only surfaces under specific input shapes
// (long videos, sub-ms drift, the first cue).
func TestFormatSRTTimestamp_Edges(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.0, "00:00:00,000"},
		{0.001, "00:00:00,001"},
		{59.999, "00:00:59,999"},
		{60.0, "00:01:00,000"},
		{3599.999, "00:59:59,999"},
		{3600.0, "01:00:00,000"},
		{3600.5, "01:00:00,500"},
		{7325.123, "02:02:05,123"},
		{-1.0, "00:00:00,000"}, // negative clamps to zero
	}
	for _, tc := range cases {
		if got := formatSRTTimestamp(tc.in); got != tc.want {
			t.Errorf("formatSRTTimestamp(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
