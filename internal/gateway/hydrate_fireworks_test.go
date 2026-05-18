// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"context"
	"log/slog"
	"testing"
)

func TestLoadFireworks(t *testing.T) {
	reg := NewRegistry()
	logger := slog.Default()

	t.Setenv("HELMDECK_FIREWORKS_API_KEY", "sk-fake-fireworks-key")

	loadFireworks(reg, logger)

	provider, ok := reg.Get("fireworks")
	if !ok {
		t.Fatalf("expected fireworks provider to be registered, but it was not found in the registry")
	}
	if provider.Name() != "fireworks" {
		t.Errorf("expected provider name %q, got %q", "fireworks", provider.Name())
	}
	op, ok := provider.(*openAIProvider)
	if !ok {
		t.Fatalf("expected *openAIProvider, got %T", provider)
	}
	if op.cfg.BaseURL != "https://api.fireworks.ai/inference/v1" {
		t.Errorf("expected default base URL, got %q", op.cfg.BaseURL)
	}
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 1 || models[0] != "accounts/fireworks/models/llama-v3p1-8b-instruct" {
		t.Errorf("default model not set: %v", models)
	}
}

func TestLoadFireworks_Overrides(t *testing.T) {
	reg := NewRegistry()
	logger := slog.Default()

	t.Setenv("HELMDECK_FIREWORKS_API_KEY", "sk-fake-fireworks-key")
	t.Setenv("HELMDECK_FIREWORKS_BASE_URL", "https://fireworks.example/v1")
	t.Setenv("HELMDECK_FIREWORKS_MODELS", "accounts/acme/models/alpha, accounts/acme/models/beta")

	loadFireworks(reg, logger)

	provider, ok := reg.Get("fireworks")
	if !ok {
		t.Fatalf("expected fireworks provider to be registered, but it was not found in the registry")
	}
	op, ok := provider.(*openAIProvider)
	if !ok {
		t.Fatalf("expected *openAIProvider, got %T", provider)
	}
	if op.cfg.BaseURL != "https://fireworks.example/v1" {
		t.Errorf("expected override base URL, got %q", op.cfg.BaseURL)
	}
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 2 || models[0] != "accounts/acme/models/alpha" || models[1] != "accounts/acme/models/beta" {
		t.Errorf("model override not honored: %v", models)
	}
}

func TestLoadCustomOpenAIProviders_FireworksModelPrefix(t *testing.T) {
	reg := NewRegistry()

	t.Setenv("HELMDECK_FIREWORKS_API_KEY", "sk-fake-fireworks-key")

	LoadCustomOpenAIProviders(reg, silentLogger())

	models, err := reg.AllModels(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	for _, model := range models {
		if model.ID == "fireworks/accounts/fireworks/models/llama-v3p1-8b-instruct" {
			return
		}
	}
	t.Fatalf("fireworks model missing from /v1/models catalog: %+v", models)
}

func TestLoadFireworks_NoKey(t *testing.T) {
	reg := NewRegistry()
	logger := slog.Default()

	loadFireworks(reg, logger)

	if _, ok := reg.Get("fireworks"); ok {
		t.Fatal("expected fireworks provider to be absent when HELMDECK_FIREWORKS_API_KEY is unset")
	}
}
