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
// + mv) fires when the probed duration is under the floor.
func TestPadTurnToMin_BelowFloor_TriggersPadPipeline(t *testing.T) {
	ex := &fakeExecutor{responses: map[string]session.ExecResult{
		"ffprobe": {Stdout: []byte("1.500\n")}, // turn is 1.5s, below 5s floor
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
