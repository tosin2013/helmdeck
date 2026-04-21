// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"log/slog"
	"testing"
)

func TestLoadMistral(t *testing.T) {
	reg := NewRegistry()
	logger := slog.Default()

	t.Setenv("HELMDECK_MISTRAL_API_KEY", "sk-fake-mistral-key")

	loadMistral(reg, logger)

	provider, ok := reg.Get("mistral")
	if !ok {
		t.Fatalf("expected mistral provider to be registered, but it was not found in the registry")
	}
	if provider.Name() != "mistral" {
		t.Errorf("expected provider name %q, got %q", "mistral", provider.Name())
	}
}

func TestLoadMistral_NoKey(t *testing.T) {
	reg := NewRegistry()
	logger := slog.Default()

	loadMistral(reg, logger)

	if _, ok := reg.Get("mistral"); ok {
		t.Fatal("expected mistral provider to be absent when HELMDECK_MISTRAL_API_KEY is unset")
	}
}
