// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	elevenLabsBaseURL = "https://api.elevenlabs.io"
	// elevenLabsDefaultFormat is the fallback ElevenLabs output_format
	// when HELMDECK_ELEVENLABS_FORMAT is unset. mp3_44100_192 is the
	// Creator-tier bitrate (192 kbps MP3, 44.1 kHz sample rate matched
	// to the in-tree avenc pipeline so no resampling happens). The
	// Starter tier caps at mp3_44100_128 — those operators must set
	// HELMDECK_ELEVENLABS_FORMAT=mp3_44100_128 in their environment to
	// stay within their subscription. See docs/howto/configure-llm-providers.md.
	elevenLabsDefaultFormat = "mp3_44100_192"
	elevenLabsDefaultModel  = "eleven_turbo_v2_5"
	elevenLabsResponseCap   = 32 << 20 // 32 MiB per turn — big enough for ~30 min of 128 kbps mp3
	elevenLabsRequestTOut   = 60 * time.Second
)

// ElevenLabsEngine implements Engine against api.elevenlabs.io. It
// reads the API key from the APIKey field — when empty, Synthesize
// returns ErrNoAPIKey so the pack handler can route to a silent-
// fallback path. Voice IDs come from SynthesizeOptions, so the same
// engine instance synthesizes multi-speaker dialogue: the pack just
// looks up turn.Speaker → voice_id in its own speakers map and
// passes that voice_id in.
//
// The API surface mirrors what slides_narrate.go used to do inline;
// extracting it here lets podcast.generate share the implementation
// and lets future PRs swap in PlayHT/Hume/Resemble without touching
// either pack handler.
type ElevenLabsEngine struct {
	APIKey string

	// BaseURL overrides the API base for tests (httptest.NewServer).
	// Empty means production (api.elevenlabs.io).
	BaseURL string
}

// ErrNoAPIKey signals the pack handler should route to silent
// fallback. Returned only by Synthesize (not by Name).
var ErrNoAPIKey = fmt.Errorf("podcast/elevenlabs: no API key configured")

// Name returns "elevenlabs".
func (e *ElevenLabsEngine) Name() string { return "elevenlabs" }

// elevenLabsFormat resolves the ElevenLabs output_format the TTS
// request should ask for. The env var HELMDECK_ELEVENLABS_FORMAT
// wins when set (operator-level override); the default fires
// otherwise. Same ladder shape as the slides_narrate.go variant —
// kept package-local rather than shared to avoid an internal/podcast
// → internal/packs/builtin import. Documented in
// docs/howto/configure-llm-providers.md.
func elevenLabsFormat() string {
	if v := strings.TrimSpace(os.Getenv("HELMDECK_ELEVENLABS_FORMAT")); v != "" {
		return v
	}
	return elevenLabsDefaultFormat
}

// Synthesize converts one turn to MP3 bytes. Voice + model come from
// opts. When APIKey is empty, returns ErrNoAPIKey — the pack handler
// catches this and falls back to anullsrc-padded silent MP3 segments.
func (e *ElevenLabsEngine) Synthesize(ctx context.Context, turn Turn, opts SynthesizeOptions) ([]byte, error) {
	if e.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	if opts.VoiceID == "" {
		return nil, fmt.Errorf("podcast/elevenlabs: voice_id required for speaker %q", turn.Speaker)
	}
	model := opts.ModelID
	if model == "" {
		model = elevenLabsDefaultModel
	}
	stability := opts.Stability
	if stability == 0 {
		stability = 0.5
	}
	sim := opts.SimilarityBoost
	if sim == 0 {
		sim = 0.75
	}

	reqBody, _ := json.Marshal(map[string]any{
		"text":     turn.Text,
		"model_id": model,
		"voice_settings": map[string]any{
			"stability":        stability,
			"similarity_boost": sim,
		},
	})
	base := e.BaseURL
	if base == "" {
		base = elevenLabsBaseURL
	}
	url := fmt.Sprintf("%s/v1/text-to-speech/%s?output_format=%s",
		base, opts.VoiceID, elevenLabsFormat())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/mpeg")
	httpReq.Header.Set("xi-api-key", e.APIKey)

	client := &http.Client{Timeout: elevenLabsRequestTOut}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, elevenLabsResponseCap))
	if err != nil {
		return nil, fmt.Errorf("read elevenlabs response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(body)
		if len(msg) > 256 {
			msg = msg[:256] + "..."
		}
		return nil, fmt.Errorf("elevenlabs %d: %s", resp.StatusCode, strings.TrimSpace(msg))
	}
	return body, nil
}
