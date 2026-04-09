// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

// hydrate.go (T202a) — wires the encrypted keystore into the live
// gateway.Registry at startup AND on every keystore mutation.
//
// Background: T202 shipped the provider adapters (openai, anthropic,
// gemini, ollama, deepseek) as Go code, but the integration step
// — instantiating each adapter from a stored key and registering it
// with the gateway — was never wired in cmd/control-plane/main.go.
// The result was a registry that booted empty: /v1/models returned
// nothing, /v1/chat/completions always 404'd, and the T607
// success-rate panel could never show data because no dispatch ever
// reached an upstream. T202a closes that loop.

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"
)

// KeystoreReader is the minimal slice of *keystore.Store that hydrate
// needs. Defining it here as an interface (instead of importing the
// concrete type) keeps internal/gateway free of any database/sql or
// SQLite import, which means hydrate_test.go can stub the keystore
// without spinning up a real database.
type KeystoreReader interface {
	List(ctx context.Context, provider string) ([]KeystoreRecord, error)
	Decrypt(ctx context.Context, id string) (string, error)
}

// KeystoreRecord mirrors the fields hydrate needs from
// keystore.Record. The control plane provides an adapter (in
// cmd/control-plane/main.go) that wraps *keystore.Store and converts
// []keystore.Record → []KeystoreRecord.
type KeystoreRecord struct {
	ID        string
	Provider  string
	Label     string
	CreatedAt time.Time
}

// defaultModelCatalog is the per-provider model list registered when
// the operator has not pinned an explicit catalog via the
// HELMDECK_<PROVIDER>_MODELS env-var override. Pragmatic defaults
// covering the well-known model ids each provider serves at the time
// of v0.6.0; operators with newer/older catalogs override per
// provider without touching code.
var defaultModelCatalog = map[string][]string{
	"openai":    {"gpt-4o", "gpt-4o-mini", "gpt-4-turbo"},
	"anthropic": {"claude-sonnet-4-6", "claude-opus-4-6", "claude-haiku-4-5"},
	"gemini":    {"gemini-2.5-flash", "gemini-2.5-pro", "gemini-2.0-flash"},
	"deepseek":  {"deepseek-chat", "deepseek-reasoner"},
	"ollama":    {"llama3", "qwen2.5-coder"},
}

// HydrateFromKeystore enumerates the keystore and registers a
// provider adapter for every supported provider name. Multiple keys
// for the same provider name are tolerated — the newest CreatedAt
// wins, with a warning logged for each shadowed key. Unknown
// provider names (anything outside defaultModelCatalog) are logged
// and skipped, never panic.
//
// The function is intentionally tolerant: a single decrypt failure
// or unknown provider must not abort startup, since the operator
// can still have other working providers (or just the OpenRouter
// env-var fast path) and we'd rather boot degraded than refuse.
//
// HydrateFromKeystore is idempotent — replaying it after a key
// add/rotate/delete is the supported hot-reload path. Registry.Register
// replaces existing entries by name, so re-running this function
// converges the live registry to the keystore state.
func HydrateFromKeystore(ctx context.Context, reg *Registry, ks KeystoreReader, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if reg == nil || ks == nil {
		return nil
	}
	recs, err := ks.List(ctx, "")
	if err != nil {
		logger.Warn("keystore list failed during gateway hydrate", "err", err)
		return nil
	}
	// Group by provider name; pick the newest record per provider.
	byProvider := make(map[string]KeystoreRecord, len(recs))
	for _, r := range recs {
		existing, ok := byProvider[r.Provider]
		if !ok {
			byProvider[r.Provider] = r
			continue
		}
		if r.CreatedAt.After(existing.CreatedAt) {
			logger.Warn("multiple keystore entries for provider, using newer",
				"provider", r.Provider, "shadowed_label", existing.Label, "new_label", r.Label)
			byProvider[r.Provider] = r
		} else {
			logger.Warn("multiple keystore entries for provider, keeping newer",
				"provider", r.Provider, "shadowed_label", r.Label, "kept_label", existing.Label)
		}
	}
	// Stable order so log output is deterministic for tests.
	names := make([]string, 0, len(byProvider))
	for name := range byProvider {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		rec := byProvider[name]
		if _, supported := defaultModelCatalog[name]; !supported {
			logger.Warn("keystore has key for unsupported provider, skipping",
				"provider", name, "id", rec.ID, "label", rec.Label)
			continue
		}
		plaintext, err := ks.Decrypt(ctx, rec.ID)
		if err != nil {
			logger.Warn("keystore decrypt failed, skipping provider",
				"provider", name, "id", rec.ID, "err", err)
			continue
		}
		models := modelCatalogFor(name)
		p := buildProvider(name, plaintext, models)
		if p == nil {
			logger.Warn("no adapter for provider, skipping", "provider", name)
			continue
		}
		reg.Register(p)
		logger.Info("registered provider from keystore",
			"provider", name, "label", rec.Label, "models", len(models))
	}
	return nil
}

// modelCatalogFor returns the model list for a provider, honoring
// the HELMDECK_<UPPER>_MODELS env-var override. Empty/missing env
// var falls through to defaultModelCatalog.
func modelCatalogFor(provider string) []string {
	envKey := "HELMDECK_" + strings.ToUpper(provider) + "_MODELS"
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		return parseModels(raw, nil)
	}
	out := make([]string, len(defaultModelCatalog[provider]))
	copy(out, defaultModelCatalog[provider])
	return out
}

// buildProvider constructs the right adapter type for a known
// provider name. Returns nil if the name has no adapter mapping
// (which should be impossible after the defaultModelCatalog gate
// in HydrateFromKeystore, but defense-in-depth keeps the function
// safe to call directly from tests).
func buildProvider(name, apiKey string, models []string) Provider {
	switch name {
	case "openai":
		return NewOpenAIProvider(OpenAIConfig{Name: "openai", APIKey: apiKey, Models: models})
	case "deepseek":
		return NewDeepseekProvider(OpenAIConfig{APIKey: apiKey, Models: models})
	case "anthropic":
		return NewAnthropicProvider(AnthropicConfig{APIKey: apiKey, Models: models})
	case "gemini":
		return NewGeminiProvider(GeminiConfig{APIKey: apiKey, Models: models})
	case "ollama":
		// Ollama is a local runtime — APIKey is ignored, BaseURL
		// defaults to http://localhost:11434 inside the adapter.
		return NewOllamaProvider(OllamaConfig{Models: models})
	}
	return nil
}

// parseModels splits a comma-separated env-var value into a clean
// model list. Empty entries are dropped. fallback is returned when
// the input is empty.
func parseModels(csv string, fallback []string) []string {
	if csv == "" {
		return fallback
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
