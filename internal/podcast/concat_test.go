// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

import (
	"context"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/session"
)

// fakeExecutor records every Exec call and returns scripted responses
// keyed by a substring match against the command. Tests configure
// `responses` to hand back ffprobe durations etc.
type fakeExecutor struct {
	calls     []session.ExecRequest
	responses map[string]session.ExecResult // map[substring]response
}

func (f *fakeExecutor) Exec(_ context.Context, _ string, req session.ExecRequest) (session.ExecResult, error) {
	f.calls = append(f.calls, req)
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
		script := req.Cmd[2]
		for sub, resp := range f.responses {
			if strings.Contains(script, sub) {
				return resp, nil
			}
		}
	}
	return session.ExecResult{}, nil
}

// commandsContaining returns the script-text of every recorded Exec
// call whose script contains the given substring. Useful for asserting
// "the pad pipeline ran" or "the pad pipeline didn't run."
func (f *fakeExecutor) commandsContaining(sub string) []string {
	var out []string
	for _, req := range f.calls {
		if len(req.Cmd) >= 3 && strings.Contains(req.Cmd[2], sub) {
			out = append(out, req.Cmd[2])
		}
	}
	return out
}

// TestPadTurnToMin_BelowFloor_TriggersPadPipeline asserts the pad
// pipeline (anullsrc generation + concat-list write + ffmpeg concat
// + mv) fires when the probed duration is under the floor. After the
// PR C migration to internal/avenc, the underlying commands are
// dispatched by avenc helpers — the externally-visible script shapes
// (anullsrc, printf, ffmpeg concat, mv) are unchanged. The fake
// executor also needs to respond to the post-encode `wc -c` size
// checks avenc's GenerateSilence and ConcatAudio now run; without
// healthy responses the avenc post-validation rejects the encode.
func TestPadTurnToMin_BelowFloor_TriggersPadPipeline(t *testing.T) {
	ex := &fakeExecutor{responses: map[string]session.ExecResult{
		"ffprobe":  {Stdout: []byte("1.500\n")}, // turn is 1.5s, below 5s floor
		"wc -c < ": {Stdout: []byte("65536\n")}, // avenc post-encode size check
	}}
	if err := padTurnToMin(context.Background(), ex, "sess", 0, 5.0); err != nil {
		t.Fatalf("padTurnToMin: %v", err)
	}
	if got := ex.commandsContaining("anullsrc=r=44100"); len(got) != 1 {
		t.Errorf("expected 1 anullsrc command, got %d: %v", len(got), got)
	}
	// The deficit should be 5.0 - 1.5 = 3.5s (with floating-point
	// tolerance — we just assert the magnitude is right).
	if got := ex.commandsContaining("-t 3.500"); len(got) != 1 {
		t.Errorf("expected silence segment of 3.500s, got: %v", ex.commandsContaining("-t "))
	}
	if got := ex.commandsContaining("printf"); len(got) != 1 {
		t.Errorf("expected printf concat-list write, got %d: %v", len(got), got)
	}
	if got := ex.commandsContaining("ffmpeg -y -f concat"); len(got) != 1 {
		t.Errorf("expected ffmpeg concat command, got %d: %v", len(got), got)
	}
	if got := ex.commandsContaining("mv "); len(got) != 1 {
		t.Errorf("expected mv to replace original turn, got %d: %v", len(got), got)
	}
}

// TestPadTurnToMin_AboveFloor_NoOp asserts that when the probed
// duration is at or above the floor, no pad commands fire — only
// the ffprobe ran.
func TestPadTurnToMin_AboveFloor_NoOp(t *testing.T) {
	ex := &fakeExecutor{responses: map[string]session.ExecResult{
		"ffprobe": {Stdout: []byte("12.000\n")}, // 12s, well above 5s floor
	}}
	if err := padTurnToMin(context.Background(), ex, "sess", 0, 5.0); err != nil {
		t.Fatalf("padTurnToMin: %v", err)
	}
	if got := ex.commandsContaining("anullsrc"); len(got) != 0 {
		t.Errorf("expected NO anullsrc commands, got %d: %v", len(got), got)
	}
	if got := ex.commandsContaining("ffmpeg -y -f concat"); len(got) != 0 {
		t.Errorf("expected NO concat commands, got %d: %v", len(got), got)
	}
	// Only the ffprobe should have run.
	if len(ex.calls) != 1 {
		t.Errorf("expected 1 exec call (ffprobe only), got %d", len(ex.calls))
	}
}

// TestPadTurnToMin_FfprobeFailure_BestEffort asserts that a failing
// ffprobe doesn't error the caller — we just skip the pad and let
// the unpadded turn through (per design comment in padTurnToMin).
func TestPadTurnToMin_FfprobeFailure_BestEffort(t *testing.T) {
	ex := &fakeExecutor{responses: map[string]session.ExecResult{
		"ffprobe": {ExitCode: 1, Stderr: []byte("corrupt mp3")},
	}}
	if err := padTurnToMin(context.Background(), ex, "sess", 0, 5.0); err != nil {
		t.Errorf("padTurnToMin should be best-effort, got %v", err)
	}
	if got := ex.commandsContaining("anullsrc"); len(got) != 0 {
		t.Errorf("expected NO pad commands when ffprobe fails, got %d", len(got))
	}
}

// TestConcat_DoesNotPostCleanupTempDir pins the v0.28.5 fix. Concat
// used to `rm -rf /tmp/helmdeck-podcast` immediately after reading
// the final.mp3 bytes back, which broke the downstream
// podcast.generate -> av-validate.sh integration: the validator
// looked for /tmp/helmdeck-podcast/final.mp3 a fraction of a second
// later and got "file not found" (exit 2). podcast.generate then
// soft-degraded into silent fallback, the scaffolded-narrated-video
// pipeline saw empty audio_url, and the chain produced a 15-second
// silent MP4. Empirically found 2026-06-15 chasing the v0.28.4
// retest's "validation isn't working" report.
//
// The cleanup at line 84 (Concat's step 1) handles the cross-call
// case: every fresh Concat rm -rfs + mkdirs the tempdir before
// writing turn files. The session container's tmpfs is also
// reclaimed when the session ends. Net: no leak across sessions, no
// leak between Concat calls, AND the file stays available for the
// in-call validation pass.
//
// This test asserts the regression doesn't sneak back in by counting
// post-readback rm -rf calls. Concat issues exactly ONE rm -rf — the
// initial cleanup at step 1. Adding any rm -rf after step 7 (the dd
// readback) breaks podcast.generate's validation; this test will
// trip the moment that happens.
func TestConcat_DoesNotPostCleanupTempDir(t *testing.T) {
	// Stub the readback's dd so Concat returns successfully with a
	// non-empty final.mp3 payload — that's enough to land at step 8
	// (the removed cleanup) without the test caring about turn
	// content. avenc's intermediate ffprobe / ffmpeg / wc calls are
	// stubbed via substring matching.
	ex := &fakeExecutor{responses: map[string]session.ExecResult{
		"dd if=":     {Stdout: []byte("fake-final-mp3-bytes")},
		"ffprobe":    {Stdout: []byte("12.500\n")},
		"wc -c < ":   {Stdout: []byte("65536\n")},
		"anullsrc":   {},
		"cat > ":     {},
		"ffmpeg":     {},
	}}
	turns := [][]byte{[]byte("turn-1 bytes"), []byte("turn-2 bytes")}
	_, _, err := Concat(context.Background(), ex, "sess", turns, ConcatOptions{
		SilenceBetweenTurnsMs: 250,
	})
	if err != nil {
		t.Fatalf("Concat: %v", err)
	}

	// Find the index of the readback exec call (dd if=…).
	readbackIdx := -1
	for i, req := range ex.calls {
		if len(req.Cmd) >= 3 && strings.Contains(req.Cmd[2], "dd if=") {
			readbackIdx = i
			break
		}
	}
	if readbackIdx < 0 {
		t.Fatal("expected a readback (dd if=) exec call; got none")
	}

	// Assert: no `rm -rf` calls appear AFTER the readback.
	// (The initial step-1 rm -rf at the top of Concat is fine.)
	for i, req := range ex.calls {
		if i <= readbackIdx {
			continue
		}
		if len(req.Cmd) >= 3 && strings.Contains(req.Cmd[2], "rm -rf") {
			t.Errorf("Concat ran a post-readback rm -rf at call %d (cmd: %q) — this is the regression that breaks podcast.generate's av-validate.sh integration",
				i, req.Cmd[2])
		}
	}
}
