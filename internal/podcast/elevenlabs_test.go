// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestElevenLabsEngine_Synthesize_HappyPath(t *testing.T) {
	captured := struct {
		path    string
		auth    string
		body    string
		query   string
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.path = r.URL.Path
		captured.auth = r.Header.Get("xi-api-key")
		captured.query = r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		captured.body = string(raw)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("\xff\xfb\x90\x00"+strings.Repeat("\x00", 1024))) // fake mp3 header
	}))
	t.Cleanup(srv.Close)

	eng := &ElevenLabsEngine{APIKey: "sk_test123", BaseURL: srv.URL}
	turn := Turn{Speaker: "Alex", Text: "Welcome to the show."}
	opts := SynthesizeOptions{VoiceID: "voice-abc", ModelID: "eleven_turbo_v2_5"}

	mp3, err := eng.Synthesize(context.Background(), turn, opts)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if len(mp3) == 0 {
		t.Fatal("expected mp3 bytes")
	}
	if captured.path != "/v1/text-to-speech/voice-abc" {
		t.Errorf("path = %q", captured.path)
	}
	if captured.auth != "sk_test123" {
		t.Errorf("auth = %q", captured.auth)
	}
	if !strings.Contains(captured.body, `"text":"Welcome to the show."`) {
		t.Errorf("body missing text: %q", captured.body)
	}
	if !strings.Contains(captured.body, `"model_id":"eleven_turbo_v2_5"`) {
		t.Errorf("body missing model_id: %q", captured.body)
	}
	if !strings.Contains(captured.query, "output_format=mp3_44100_192") {
		t.Errorf("query missing output_format (expect Creator-tier 192k default): %q", captured.query)
	}
}

func TestElevenLabsEngine_Synthesize_NoAPIKey(t *testing.T) {
	eng := &ElevenLabsEngine{APIKey: ""}
	_, err := eng.Synthesize(context.Background(),
		Turn{Speaker: "Alex", Text: "hi"},
		SynthesizeOptions{VoiceID: "v"})
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
	if !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("expected ErrNoAPIKey, got %v", err)
	}
}

func TestElevenLabsEngine_Synthesize_NoVoiceID(t *testing.T) {
	eng := &ElevenLabsEngine{APIKey: "k"}
	_, err := eng.Synthesize(context.Background(),
		Turn{Speaker: "Alex", Text: "hi"},
		SynthesizeOptions{})
	if err == nil {
		t.Fatal("expected error for empty voice_id")
	}
	if !strings.Contains(err.Error(), "voice_id required") {
		t.Errorf("expected voice_id error, got %v", err)
	}
}

func TestElevenLabsEngine_Synthesize_API401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":{"status":"unauthorized","message":"Invalid API key"}}`))
	}))
	t.Cleanup(srv.Close)

	eng := &ElevenLabsEngine{APIKey: "sk_bad", BaseURL: srv.URL}
	_, err := eng.Synthesize(context.Background(),
		Turn{Speaker: "Alex", Text: "hi"},
		SynthesizeOptions{VoiceID: "v"})
	if err == nil {
		t.Fatal("expected 401 to surface")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("expected upstream message in error, got %v", err)
	}
}

func TestElevenLabsEngine_Name(t *testing.T) {
	eng := &ElevenLabsEngine{}
	if eng.Name() != "elevenlabs" {
		t.Errorf("name = %q", eng.Name())
	}
}

// TestElevenLabsFormat_EnvVarOverride pins the Starter-tier escape
// hatch: operators on the ElevenLabs Starter subscription cap out at
// mp3_44100_128 and would get 4xx from the Creator-tier default
// without this. The env var ladder must take precedence over the
// 192k built-in default.
func TestElevenLabsFormat_EnvVarOverride(t *testing.T) {
	t.Setenv("HELMDECK_ELEVENLABS_FORMAT", "mp3_44100_128")
	if got := elevenLabsFormat(); got != "mp3_44100_128" {
		t.Errorf("HELMDECK_ELEVENLABS_FORMAT must win over the 192k default; got %q", got)
	}
}

func TestElevenLabsFormat_DefaultWhenUnset(t *testing.T) {
	t.Setenv("HELMDECK_ELEVENLABS_FORMAT", "")
	if got := elevenLabsFormat(); got != "mp3_44100_192" {
		t.Errorf("unset env must fall back to Creator-tier mp3_44100_192; got %q", got)
	}
}
