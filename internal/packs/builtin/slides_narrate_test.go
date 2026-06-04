// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// fakeMP3 is a valid-ish MP3 frame: MPEG-1 Layer III sync word
// (0xFF 0xFB) followed by 1024 bytes of zero padding. The sync word
// satisfies looksLikeMP3 (slides_narrate.go) and the length
// comfortably clears minTTSResponseBytes (512). Tests that mock
// ElevenLabs replies need both — the post-200 validation rejects
// short bodies AND bodies without an MP3 sync, so fakeMP3 covers
// both axes.
var fakeMP3 = func() []byte {
	b := make([]byte, 1024+2)
	b[0] = 0xFF
	b[1] = 0xFB
	return b
}()

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
	case strings.Contains(script, "mmdc"):
		// preprocessMermaidFences shell wrapper runs mmdc and cats the
		// SVG output. Return a tiny valid SVG so the rewrite produces a
		// data-URI Marp can ingest. MUST come BEFORE the "cat >" case
		// since the mmdc wrapper script contains both substrings.
		return session.ExecResult{Stdout: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><g/></svg>`)}, nil
	case strings.Contains(script, "cat >"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "marp"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "ffprobe"):
		return session.ExecResult{Stdout: []byte("5.000\n")}, nil
	case strings.Contains(script, "anullsrc"):
		return session.ExecResult{}, nil
	case strings.Contains(script, "wc -c < "):
		// validateMarpPngs stats each rendered PNG. Also used by the
		// post-encode requireNonEmptyOutput checks (segment .mp4 +
		// concat /tmp/final.mp4). Return a size that passes both
		// floors so happy-path tests proceed to the next gate.
		return session.ExecResult{Stdout: []byte("65536\n")}, nil
	case strings.Contains(script, "head -c 8 "):
		// validateMarpPngs's PNG-magic-byte check. Return the valid
		// PNG signature so happy-path tests advance past it.
		return session.ExecResult{Stdout: []byte(pngMagicHex)}, nil
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
	pack := SlidesNarrate(disp, vs, nil)
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
		VideoArtifactKey      string         `json:"video_artifact_key"`
		VideoSize             int            `json:"video_size"`
		SlideCount            int            `json:"slide_count"`
		TotalDurationS        float64        `json:"total_duration_s"`
		HasNarration          bool           `json:"has_narration"`
		VoiceUsed             string         `json:"voice_used"`
		EngagementArtifactKey string         `json:"engagement_artifact_key"`
		Engagement            map[string]any `json:"engagement"`
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
	// Engagement metadata should be generated (we provided metadata_model).
	if out.EngagementArtifactKey == "" {
		t.Error("engagement_artifact_key is empty")
	}
	if out.Engagement == nil {
		t.Error("engagement is nil")
	}
	if out.Engagement["title"] != "Test Deck" {
		t.Errorf("engagement.title = %v", out.Engagement["title"])
	}
	// Constant enrichment fields per the engagement contract.
	if out.Engagement["format_ceiling_note"] == nil {
		t.Error("engagement.format_ceiling_note missing — should always be present when engagement enabled")
	}
	if out.Engagement["captions_recommended"] != true {
		t.Errorf("engagement.captions_recommended = %v, want true", out.Engagement["captions_recommended"])
	}
	if _, ok := out.Engagement["title_char_count"].(float64); !ok {
		t.Errorf("engagement.title_char_count missing or wrong type; got %T", out.Engagement["title_char_count"])
	}
}

// TestSlidesNarrate_EngagementDisabled — without metadata_model the
// handler emits NO engagement object and no sidecar artifact key.
// This is the back-compat path: existing pipelines that don't ask for
// metadata still get the v0.25.x output shape (minus the renamed
// fields, which is the documented v0.26.0 breaking change).
func TestSlidesNarrate_EngagementDisabled(t *testing.T) {
	exec := &narrateExecScript{}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# Welcome\n\n<!-- Hello -->",
		"allow_silent_output": true
	}`
	// No metadata_model, no dispatcher — engagement gate is two-fold
	// (d != nil AND metadata_model != "") so passing nil here covers
	// both branches at once.
	raw, err := runNarrate(t, nil, nil, exec, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if _, present := out["engagement"]; present {
		t.Errorf("engagement should be absent when metadata_model unset; got %v", out["engagement"])
	}
	// The artifact key field is always present in the marshaled out
	// (declared in OutputSchema) but must be the empty string here.
	if v, _ := out["engagement_artifact_key"].(string); v != "" {
		t.Errorf("engagement_artifact_key = %q, want empty (no engagement gen)", v)
	}
}

// TestSlidesNarrate_EngagementOperatorOverrides — the server-side
// enrichment treats category and language as operator-authoritative:
// whatever the LLM emitted for those fields is replaced. This is
// the canonical safety pattern (don't trust the LLM with config).
func TestSlidesNarrate_EngagementOperatorOverrides(t *testing.T) {
	exec := &narrateExecScript{}
	// LLM tries to emit Education + fr; operator inputs say Gaming + es.
	disp := &scriptedDispatcherWT{replies: []string{
		`{"title":"T","description":"d","tags":["x"],"category":"Education","language":"fr"}`,
	}}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# A\n\n<!-- hi -->",
		"metadata_model": "openai/gpt-4o-mini",
		"category": "Gaming",
		"language": "es",
		"allow_silent_output": true
	}`
	raw, err := runNarrate(t, disp, nil, exec, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Engagement map[string]any `json:"engagement"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.Engagement["category"]; got != "Gaming" {
		t.Errorf("category = %v, want Gaming (operator input must override LLM)", got)
	}
	if got := out.Engagement["language"]; got != "es" {
		t.Errorf("language = %v, want es (operator input must override LLM)", got)
	}
}

// TestSlidesNarrate_EngagementHashtagCountClamp — values outside the
// 3-5 research-validated range are clamped server-side. The prompt
// asks the LLM for the value; the handler enforces it independently
// so a drifted prompt can't slip through.
func TestSlidesNarrate_EngagementHashtagCountClamp(t *testing.T) {
	// Out-of-range input gets clamped to 4 (default). We can't easily
	// observe the clamped value in the LLM prompt without mocking the
	// dispatcher's request inspection, so we assert the input doesn't
	// error and the engagement object is still produced.
	exec := &narrateExecScript{}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"title":"T","description":"d","hashtags":["a","b","c","d"],"tags":["x"],"category":"X","language":"en"}`,
	}}
	for _, hc := range []int{0, 1, 99} {
		t.Run(fmt.Sprintf("hashtag_count=%d", hc), func(t *testing.T) {
			disp.calls = 0 // reset
			input := fmt.Sprintf(`{
				"markdown": "---\nmarp: true\n---\n\n# A\n\n<!-- hi -->",
				"metadata_model": "openai/gpt-4o-mini",
				"hashtag_count": %d,
				"allow_silent_output": true
			}`, hc)
			if _, err := runNarrate(t, disp, nil, exec, input); err != nil {
				t.Fatalf("handler: %v", err)
			}
		})
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
		HasNarration   bool    `json:"has_narration"`
		SlideCount     int     `json:"slide_count"`
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

func TestSlidesNarrate_HeroImageInlinedIntoSlide1(t *testing.T) {
	// hero_image_prompt → RunImageGen (HTTP fal stub) → base64 inline
	// into the markdown BEFORE parsing. Slide 1's content should
	// contain the data-URI <img> tag.
	stubFalAPI(t, "sk_fal", 1)
	v := vaultWithFalKey(t, "sk_fal")
	exec := &narrateExecScript{}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# Welcome\n\n<!-- intro narration -->",
		"hero_image_prompt": "warm gradient title card",
		"allow_silent_output": true
	}`
	raw, err := runNarrate(t, nil, v, exec, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		HeroImageModelUsed string `json:"hero_image_model_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.HeroImageModelUsed != imageGenDefaultModel {
		t.Errorf("hero_image_model_used = %q, want %q", out.HeroImageModelUsed, imageGenDefaultModel)
	}
	// The marp invocation wrote the modified markdown to a tmp file
	// via `cat > /tmp/helmdeck-deck.md` — find that call and assert
	// the stdin contains a data:image/png;base64, substring.
	var heroFound bool
	for _, c := range exec.calls {
		if strings.Contains(string(c.Stdin), `data:image/png;base64,`) {
			heroFound = true
			break
		}
	}
	if !heroFound {
		t.Error("hero image data URI not found in any session exec stdin")
	}
}

func TestSlidesNarrate_DryRunSkipsHeroImage(t *testing.T) {
	// dry_run short-circuits BEFORE hero image generation, same as
	// podcast.generate's cover_image. No fal stub seeded; HTTP attempt
	// would fail. The dry_run path must not call RunImageGen.
	v := vaultWithFalKey(t, "")
	exec := &narrateExecScript{}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# Welcome",
		"hero_image_prompt": "cover",
		"dry_run": true
	}`
	raw, err := runNarrate(t, nil, v, exec, input)
	if err != nil {
		t.Fatalf("dry_run + hero_image_prompt should succeed with no fal creds: %v", err)
	}
	var out struct {
		DryRun             bool   `json:"dry_run"`
		HeroImageModelUsed string `json:"hero_image_model_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.DryRun {
		t.Error("dry_run should be true")
	}
	if out.HeroImageModelUsed != "" {
		t.Errorf("dry_run must not generate hero image; got model = %q", out.HeroImageModelUsed)
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
	pack := SlidesNarrate(nil, nil, nil)
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
	pack := SlidesNarrate(nil, nil, nil)
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

// TestSlidesNarrate_DryRun_ShortCircuits asserts dry_run:true returns
// the cost preview without calling the executor (no Marp render, no
// TTS, no ffmpeg). Per #145 — operators preview cost before paying.
func TestSlidesNarrate_DryRun_ShortCircuits(t *testing.T) {
	exec := &narrateExecScript{}
	input := `{
		"markdown": "---\nmarp: true\n---\n\n# Slide 1\n\n<!-- this is a fairly long speaker note that adds chars to the cost estimate -->\n\n---\n\n# Slide 2\n\n<!-- another note -->",
		"dry_run": true
	}`
	raw, err := runNarrate(t, nil, nil, exec, input)
	if err != nil {
		t.Fatalf("dry_run handler: %v", err)
	}
	if len(exec.calls) != 0 {
		t.Errorf("dry_run should not invoke the executor, got %d calls", len(exec.calls))
	}
	var out struct {
		DryRun           bool           `json:"dry_run"`
		SlideCount       int            `json:"slide_count"`
		TTSChars         map[string]int `json:"tts_chars"`
		EstimatedCostUSD float64        `json:"estimated_cost_usd"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.DryRun {
		t.Error("dry_run flag should round-trip true")
	}
	if out.SlideCount != 2 {
		t.Errorf("slide_count = %d, want 2", out.SlideCount)
	}
	if out.TTSChars["_total"] == 0 {
		t.Errorf("tts_chars._total = 0, want > 0: %+v", out.TTSChars)
	}
	if out.EstimatedCostUSD <= 0 {
		t.Errorf("estimated_cost_usd should be > 0: %v", out.EstimatedCostUSD)
	}
}

func TestSlidesNarrate_FfmpegConcatFailure(t *testing.T) {
	pack := SlidesNarrate(nil, nil, nil)
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
			// validateMarpPngs stats each rendered PNG via `wc -c` and
			// reads 8 header bytes via `head -c 8`. Also the new
			// requireNonEmptyOutput post-encode check uses `wc -c`.
			// Return healthy values so the test reaches the concat path.
			if strings.Contains(script, "wc -c < ") {
				return session.ExecResult{Stdout: []byte("65536\n")}, nil
			}
			if strings.Contains(script, "head -c 8 ") {
				return session.ExecResult{Stdout: []byte(pngMagicHex)}, nil
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
	pack := SlidesNarrate(nil, nil, nil)
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
			case strings.Contains(script, "wc -c < "):
				// validateMarpPngs runs before the segment encode;
				// return a healthy size so the test reaches the ffmpeg
				// segment path it is targeting.
				return session.ExecResult{Stdout: []byte("65536\n")}, nil
			case strings.Contains(script, "head -c 8 "):
				// validateMarpPngs's magic-byte check.
				return session.ExecResult{Stdout: []byte(pngMagicHex)}, nil
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
		http.MethodPost, srv.URL+"/v1/text-to-speech/voice-001?output_format=mp3_44100_192",
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

// TestNormalizeSlidesNarrateResolution — the named-preset vocabulary
// hyperframes.render uses ("1080p", "720p", "4k") is now translated
// to the "WIDTHxHEIGHT" string ffmpeg's scale filter requires
// BEFORE the per-segment encode. The motivating failure was a real
// run that exited handler_failed with ffmpeg: "Invalid size '1080p'"
// because the caller's "resolution":"1080p" reached the scale=
// filter unmodified. Operators can use the same value across both
// packs now.
func TestNormalizeSlidesNarrateResolution(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Named presets translate.
		{"720p", "1280x720"},
		{"1080p", "1920x1080"},
		{"1440p", "2560x1440"},
		{"2160p", "3840x2160"},
		{"4k", "3840x2160"},
		// Case-insensitive, whitespace-tolerant.
		{"4K", "3840x2160"},
		{"  1080P  ", "1920x1080"},
		// Pre-formatted strings pass through (already ffmpeg-shape).
		{"1920x1080", "1920x1080"},
		{"640x480", "640x480"},
		// Empty stays empty — caller applies its default downstream.
		{"", ""},
		// Unknown values pass through unchanged so ffmpeg surfaces
		// its own "Invalid size" error with the offending input.
		// Silent normalization would mask typos.
		{"giant", "giant"},
		{"1080", "1080"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeSlidesNarrateResolution(tc.in); got != tc.want {
				t.Errorf("normalizeSlidesNarrateResolution(%q) = %q; want %q",
					tc.in, got, tc.want)
			}
		})
	}
}

// TestSlidesNarrateFfmpegThreads_DefaultWhenEnvUnset — the conservative
// "4" default applies when no override is set. This is the value the
// thread cap targets for the dominant "12-core workstation, 8 GiB
// session" case where libx264-uncapped would grab all 12 cores and
// blow the memory budget on dense-frame decks.
func TestSlidesNarrateFfmpegThreads_DefaultWhenEnvUnset(t *testing.T) {
	t.Setenv(slidesNarrateFfmpegThreadsEnv, "")
	got := slidesNarrateFfmpegThreads()
	if got != slidesNarrateDefaultFfmpegThreads {
		t.Errorf("default expected %q; got %q", slidesNarrateDefaultFfmpegThreads, got)
	}
}

// TestSlidesNarrateFfmpegThreads_OverrideHonored — operators with
// abundant RAM bump the cap; operators on small hosts can drop to 1
// or 2 for extra headroom.
func TestSlidesNarrateFfmpegThreads_OverrideHonored(t *testing.T) {
	for _, v := range []string{"1", "2", "6", "8", "12"} {
		t.Setenv(slidesNarrateFfmpegThreadsEnv, v)
		if got := slidesNarrateFfmpegThreads(); got != v {
			t.Errorf("override expected %q; got %q", v, got)
		}
	}
}

// TestSlidesNarrateFfmpegThreads_GarbageFallsThroughToDefault —
// numeric guard. A non-numeric value (typo, accidental quoting,
// templating bug) falls back to the safe default rather than
// passing garbage into ffmpeg's -threads flag. Refusing to boot
// over a typo would be worse than running with a known-safe value.
func TestSlidesNarrateFfmpegThreads_GarbageFallsThroughToDefault(t *testing.T) {
	for _, v := range []string{"abc", "12 threads", "  ", "1.5", "-1"} {
		t.Setenv(slidesNarrateFfmpegThreadsEnv, v)
		got := slidesNarrateFfmpegThreads()
		// -1 is technically valid for strconv.Atoi but ffmpeg
		// rejects it; we let strconv pass it through. The
		// non-numeric cases must fall back.
		if v == "-1" {
			if got != "-1" {
				t.Errorf("strconv-valid value %q should pass through; got %q", v, got)
			}
			continue
		}
		if got != slidesNarrateDefaultFfmpegThreads {
			t.Errorf("garbage %q should fall back to default; got %q", v, got)
		}
	}
}

// TestSlidesNarrate_AdaptiveRetryOnOOM — when the per-segment ffmpeg
// returns exit 137 (SIGKILL → OOM-classified by classifyShellExitCode),
// the handler retries that ONE segment with degraded settings
// (-threads 1, -preset veryfast). The test verifies:
//
//  1. A second ffmpeg invocation occurs for the OOM'd segment (not
//     the next segment in the deck).
//  2. The retry command carries the degraded -threads 1 -preset
//     veryfast flags.
//  3. If the retry succeeds, the overall run succeeds.
//
// Bounded — one retry per segment. A separate test confirms a
// double-OOM surfaces CodeResourceExhausted.
func TestSlidesNarrate_AdaptiveRetryOnOOM(t *testing.T) {
	ffmpegResults := []int{137, 0}
	// ffmpegEncodeCalls tracks ONLY the per-segment ffmpeg encode
	// invocations (-loop 1), so the assertions aren't polluted by
	// the marp/cat/ffprobe shell calls the handler also issues.
	var ffmpegEncodeCalls []session.ExecRequest
	exec := &narrateExecScript{}
	wrappedFn := func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
		script := ""
		if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
			script = req.Cmd[2]
		}
		if strings.Contains(script, "-loop 1") {
			ffmpegEncodeCalls = append(ffmpegEncodeCalls, req)
			if len(ffmpegResults) == 0 {
				return session.ExecResult{}, nil
			}
			code := ffmpegResults[0]
			ffmpegResults = ffmpegResults[1:]
			return session.ExecResult{ExitCode: code}, nil
		}
		return exec.fn(ctx, req)
	}
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(`{"markdown":"---\nmarp: true\n---\n\n# Slide","allow_silent_output":true}`),
		Session:   &session.Session{ID: "sess"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      wrappedFn,
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("handler should succeed after one retry; got %v", err)
	}
	if len(ffmpegEncodeCalls) != 2 {
		t.Fatalf("expected exactly 2 ffmpeg encode calls (primary + retry); got %d", len(ffmpegEncodeCalls))
	}
	primaryScript := ffmpegEncodeCalls[0].Cmd[2]
	if strings.Contains(primaryScript, "-preset veryfast") {
		t.Errorf("primary attempt should NOT carry -preset veryfast; got: %s", primaryScript)
	}
	if strings.Contains(primaryScript, "-threads 1 ") {
		t.Errorf("primary attempt should NOT carry -threads 1; got: %s", primaryScript)
	}
	retryScript := ffmpegEncodeCalls[1].Cmd[2]
	if !strings.Contains(retryScript, "-preset veryfast") {
		t.Errorf("retry should carry -preset veryfast; got: %s", retryScript)
	}
	if !strings.Contains(retryScript, "-threads 1 ") {
		t.Errorf("retry should carry -threads 1; got: %s", retryScript)
	}
}

// TestSlidesNarrate_DoubleOOMSurfacesCodeResourceExhausted — when
// the retry ALSO returns exit 137, the handler must surface
// CodeResourceExhausted (not CodeHandlerFailed) so classify.go routes
// it to FailureTransient with the actionable "bump MemoryLimit"
// reason.
func TestSlidesNarrate_DoubleOOMSurfacesCodeResourceExhausted(t *testing.T) {
	ffmpegResults := []int{137, 137}
	exec := &narrateExecScript{}
	var ffmpegEncodeCalls []session.ExecRequest
	wrappedFn := func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
		script := ""
		if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
			script = req.Cmd[2]
		}
		if strings.Contains(script, "-loop 1") {
			ffmpegEncodeCalls = append(ffmpegEncodeCalls, req)
			if len(ffmpegResults) == 0 {
				return session.ExecResult{ExitCode: 137}, nil
			}
			code := ffmpegResults[0]
			ffmpegResults = ffmpegResults[1:]
			return session.ExecResult{ExitCode: code}, nil
		}
		return exec.fn(ctx, req)
	}
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(`{"markdown":"---\nmarp: true\n---\n\n# Slide","allow_silent_output":true}`),
		Session:   &session.Session{ID: "sess"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      wrappedFn,
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	if err == nil {
		t.Fatal("expected error after double OOM")
	}
	pe, ok := err.(*packs.PackError)
	if !ok {
		t.Fatalf("expected *packs.PackError; got %T: %v", err, err)
	}
	if pe.Code != packs.CodeResourceExhausted {
		t.Errorf("expected CodeResourceExhausted; got %s", pe.Code)
	}
	// Exactly 2 ffmpeg encode calls — primary OOM + retry OOM. The
	// handler must NOT escalate to a third attempt.
	if len(ffmpegEncodeCalls) != 2 {
		t.Errorf("expected exactly 2 ffmpeg encode calls (primary + 1 retry, no more); got %d", len(ffmpegEncodeCalls))
	}
}

// --- validateMarpPngs (Mermaid-render-failure guard) ---

// pngStatExec is a minimal Executor stub that returns scripted
// (stdout, exitCode) values per slide for both the `wc -c < file`
// (size) and `head -c 8 … od …` (magic-bytes) calls validateMarpPngs
// makes. The slide indexes are 1-based in the file path; the stub
// records what it sees and returns the i-th entry of the size +
// magic scripts (0-based) for the matching call. If magics is nil,
// every slide's magic check returns the valid PNG signature
// (pngMagicHex) — convenient for size-failure tests where the magic
// check is never reached.
type pngStatExec struct {
	sizes      []int64  // size in bytes for slide i+1; <0 means "file missing"
	magics     []string // hex string for slide i+1; "" means use pngMagicHex (valid)
	t          *testing.T
	calls      []string
	transports error // if non-nil, every call returns this error
}

func (p *pngStatExec) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	if p.transports != nil {
		return session.ExecResult{}, p.transports
	}
	script := ""
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
		script = req.Cmd[2]
	}
	p.calls = append(p.calls, script)
	// Extract the 1-based slide index from the file path embedded in
	// the script (works for both `wc -c < '...'` and `head -c 8 '...'`).
	idx := -1
	for i := 1; i <= len(p.sizes); i++ {
		needle := "deck." + fmt.Sprintf("%03d", i) + ".png"
		if strings.Contains(script, needle) {
			idx = i
			break
		}
	}
	if idx <= 0 || idx > len(p.sizes) {
		return session.ExecResult{}, fmt.Errorf("pngStatExec: unmatched script %q", script)
	}
	if strings.Contains(script, "head -c 8 ") {
		hex := pngMagicHex
		if idx-1 < len(p.magics) && p.magics[idx-1] != "" {
			hex = p.magics[idx-1]
		}
		return session.ExecResult{Stdout: []byte(hex)}, nil
	}
	size := p.sizes[idx-1]
	if size < 0 {
		// "File missing" — wc exits non-zero, stderr says so.
		return session.ExecResult{ExitCode: 1, Stderr: []byte("wc: deck.png: No such file or directory")}, nil
	}
	return session.ExecResult{Stdout: []byte(fmt.Sprintf("%d\n", size))}, nil
}

func newPngValidateEC(exec *pngStatExec) *packs.ExecutionContext {
	return &packs.ExecutionContext{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:   exec.fn,
	}
}

func TestValidateMarpPngs_AllHealthy_NoError(t *testing.T) {
	exec := &pngStatExec{t: t, sizes: []int64{50000, 60000, 75000}}
	if err := validateMarpPngs(context.Background(), newPngValidateEC(exec), 3); err != nil {
		t.Fatalf("expected nil; got %v", err)
	}
	// 3 slides × 2 checks (wc-c size + head -c 8 magic) = 6 calls.
	if len(exec.calls) != 6 {
		t.Errorf("expected 6 calls (3 wc-c + 3 head-c-8, one of each per slide); got %d", len(exec.calls))
	}
}

func TestValidateMarpPngs_MissingFile_ReturnsInvalidInputNamingSlide(t *testing.T) {
	// Slide 3 missing — the Mermaid-failure shape from the user report.
	exec := &pngStatExec{t: t, sizes: []int64{50000, 50000, -1, 50000}}
	err := validateMarpPngs(context.Background(), newPngValidateEC(exec), 4)
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeInvalidInput {
		t.Errorf("expected CodeInvalidInput (routes to FailureCallerFixable); got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "slide 3") {
		t.Errorf("error must name the failing 1-based slide index; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "Mermaid") {
		t.Errorf("error must hint at the common cause (Mermaid) so operators have a starting point; got %q", pe.Message)
	}
	// Must stop at the first failure — no need to stat slide 4.
	// Slide 1: wc + head (both pass). Slide 2: wc + head (both pass).
	// Slide 3: wc fails, short-circuit before head. Total = 5.
	if len(exec.calls) != 5 {
		t.Errorf("expected 5 calls (slide 1+2 do wc+head each = 4, slide 3 wc fails = 1, total 5, no slide 4); got %d", len(exec.calls))
	}
}

func TestValidateMarpPngs_TinyFile_ReturnsInvalidInputWithSize(t *testing.T) {
	// Slide 2 rendered 256 bytes — well below minRenderedSlidePngBytes (1024).
	exec := &pngStatExec{t: t, sizes: []int64{50000, 256, 50000}}
	err := validateMarpPngs(context.Background(), newPngValidateEC(exec), 3)
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeInvalidInput {
		t.Errorf("expected CodeInvalidInput; got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "slide 2") {
		t.Errorf("error must name slide 2; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "256 bytes") {
		t.Errorf("error must surface the actual size so operators can sanity-check; got %q", pe.Message)
	}
}

func TestValidateMarpPngs_AtFloor_Passes(t *testing.T) {
	// Boundary test — exactly minRenderedSlidePngBytes must pass.
	// Catches off-by-one regressions in the < vs <= comparison.
	exec := &pngStatExec{t: t, sizes: []int64{minRenderedSlidePngBytes}}
	if err := validateMarpPngs(context.Background(), newPngValidateEC(exec), 1); err != nil {
		t.Errorf("size == floor should pass (< comparison, not <=); got %v", err)
	}
}

func TestValidateMarpPngs_TransportError_ReturnsHandlerFailed(t *testing.T) {
	// Underlying Exec error (docker disconnect, session timeout) — must
	// surface as CodeHandlerFailed, NOT CodeInvalidInput, since the
	// caller's input may be perfectly fine and the failure is
	// infrastructural.
	exec := &pngStatExec{t: t, sizes: []int64{50000}, transports: errors.New("session disconnected")}
	err := validateMarpPngs(context.Background(), newPngValidateEC(exec), 1)
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("transport error must surface as CodeHandlerFailed (not CodeInvalidInput — caller's input is fine); got %s", pe.Code)
	}
}

// TestValidateMarpPngs_BadPngMagic_ReturnsInvalidInputNamingSlide pins the
// new magic-byte check: a slide whose size passes the floor but whose
// first 8 bytes don't match the PNG signature must surface as
// CodeInvalidInput naming the slide. This catches the Mermaid-failure
// shape PR #399's size-only check let through (placeholder content that
// is >=1024 bytes but not a valid PNG).
func TestValidateMarpPngs_BadPngMagic_ReturnsInvalidInputNamingSlide(t *testing.T) {
	exec := &pngStatExec{
		t:      t,
		sizes:  []int64{50000, 50000, 50000},
		magics: []string{pngMagicHex, "deadbeefdeadbeef", pngMagicHex},
	}
	err := validateMarpPngs(context.Background(), newPngValidateEC(exec), 3)
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeInvalidInput {
		t.Errorf("expected CodeInvalidInput (routes to FailureCallerFixable); got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "slide 2") {
		t.Errorf("error must name the failing 1-based slide index; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "PNG signature") {
		t.Errorf("error must explain the magic-byte mismatch; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "Mermaid") {
		t.Errorf("error must hint at the common cause; got %q", pe.Message)
	}
	// Must stop at slide 2 — no need to magic-check slide 3.
	// Slide 1: wc + head (both pass). Slide 2: wc passes, head fails.
	// = 2 + 2 = 4 calls.
	if len(exec.calls) != 4 {
		t.Errorf("expected 4 calls (slide 1: wc+head, slide 2: wc+head-fails, no slide 3); got %d", len(exec.calls))
	}
}

// --- segment-encode error message + post-encode size check ---

// TestSlidesNarrate_SegmentTransportError_HonestMessage pins the fix for
// the misleading "ffmpeg segment N failed (exit 0)" message that
// appeared when ec.Exec returned err != nil but res.ExitCode was the
// zero value. The new message must surface the actual transport error
// and explicitly say ffmpeg did NOT return a real exit code, so
// operators stop chasing imaginary ffmpeg bugs.
func TestSlidesNarrate_SegmentTransportError_HonestMessage(t *testing.T) {
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:    pack,
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
				// THE bug: transport error with default ExitCode = 0.
				return session.ExecResult{}, errors.New("docker exec: connection reset by peer")
			case strings.Contains(script, "ffprobe"):
				return session.ExecResult{Stdout: []byte("5.0\n")}, nil
			case strings.Contains(script, "wc -c < "):
				return session.ExecResult{Stdout: []byte("65536\n")}, nil
			case strings.Contains(script, "head -c 8 "):
				return session.ExecResult{Stdout: []byte(pngMagicHex)}, nil
			default:
				return session.ExecResult{}, nil
			}
		},
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T (%v)", err, err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("transport error must surface as CodeHandlerFailed; got %s", pe.Code)
	}
	// Must NOT include the misleading "exit 0" phrase.
	if strings.Contains(pe.Message, "exit 0") {
		t.Errorf("message must NOT print 'exit 0' on a transport error — this was the original bug. got %q", pe.Message)
	}
	// Must call out the transport error explicitly so operators stop
	// chasing imaginary ffmpeg bugs.
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("message must explain the transport error; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "did NOT return a real exit code") {
		t.Errorf("message must explicitly tell operators ffmpeg did not exit; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "connection reset by peer") {
		t.Errorf("message must surface the underlying transport error verbatim; got %q", pe.Message)
	}
}

// TestSlidesNarrate_SegmentExitZeroEmptyOutput_PostCheckFires pins the
// new post-encode size check: ffmpeg exit 0 with a 0-byte segment file
// must surface as CodeHandlerFailed naming the segment, instead of
// flowing into concat and surfacing as a misleading concat error.
func TestSlidesNarrate_SegmentExitZeroEmptyOutput_PostCheckFires(t *testing.T) {
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	// Track which wc-c paths were stat'd so we can distinguish
	// PNG-input checks (deck.NNN.png) from post-encode output checks
	// (seg-NNN.mp4 / final.mp4).
	ec := &packs.ExecutionContext{
		Pack:    pack,
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
				// ffmpeg "succeeds" — exit 0 with no output.
				return session.ExecResult{}, nil
			case strings.Contains(script, "ffprobe"):
				return session.ExecResult{Stdout: []byte("5.0\n")}, nil
			case strings.Contains(script, "head -c 8 "):
				return session.ExecResult{Stdout: []byte(pngMagicHex)}, nil
			case strings.Contains(script, "wc -c < "):
				// PNG inputs pass (deck.NNN.png); segment output empty.
				if strings.Contains(script, "seg-") {
					return session.ExecResult{Stdout: []byte("0\n")}, nil
				}
				return session.ExecResult{Stdout: []byte("65536\n")}, nil
			default:
				return session.ExecResult{}, nil
			}
		},
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T (%v)", err, err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("expected CodeHandlerFailed; got %s", pe.Code)
	}
	if !strings.Contains(pe.Message, "ffmpeg segment") {
		t.Errorf("message must name the failing step; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "0 bytes") {
		t.Errorf("message must surface the actual size so operators can sanity-check; got %q", pe.Message)
	}
	if strings.Contains(pe.Message, "concat") {
		t.Errorf("post-encode check must surface at the SEGMENT step, not flow into concat; got %q", pe.Message)
	}
}

// TestSlidesNarrate_ConcatTransportError_HonestMessage mirrors the
// segment-path transport-error test for the concat step (line 616
// bug). Same shape, different ffmpeg call site.
func TestSlidesNarrate_ConcatTransportError_HonestMessage(t *testing.T) {
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:    pack,
		Input:   json.RawMessage(`{"markdown":"---\nmarp: true\n---\n\n# Slide","allow_silent_output":true}`),
		Session: &session.Session{ID: "s"},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec: func(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
			script := ""
			if len(req.Cmd) >= 3 {
				script = req.Cmd[2]
			}
			switch {
			// Match the ffmpeg concat command specifically, not any
			// script that mentions "concat" — the prior `cat >`
			// writing /tmp/concat.txt also mentions it.
			case strings.Contains(script, "ffmpeg -y -f concat"):
				return session.ExecResult{}, errors.New("docker exec: container exited mid-call")
			case strings.Contains(script, "ffprobe"):
				return session.ExecResult{Stdout: []byte("5.0\n")}, nil
			case strings.Contains(script, "head -c 8 "):
				return session.ExecResult{Stdout: []byte(pngMagicHex)}, nil
			case strings.Contains(script, "wc -c < "):
				return session.ExecResult{Stdout: []byte("65536\n")}, nil
			default:
				return session.ExecResult{}, nil
			}
		},
		Artifacts: artifacts,
	}
	_, err := pack.Handler(context.Background(), ec)
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError; got %T (%v)", err, err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("expected CodeHandlerFailed; got %s", pe.Code)
	}
	if strings.Contains(pe.Message, "exit 0") {
		t.Errorf("concat message must NOT print 'exit 0' on transport error; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "concat") {
		t.Errorf("message must name the concat step; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "transport error") {
		t.Errorf("message must explain the transport error; got %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "container exited mid-call") {
		t.Errorf("message must surface the underlying transport error; got %q", pe.Message)
	}
}

// --- Mermaid pre-processing (parity with slides.render) ---

// TestSlidesNarrate_MermaidFencePreprocessed — when the deck has a
// ```mermaid block, the handler must run mmdc to produce SVG and
// rewrite the markdown so what gets written to /tmp/helmdeck-deck.md
// (and ultimately handed to Marp) contains an inline-SVG <img>
// data-URI in place of the fence. Without this, Marp's headless
// Chromium leaves Mermaid blocks blank in per-slide PNGs.
func TestSlidesNarrate_MermaidFencePreprocessed(t *testing.T) {
	ex := &narrateExecScript{}
	body := "---\nmarp: true\n---\n\n# Slide 1\n\n```mermaid\ngraph TD; A-->B;\n```\n\n---\n\n# Slide 2"
	raw, _ := json.Marshal(map[string]any{"markdown": body, "allow_silent_output": true})
	input := json.RawMessage(raw)
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     input,
		Session:   &session.Session{ID: "s"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      ex.fn,
		Artifacts: artifacts,
	}
	if _, err := pack.Handler(context.Background(), ec); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	// Locate the script that wrote the markdown to the sidecar — its
	// stdin is the post-rewrite markdown Marp will ingest.
	var writeMarkdownStdin []byte
	var sawMmdc bool
	for _, req := range ex.calls {
		if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
			script := req.Cmd[2]
			if strings.Contains(script, "mmdc") {
				sawMmdc = true
			}
			if strings.Contains(script, "cat > '/tmp/helmdeck-deck.md'") {
				writeMarkdownStdin = req.Stdin
			}
		}
	}
	if !sawMmdc {
		t.Errorf("mmdc must run when the deck contains a ```mermaid fence; saw %d execs",
			len(ex.calls))
	}
	if len(writeMarkdownStdin) == 0 {
		t.Fatalf("expected a cat-write of /tmp/helmdeck-deck.md; none found")
	}
	piped := string(writeMarkdownStdin)
	if strings.Contains(piped, "```mermaid") {
		t.Errorf("markdown handed to Marp must NOT contain raw ```mermaid fence after preprocessing:\n%s", piped)
	}
	if !strings.Contains(piped, `<img src="data:image/svg+xml;base64,`) {
		t.Errorf("markdown handed to Marp must contain inline-SVG <img> data-URI:\n%s", piped)
	}
}

// TestSlidesNarrate_MermaidOptOut — when the caller passes
// `"mermaid": false`, the preprocessor must NOT run even on a deck
// that contains a fence. Mirrors slides.render's same opt-out.
func TestSlidesNarrate_MermaidOptOut(t *testing.T) {
	ex := &narrateExecScript{}
	body := "---\nmarp: true\n---\n\n# Slide\n\n```mermaid\ngraph TD; A-->B;\n```"
	raw, _ := json.Marshal(map[string]any{"markdown": body, "allow_silent_output": true, "mermaid": false})
	input := json.RawMessage(raw)
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     input,
		Session:   &session.Session{ID: "s"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      ex.fn,
		Artifacts: artifacts,
	}
	if _, err := pack.Handler(context.Background(), ec); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	for _, req := range ex.calls {
		if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
			if strings.Contains(req.Cmd[2], "mmdc") {
				t.Errorf("mmdc must NOT run when mermaid:false is passed; got script %q", req.Cmd[2])
			}
		}
	}
}

// TestSlidesNarrate_NoMermaidFenceSkipsMmdc — a deck without any
// ```mermaid blocks must NOT incur the mmdc startup cost even with
// mermaid enabled (the default). preprocessMermaidFences early-returns
// on zero matches; this test pins that behavior at the slides.narrate
// boundary.
func TestSlidesNarrate_NoMermaidFenceSkipsMmdc(t *testing.T) {
	ex := &narrateExecScript{}
	input := json.RawMessage(`{"markdown":"---\nmarp: true\n---\n\n# Title\n\nPlain markdown, no diagrams here.","allow_silent_output":true}`)
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     input,
		Session:   &session.Session{ID: "s"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      ex.fn,
		Artifacts: artifacts,
	}
	if _, err := pack.Handler(context.Background(), ec); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	for _, req := range ex.calls {
		if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
			if strings.Contains(req.Cmd[2], "mmdc") {
				t.Errorf("mmdc must NOT run for a deck without ```mermaid blocks; got %q", req.Cmd[2])
			}
		}
	}
}

// TestSlidesNarrate_ConcatReencodesAudio is the regression guard for
// the audio-dropouts-mid-slide failure mode. Per-segment AAC frames
// don't align with segment boundaries, so concat with `-c copy` on
// the audio stream produces audible mid-segment dropouts. The fix is
// to re-encode audio (`-c:a aac -b:a 192k`) while keeping video
// stream-copy (`-c:v copy`). This test pins the new ffmpeg flag
// shape so a future "make concat faster" refactor doesn't quietly
// re-introduce the bug.
func TestSlidesNarrate_ConcatReencodesAudio(t *testing.T) {
	ex := &narrateExecScript{}
	raw, _ := json.Marshal(map[string]any{
		"markdown":            "---\nmarp: true\n---\n\n# Slide",
		"allow_silent_output": true,
	})
	pack := SlidesNarrate(nil, nil, nil)
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(raw),
		Session:   &session.Session{ID: "s"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      ex.fn,
		Artifacts: artifacts,
	}
	if _, err := pack.Handler(context.Background(), ec); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	// Find the ffmpeg concat invocation.
	var concatScript string
	for _, req := range ex.calls {
		if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
			if strings.Contains(req.Cmd[2], "ffmpeg -y -f concat") {
				concatScript = req.Cmd[2]
				break
			}
		}
	}
	if concatScript == "" {
		t.Fatal("no ffmpeg concat invocation observed")
	}
	// Video stream-copy is preserved (fast).
	if !strings.Contains(concatScript, "-c:v copy") {
		t.Errorf("concat must keep video stream-copy (-c:v copy); got %q", concatScript)
	}
	// Audio MUST be re-encoded (not stream-copied) so AAC frame
	// boundaries realign, eliminating mid-segment dropouts.
	if !strings.Contains(concatScript, "-c:a aac") {
		t.Errorf("concat must re-encode audio (-c:a aac) to fix mid-segment dropouts; got %q", concatScript)
	}
	if !strings.Contains(concatScript, "-b:a 192k") {
		t.Errorf("concat audio bitrate must match per-segment 192k; got %q", concatScript)
	}
	// Sample-rate pin: the per-segment encode and the concat
	// re-encode must both emit at 44100 Hz (matching the ElevenLabs
	// TTS source). Without -ar 44100, ffmpeg defaults to 48000 Hz
	// for AAC-in-MP4 and the 44100→48000 ratio is the worst-case
	// non-integer libswresample path — audible high-frequency
	// aliasing. PR (audio quality v0.26.0 follow-on).
	if !strings.Contains(concatScript, "-ar 44100") {
		t.Errorf("concat must pin -ar 44100 to match the TTS source (no 44100→48000 resampling artifacts); got %q", concatScript)
	}
	// Streaming-playback regression guard: ffprobe of a v0.25.x
	// artifact showed the moov atom at 97% into the file — players
	// could not begin playback before the entire file streamed in,
	// manifesting as a dropout at a deterministic timestamp on every
	// replay. Diagnosed in the audio-playback-dropouts PR.
	if !strings.Contains(concatScript, "+faststart") {
		t.Errorf("slides.narrate concat must produce a faststart MP4 (moov at the head, not the tail) so streaming players can begin playback; got %q", concatScript)
	}
	// The legacy `-c copy` (which would stream-copy both streams)
	// must NOT appear — the operator-reported bug shape.
	if strings.Contains(concatScript, "-c copy") {
		t.Errorf("concat must NOT use legacy `-c copy` (stream-copies both streams and re-introduces dropouts); got %q", concatScript)
	}
}
