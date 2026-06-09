// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import "testing"

func TestDefaultPackModel_CallerInputWins(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "openrouter/operator-override")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", "stack-pin")
	got := defaultPackModel("openrouter/caller-explicit")
	if got != "openrouter/caller-explicit" {
		t.Errorf("got %q, want caller's explicit value to win", got)
	}
}

func TestDefaultPackModel_CallerInputTrimmed(t *testing.T) {
	// Whitespace-padded caller input is treated as a real value
	// (non-empty after trim), but the trimmed form is returned so
	// downstream provider routing doesn't have to re-trim.
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "openrouter/operator-override")
	got := defaultPackModel("  openrouter/with-spaces  ")
	if got != "openrouter/with-spaces" {
		t.Errorf("got %q, want caller value trimmed (still wins over env override)", got)
	}
}

func TestDefaultPackModel_OperatorOverride(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "openrouter/operator-override")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", "stack-pin")
	got := defaultPackModel("")
	if got != "openrouter/operator-override" {
		t.Errorf("got %q, want HELMDECK_DEFAULT_PACK_MODEL value", got)
	}
}

func TestDefaultPackModel_StackPin(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", "minimax/minimax-m2.7,foo,bar")
	got := defaultPackModel("")
	if got != "openrouter/minimax/minimax-m2.7" {
		t.Errorf("got %q, want first OPENROUTER_MODELS entry prefixed with openrouter/", got)
	}
}

func TestDefaultPackModel_StackPinAlreadyPrefixed(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", "openrouter/auto,foo")
	got := defaultPackModel("")
	if got != "openrouter/auto" {
		t.Errorf("got %q, want existing openrouter/ prefix preserved", got)
	}
}

func TestDefaultPackModel_HardFallback(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", "")
	got := defaultPackModel("")
	if got != "openrouter/auto" {
		t.Errorf("got %q, want openrouter/auto hard fallback", got)
	}
}

func TestDefaultPackModel_EmptyEnvIsTreatedAsAbsent(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "   ")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", ",,  ,")
	got := defaultPackModel("")
	if got != "openrouter/auto" {
		t.Errorf("got %q, want fallback when env vars are whitespace/commas only", got)
	}
}

func TestFirstOpenrouterModel_SkipsLeadingEmpty(t *testing.T) {
	got := firstOpenrouterModel(",,a,b")
	if got != "openrouter/a" {
		t.Errorf("got %q, want openrouter/a", got)
	}
}

func TestFirstOpenrouterModel_EmptyInput(t *testing.T) {
	if got := firstOpenrouterModel(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := firstOpenrouterModel(",,,"); got != "" {
		t.Errorf("got %q, want empty for commas-only input", got)
	}
}

func TestFirstOpenrouterModel_PreservesProviderPrefix(t *testing.T) {
	got := firstOpenrouterModel("openrouter/foo,bar")
	if got != "openrouter/foo" {
		t.Errorf("got %q, want openrouter/foo (prefix preserved)", got)
	}
}
