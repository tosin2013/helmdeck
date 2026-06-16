// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
	"github.com/tosin2013/helmdeck/internal/vision"
)

// podcastTestExecutor stubs the session executor for the podcast
// handler's ffmpeg + cat pipeline. We don't actually run ffmpeg in
// unit tests — we just need every Exec call to return success and
// plausible stdout (bytes for `cat /...`, "5.0" for ffprobe duration).
type podcastTestExecutor struct {
	mp3Bytes []byte // returned by `cat /tmp/helmdeck-podcast/final.mp3`
	calls    []session.ExecRequest
}

func (e *podcastTestExecutor) Exec(_ context.Context, _ string, req session.ExecRequest) (session.ExecResult, error) {
	e.calls = append(e.calls, req)
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
		script := req.Cmd[2]
		switch {
		// avenc.ProbeAudioDuration prefixes "LC_ALL=C " so the
		// HasPrefix check matched the un-prefixed shape pre-PR-C.
		// Use Contains so both shapes match.
		case strings.Contains(script, "ffprobe"):
			return session.ExecResult{Stdout: []byte("5.123\n")}, nil
		// avenc's post-encode checks (GenerateSilence + ConcatAudio)
		// stat the output via `wc -c < FILE`; return a healthy size
		// so the validation passes.
		case strings.Contains(script, "wc -c < "):
			return session.ExecResult{Stdout: []byte("65536\n")}, nil
		case strings.HasPrefix(script, "dd if=") && strings.Contains(script, "final.mp3"):
			return session.ExecResult{Stdout: e.mp3Bytes}, nil
		case strings.HasPrefix(script, "cat ") && strings.Contains(script, "silent-turn.mp3"):
			return session.ExecResult{Stdout: []byte("\xff\xfb\x90silent")}, nil
		case strings.HasPrefix(script, "cat ") && strings.Contains(script, ".mp3"):
			return session.ExecResult{Stdout: []byte("\xff\xfb\x90readback")}, nil
		default:
			return session.ExecResult{}, nil
		}
	}
	return session.ExecResult{}, nil
}

// vaultWithElevenAliasKey is the #138-back-compat-alias variant: seeds
// the vault under the alias name "elevenlabs-api-key" instead of the
// canonical "elevenlabs-key", to verify the resolveElevenLabsKey ladder
// finds it.
func vaultWithElevenAliasKey(t *testing.T, key string) *vault.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	master := make([]byte, 32)
	v, err := vault.New(db, master)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        "elevenlabs-api-key",
		Type:        vault.TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	return v
}

// vaultWithElevenKey returns an in-memory vault store with the
// elevenlabs-key credential. Pass empty key string to seed without
// the credential (for silent-fallback tests).
func vaultWithElevenKey(t *testing.T, key string) *vault.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	master := make([]byte, 32)
	v, err := vault.New(db, master)
	if err != nil {
		t.Fatal(err)
	}
	if key == "" {
		return v
	}
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        "elevenlabs-key",
		Type:        vault.TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	return v
}

// runPodcastGenerate invokes the handler directly with a hand-built
// ExecutionContext. nil dispatcher means script-mode-only.
func runPodcastGenerate(t *testing.T, v *vault.Store, ex session.Executor, input string) (json.RawMessage, error) {
	t.Helper()
	return runPodcastGenerateWithDisp(t, v, ex, nil, input)
}

// runPodcastGenerateWithDisp wires a dispatcher into the pack
// construction so the engagement-metadata path runs. Used by the
// TestPodcastGenerate_Engagement_* tests below; otherwise prefer the
// simpler runPodcastGenerate which matches the existing test scaffold.
func runPodcastGenerateWithDisp(t *testing.T, v *vault.Store, ex session.Executor, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	var d vision.Dispatcher
	if disp != nil {
		d = disp
	}
	pack := PodcastGenerate(v, nil, d)
	sessionID := "sess-test-podcast"
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: packs.NewMemoryArtifactStore(),
		Session:   &session.Session{ID: sessionID},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec: func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
			return ex.Exec(ctx, sessionID, req)
		},
	}
	return pack.Handler(context.Background(), ec)
}

// --- validation tests -----------------------------------------------------

func TestPodcastGenerate_Validation_NoSpeakers(t *testing.T) {
	_, err := runPodcastGenerate(t, nil, nil, `{
		"script": [{"speaker":"A","text":"hi"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "speakers map is required") {
		t.Fatalf("expected speakers-required error, got %v", err)
	}
}

func TestPodcastGenerate_Validation_NoMode(t *testing.T) {
	_, err := runPodcastGenerate(t, nil, nil, `{
		"speakers": {"A": "v1"}
	}`)
	if err == nil || !strings.Contains(err.Error(), "must provide one of") {
		t.Fatalf("expected mode-required error, got %v", err)
	}
}

func TestPodcastGenerate_Validation_MultipleModes(t *testing.T) {
	_, err := runPodcastGenerate(t, nil, nil, `{
		"speakers": {"A": "v1"},
		"script":   [{"speaker":"A","text":"hi"}],
		"prompt":   "do a podcast"
	}`)
	if err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("expected multiple-modes error, got %v", err)
	}
}

func TestPodcastGenerate_Validation_PromptWithoutModel(t *testing.T) {
	_, err := runPodcastGenerate(t, nil, nil, `{
		"speakers": {"A": "v1"},
		"prompt":   "do a podcast"
	}`)
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("expected model-required error, got %v", err)
	}
}

func TestPodcastGenerate_Validation_BadTheme(t *testing.T) {
	_, err := runPodcastGenerate(t, nil, nil, `{
		"speakers": {"A": "v1"},
		"theme":    "rant",
		"script":   [{"speaker":"A","text":"hi"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "theme must be one of") {
		t.Fatalf("expected theme error, got %v", err)
	}
}

func TestPodcastGenerate_Validation_BadEngine(t *testing.T) {
	_, err := runPodcastGenerate(t, nil, nil, `{
		"speakers": {"A": "v1"},
		"engine":   "playht",
		"script":   [{"speaker":"A","text":"hi"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), `engine must be "elevenlabs"`) {
		t.Fatalf("expected engine error, got %v", err)
	}
}

func TestPodcastGenerate_Validation_SpeakerNotInMap(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90fakefinalmp3")}
	_, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1"},
		"script":   [{"speaker":"Carol","text":"who am I"}]
	}`)
	if err == nil || !strings.Contains(err.Error(), `not in speakers map`) {
		t.Fatalf("expected speaker-not-in-map error, got %v", err)
	}
}

// --- happy path -----------------------------------------------------------

func TestPodcastGenerate_ScriptMode_HappyPath(t *testing.T) {
	// No real ElevenLabs server in unit tests, so seed the vault
	// WITHOUT a key and opt into the silent path explicitly via
	// allow_silent_output:true (per #138, missing key now hard-fails
	// by default). Validates the dispatch path + artifact upload.
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90finalmp3goeshere")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1", "Jordan": "v2"},
		"script": [
			{"speaker":"Alex","text":"Welcome back."},
			{"speaker":"Jordan","text":"Today we discuss..."},
			{"speaker":"Alex","text":"Let's dig in."}
		],
		"theme": "deep-dive",
		"silence_between_turns_ms": 400,
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Engine           string            `json:"engine"`
		AudioArtifactKey string            `json:"audio_artifact_key"`
		AudioSize        int               `json:"audio_size"`
		DurationS        float64           `json:"duration_s"`
		SpeakerCount     int               `json:"speaker_count"`
		TurnCount        int               `json:"turn_count"`
		ScriptSource     string            `json:"script_source"`
		HasNarration     bool              `json:"has_narration"`
		Theme            string            `json:"theme"`
		VoicesUsed       map[string]string `json:"voices_used"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Engine != "elevenlabs" {
		t.Errorf("engine = %q", out.Engine)
	}
	if out.HasNarration {
		t.Errorf("expected has_narration=false (no key), got true")
	}
	if out.SpeakerCount != 2 {
		t.Errorf("speaker_count = %d, want 2", out.SpeakerCount)
	}
	if out.TurnCount != 3 {
		t.Errorf("turn_count = %d, want 3", out.TurnCount)
	}
	if out.ScriptSource != "input" {
		t.Errorf("script_source = %q, want input", out.ScriptSource)
	}
	if out.Theme != "deep-dive" {
		t.Errorf("theme = %q", out.Theme)
	}
	if !strings.HasSuffix(out.AudioArtifactKey, ".mp3") {
		t.Errorf("artifact key %q should end in .mp3", out.AudioArtifactKey)
	}
	if out.VoicesUsed["Alex"] != "v1" || out.VoicesUsed["Jordan"] != "v2" {
		t.Errorf("voices_used = %+v", out.VoicesUsed)
	}
}

// --- engagement metadata tests --------------------------------------------

// engagementShapedReply is a podcast-engagement JSON shape that matches
// the generatePodcastEngagementPrompt. Shared by the tests below.
const engagementShapedReply = `{
  "title": "Three Architectural Patterns That Outlast Hype Cycles",
  "subtitle": "From scrappy startups to billion-dollar exits",
  "summary": "In this episode Alex and Jordan dig into the architectural patterns that survive the rise and fall of language frameworks.",
  "show_notes_md": "**Patterns that outlast hype.**\n\n- Pattern 1\n- Pattern 2\n- Pattern 3",
  "chapters": [
    {"startTime": 0, "title": "Cold open"},
    {"startTime": 180, "title": "Pattern 1"},
    {"startTime": 480, "title": "Pattern 2"}
  ],
  "hook_30s": "What if every framework you've shipped in is on a 7-year half-life?",
  "cta": {"placement": "wrong-pre-roll", "copy": "Subscribe wherever you listen."}
}`

// TestPodcastGenerate_ValidationDefaultOn — Phase 3 of the validation
// arc: validate is default-on (pointer-bool nil → run); the handler
// invokes av-validate.sh via session exec post-concat. The script
// stub here doesn't match the av-validate command pattern so the
// script-exec path hits a JSON-parse failure → soft-surface fallback
// → output ships without a validation field. Regression guard
// against accidentally flipping default-on to off OR converting
// validation failures into hard errors that block artifact ship.
func TestPodcastGenerate_ValidationDefaultOn(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	if _, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1"},
		"script": [{"speaker":"Alex","text":"Hi."}],
		"allow_silent_output": true,
		"metadata_model": ""
	}`); err != nil {
		t.Fatalf("handler: %v — validation script-exec failure must NOT fail the pack", err)
	}
	// Confirm the handler attempted to invoke av-validate.sh.
	attempted := false
	for _, c := range ex.calls {
		for _, a := range c.Cmd {
			if strings.Contains(a, "av-validate.sh") {
				attempted = true
				break
			}
		}
		if attempted {
			break
		}
	}
	if !attempted {
		t.Errorf("validate default-on; handler must invoke av-validate.sh; got %d exec calls", len(ex.calls))
	}
}

// TestPodcastGenerate_ValidationExplicitlyDisabled — validate:false
// suppresses the av-validate.sh invocation entirely.
func TestPodcastGenerate_ValidationExplicitlyDisabled(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	if _, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1"},
		"script": [{"speaker":"Alex","text":"Hi."}],
		"allow_silent_output": true,
		"metadata_model": "",
		"validate": false
	}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	for _, c := range ex.calls {
		for _, a := range c.Cmd {
			if strings.Contains(a, "av-validate.sh") {
				t.Errorf("validate:false must suppress av-validate.sh; got call %v", c.Cmd)
			}
		}
	}
}

// TestPodcastGenerate_EngagementDefault — verifies that without an
// explicit metadata_model the engagement object IS generated when a
// dispatcher is wired (default-on behavior per v0.26.0). Also pins
// the constant enrichment (format_ceiling_note, language) and the
// defensive CTA placement override.
func TestPodcastGenerate_EngagementDefault(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	disp := &scriptedDispatcherWT{replies: []string{engagementShapedReply}}
	raw, err := runPodcastGenerateWithDisp(t, v, ex, disp, `{
		"speakers": {"Alex": "v1", "Jordan": "v2"},
		"script": [
			{"speaker":"Alex","text":"Welcome."},
			{"speaker":"Jordan","text":"Today we discuss..."}
		],
		"theme": "deep-dive",
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Engagement            map[string]any `json:"engagement"`
		EngagementArtifactKey string         `json:"engagement_artifact_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Engagement == nil {
		t.Fatal("engagement absent — should default-on when dispatcher wired and metadata_model unset")
	}
	if out.EngagementArtifactKey == "" {
		t.Error("engagement_artifact_key empty — sidecar artifact must be persisted")
	}
	if out.Engagement["title"] != "Three Architectural Patterns That Outlast Hype Cycles" {
		t.Errorf("engagement.title = %v", out.Engagement["title"])
	}
	// Constant enrichment — server-side, not LLM-supplied.
	if out.Engagement["format_ceiling_note"] == nil {
		t.Error("format_ceiling_note missing")
	}
	if out.Engagement["language"] != "en" {
		t.Errorf("language = %v, want en (default)", out.Engagement["language"])
	}
	// Defensive CTA placement override — LLM emitted "wrong-pre-roll"
	// but the handler MUST force "mid-roll". This is the single line
	// that prevents a future prompt drift from silently flipping the
	// research-validated placement.
	cta, ok := out.Engagement["cta"].(map[string]any)
	if !ok {
		t.Fatalf("cta not an object: %v", out.Engagement["cta"])
	}
	if cta["placement"] != "mid-roll" {
		t.Errorf("cta.placement = %v, want mid-roll (defensive override)", cta["placement"])
	}
}

// TestPodcastGenerate_EngagementDisabled — passing
// "metadata_model":"" (empty, NOT absent) opts out of engagement gen.
// Required for back-compat with operators who explicitly don't want
// the extra LLM call.
func TestPodcastGenerate_EngagementDisabled(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	disp := &scriptedDispatcherWT{replies: []string{"SHOULD NOT BE CALLED"}}
	raw, err := runPodcastGenerateWithDisp(t, v, ex, disp, `{
		"speakers": {"Alex": "v1"},
		"script": [{"speaker":"Alex","text":"Hi."}],
		"metadata_model": "",
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if _, present := out["engagement"]; present {
		t.Errorf("engagement should be absent when metadata_model:\"\"; got %v", out["engagement"])
	}
	if disp.calls != 0 {
		t.Errorf("dispatcher called %d times — engagement gate should have prevented any call", disp.calls)
	}
}

// TestPodcastGenerate_EngagementCustomCTAStyle — operator can tune
// CTA TONE via cta_style; the prompt receives it. Placement remains
// "mid-roll" — non-overridable per research.
func TestPodcastGenerate_EngagementCustomCTAStyle(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	disp := &scriptedDispatcherWT{replies: []string{engagementShapedReply}}
	_, err := runPodcastGenerateWithDisp(t, v, ex, disp, `{
		"speakers": {"Alex": "v1"},
		"script": [{"speaker":"Alex","text":"Hi."}],
		"cta_style": "direct",
		"language": "es",
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	// The prompt should carry the operator-specified CTA tone; we
	// inspect the captured request payload to confirm the wire shape.
	if len(disp.captured) == 0 {
		t.Fatal("no captured dispatcher requests")
	}
	userMsg := disp.captured[0].Messages[1].Content.Text()
	if !strings.Contains(userMsg, `"direct"`) {
		t.Errorf("prompt should carry cta_style=direct; got user message: %q", userMsg)
	}
	if !strings.Contains(userMsg, `"es"`) {
		t.Errorf("prompt should carry language=es; got user message: %q", userMsg)
	}
}

func TestPodcastGenerate_CoverPromptEmitted(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1"},
		"script":   [{"speaker":"Alex","text":"Today on the show..."}],
		"theme":    "solo-essay",
		"generate_cover_prompt": true,
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		CoverImagePrompt string `json:"cover_image_prompt"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.CoverImagePrompt == "" {
		t.Fatal("expected cover_image_prompt to be emitted")
	}
	if !strings.Contains(out.CoverImagePrompt, "Alex") {
		t.Errorf("cover prompt should reference speakers: %q", out.CoverImagePrompt)
	}
	if !strings.Contains(out.CoverImagePrompt, "Today on the show") {
		t.Errorf("cover prompt should reference hook: %q", out.CoverImagePrompt)
	}
}

func TestPodcastGenerate_CoverImageGenerated(t *testing.T) {
	// cover_image:true triggers RunImageGen internally. We stub fal.ai
	// at the package var ImageGenFalBaseURL and seed the vault with
	// both the elevenlabs-key (so TTS resolution doesn't trip) and the
	// fal-key (so cover gen has credentials). allow_silent_output is
	// also set so we don't need real ElevenLabs bytes flowing.
	stubFalAPI(t, "sk_fal", 1)
	v := vaultWithElevenKey(t, "")
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        "fal-key",
		Type:        vault.TypeAPIKey,
		HostPattern: "fal.run",
		Plaintext:   []byte("sk_fal"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}

	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1"},
		"script":   [{"speaker":"Alex","text":"Today on the show..."}],
		"theme":    "solo-essay",
		"cover_image": true,
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		CoverImageArtifactKey string `json:"cover_image_artifact_key"`
		CoverImageModelUsed   string `json:"cover_image_model_used"`
		CoverImagePrompt      string `json:"cover_image_prompt"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.CoverImageArtifactKey == "" {
		t.Fatal("expected cover_image_artifact_key to be set")
	}
	if !strings.HasPrefix(out.CoverImageArtifactKey, "podcast.generate/") {
		t.Errorf("artifact should be namespaced under podcast.generate, got %q", out.CoverImageArtifactKey)
	}
	if out.CoverImageModelUsed != imageGenDefaultModel {
		t.Errorf("model_used = %q, want %q", out.CoverImageModelUsed, imageGenDefaultModel)
	}
	// generate_cover_prompt was NOT set, so the prompt should NOT be
	// surfaced even though it was computed internally.
	if out.CoverImagePrompt != "" {
		t.Errorf("cover_image_prompt should be empty unless generate_cover_prompt:true; got %q", out.CoverImagePrompt)
	}
}

func TestPodcastGenerate_CoverImageRespectsExplicitModel(t *testing.T) {
	// cover_image_model override goes through to RunImageGen.
	stubFalAPI(t, "sk_fal", 1)
	v := vaultWithElevenKey(t, "")
	rec, _ := v.Create(context.Background(), vault.CreateInput{
		Name: "fal-key", Type: vault.TypeAPIKey, HostPattern: "fal.run",
		Plaintext: []byte("sk_fal"),
	})
	_ = v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"})

	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1"},
		"script":   [{"speaker":"Alex","text":"Hi."}],
		"theme":    "solo-essay",
		"cover_image": true,
		"cover_image_model": "fal-ai/flux/dev",
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		CoverImageModelUsed string `json:"cover_image_model_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.CoverImageModelUsed != "fal-ai/flux/dev" {
		t.Errorf("model_used = %q, want fal-ai/flux/dev", out.CoverImageModelUsed)
	}
}

func TestPodcastGenerate_DryRunSkipsCoverImage(t *testing.T) {
	// dry_run short-circuits BEFORE the cover-image path. Even with
	// cover_image:true, no fal.ai call should happen (we set no fal
	// stub — any HTTP attempt would fail loudly).
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1"},
		"script":   [{"speaker":"Alex","text":"Hi."}],
		"theme":    "solo-essay",
		"cover_image": true,
		"dry_run": true
	}`)
	if err != nil {
		t.Fatalf("dry_run + cover_image should succeed without fal credentials: %v", err)
	}
	var out struct {
		DryRun                bool   `json:"dry_run"`
		CoverImageArtifactKey string `json:"cover_image_artifact_key"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.DryRun {
		t.Error("dry_run should be true in response")
	}
	if out.CoverImageArtifactKey != "" {
		t.Errorf("dry_run must not generate a cover image; got %q", out.CoverImageArtifactKey)
	}
}

func TestPodcastGenerate_PromptModeWithoutDispatcher(t *testing.T) {
	v := vaultWithElevenKey(t, "sk_test")
	ex := &podcastTestExecutor{mp3Bytes: []byte("mp3")}
	_, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A": "v1"},
		"prompt":   "do a podcast",
		"model":    "openai/gpt-4o-mini"
	}`)
	if err == nil || !strings.Contains(err.Error(), "registered without a gateway dispatcher") {
		t.Fatalf("expected no-dispatcher error, got %v", err)
	}
}

func TestPodcastGenerate_SilentFallback_NoKey(t *testing.T) {
	// vault has no elevenlabs-key + caller opts in via allow_silent_output:
	// → has_narration:false, MP3 still produced. Per #138 the opt-in is
	// mandatory; without it the handler returns missing_credential
	// (covered by TestPodcastGenerate_NoKey_HardFails below).
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90silentfinal")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A": "v1"},
		"script":   [{"speaker":"A","text":"silence please"}],
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		HasNarration bool `json:"has_narration"`
		AudioSize    int  `json:"audio_size"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.HasNarration {
		t.Error("expected has_narration=false")
	}
	if out.AudioSize == 0 {
		t.Error("expected non-zero audio_size even in silent fallback")
	}
}

// TestPodcastGenerate_NoKey_HardFails pins the #138 contract change:
// when no ElevenLabs key resolves through the four-step ladder
// (explicit / vault:elevenlabs-key / vault:elevenlabs-api-key /
// env:HELMDECK_ELEVENLABS_API_KEY) AND allow_silent_output is not set,
// the pack returns a typed missing_credential error rather than
// silently producing a silence-padded MP3. The pre-#138 silent
// behavior caused operators to think podcast.generate was working
// when it wasn't.
func TestPodcastGenerate_NoKey_HardFails(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	_, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A": "v1"},
		"script":   [{"speaker":"A","text":"this should fail"}]
	}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) {
		t.Fatalf("want PackError, got %v", err)
	}
	if pe.Code != packs.CodeInvalidInput {
		t.Errorf("code = %q, want invalid_input", pe.Code)
	}
	if !strings.Contains(pe.Message, "ElevenLabs key not found") {
		t.Errorf("message should explain the missing-credential failure: %q", pe.Message)
	}
	if !strings.Contains(pe.Message, "allow_silent_output") {
		t.Errorf("message should hint at the allow_silent_output opt-in: %q", pe.Message)
	}
}

// TestPodcastGenerate_KeyResolvedFromAlias asserts the #138 back-compat
// alias: operators who created their credential as "elevenlabs-api-key"
// (matching HELMDECK_ELEVENLABS_API_KEY minus the prefix) still get a
// working podcast without renaming.
func TestPodcastGenerate_KeyResolvedFromAlias(t *testing.T) {
	v := vaultWithElevenAliasKey(t, "sk_alias")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90mp3")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A": "v1"},
		"script":   [{"speaker":"A","text":"alias path"}]
	}`)
	if err != nil {
		// We expect the synthesis call to fail (no real ElevenLabs in
		// unit tests), but specifically with a synthesis error, not a
		// missing-credential error. Either succeeding or failing past
		// credential-resolve proves the alias resolved.
		if strings.Contains(err.Error(), "ElevenLabs key not found") {
			t.Fatalf("alias should have resolved; got missing_credential: %v", err)
		}
		// Any other error is fine for this test — we're only pinning
		// that the resolve ladder accepted the alias.
		return
	}
	if raw == nil {
		t.Fatal("expected a response when alias resolves")
	}
}

// TestPodcastGenerate_DryRun_ShortCircuits asserts that dry_run:true
// returns the cost block without invoking the executor (no ffmpeg
// or TTS calls fire). Per #145 the response should include script,
// per-speaker char counts with a _total, and the cost estimate.
func TestPodcastGenerate_DryRun_ShortCircuits(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	// Wire an executor that records every call — we assert it stays
	// empty so dry_run truly skips the synthesis pipeline.
	ex := &podcastTestExecutor{mp3Bytes: []byte("should-not-be-read")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"Alex": "v1", "Jordan": "v2"},
		"script": [
			{"speaker":"Alex","text":"Hello, this is a 30-character line."},
			{"speaker":"Jordan","text":"And this is the second one too!"}
		],
		"dry_run": true
	}`)
	if err != nil {
		t.Fatalf("dry_run handler: %v", err)
	}
	if len(ex.calls) != 0 {
		t.Errorf("dry_run should not invoke the executor, got %d calls: %+v", len(ex.calls), ex.calls)
	}
	var out struct {
		DryRun           bool             `json:"dry_run"`
		Engine           string           `json:"engine"`
		SpeakerCount     int              `json:"speaker_count"`
		TurnCount        int              `json:"turn_count"`
		TTSChars         map[string]int   `json:"tts_chars"`
		EstimatedCostUSD float64          `json:"estimated_cost_usd"`
		Breakdown        map[string]any   `json:"estimated_cost_breakdown"`
		Script           []map[string]any `json:"script"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.DryRun {
		t.Error("dry_run flag should round-trip true")
	}
	if out.Engine != "elevenlabs" {
		t.Errorf("engine = %q", out.Engine)
	}
	if out.SpeakerCount != 2 || out.TurnCount != 2 {
		t.Errorf("counts wrong: speakers=%d turns=%d", out.SpeakerCount, out.TurnCount)
	}
	totalChars := out.TTSChars["_total"]
	if totalChars == 0 {
		t.Errorf("tts_chars._total = 0, want > 0")
	}
	if out.TTSChars["Alex"] == 0 || out.TTSChars["Jordan"] == 0 {
		t.Errorf("per-speaker chars should be populated: %+v", out.TTSChars)
	}
	if out.EstimatedCostUSD <= 0 {
		t.Errorf("estimated_cost_usd should be > 0 for nonzero chars: %v", out.EstimatedCostUSD)
	}
	if out.Breakdown["plan"] != "creator" {
		t.Errorf("breakdown plan = %v, want creator (default)", out.Breakdown["plan"])
	}
	if len(out.Script) != 2 {
		t.Errorf("script should round-trip in dry_run output: %+v", out.Script)
	}
}

// TestPodcastGenerate_RealRun_IncludesCostBlock asserts that a
// non-dry-run response also carries the cost block — operators logging
// real-run costs to accounting workflows shouldn't have to recompute.
//
// Uses allow_silent_output:true to keep the test running without a real
// ElevenLabs server (per #138). The cost block is always populated
// regardless of whether narration succeeded.
func TestPodcastGenerate_RealRun_IncludesCostBlock(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90real")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A": "v1"},
		"script":   [{"speaker":"A","text":"normal run with cost reporting"}],
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		AudioArtifactKey string         `json:"audio_artifact_key"`
		TTSChars         map[string]int `json:"tts_chars"`
		EstimatedCostUSD float64        `json:"estimated_cost_usd"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.AudioArtifactKey == "" {
		t.Error("real run should still produce an audio artifact")
	}
	if out.TTSChars["_total"] == 0 {
		t.Error("real run should include tts_chars._total in response")
	}
	if out.EstimatedCostUSD <= 0 {
		t.Error("real run should include estimated_cost_usd in response")
	}
}

// --- JIT length-sizing (issue #528 / convention #525) ---------------------

// TestPodcastGenerate_Inspect_NoDispatcherNoSession — inspect mode must
// short-circuit before any dispatcher / session / vault use, so it works
// in gateway-less / session-less environments. The agent's planning
// flow uses this to size before committing tokens.
func TestPodcastGenerate_Inspect_NoDispatcherNoSession(t *testing.T) {
	pack := PodcastGenerate(nil, nil, nil)
	ec := &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(`{"speakers":{"A":"v1"},"source_text":"` + strings.Repeat("word ", 3000) + `","inspect":true,"length_intent":"thorough"}`),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("inspect with nil dispatcher/session should not error: %v", err)
	}
	var out struct {
		Inspect              bool   `json:"inspect"`
		SourceWords          int    `json:"source_words"`
		SuggestedDurationMin int    `json:"suggested_duration_min"`
		LengthIntentApplied  string `json:"length_intent_applied"`
		Reason               string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Inspect {
		t.Errorf("inspect flag not echoed: %+v", out)
	}
	if out.SourceWords != 3000 {
		t.Errorf("source_words = %d, want 3000", out.SourceWords)
	}
	// 3000 words / 150 wpm = 20 reading min; * 0.50 = 10 min;
	// clamped to thorough ceiling 8.
	if out.SuggestedDurationMin != 8 {
		t.Errorf("suggested_duration_min = %d, want 8 (clamped to thorough ceiling)", out.SuggestedDurationMin)
	}
	if out.LengthIntentApplied != "intent:thorough" {
		t.Errorf("length_intent_applied = %q, want intent:thorough", out.LengthIntentApplied)
	}
	if !strings.Contains(out.Reason, "3000") || !strings.Contains(out.Reason, "thorough") {
		t.Errorf("reason should mention source size + applied intent: %q", out.Reason)
	}
}

// TestPodcastGenerate_Inspect_ScriptMode — inspect on script mode reports
// the script's word count and flags applied as "n/a:script" so the
// caller knows JIT didn't pick a target (script length is intrinsic).
func TestPodcastGenerate_Inspect_ScriptMode(t *testing.T) {
	// Note: script-mode inspect picks intent:thorough by default
	// since the inspect branch runs sizeForPodcastIntent regardless
	// of mode. The applied label is intent:thorough; mode-specific
	// "n/a:script" only applies on the generate path.
	pack := PodcastGenerate(nil, nil, nil)
	ec := &packs.ExecutionContext{
		Pack: pack,
		Input: json.RawMessage(`{
			"speakers": {"A":"v1"},
			"script":   [{"speaker":"A","text":"one two three four five"}],
			"inspect":  true
		}`),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("inspect script-mode: %v", err)
	}
	var out struct {
		SourceWords          int `json:"source_words"`
		SuggestedDurationMin int `json:"suggested_duration_min"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.SourceWords != 5 {
		t.Errorf("source_words = %d, want 5", out.SourceWords)
	}
	// 5 words → 0.03 reading min → 0.015 * thorough; clamps to floor 3.
	if out.SuggestedDurationMin != 3 {
		t.Errorf("suggested_duration_min = %d, want 3 (floor)", out.SuggestedDurationMin)
	}
}

// TestPodcastGenerate_Inspect_SourceURL_NoScrape — inspect mode does NOT
// scrape source_url; reports source_words=0 with a helpful reason
// mentioning the scrape would be required. Per #528 acceptance: inspect
// is the cheap pack-internal path, no network.
func TestPodcastGenerate_Inspect_SourceURL_NoScrape(t *testing.T) {
	pack := PodcastGenerate(nil, nil, nil)
	ec := &packs.ExecutionContext{
		Pack: pack,
		Input: json.RawMessage(`{
			"speakers":      {"A":"v1"},
			"source_url":    "https://example.com/article",
			"model":         "openrouter/auto",
			"inspect":       true,
			"length_intent": "exhaustive"
		}`),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("inspect source_url: %v", err)
	}
	var out struct {
		SourceWords          int    `json:"source_words"`
		SuggestedDurationMin int    `json:"suggested_duration_min"`
		Reason               string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.SourceWords != 0 {
		t.Errorf("source_words = %d, want 0 (no scrape in inspect mode)", out.SourceWords)
	}
	if out.SuggestedDurationMin != 6 {
		t.Errorf("suggested_duration_min = %d, want 6 (exhaustive floor)", out.SuggestedDurationMin)
	}
	if !strings.Contains(out.Reason, "source_url not scraped") {
		t.Errorf("reason should explain no-scrape: %q", out.Reason)
	}
}

// TestPodcastGenerate_LengthIntent_BackCompat_NoIntentNoNumeric — when
// neither length_intent nor duration_target_min is set, the pack
// preserves the legacy 8-min default. THIS IS THE CRITICAL BACK-COMPAT
// TEST — existing callers must see zero behavior change.
func TestPodcastGenerate_LengthIntent_BackCompat_NoIntentNoNumeric(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90fakemp3")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A":"v1"},
		"script":   [{"speaker":"A","text":"hi"}],
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		LengthIntentApplied     string `json:"length_intent_applied"`
		TargetDurationMinChosen int    `json:"target_duration_min_chosen"`
	}
	_ = json.Unmarshal(raw, &out)
	// Script mode flips applied to "n/a:script" after the size
	// resolver runs. The chosen value still reflects the
	// resolver's pick — which for no-intent-no-numeric is the
	// legacy 8-min default.
	if out.LengthIntentApplied != "n/a:script" {
		t.Errorf("script mode should label applied as n/a:script, got %q", out.LengthIntentApplied)
	}
	if out.TargetDurationMinChosen != 8 {
		t.Errorf("back-compat: target_duration_min_chosen = %d, want 8 (legacy default)", out.TargetDurationMinChosen)
	}
}

// TestPodcastGenerate_LengthIntent_BackCompat_ExplicitDurationWins —
// when DurationTargetMin is set, it wins regardless of LengthIntent.
// Power callers can still bypass the intent table.
func TestPodcastGenerate_LengthIntent_BackCompat_ExplicitDurationWins(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90fakemp3")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A":"v1"},
		"script":   [{"speaker":"A","text":"hi"}],
		"duration_target_min": 11,
		"length_intent": "summary",
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		TargetDurationMinChosen int    `json:"target_duration_min_chosen"`
		LengthIntentApplied     string `json:"length_intent_applied"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.TargetDurationMinChosen != 11 {
		t.Errorf("explicit duration_target_min ignored: got %d, want 11", out.TargetDurationMinChosen)
	}
	// Script mode overrides applied to "n/a:script" after size
	// resolution; in non-script mode this would be "explicit".
	if out.LengthIntentApplied != "n/a:script" {
		t.Errorf("script mode should label applied n/a:script; got %q", out.LengthIntentApplied)
	}
}

// TestPodcastGenerate_OutputMetricsAlwaysPresent — verifies the new
// JIT fields land on every generate response so downstream callers
// can rely on them.
func TestPodcastGenerate_OutputMetricsAlwaysPresent(t *testing.T) {
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90fakemp3")}
	raw, err := runPodcastGenerate(t, v, ex, `{
		"speakers": {"A":"v1"},
		"script":   [{"speaker":"A","text":"hello world this is a test"}],
		"allow_silent_output": true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	for _, k := range []string{
		"source_words", "target_duration_min_chosen", "actual_duration_min",
		"length_intent_applied", "truncated",
	} {
		if _, ok := out[k]; !ok {
			t.Errorf("missing JIT metric %q in output", k)
		}
	}
}

// TestPodcastGenerate_SizeForIntent_TableUnit — unit-level test of
// sizeForPodcastIntent across the three intents at a known source size.
// Catches regression in the intent table or the multiplier math.
func TestPodcastGenerate_SizeForIntent_TableUnit(t *testing.T) {
	// 3000 words / 150 wpm = 20 reading min.
	const srcWords = 3000
	cases := []struct {
		intent string
		want   int
	}{
		{"summary", 3},     // 20 * 0.20 = 4, clamped to ceiling 3
		{"thorough", 8},    // 20 * 0.50 = 10, clamped to ceiling 8
		{"exhaustive", 12}, // 20 * 0.90 = 18, clamped to ceiling 12
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			size := sizeForPodcastIntent(srcWords, tc.intent)
			if size.chosen != tc.want {
				t.Errorf("intent %s: chosen = %d, want %d", tc.intent, size.chosen, tc.want)
			}
			if size.applied != "intent:"+tc.intent {
				t.Errorf("applied = %q", size.applied)
			}
		})
	}
}

// TestPodcastGenerate_SizeForIntent_ClampFloor — tiny source with
// exhaustive intent clamps to the floor rather than producing a
// 0-minute podcast.
func TestPodcastGenerate_SizeForIntent_ClampFloor(t *testing.T) {
	size := sizeForPodcastIntent(50, "exhaustive") // 50/150*0.90 = 0.3 min
	if size.chosen != 6 {                          // exhaustive floor
		t.Errorf("chosen = %d, want 6 (exhaustive floor)", size.chosen)
	}
}

// TestPodcastGenerate_SizeForIntent_UnknownIntentFallsBackToDefault —
// misspelled intent falls back to thorough rather than erroring; agents
// that fat-finger don't lose the whole call.
func TestPodcastGenerate_SizeForIntent_UnknownIntentFallsBackToDefault(t *testing.T) {
	size := sizeForPodcastIntent(3000, "deeper-than-deep-dive")
	if size.applied != "intent:thorough" {
		t.Errorf("applied = %q, want intent:thorough (fallback)", size.applied)
	}
}

// TestPodcastGenerate_ResolvePodcastSize_Precedence — explicit numeric
// > intent > default. Pinned at the resolver level so the precedence
// rule lives in code, not just docs.
func TestPodcastGenerate_ResolvePodcastSize_Precedence(t *testing.T) {
	// (1) explicit numeric wins over intent.
	in := podcastGenerateInput{DurationTargetMin: 9, LengthIntent: "summary"}
	size := resolvePodcastSize(3000, &in)
	if size.chosen != 9 || size.applied != "explicit" {
		t.Errorf("explicit numeric should win; got chosen=%d applied=%q", size.chosen, size.applied)
	}
	// (2) intent wins when numeric absent.
	in = podcastGenerateInput{LengthIntent: "summary"}
	size = resolvePodcastSize(3000, &in)
	if size.applied != "intent:summary" {
		t.Errorf("intent should apply; got applied=%q", size.applied)
	}
	// (3) legacy default when neither set.
	in = podcastGenerateInput{}
	size = resolvePodcastSize(3000, &in)
	if size.chosen != defaultPodcastDurationMin || size.applied != "default:legacy-8min" {
		t.Errorf("legacy default expected; got chosen=%d applied=%q", size.chosen, size.applied)
	}
}
