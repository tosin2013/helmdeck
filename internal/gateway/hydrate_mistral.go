// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

// hydrate_mistral.go — env-var fast path for Mistral's OpenAI-compatible
// API. Mirrors loadOpenRouter: operators export HELMDECK_MISTRAL_API_KEY
// (and optionally _BASE_URL / _MODELS) and the adapter shows up in
// /v1/models under the `mistral/` prefix that gateway.ListModels adds.
//
// Default models intentionally have NO provider prefix — the gateway
// prefixes provider names itself when building the /v1/models response
// (gateway.go:333). Upstream Mistral IDs are bare strings like
// `mistral-large-latest`, so the fallback list mirrors that.

import (
	"log/slog"
	"os"
)

func loadMistral(reg *Registry, logger *slog.Logger) {
	key := envOrFile("HELMDECK_MISTRAL_API_KEY", "HELMDECK_MISTRAL_API_KEY_FILE")
	if key == "" {
		return
	}
	baseURL := os.Getenv("HELMDECK_MISTRAL_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.mistral.ai/v1"
	}
	models := parseModels(os.Getenv("HELMDECK_MISTRAL_MODELS"), []string{"mistral-large-latest"})
	reg.Register(NewOpenAIProvider(OpenAIConfig{
		Name:    "mistral",
		APIKey:  key,
		BaseURL: baseURL,
		Models:  models,
	}))
	logger.Info("registered Mistral from env",
		"base_url", baseURL, "models", len(models))
}
