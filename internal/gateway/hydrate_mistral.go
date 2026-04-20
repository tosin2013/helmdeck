// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"log/slog"
	"os"
)

// loadMistral registers the Mistral API provider from environment variables.
// It uses the OpenAI-compatible endpoint.
func loadMistral(reg *Registry, logger *slog.Logger) {
	key := envOrFile("HELMDECK_MISTRAL_API_KEY", "HELMDECK_MISTRAL_API_KEY_FILE")
	if key == "" {
		return
	}

	baseURL := os.Getenv("HELMDECK_MISTRAL_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.mistral.ai/v1"
	}

	models := parseModels(os.Getenv("HELMDECK_MISTRAL_MODELS"), []string{"mistral/mistral-large-latest"})

	reg.Register(NewOpenAIProvider(OpenAIConfig{
		Name:    "mistral",
		APIKey:  key,
		BaseURL: baseURL,
		Models:  models,
	}))

	logger.Info("registered Mistral from env",
		"base_url", baseURL, "models", len(models))
}