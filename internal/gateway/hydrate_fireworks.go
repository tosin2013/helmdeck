// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

// hydrate_fireworks.go — env-var fast path for Fireworks AI's
// OpenAI-compatible inference API. Operators export
// HELMDECK_FIREWORKS_API_KEY (and optionally _BASE_URL / _MODELS) and
// the adapter appears in /v1/models under the `fireworks/` prefix.
//
// Fireworks model IDs are globally qualified strings such as
// `accounts/fireworks/models/llama-v3p1-8b-instruct`. Helmdeck splits
// provider/model on the FIRST `/`, so requests keep the Fireworks model
// ID intact after the `fireworks/` prefix is removed.

import (
	"log/slog"
	"os"
)

func loadFireworks(reg *Registry, logger *slog.Logger) {
	key := envOrFile("HELMDECK_FIREWORKS_API_KEY", "HELMDECK_FIREWORKS_API_KEY_FILE")
	if key == "" {
		return
	}
	baseURL := os.Getenv("HELMDECK_FIREWORKS_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.fireworks.ai/inference/v1"
	}
	models := parseModels(
		os.Getenv("HELMDECK_FIREWORKS_MODELS"),
		[]string{"accounts/fireworks/models/llama-v3p1-8b-instruct"},
	)
	reg.Register(NewOpenAIProvider(OpenAIConfig{
		Name:    "fireworks",
		APIKey:  key,
		BaseURL: baseURL,
		Models:  models,
	}))
	logger.Info("registered Fireworks from env",
		"base_url", baseURL, "models", len(models))
}
