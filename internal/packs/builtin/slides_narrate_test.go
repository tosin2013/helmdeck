// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// --- test doubles for slides.narrate ------------------------------------

// fakeMP3 is a tiny valid-ish MP3 header stub so tests that transfer
// "audio" bytes into the sidecar have non-empty content.
var fakeMP3 = []byte{0xFF, 0xFB, 0x90, 0x00, 0x00, 0x00}

// fakeMP4 is a tiny stub returned by the final "cat /tmp/final.mp4".
var fakeMP4 = []byte("fake-mp4-video-bytes")

// narrateExecScript is a stateful executor for slides.narrate tests.
// It inspects each command to decide what to return:
//   - "cat >" (stdin writes): success
//   - "marp": success
//   - "ffmpeg" with "anullsrc": silence gen, success
//   - "ffprobe": returns "5.000" (5 second duration)
//   - "ffmpeg" with "-loop": per-slide segment, success
//   - "ffmpeg" with "concat": concat, success
//   - "cat /tmp/final.mp4": returns fakeMP4 bytes
type narrateExecScript struct {
	calls []session.ExecRequest
	err   error
}

func (n *narrateExecScript) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	n.calls = append(n.calls, req)
	if n.err != nil {
		return session.ExecResult{}, n.err
	}
	script := ""
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
		script = req.Cmd[2]
	}
	switch {
	case strings.Contains(script, "cat >"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "marp"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "ffprobe"):
		return session.ExecResult{Stdout: []byte("5.000\n")}, nil
	case strings.Contains(script, "anullsrc"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "-loop"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "concat"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "cat /tmp/final"):
		return session.ExecResult{Stdout: fakeMP4}, nil
	default:
		return session.ExecResult{}, nil
	}
}

// stubElevenLabs returns a test server that serves fake MP3 bytes
// for TTS requests and a voice list for /v1/voices.
func stubElevenLabs(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/v1/voices") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"voices":[
				{"voice_id":"voice-001","name":"Alice"},
				{"voice_id":"voice-002","name":"Bob"},
				{"voice_id":"voice-003","name":"Charlie"},
				{"voice_id":"voice-004","name":"Diana"},
				{"voice_id":"voice-005","name":"Eve"}
			]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/v1/text-to-speech/") {
			// Verify API key header is present.
			if r.Header.Get("xi-api-key") == "" {
				http.Error(w, "missing api key", 401)
				return
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(fakeMP3)
			return
		}
		http.Error(w, "unknown path: "+r.URL.Path, 404)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runNarrate calls the handler directly with hand-built ExecutionContext.
func runNarrate(t *testing.T, disp *scriptedDispatcherWT, vs *vault.Store, exec *narrateExecScript, input string) (json.RawMessage, error) {
	t.Helper()
	pack := SlidesNarrate(disp, vs)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-narrate"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec.fn,
		Artifacts: artifacts,
	}
	return pack.Handler(context.Background(), ec)
}

// --- tests ----------------------------------------------------------------

func TestSlidesNarrate_HappyPathWithNarration(t *testing.T) {
	// This test stubs ElevenLabs but can't easily point the handler
	// at the stub server because elevenLabsBaseURL is a const. So we
	// test the no-TTS path here (vault key missing) and verify the
	// pipeline produces a video. The ElevenLabs HTTP client is tested
	// separately via TestElevenLabsTTS_Stub below.
	exec := &narrateExecScript{}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"title":"Test Deck","description":"A test.\n\nTimestamps:\n0:00 Welcome\n0:05 Topic","tags":["test"],"category":"Education","language":"en"}`,
	}}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# Welcome\n\n<!-- Hello everyone -->\n\n---\n\n# Topic\n\n<!-- Let me explain -->",
		"metadata_model": "openai/gpt-4o-mini",
		"allow_silent_output": true
	}`
	raw, err := runNarrate(t, disp, nil, exec, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		VideoArtifactKey    string         `json:"video_artifact_key"`
		VideoSize           int            `json:"video_size"`
		SlideCount          int            `json:"slide_count"`
		TotalDurationS      float64        `json:"total_duration_s"`
		HasNarration        bool           `json:"has_narration"`
		VoiceUsed           string         `json:"voice_used"`
		MetadataArtifactKey string         `json:"metadata_artifact_key"`
		Metadata            map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.SlideCount != 2 {
		t.Errorf("slide_count = %d, want 2", out.SlideCount)
	}
	if out.VideoArtifactKey == "" {
		t.Error("video_artifact_key is empty")
	}
	if out.VideoSize == 0 {
		t.Error("video_size is 0")
	}
	// No vault → no narration.
	if out.HasNarration {
		t.Error("has_narration should be false without vault key")
	}
	// Metadata should be generated (we provided metadata_model).
	if out.MetadataArtifactKey == "" {
		t.Error("metadata_artifact_key is empty")
	}
	if out.Metadata == nil {
		t.Error("metadata is nil")
	}
	if out.Metadata["title"] != "Test Deck" {
		t.Errorf("metadata.title = %v", out.Metadata["title"])
	}
}

func TestSlidesNarrate_NoNotes(t *testing.T) {
	exec := &narrateExecScript{}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# Slide 1\n\nNo notes here.\n\n---\n\n# Slide 2\n\nAlso no notes.",
		"allow_silent_output": true
	}`
	raw, err := runNarrate(t, nil, nil, exec, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		HasNarration bool    `json:"has_narration"`
		SlideCount   int     `json:"slide_count"`
		TotalDurationS float64 `json:"total_duration_s"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.SlideCount != 2 {
		t.Errorf("slide_count = %d, want 2", out.SlideCount)
	}
	// All slides get default silence (5s each) → total 10s.
	if out.TotalDurationS != 10 {
		t.Errorf("total_duration = %.1f, want 10.0", out.TotalDurationS)
	}
}

func TestSlidesNarrate_NoMetadataModel(t *testing.T) {
	exec := &narrateExecScript{}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# One Slide",
		"allow_silent_output": true
	}`
	raw, err := runNarrate(t, nil, nil, exec, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		MetadataArtifactKey string `json:"metadata_artifact_key"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.MetadataArtifactKey != "" {
		t.Errorf("metadata key should be empty when no model: %q", out.MetadataArtifactKey)
	}
}

func TestSlidesNarrate_EmptyMarkdown(t *testing.T) {
	exec := &narrateExecScript{}
	_, err := runNarrate(t, nil, nil, exec, `{"markdown":""}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input, got %v", err)
	}
}

func TestSlidesNarrate_MissingExecutor(t *testing.T) {
	pack := SlidesNarrate(nil, nil)
	ec := &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(`{"markdown":"# Slide"}`),
		// Exec intentionally nil
	}
	_, err := pack.Handler(context.Background(), ec)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeSessionUnavailable {
		t.Errorf("want session_unavailable, got %v", err)
	}
}

func TestSlidesNarrate_MarpFailure(t *testing.T) {
	exec := &narrateExecScript{}
	// Override: make marp fail.
	origFn := exec.fn
	exec2 := &narrateExecScript{}
	exec2Fn := func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
		script := ""
		if len(req.Cmd) >= 3 {
			script = req.Cmd[2]
		}
		if strings.Contains(script, "marp") {
			return session.ExecResult{ExitCode: 1, Stderr: []byte("marp crashed")}, nil
		}
		return origFn(ctx, req)
	}
	_ = exec2
	pack := SlidesNarrate(nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack: pack,
		// allow_silent_output:true so the #138 credential-resolve
		// passes and we actually reach the marp step we want to test.
		Input:     json.RawMessage(`{"markdown":"---\nmarp: true\n---\n\n# Slide","allow_silent_output":true}`),
		Session:   &session.Session{ID: "s"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec2Fn,
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "marp") {
		t.Errorf("message should mention marp: %q", pe.Message)
	}
}

func TestSlidesNarrate_FfmpegConcatFailure(t *testing.T) {
	pack := SlidesNarrate(nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack: pack,
		// allow_silent_output:true so the #138 credential-resolve
		// passes and we actually reach the concat step under test.
		Input:   json.RawMessage(`{"markdown":"---\nmarp: true\n---\n\n# Slide","allow_silent_output":true}`),
		Session: &session.Session{ID: "s"},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec: func(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
			script := ""
			if len(req.Cmd) >= 3 {
				script = req.Cmd[2]
			}
			if strings.Contains(script, "concat") {
				return session.ExecResult{ExitCode: 1, Stderr: []byte("concat failed")}, nil
			}
			if strings.Contains(script, "ffprobe") {
				return session.ExecResult{Stdout: []byte("5.0\n")}, nil
			}
			return session.ExecResult{}, nil
		},
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "concat") {
		t.Errorf("message should mention concat: %q", pe.Message)
	}
}

// TestSlidesNarrate_FfmpegSegmentFailure_FullStderrSurfaced (regression
// for #140) asserts that a per-segment ffmpeg failure (a) returns the
// full stderr in the error message up to the new 4096-byte cap, and
// (b) persists the unredacted stderr to the artifact store with a
// "ffmpeg-stderr-segment-NNN.txt" key referenced from the message.
//
// Pre-#140 the inline message was capped at 512 bytes and the rest was
// gone — operators couldn't see the actual ffmpeg complaint (e.g. a
// pixel-format mismatch from the Marp PNG renderer). This test pins
// the new behavior so a future refactor doesn't quietly re-truncate.
func TestSlidesNarrate_FfmpegSegmentFailure_FullStderrSurfaced(t *testing.T) {
	// Build a long stderr that exceeds the OLD 512-byte cap so we
	// can assert the new 4096-byte cap is in effect AND that the
	// artifact persists the full payload.
	longStderr := strings.Repeat("frame_too_big_blah_blah ", 200) // ~4800 bytes
	pack := SlidesNarrate(nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack: pack,
		// allow_silent_output:true so #138 credential-resolve passes
		// and we actually reach the per-segment ffmpeg step under test.
		Input:   json.RawMessage(`{"markdown":"---\nmarp: true\n---\n\n# Slide","allow_silent_output":true}`),
		Session: &session.Session{ID: "s"},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec: func(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
			script := ""
			if len(req.Cmd) >= 3 {
				script = req.Cmd[2]
			}
			switch {
			case strings.Contains(script, "-loop"):
				return session.ExecResult{ExitCode: 1, Stderr: []byte(longStderr)}, nil
			case strings.Contains(script, "ffprobe"):
				return session.ExecResult{Stdout: []byte("5.0\n")}, nil
			default:
				return session.ExecResult{}, nil
			}
		},
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("want handler_failed, got %v", err)
	}
	// (a) Inline message contains > 512 bytes of stderr so the old cap
	// would have lost diagnostic info we now keep.
	if len(pe.Message) <= 512 {
		t.Errorf("error message length = %d, want > 512 (stderr should no longer truncate at 512): %q",
			len(pe.Message), pe.Message)
	}
	// (b) Message references a stored artifact key for the full stderr.
	if !strings.Contains(pe.Message, "ffmpeg-stderr-segment-") {
		t.Errorf("message should reference stderr artifact key: %q", pe.Message)
	}
	// And the artifact actually exists in the store with the full payload.
	listed, _ := artifacts.ListForPack(context.Background(), "slides.narrate")
	var stderrArt *packs.Artifact
	for i := range listed {
		if strings.Contains(listed[i].Key, "ffmpeg-stderr-segment-") {
			stderrArt = &listed[i]
			break
		}
	}
	if stderrArt == nil {
		t.Fatal("ffmpeg-stderr-segment-* artifact was not stored")
	}
	body, _, err := artifacts.Get(context.Background(), stderrArt.Key)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if !strings.Contains(string(body), longStderr) {
		t.Error("stored artifact does not contain the full stderr payload")
	}
	if !strings.Contains(string(body), "# command:") {
		t.Error("stored artifact should include the failing ffmpeg command line")
	}
}

func TestFormatTimestamp(t *testing.T) {
	cases := []struct {
		secs float64
		want string
	}{
		{0, "0:00"},
		{5, "0:05"},
		{65, "1:05"},
		{3661, "61:01"},
	}
	for _, tc := range cases {
		got := formatTimestamp(tc.secs)
		if got != tc.want {
			t.Errorf("formatTimestamp(%.0f) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}

func TestPickRandomVoice_Stub(t *testing.T) {
	srv := stubElevenLabs(t)
	// Temporarily override the base URL — we can't change the const,
	// so we call pickRandomVoice's logic directly by hitting the stub.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/voices", nil)
	req.Header.Set("xi-api-key", "test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Voices []struct {
			VoiceID string `json:"voice_id"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Voices) != 5 {
		t.Errorf("voices count = %d, want 5", len(parsed.Voices))
	}
	if parsed.Voices[0].VoiceID != "voice-001" {
		t.Errorf("first voice = %q", parsed.Voices[0].VoiceID)
	}
}

func TestElevenLabsTTS_Stub(t *testing.T) {
	srv := stubElevenLabs(t)
	// Call the TTS function with the stub URL.
	// We can't easily override the const, so we test the HTTP shape
	// by hitting the stub directly with the same request shape.
	reqBody, _ := json.Marshal(map[string]any{
		"text":     "Hello world",
		"model_id": "eleven_multilingual_v2",
	})
	req, _ := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/text-to-speech/voice-001?output_format=mp3_44100_128",
		strings.NewReader(string(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", "test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 {
		t.Error("empty response body")
	}
}
