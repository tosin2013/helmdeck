// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func defaultEncOpts() EncodeSegmentOpts {
	return EncodeSegmentOpts{VideoFilter: "scale=1920:1080"}
}

// --- happy path / defaults / output validation ---

func TestEncodeVideoSegment_HappyPath(t *testing.T) {
	exec := newMockExec().
		on("-loop 1 -i", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	err := EncodeVideoSegment(context.Background(), exec.fn,
		"/tmp/img.png", "/tmp/aud.mp3", "/tmp/seg.mp4",
		defaultEncOpts(), silentLogger(), "ffmpeg segment 0")
	if err != nil {
		t.Fatalf("happy path returned %v", err)
	}
	scripts := exec.scripts()
	// Should be exactly 1 ffmpeg + 1 wc -c.
	if len(scripts) != 2 {
		t.Errorf("expected 2 execs (ffmpeg + wc -c); got %d (%v)", len(scripts), scripts)
	}
	if !strings.Contains(scripts[0], "-threads 4") {
		t.Errorf("default Threads must be 4; got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-b:a 192k") {
		t.Errorf("default AudioBitrate must be 192k; got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-ar 44100") {
		t.Errorf("per-segment encode must pin -ar 44100 (matches the TTS source so libswresample doesn't introduce 44100→48000 aliasing); got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-tune stillimage") {
		t.Errorf("must use stillimage tune for static-slide encode; got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-shortest") {
		t.Errorf("must use -shortest to cap encode at audio duration; got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-pix_fmt yuv420p") {
		t.Errorf("must force yuv420p for QuickTime compatibility; got %q", scripts[0])
	}
}

func TestEncodeVideoSegment_CustomThreadsAndPreset(t *testing.T) {
	exec := newMockExec().
		on("-loop 1 -i", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	opts := defaultEncOpts()
	opts.Threads = "2"
	opts.Preset = "slow"
	_ = EncodeVideoSegment(context.Background(), exec.fn,
		"/tmp/img.png", "/tmp/aud.mp3", "/tmp/seg.mp4",
		opts, silentLogger(), "test")
	scripts := exec.scripts()
	if !strings.Contains(scripts[0], "-threads 2") {
		t.Errorf("custom Threads not propagated; got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-preset slow") {
		t.Errorf("custom Preset not propagated; got %q", scripts[0])
	}
}

// --- OOM retry pattern (the PR #390 fix) ---

func TestEncodeVideoSegment_OOMRetryFiresAndSucceeds(t *testing.T) {
	// Primary attempt OOMs (exit 137); retry succeeds (exit 0).
	// MUST fire exactly twice with the second attempt using
	// -threads 1 -preset veryfast.
	var mu sync.Mutex
	var calls []string
	exec := newMockExec().
		on("-loop 1 -i", func(req session.ExecRequest) (session.ExecResult, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, scriptOf(req))
			if len(calls) == 1 {
				return session.ExecResult{ExitCode: 137, Stderr: []byte("Killed")}, nil
			}
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	err := EncodeVideoSegment(context.Background(), exec.fn,
		"/tmp/img.png", "/tmp/aud.mp3", "/tmp/seg.mp4",
		defaultEncOpts(), silentLogger(), "ffmpeg segment 7")
	if err != nil {
		t.Fatalf("retry path returned %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 ffmpeg attempts (primary + 1 retry); got %d", len(calls))
	}
	// Primary uses default Threads (4).
	if !strings.Contains(calls[0], "-threads 4") {
		t.Errorf("primary must use Threads=4; got %q", calls[0])
	}
	// Retry uses degraded settings.
	if !strings.Contains(calls[1], "-threads 1") {
		t.Errorf("retry must use Threads=1; got %q", calls[1])
	}
	if !strings.Contains(calls[1], "-preset veryfast") {
		t.Errorf("retry must use veryfast preset; got %q", calls[1])
	}
}

func TestEncodeVideoSegment_DoubleOOMSurfacesCodeResourceExhausted(t *testing.T) {
	// Both primary AND retry OOM. Helper MUST surface
	// CodeResourceExhausted (not CodeHandlerFailed) and MUST NOT
	// escalate to a third attempt.
	var attempts int
	exec := newMockExec().on("-loop 1 -i", func(req session.ExecRequest) (session.ExecResult, error) {
		attempts++
		return session.ExecResult{ExitCode: 137, Stderr: []byte("Killed")}, nil
	})
	err := EncodeVideoSegment(context.Background(), exec.fn,
		"/tmp/img.png", "/tmp/aud.mp3", "/tmp/seg.mp4",
		defaultEncOpts(), silentLogger(), "ffmpeg segment 4")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeResourceExhausted {
		t.Errorf("double-OOM must surface CodeResourceExhausted; got %s", pe.Code)
	}
	if attempts != 2 {
		t.Errorf("must bound retries at 1 (2 attempts total); got %d", attempts)
	}
	if !strings.Contains(pe.Message, "bump SessionSpec.MemoryLimit") {
		t.Errorf("message must give the operator a recovery hint; got %q", pe.Message)
	}
}

// Transport errors are NOT retried — they're infrastructural, not
// memory-pressure. Retry would just compound the failure.
func TestEncodeVideoSegment_TransportErrorIsNotRetried(t *testing.T) {
	var attempts int
	exec := newMockExec().on("-loop 1 -i", func(req session.ExecRequest) (session.ExecResult, error) {
		attempts++
		return session.ExecResult{}, errors.New("docker exec died")
	})
	err := EncodeVideoSegment(context.Background(), exec.fn,
		"/tmp/img.png", "/tmp/aud.mp3", "/tmp/seg.mp4",
		defaultEncOpts(), silentLogger(), "ffmpeg segment 0")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("must surface transport error; got %q", pe.Message)
	}
	if attempts != 1 {
		t.Errorf("transport errors must NOT trigger retry; got %d attempts", attempts)
	}
}

// Generic non-OOM exit codes (e.g. exit 1 = bad input) are NOT
// retried either — retry won't fix a malformed input PNG.
func TestEncodeVideoSegment_GenericExitErrorIsNotRetried(t *testing.T) {
	var attempts int
	exec := newMockExec().on("-loop 1 -i", func(req session.ExecRequest) (session.ExecResult, error) {
		attempts++
		return session.ExecResult{ExitCode: 1, Stderr: []byte("Invalid argument")}, nil
	})
	err := EncodeVideoSegment(context.Background(), exec.fn,
		"/tmp/img.png", "/tmp/aud.mp3", "/tmp/seg.mp4",
		defaultEncOpts(), silentLogger(), "ffmpeg segment 0")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("non-OOM exit must be CodeHandlerFailed; got %s", pe.Code)
	}
	if attempts != 1 {
		t.Errorf("non-OOM exits must NOT trigger retry; got %d attempts", attempts)
	}
}

// Post-encode size validation: ffmpeg exit 0 but the segment file is
// 0 bytes — the canonical silent-failure shape. Must surface as
// CodeHandlerFailed naming the segment, not propagate as success.
func TestEncodeVideoSegment_PostValidateCatchesEmpty(t *testing.T) {
	exec := newMockExec().
		on("-loop 1 -i", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "0\n")
	err := EncodeVideoSegment(context.Background(), exec.fn,
		"/tmp/img.png", "/tmp/aud.mp3", "/tmp/seg.mp4",
		defaultEncOpts(), silentLogger(), "ffmpeg segment 0")
	if err == nil {
		t.Fatal("expected error on 0-byte segment; got nil")
	}
	if !strings.Contains(err.Error(), "0 bytes") {
		t.Errorf("must surface the actual size; got %q", err.Error())
	}
}
