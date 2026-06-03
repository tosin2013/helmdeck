// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// mockExec is the avenc test executor: a substring-keyed scripted
// responder modelled on slides_narrate_test.go's narrateExecScript,
// reduced to the avenc surface. Goroutine-safe — race-clean test
// runs need the mutex on calls/err.
type mockExec struct {
	mu       sync.Mutex
	calls    []session.ExecRequest
	handlers []handler // matched in order; first matching script wins
	fallback session.ExecResult
}

type handler struct {
	matches func(script string) bool
	respond func(req session.ExecRequest) (session.ExecResult, error)
}

func newMockExec() *mockExec {
	return &mockExec{fallback: session.ExecResult{}}
}

func (m *mockExec) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	m.mu.Unlock()
	script := scriptOf(req)
	for _, h := range m.handlers {
		if h.matches(script) {
			return h.respond(req)
		}
	}
	return m.fallback, nil
}

// on adds a handler that matches when `script` contains `needle`.
// Returns the mock so calls chain.
func (m *mockExec) on(needle string, respond func(req session.ExecRequest) (session.ExecResult, error)) *mockExec {
	m.handlers = append(m.handlers, handler{
		matches: func(script string) bool { return strings.Contains(script, needle) },
		respond: respond,
	})
	return m
}

// stdout adds a happy-exit handler returning the given bytes on stdout.
func (m *mockExec) stdout(needle, out string) *mockExec {
	return m.on(needle, func(req session.ExecRequest) (session.ExecResult, error) {
		return session.ExecResult{Stdout: []byte(out)}, nil
	})
}

// fail adds a non-zero-exit handler with the given stderr.
func (m *mockExec) fail(needle string, exitCode int, stderr string) *mockExec {
	return m.on(needle, func(req session.ExecRequest) (session.ExecResult, error) {
		return session.ExecResult{ExitCode: exitCode, Stderr: []byte(stderr)}, nil
	})
}

// transport adds a transport-error handler — err != nil, zero-value
// ExitCode. This is the most-misleading historical failure shape.
func (m *mockExec) transport(needle, errMsg string) *mockExec {
	return m.on(needle, func(req session.ExecRequest) (session.ExecResult, error) {
		return session.ExecResult{}, errors.New(errMsg)
	})
}

func scriptOf(req session.ExecRequest) string {
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
		return req.Cmd[2]
	}
	return ""
}

func (m *mockExec) scripts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	for i, c := range m.calls {
		out[i] = scriptOf(c)
	}
	return out
}

// --- IsOOMExitCode ---

func TestIsOOMExitCode(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{137, true},  // SIGKILL — the canonical OOM signal
		{0, false},   // success
		{1, false},   // generic failure
		{124, false}, // GNU timeout — separate signal, may add later
		{143, false}, // SIGTERM — separate signal, may add later
		{139, false}, // SIGSEGV — separate signal
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("exit=%d", tc.code), func(t *testing.T) {
			if got := IsOOMExitCode(tc.code); got != tc.want {
				t.Errorf("IsOOMExitCode(%d) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

// --- RequireNonEmptyOutput ---

func TestRequireNonEmptyOutput_Healthy(t *testing.T) {
	exec := newMockExec().stdout("wc -c < ", "65536\n")
	if err := RequireNonEmptyOutput(context.Background(), exec.fn, "/tmp/x", MinEncodedSegmentBytes, "step"); err != nil {
		t.Errorf("healthy stat should pass; got %v", err)
	}
}

func TestRequireNonEmptyOutput_TransportError(t *testing.T) {
	exec := newMockExec().transport("wc -c < ", "docker exec: connection reset")
	err := RequireNonEmptyOutput(context.Background(), exec.fn, "/tmp/x", MinEncodedSegmentBytes, "ffmpeg segment 7")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("expected CodeHandlerFailed; got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("message must surface transport-error nature; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "ffmpeg segment 7") {
		t.Errorf("message must name the step label; got %q", pe.Message)
	}
}

func TestRequireNonEmptyOutput_MissingFile(t *testing.T) {
	exec := newMockExec().fail("wc -c < ", 1, "wc: No such file or directory")
	err := RequireNonEmptyOutput(context.Background(), exec.fn, "/tmp/x", MinEncodedSegmentBytes, "concat")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("expected CodeHandlerFailed; got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "produced no output") {
		t.Errorf("must explain the missing output; got %q", pe.Message)
	}
}

func TestRequireNonEmptyOutput_BelowFloor(t *testing.T) {
	exec := newMockExec().stdout("wc -c < ", "100\n")
	err := RequireNonEmptyOutput(context.Background(), exec.fn, "/tmp/x", MinEncodedSegmentBytes, "ffmpeg concat")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "100 bytes") {
		t.Errorf("must surface actual size; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "ffmpeg concat") {
		t.Errorf("must name the step; got %q", pe.Message)
	}
}

// At-floor boundary — exactly minBytes must pass.
func TestRequireNonEmptyOutput_AtFloorPasses(t *testing.T) {
	exec := newMockExec().stdout("wc -c < ", fmt.Sprintf("%d\n", MinEncodedSegmentBytes))
	if err := RequireNonEmptyOutput(context.Background(), exec.fn, "/tmp/x", MinEncodedSegmentBytes, "boundary"); err != nil {
		t.Errorf("size == floor should pass (< comparison, not <=); got %v", err)
	}
}

// --- LooksLikeMP3 ---

func TestLooksLikeMP3(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"MPEG-1 Layer III 0xFB sync", []byte{0xFF, 0xFB, 0x90, 0x00}, true},
		{"MPEG-1 Layer III 0xFA sync", []byte{0xFF, 0xFA, 0x90, 0x00}, true},
		{"MPEG-2 Layer III 0xF3 sync", []byte{0xFF, 0xF3, 0x90, 0x00}, true},
		{"MPEG-2 Layer III 0xF2 sync", []byte{0xFF, 0xF2, 0x90, 0x00}, true},
		{"ID3v2 tag header", []byte("ID3\x03\x00"), true},
		{"JSON error envelope (the actual provider bug)", []byte(`{"error":"quota exceeded"}`), false},
		{"HTML error page", []byte("<html><body>Service Unavailable"), false},
		{"empty body", []byte{}, false},
		{"single byte", []byte{0xFF}, false},
		{"two bytes (under 3-byte minimum)", []byte{0xFF, 0xFB}, false},
		{"wrong second-byte mask (Layer I/II sync)", []byte{0xFF, 0xE0, 0x00, 0x00}, false},
		{"random garbage", []byte{0x00, 0x00, 0x00}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeMP3(tc.in); got != tc.want {
				t.Errorf("LooksLikeMP3(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// --- ValidateMP3Body ---

func TestValidateMP3Body(t *testing.T) {
	cases := []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{"healthy MP3 (FakeMP3 fixture)", FakeMP3, false},
		{"JSON error envelope in HTTP 200", []byte(`{"error":"quota exceeded","detail":"provider wraps errors as 200"}`), true},
		{"empty body", []byte{}, true},
		{"under floor (256 bytes of valid sync)", append([]byte{0xFF, 0xFB}, make([]byte, 256)...), true},
		{"≥floor but not MP3 (HTML error page)", append([]byte("<html><body>Service Unavailable"), make([]byte, MinTTSResponseBytes+100)...), true},
		{"≥floor MP3 sync — passes", append([]byte{0xFF, 0xFB}, make([]byte, MinTTSResponseBytes)...), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMP3Body(tc.body)
			if (err != nil) != tc.wantErr {
				t.Errorf("wantErr=%v, got err=%v", tc.wantErr, err)
			}
		})
	}
}

// --- ValidateMP4Streams ---

func TestValidateMP4Streams_BothStreamsPresent(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries stream=codec_type", "video\naudio\n")
	if err := ValidateMP4Streams(context.Background(), exec.fn, "/tmp/x.mp4", true, true, "concat"); err != nil {
		t.Errorf("both streams present should pass; got %v", err)
	}
}

func TestValidateMP4Streams_MissingAudio(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries stream=codec_type", "video\n")
	err := ValidateMP4Streams(context.Background(), exec.fn, "/tmp/x.mp4", true, true, "ffmpeg segment 4")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "NO audio stream") {
		t.Errorf("must name the missing audio stream; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "ffmpeg segment 4") {
		t.Errorf("must include label; got %q", pe.Message)
	}
}

func TestValidateMP4Streams_MissingVideo(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries stream=codec_type", "audio\n")
	err := ValidateMP4Streams(context.Background(), exec.fn, "/tmp/x.mp4", true, true, "concat")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "NO video stream") {
		t.Errorf("must name the missing video stream; got %q", pe.Message)
	}
}

func TestValidateMP4Streams_AudioOnlyOK(t *testing.T) {
	// Audio-only output (podcast.mp3 converted to mp4-in-faststart) —
	// caller passes wantVideo=false so missing video does NOT error.
	exec := newMockExec().stdout("ffprobe -v error -show_entries stream=codec_type", "audio\n")
	if err := ValidateMP4Streams(context.Background(), exec.fn, "/tmp/x.mp4", false, true, "audio-only"); err != nil {
		t.Errorf("wantVideo=false should not require video; got %v", err)
	}
}

func TestValidateMP4Streams_TransportError(t *testing.T) {
	exec := newMockExec().transport("ffprobe -v error", "docker exec died")
	err := ValidateMP4Streams(context.Background(), exec.fn, "/tmp/x.mp4", true, true, "concat")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("must surface transport error; got %q", pe.Message)
	}
	if strings.Contains(pe.Message, "exit 0") {
		t.Errorf("must NOT print misleading 'exit 0'; got %q", pe.Message)
	}
}

func TestValidateMP4Streams_FfprobeNonZeroExit(t *testing.T) {
	exec := newMockExec().fail("ffprobe -v error", 1, "moov atom not found")
	err := ValidateMP4Streams(context.Background(), exec.fn, "/tmp/x.mp4", true, true, "concat")
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("ffprobe failure should be CodeHandlerFailed; got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "moov atom not found") {
		t.Errorf("must surface ffprobe stderr; got %q", pe.Message)
	}
}

// Uses LC_ALL=C — the locale-stability gap closer from external research.
func TestValidateMP4Streams_PassesLC_ALLCToFfprobe(t *testing.T) {
	exec := newMockExec().stdout("ffprobe -v error -show_entries stream=codec_type", "video\naudio\n")
	_ = ValidateMP4Streams(context.Background(), exec.fn, "/tmp/x.mp4", true, true, "concat")
	if len(exec.scripts()) == 0 {
		t.Fatal("no exec calls observed")
	}
	if !strings.HasPrefix(exec.scripts()[0], "LC_ALL=C ") {
		t.Errorf("ffprobe invocation must be prefixed with LC_ALL=C for locale stability; got %q", exec.scripts()[0])
	}
}
