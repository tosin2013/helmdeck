// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package voices is the per-engine voice catalog. Today only
// ElevenLabs is supported; the package is shaped to support multiple
// engines (PlayHT, Hume, Resemble) without each pack reimplementing
// HTTP plumbing.
//
// The single canonical caller is the `helmdeck://voices` MCP resource
// (#143). slides.narrate's pickRandomVoice helper (renamed and
// migrated) and a future podcast.generate voice-id pre-validation
// path also call into this package.
package voices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ElevenLabsBaseURL is the API host. Exported as a package var
// (rather than a const) so tests can point at an httptest stub
// without injecting a client into every call site.
var ElevenLabsBaseURL = "https://api.elevenlabs.io"

// Voice is the engine-agnostic shape we surface upstream. Mirrors
// ElevenLabs' /v1/voices payload but trims it to the fields agents
// actually need to pick a voice — name + labels (accent, gender,
// use-case) + a preview URL for human review. Source identifies the
// originating engine so future multi-engine output can disambiguate.
type Voice struct {
	VoiceID    string            `json:"voice_id"`
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels,omitempty"`
	PreviewURL string            `json:"preview_url,omitempty"`
	Source     string            `json:"source"`
}

// ListVoices fetches the operator's full ElevenLabs voice catalog.
// Returns the trimmed Voice shape (not the raw API blob). Caller is
// responsible for caching — this function makes one HTTP round-trip
// per call.
//
// Returns an empty slice with nil error when the account has no
// voices. Returns an error with the upstream status code when the
// API rejects the request (most commonly 401 for an invalid key).
func ListVoices(ctx context.Context, apiKey string) ([]Voice, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("voices: api key is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ElevenLabsBaseURL+"/v1/voices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elevenlabs /v1/voices %d", resp.StatusCode)
	}

	var parsed struct {
		Voices []struct {
			VoiceID     string            `json:"voice_id"`
			Name        string            `json:"name"`
			Labels      map[string]string `json:"labels"`
			PreviewURL  string            `json:"preview_url"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("voices: parse: %w", err)
	}
	out := make([]Voice, 0, len(parsed.Voices))
	for _, v := range parsed.Voices {
		out = append(out, Voice{
			VoiceID:    v.VoiceID,
			Name:       v.Name,
			Labels:     v.Labels,
			PreviewURL: v.PreviewURL,
			Source:     "elevenlabs",
		})
	}
	return out, nil
}
