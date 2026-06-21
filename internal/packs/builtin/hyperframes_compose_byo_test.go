// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_compose_byo_test.go — bring-your-own-audio tests for
// hyperframes.compose. Covers the audio_artifact_key input path that
// resolves a pre-existing artifact's URL into the audio_url slot,
// the mutual-exclusion guard between audio_url and audio_artifact_key,
// and the failure modes when the artifact store is misconfigured or
// the key is missing.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// runComposeWithArtifacts is runCompose's BYO twin — wires a memory
// artifact store into the execution context so audio_artifact_key
// resolution exercises the real Get() path.
func runComposeWithArtifacts(t *testing.T, disp *scriptedDispatcherWT, store packs.ArtifactStore, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesCompose(disp)
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: store,
	}
	return pack.Handler(context.Background(), ec)
}

// --- happy path -----------------------------------------------------------

func TestHyperframesCompose_BYO_AudioArtifactKey_ResolvesAndComposes(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	art, err := store.Put(context.Background(), "operator-uploads", "narration.mp3",
		[]byte("ID3 stub bytes"), "audio/mpeg")
	if err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runComposeWithArtifacts(t, disp, store, `{
		"description":"narrated explainer about a topic",
		"model":"openrouter/auto",
		"aspect_ratio":"16:9",
		"audio_artifact_key":"`+art.Key+`",
		"duration_seconds":12
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeCompose(t, raw)
	if !out.HasAudio {
		t.Errorf("expected has_audio=true when audio_artifact_key resolves")
	}
	if !strings.Contains(out.CompositionHTML, art.URL) {
		t.Errorf("expected composition to embed resolved URL %q; got: %s",
			art.URL, out.CompositionHTML[:200])
	}
	if out.DurationSeconds != 12 {
		t.Errorf("expected duration 12, got %f", out.DurationSeconds)
	}
}

// --- mutual exclusion -----------------------------------------------------

func TestHyperframesCompose_BYO_BothInputs_RejectsInvalidInput(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	_, err := runComposeWithArtifacts(t, disp, store, `{
		"description":"x",
		"model":"openrouter/auto",
		"audio_url":"https://store/a.mp3",
		"audio_artifact_key":"some-key",
		"duration_seconds":12
	}`)
	if err == nil {
		t.Fatalf("expected error when both audio_url and audio_artifact_key supplied")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got: %v", err)
	}
	if !strings.Contains(pe.Message, "mutually exclusive") {
		t.Errorf("error message should explain mutual-exclusion: %s", pe.Message)
	}
}

// --- error: artifact key missing ------------------------------------------

func TestHyperframesCompose_BYO_KeyNotInStore_RejectsInvalidInput(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	_, err := runComposeWithArtifacts(t, disp, store, `{
		"description":"x",
		"model":"openrouter/auto",
		"audio_artifact_key":"nonexistent-key",
		"duration_seconds":12
	}`)
	if err == nil {
		t.Fatalf("expected error for missing artifact key")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got: %v", err)
	}
	if !strings.Contains(pe.Message, "not found") {
		t.Errorf("error message should explain artifact missing: %s", pe.Message)
	}
}

// --- error: no artifact store wired ---------------------------------------

func TestHyperframesCompose_BYO_NoArtifactStore_RejectsArtifactFailed(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	// Call without store (nil)
	pack := HyperframesCompose(disp)
	ec := &packs.ExecutionContext{
		Pack: pack,
		Input: json.RawMessage(`{
			"description":"x",
			"model":"openrouter/auto",
			"audio_artifact_key":"some-key",
			"duration_seconds":12
		}`),
	}
	_, err := pack.Handler(context.Background(), ec)
	if err == nil {
		t.Fatalf("expected error when artifact store is nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeArtifactFailed {
		t.Fatalf("expected CodeArtifactFailed, got: %v", err)
	}
}

// --- back-compat: audio_url path unchanged -------------------------------

func TestHyperframesCompose_BYO_AudioURLPath_Unchanged(t *testing.T) {
	// The existing audio_url path must work without an artifact store
	// (no BYO trigger). Regression guard.
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runCompose(t, disp, `{
		"description":"x",
		"model":"openrouter/auto",
		"audio_url":"https://store/a.mp3",
		"duration_seconds":12
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeCompose(t, raw)
	if !out.HasAudio {
		t.Errorf("expected has_audio=true via audio_url path")
	}
	if !strings.Contains(out.CompositionHTML, "https://store/a.mp3") {
		t.Errorf("expected audio_url embedded in composition")
	}
}
