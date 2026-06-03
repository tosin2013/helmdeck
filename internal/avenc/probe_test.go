// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"strings"
	"testing"
)

func TestProbeAudioDuration_Healthy(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "3.14\n")
	dur, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err != nil {
		t.Fatalf("expected nil; got %v", err)
	}
	if dur != 3.14 {
		t.Errorf("dur = %v; want 3.14", dur)
	}
}

// Critical pin: every ffprobe invocation must prefix LC_ALL=C so a
// sidecar with LC_NUMERIC=de_DE doesn't emit "3,14" and silently
// parse to 0. External-research gap from the plan file.
func TestProbeAudioDuration_PrefixesLC_ALLC(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "3.14\n")
	_, _ = ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if len(exec.scripts()) != 1 {
		t.Fatalf("expected 1 exec call; got %d", len(exec.scripts()))
	}
	if !strings.HasPrefix(exec.scripts()[0], "LC_ALL=C ") {
		t.Errorf("ffprobe invocation must start with LC_ALL=C; got %q", exec.scripts()[0])
	}
}

func TestProbeAudioDuration_RejectsNaN(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "NaN\n")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on NaN; got nil")
	}
	if !strings.Contains(err.Error(), "NaN") && !strings.Contains(err.Error(), "non-positive") {
		t.Errorf("must explain NaN/non-positive rejection; got %q", err.Error())
	}
}

func TestProbeAudioDuration_RejectsPositiveInfinity(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "+Inf\n")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on +Inf; got nil")
	}
}

func TestProbeAudioDuration_RejectsNegativeInfinity(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "-Inf\n")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on -Inf; got nil")
	}
}

func TestProbeAudioDuration_RejectsZero(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "0\n")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on 0; got nil")
	}
}

func TestProbeAudioDuration_RejectsNegative(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "-1.5\n")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on negative; got nil")
	}
}

func TestProbeAudioDuration_TransportError(t *testing.T) {
	exec := newMockExec().transport("ffprobe -v error", "docker exec died")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on transport failure; got nil")
	}
	if !strings.Contains(err.Error(), "transport error") {
		t.Errorf("must name the transport failure; got %q", err.Error())
	}
}

func TestProbeAudioDuration_NonZeroExit(t *testing.T) {
	exec := newMockExec().fail("ffprobe -v error", 1, "no such file")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on non-zero exit; got nil")
	}
	if !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("must surface real exit code; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("must surface stderr; got %q", err.Error())
	}
}

func TestProbeAudioDuration_GarbageStdout(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "not-a-number\n")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected error on garbage stdout; got nil")
	}
	if !strings.Contains(err.Error(), "parse duration") {
		t.Errorf("must name parse failure; got %q", err.Error())
	}
}

// Pin the "valid float with comma decimal separator" case: when
// LC_ALL=C is correctly applied, ffprobe will NOT emit "3,14" — but
// if a future refactor drops the prefix, the parser will reject the
// comma form correctly (strconv expects period). This test
// documents that the locale prefix is the only guard.
func TestProbeAudioDuration_RejectsCommaDecimalAsParseFailure(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries format=duration", "3,14\n")
	_, err := ProbeAudioDuration(context.Background(), exec.fn, "/tmp/a.mp3")
	if err == nil {
		t.Fatal("expected parse error on locale-mangled duration; got nil")
	}
}
