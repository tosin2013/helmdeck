// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"log/slog"
	"os"
)

func LoadGroqProvider(reg *Registry, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if reg == nil {
		return
	}
	key := envOrFile("HELMDECK_GROQ_API_KEY", "HELMDECK_GROQ_API_KEY_FILE")
	if key == "" {
		return
	}
	baseURL := os.Getenv("HELMDECK_GROQ_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1"
	}
	models := parseModels(os.Getenv("HELMDECK_GROQ_MODELS"), []string{"llama3-8b-8192", "llama3-70b-8192", "mixtral-8x7b-32768"})
	reg.Register(NewOpenAIProvider(OpenAIConfig{
		Name:    "groq",
		APIKey:  key,
		BaseURL: baseURL,
		Models:  models,
	}))
	logger.Info("registered Groq from env",
		"base_url", baseURL, "models", len(models))
}
