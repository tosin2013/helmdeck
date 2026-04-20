// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

// hydrate_openrouter.go (T202a) — env-var fast path for
// OpenAI-compatible aggregators that don't have their own keystore
// entry yet. Today this ships exactly one provider (OpenRouter) so
// the v0.6.0 validation run can use the maintainer's OpenRouter
// token without first extending the keystore schema with a
// per-key BaseURL column.
//
// Why an env-var path: the keystore stores (provider, label, key)
// triples but no base URL, so an aggregator like OpenRouter
// can't be persisted there yet without a schema migration. The
// env-var path is the smallest possible bridge — operators export
// HELMDECK_OPENROUTER_API_KEY (and optionally _BASE_URL / _MODELS)
// and the aggregator shows up in /v1/models without any UI work.
// When the keystore grows a base_url column post-v0.6.0 this file
// can be retired in favor of the keystore path.
//
// Helmdeck splits req.Model on the FIRST `/` only (gateway.go:172),
// so an OpenRouter request comes in as `openrouter/minimax/minimax-m2.7`
// and parses cleanly: provider=openrouter, model=minimax/minimax-m2.7.
// The bare model is what OpenRouter expects in the request body, so
// no string surgery is needed beyond the prefix split helmdeck
// already does.

import (
	"log/slog"
	"os"
)

// LoadCustomOpenAIProviders registers OpenAI-compatible aggregators
// from env vars. Today exactly one is supported (OpenRouter); the
// pattern is structured so additional aggregators can be added with
// one extra call without touching the rest of the gateway.
//
// Idempotent: re-running replaces the existing entry by name. Safe
// to call from the hot-reload path even though the env vars don't
// change at runtime — keeps the call site uniform with HydrateFromKeystore.
func LoadCustomOpenAIProviders(reg *Registry, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if reg == nil {
		return
	}
	loadOpenRouter(reg, logger)
	LoadGroqProvider(reg, logger)
}

func loadOpenRouter(reg *Registry, logger *slog.Logger) {
	key := envOrFile("HELMDECK_OPENROUTER_API_KEY", "HELMDECK_OPENROUTER_API_KEY_FILE")
	if key == "" {
		return
	}
	baseURL := os.Getenv("HELMDECK_OPENROUTER_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api"
	}
	models := parseModels(os.Getenv("HELMDECK_OPENROUTER_MODELS"), []string{"minimax/minimax-m2.7"})
	reg.Register(NewOpenAIProvider(OpenAIConfig{
		Name:    "openrouter",
		APIKey:  key,
		BaseURL: baseURL,
		Models:  models,
	}))
	logger.Info("registered OpenRouter from env",
		"base_url", baseURL, "models", len(models))
}

// envOrFile mirrors the helper of the same name in cmd/control-plane/main.go.
// Duplicated here so the gateway package stays free of any cmd/ import.
// Reads the named env var first; falls back to the contents of the
// file at the path in the *_FILE variant. Returns "" if neither is set.
func envOrFile(envKey, fileEnvKey string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	path := os.Getenv(fileEnvKey)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	out := string(b)
	// Trim trailing newlines from heredoc/echo-generated secret files.
	for len(out) > 0 && (out[len(out)-1] == '\n' || out[len(out)-1] == '\r') {
		out = out[:len(out)-1]
	}
	return out
}
