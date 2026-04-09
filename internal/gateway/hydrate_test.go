// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// stubKeystore is the test double for KeystoreReader. Records the
// list it returns and the decrypt outcomes by id, so tests can
// stage exactly the rows they need without booting a real database.
type stubKeystore struct {
	rows     []KeystoreRecord
	plain    map[string]string // id → plaintext
	decErr   map[string]error  // id → decrypt error
	listErr  error
}

func (s *stubKeystore) List(ctx context.Context, provider string) ([]KeystoreRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	if provider == "" {
		return s.rows, nil
	}
	out := make([]KeystoreRecord, 0, len(s.rows))
	for _, r := range s.rows {
		if r.Provider == provider {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *stubKeystore) Decrypt(ctx context.Context, id string) (string, error) {
	if err, ok := s.decErr[id]; ok {
		return "", err
	}
	if pt, ok := s.plain[id]; ok {
		return pt, nil
	}
	return "", errors.New("stub: id not found")
}

// silentLogger discards every log line — keeps test output clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHydrateFromKeystore_RegistersAllProviders(t *testing.T) {
	now := time.Now()
	ks := &stubKeystore{
		rows: []KeystoreRecord{
			{ID: "k1", Provider: "openai", Label: "prod", CreatedAt: now},
			{ID: "k2", Provider: "anthropic", Label: "prod", CreatedAt: now},
			{ID: "k3", Provider: "gemini", Label: "prod", CreatedAt: now},
		},
		plain: map[string]string{"k1": "sk-1", "k2": "sk-2", "k3": "sk-3"},
	}
	reg := NewRegistry()
	if err := HydrateFromKeystore(context.Background(), reg, ks, silentLogger()); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	for _, want := range []string{"openai", "anthropic", "gemini"} {
		if _, ok := reg.Get(want); !ok {
			t.Errorf("expected provider %q registered", want)
		}
	}
}

func TestHydrateFromKeystore_NewestKeyWins(t *testing.T) {
	older := time.Now().Add(-1 * time.Hour)
	newer := time.Now()
	ks := &stubKeystore{
		rows: []KeystoreRecord{
			{ID: "old", Provider: "openai", Label: "v1", CreatedAt: older},
			{ID: "new", Provider: "openai", Label: "v2", CreatedAt: newer},
		},
		plain: map[string]string{"old": "sk-old", "new": "sk-new"},
	}
	reg := NewRegistry()
	if err := HydrateFromKeystore(context.Background(), reg, ks, silentLogger()); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	p, ok := reg.Get("openai")
	if !ok {
		t.Fatal("openai not registered")
	}
	op, ok := p.(*openAIProvider)
	if !ok {
		t.Fatalf("expected *openAIProvider, got %T", p)
	}
	if op.cfg.APIKey != "sk-new" {
		t.Errorf("newer key did not win: got %q want %q", op.cfg.APIKey, "sk-new")
	}
}

func TestHydrateFromKeystore_DecryptErrorIsTolerated(t *testing.T) {
	now := time.Now()
	ks := &stubKeystore{
		rows: []KeystoreRecord{
			{ID: "good", Provider: "openai", Label: "prod", CreatedAt: now},
			{ID: "bad", Provider: "anthropic", Label: "prod", CreatedAt: now},
		},
		plain:  map[string]string{"good": "sk-good"},
		decErr: map[string]error{"bad": errors.New("decrypt boom")},
	}
	reg := NewRegistry()
	if err := HydrateFromKeystore(context.Background(), reg, ks, silentLogger()); err != nil {
		t.Fatalf("hydrate should tolerate decrypt errors, got %v", err)
	}
	if _, ok := reg.Get("openai"); !ok {
		t.Error("openai should be registered despite anthropic decrypt failure")
	}
	if _, ok := reg.Get("anthropic"); ok {
		t.Error("anthropic should NOT be registered after decrypt failure")
	}
}

func TestHydrateFromKeystore_UnknownProviderSkipped(t *testing.T) {
	now := time.Now()
	ks := &stubKeystore{
		rows: []KeystoreRecord{
			{ID: "k1", Provider: "future-llm", Label: "prod", CreatedAt: now},
			{ID: "k2", Provider: "openai", Label: "prod", CreatedAt: now},
		},
		plain: map[string]string{"k1": "sk-1", "k2": "sk-2"},
	}
	reg := NewRegistry()
	if err := HydrateFromKeystore(context.Background(), reg, ks, silentLogger()); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if _, ok := reg.Get("future-llm"); ok {
		t.Error("unknown provider should be skipped, not registered")
	}
	if _, ok := reg.Get("openai"); !ok {
		t.Error("openai should still be registered")
	}
}

func TestModelCatalogFor_EnvOverride(t *testing.T) {
	t.Setenv("HELMDECK_OPENAI_MODELS", "gpt-5,gpt-5-mini")
	got := modelCatalogFor("openai")
	if len(got) != 2 || got[0] != "gpt-5" || got[1] != "gpt-5-mini" {
		t.Errorf("env override not honored: %v", got)
	}
}

func TestModelCatalogFor_Default(t *testing.T) {
	got := modelCatalogFor("openai")
	if len(got) == 0 {
		t.Error("default catalog should not be empty for openai")
	}
}

func TestLoadCustomOpenAIProviders_OpenRouterEnvVar(t *testing.T) {
	t.Setenv("HELMDECK_OPENROUTER_API_KEY", "sk-or-test")
	reg := NewRegistry()
	LoadCustomOpenAIProviders(reg, silentLogger())
	p, ok := reg.Get("openrouter")
	if !ok {
		t.Fatal("openrouter not registered")
	}
	op, ok := p.(*openAIProvider)
	if !ok {
		t.Fatalf("expected *openAIProvider, got %T", p)
	}
	if op.cfg.BaseURL != "https://openrouter.ai/api" {
		t.Errorf("wrong base URL: %q", op.cfg.BaseURL)
	}
	if op.cfg.APIKey != "sk-or-test" {
		t.Errorf("wrong api key: %q", op.cfg.APIKey)
	}
	models, _ := p.Models(context.Background())
	if len(models) != 1 || models[0] != "minimax/minimax-m2.7" {
		t.Errorf("default model not set: %v", models)
	}
}

func TestLoadCustomOpenAIProviders_NoEnvIsNoop(t *testing.T) {
	// Make sure no env vars are set in this subtest's environment.
	t.Setenv("HELMDECK_OPENROUTER_API_KEY", "")
	t.Setenv("HELMDECK_OPENROUTER_API_KEY_FILE", "")
	reg := NewRegistry()
	LoadCustomOpenAIProviders(reg, silentLogger())
	if _, ok := reg.Get("openrouter"); ok {
		t.Error("openrouter should not be registered with no env var")
	}
}

func TestParseModels(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ", []string{"a", "b"}},
		{"", nil},
		{",,,", nil},
	}
	for _, c := range cases {
		got := parseModels(c.in, nil)
		if len(got) != len(c.want) {
			t.Errorf("parseModels(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseModels(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
