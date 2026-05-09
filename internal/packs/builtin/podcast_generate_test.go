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
		case strings.HasPrefix(script, "ffprobe"):
			return session.ExecResult{Stdout: []byte("5.123\n")}, nil
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
	pack := PodcastGenerate(v, nil, nil)
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
