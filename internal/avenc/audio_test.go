// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// --- GenerateSilence ---

func TestGenerateSilence_Healthy(t *testing.T) {
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil // exit 0
		}).
		stdout("wc -c < ", "1024\n") // post-validate passes
	if err := GenerateSilence(context.Background(), exec.fn, 5.0, "/tmp/sil.mp3", "test"); err != nil {
		t.Errorf("healthy path returned %v", err)
	}
	// First call must be the ffmpeg anullsrc; second call is the
	// wc-c post-validation. Exactly 2 ffmpeg-related execs.
	scripts := exec.scripts()
	if len(scripts) != 2 {
		t.Fatalf("expected 2 execs (ffmpeg + wc -c); got %d (%v)", len(scripts), scripts)
	}
	if !strings.Contains(scripts[0], "anullsrc") {
		t.Errorf("first exec must be the silence generation; got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "libmp3lame") {
		t.Errorf("silence must encode with libmp3lame; got %q", scripts[0])
	}
}

func TestGenerateSilence_TransportError(t *testing.T) {
	exec := newMockExec().transport("anullsrc", "docker exec died")
	err := GenerateSilence(context.Background(), exec.fn, 5.0, "/tmp/sil.mp3", "slide 4")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("must surface transport error; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "slide 4") {
		t.Errorf("must include label; got %q", pe.Message)
	}
}

func TestGenerateSilence_NonZeroExitOOM(t *testing.T) {
	exec := newMockExec().fail("anullsrc", 137, "Killed")
	err := GenerateSilence(context.Background(), exec.fn, 5.0, "/tmp/sil.mp3", "slide 9")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeResourceExhausted {
		t.Errorf("exit 137 must lift to CodeResourceExhausted; got %s", pe.Code)
	}
}

func TestGenerateSilence_NonZeroExitGeneric(t *testing.T) {
	exec := newMockExec().fail("anullsrc", 1, "bad codec")
	err := GenerateSilence(context.Background(), exec.fn, 5.0, "/tmp/sil.mp3", "slide 0")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("generic exit must be CodeHandlerFailed; got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "bad codec") {
		t.Errorf("must surface stderr; got %q", pe.Message)
	}
}

func TestGenerateSilence_PostValidateCatchesEmpty(t *testing.T) {
	// ffmpeg succeeds (exit 0) but the produced file is 0 bytes —
	// the canonical silent-failure shape.
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "0\n")
	err := GenerateSilence(context.Background(), exec.fn, 5.0, "/tmp/sil.mp3", "slide 7")
	if err == nil {
		t.Fatal("expected error on 0-byte silence; got nil")
	}
	if !strings.Contains(err.Error(), "slide 7") {
		t.Errorf("must name the slot; got %q", err.Error())
	}
}

// --- ConcatAudio ---

func TestConcatAudio_HealthyWithLibmp3lame(t *testing.T) {
	exec := newMockExec().
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	err := ConcatAudio(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/out.mp3", ConcatAudioOpts{}, "podcast")
	if err != nil {
		t.Errorf("healthy concat returned %v", err)
	}
	scripts := exec.scripts()
	if len(scripts) == 0 || !strings.Contains(scripts[0], "libmp3lame") {
		t.Errorf("ConcatAudio default codec must be libmp3lame; got %q", scripts)
	}
	if !strings.Contains(scripts[0], "-b:a 128k") {
		t.Errorf("ConcatAudio default bitrate must be 128k; got %q", scripts[0])
	}
}

func TestConcatAudio_CustomCodecAndBitrate(t *testing.T) {
	exec := newMockExec().
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	_ = ConcatAudio(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/out.aac",
		ConcatAudioOpts{Codec: "aac", BitrateKbps: 192}, "test")
	scripts := exec.scripts()
	if !strings.Contains(scripts[0], "-acodec aac") {
		t.Errorf("custom codec not propagated; got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-b:a 192k") {
		t.Errorf("custom bitrate not propagated; got %q", scripts[0])
	}
}

func TestConcatAudio_RequiresReencode(t *testing.T) {
	// Critical regression guard for PR #404: the command MUST include
	// an explicit -acodec flag (i.e. re-encode), NOT `-c copy`.
	// stream-copy reintroduces AAC frame-boundary dropouts.
	exec := newMockExec().
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	_ = ConcatAudio(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/out.mp3", ConcatAudioOpts{}, "test")
	scripts := exec.scripts()
	if strings.Contains(scripts[0], "-c copy") {
		t.Errorf("ConcatAudio must NOT use -c copy (reintroduces frame-boundary dropouts); got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-acodec ") {
		t.Errorf("ConcatAudio must explicitly set -acodec (re-encode); got %q", scripts[0])
	}
}

func TestConcatAudio_TransportError(t *testing.T) {
	exec := newMockExec().transport("ffmpeg -y -f concat", "docker exec died")
	err := ConcatAudio(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/out.mp3", ConcatAudioOpts{}, "podcast")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("must surface transport error; got %q", pe.Message)
	}
	if strings.Contains(pe.Message, "exit 0") {
		t.Errorf("must NOT say misleading 'exit 0'; got %q", pe.Message)
	}
}

// --- ConcatVideoMP4s ---

// THE PR #404 regression guard: video stream-copy + audio re-encode.
func TestConcatVideoMP4s_VideoStreamCopyAudioReencode(t *testing.T) {
	exec := newMockExec().
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	_ = ConcatVideoMP4s(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/final.mp4", ConcatVideoMP4sOpts{}, "narrate")
	scripts := exec.scripts()
	if len(scripts) == 0 {
		t.Fatal("no exec calls")
	}
	if !strings.Contains(scripts[0], "-c:v copy") {
		t.Errorf("ConcatVideoMP4s must keep video stream-copy (-c:v copy); got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-c:a aac") {
		t.Errorf("ConcatVideoMP4s must re-encode audio (-c:a aac); got %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "-b:a 192k") {
		t.Errorf("ConcatVideoMP4s default audio bitrate must be 192k; got %q", scripts[0])
	}
	if strings.Contains(scripts[0], "-c copy ") || strings.HasSuffix(scripts[0], "-c copy") {
		t.Errorf("ConcatVideoMP4s must NOT use legacy `-c copy` (stream-copies both streams, reintroduces dropouts); got %q", scripts[0])
	}
}

func TestConcatVideoMP4s_CustomAudioBitrate(t *testing.T) {
	exec := newMockExec().
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "65536\n")
	_ = ConcatVideoMP4s(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/final.mp4",
		ConcatVideoMP4sOpts{AudioBitrateKbps: 128}, "narrate")
	scripts := exec.scripts()
	if !strings.Contains(scripts[0], "-b:a 128k") {
		t.Errorf("custom audio bitrate not propagated; got %q", scripts[0])
	}
}

func TestConcatVideoMP4s_TransportError(t *testing.T) {
	exec := newMockExec().transport("ffmpeg -y -f concat", "docker exec died")
	err := ConcatVideoMP4s(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/final.mp4", ConcatVideoMP4sOpts{}, "narrate")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("must surface transport error; got %q", pe.Message)
	}
}

func TestConcatVideoMP4s_OOMLifted(t *testing.T) {
	exec := newMockExec().fail("ffmpeg -y -f concat", 137, "Killed")
	err := ConcatVideoMP4s(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/final.mp4", ConcatVideoMP4sOpts{}, "narrate")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeResourceExhausted {
		t.Errorf("exit 137 must lift to CodeResourceExhausted; got %s", pe.Code)
	}
}

func TestConcatVideoMP4s_PostValidateCatchesEmpty(t *testing.T) {
	exec := newMockExec().
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "0\n")
	err := ConcatVideoMP4s(context.Background(), exec.fn, "/tmp/list.txt", "/tmp/final.mp4", ConcatVideoMP4sOpts{}, "narrate")
	if err == nil {
		t.Fatal("expected error on 0-byte concat output; got nil")
	}
	if !strings.Contains(err.Error(), "0 bytes") {
		t.Errorf("must surface size; got %q", err.Error())
	}
}

// --- PadAudioToMin ---

func TestPadAudioToMin_NoOpWhenAlreadyAboveFloor(t *testing.T) {
	exec := newMockExec()
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 5.5, 5.0, "test")
	if err != nil {
		t.Errorf("no-op path returned %v", err)
	}
	if len(exec.scripts()) != 0 {
		t.Errorf("no-op must not exec anything; got %d calls", len(exec.scripts()))
	}
}

func TestPadAudioToMin_NoOpAtEpsilonBoundary(t *testing.T) {
	// Within the 1ms epsilon — must NOT trigger a pad cycle.
	exec := newMockExec()
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 4.9995, 5.0, "test")
	if err != nil {
		t.Errorf("epsilon-boundary returned %v", err)
	}
	if len(exec.scripts()) != 0 {
		t.Errorf("epsilon-boundary must not exec; got %d calls", len(exec.scripts()))
	}
}

func TestPadAudioToMin_HappyPath4Steps(t *testing.T) {
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "1024\n"). // both the silence post-check AND concat post-check
		on("printf ", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		on("mv ", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		})
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 2.0, 5.0, "test")
	if err != nil {
		t.Fatalf("happy pad returned %v", err)
	}
	// Steps:
	//  silence-gen (anullsrc) + post-validate (wc -c)
	//  printf write-list
	//  ConcatAudio (ffmpeg -y -f concat) + post-validate (wc -c)
	//  mv
	scripts := exec.scripts()
	if len(scripts) != 6 {
		t.Errorf("expected 6 execs (silence + wc + printf + concat + wc + mv); got %d (%v)",
			len(scripts), scripts)
	}
}

func TestPadAudioToMin_StopsAtSilenceFailure(t *testing.T) {
	exec := newMockExec().fail("anullsrc", 1, "bad arg")
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 2.0, 5.0, "test")
	if err == nil {
		t.Fatal("expected error on silence failure; got nil")
	}
	// MUST stop at the silence step — no printf / concat / mv after.
	if len(exec.scripts()) != 1 {
		t.Errorf("expected 1 exec before short-circuit; got %d (%v)", len(exec.scripts()), exec.scripts())
	}
}

func TestPadAudioToMin_StopsAtConcatFailure(t *testing.T) {
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "1024\n"). // silence post-check ok
		on("printf ", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		fail("ffmpeg -y -f concat", 1, "concat failed")
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 2.0, 5.0, "test")
	if err == nil {
		t.Fatal("expected error on concat failure; got nil")
	}
	// No mv must run after concat fails.
	for _, s := range exec.scripts() {
		if strings.HasPrefix(s, "mv ") {
			t.Errorf("mv must NOT run after concat fails; got %q", s)
		}
	}
}

func TestPadAudioToMin_PrintfTransportError(t *testing.T) {
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "1024\n").
		transport("printf ", "docker exec died on write-list")
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 2.0, 5.0, "test")
	if err == nil {
		t.Fatal("expected error on printf transport failure; got nil")
	}
	if !strings.Contains(err.Error(), "transport error") {
		t.Errorf("must surface transport error; got %q", err.Error())
	}
}

func TestPadAudioToMin_PrintfNonZeroExit(t *testing.T) {
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "1024\n").
		fail("printf ", 1, "disk full")
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 2.0, 5.0, "test")
	if err == nil {
		t.Fatal("expected error on printf non-zero exit; got nil")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("must surface stderr; got %q", err.Error())
	}
}

func TestPadAudioToMin_MvTransportError(t *testing.T) {
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "1024\n").
		on("printf ", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		transport("mv ", "docker exec died on mv")
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 2.0, 5.0, "test")
	if err == nil {
		t.Fatal("expected error on mv transport failure; got nil")
	}
	if !strings.Contains(err.Error(), "transport error") {
		t.Errorf("must surface transport error; got %q", err.Error())
	}
}

func TestPadAudioToMin_MvNonZeroExit(t *testing.T) {
	exec := newMockExec().
		on("anullsrc", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		stdout("wc -c < ", "1024\n").
		on("printf ", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		on("ffmpeg -y -f concat", func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{}, nil
		}).
		fail("mv ", 1, "Permission denied")
	err := PadAudioToMin(context.Background(), exec.fn, "/tmp/a.mp3", "/tmp", "0", 2.0, 5.0, "test")
	if err == nil {
		t.Fatal("expected error on mv non-zero exit; got nil")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("must surface stderr; got %q", err.Error())
	}
}
