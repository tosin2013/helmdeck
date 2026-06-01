package llmcontext

import "testing"

// TestBudgetFor_ExactMatch — a model id that appears verbatim in the
// table returns that exact budget, not a less-specific prefix.
func TestBudgetFor_ExactMatch(t *testing.T) {
	got := BudgetFor("openrouter/openrouter/free")
	if got.Tier != TierC {
		t.Errorf("openrouter/openrouter/free should be TierC; got %q", got.Tier)
	}
	if got.MaxCatalogBytes != 10000 {
		t.Errorf("MaxCatalogBytes: want 10000, got %d", got.MaxCatalogBytes)
	}
	if got.Model != "openrouter/openrouter/free" {
		t.Errorf("Model echo: %q", got.Model)
	}
}

// TestBudgetFor_PrefixMatch — a model that matches a "model-family-"
// prefix gets that family's budget. Concrete: any anthropic claude
// haiku variant gets TierA.
func TestBudgetFor_PrefixMatch(t *testing.T) {
	cases := []struct {
		model    string
		wantTier Tier
	}{
		{"anthropic/claude-haiku-4-5", TierA},
		{"anthropic/claude-opus-4-7-20260301", TierA},
		{"openrouter/anthropic/claude-sonnet-4-6", TierA},
		{"openrouter/nvidia/nemotron-3-super-120b-a12b:free", TierC},
		{"openrouter/z-ai/glm-4.5-air:free", TierC},
		{"openrouter/meta-llama/llama-3.1-70b-instruct", TierB},
		// ADR 051 PR #1 additions — calibrated from the research report.
		{"openai/o3-mini", TierA},
		{"google/gemini-2.5-pro", TierA},
		{"google/gemini-2.5-flash", TierA},
		{"anthropic/claude-3.7-sonnet", TierA},
		{"openrouter/openai/o3-mini", TierA},
		{"openrouter/google/gemini-2.5-pro", TierA},
		{"openrouter/deepseek/deepseek-v4-pro", TierB},
		{"openrouter/deepseek/deepseek-v3.2", TierB},
		{"openrouter/x-ai/grok-4", TierB},
		{"openrouter/moonshotai/kimi-k2.6", TierC},
		{"openrouter/moonshotai/kimi-k2.6:free", TierC},
		{"openrouter/tencent/hy3-preview", TierC},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := BudgetFor(tc.model)
			if got.Tier != tc.wantTier {
				t.Errorf("Tier: want %q, got %q", tc.wantTier, got.Tier)
			}
			if got.Model != tc.model {
				t.Errorf("Model: want %q, got %q", tc.model, got.Model)
			}
		})
	}
}

// TestBudgetFor_TierAHasNoCompaction — frontier-tier budgets must
// have MaxCatalogBytes=0 so CompactCatalog is a pass-through. If
// someone accidentally sets a cap on Tier A we'll start compacting
// for models that don't need it.
func TestBudgetFor_TierAHasNoCompaction(t *testing.T) {
	for _, b := range budgetTable {
		if b.Tier == TierA && b.MaxCatalogBytes != 0 {
			t.Errorf("Tier A entry %q has MaxCatalogBytes=%d; should be 0", b.Model, b.MaxCatalogBytes)
		}
	}
}

// TestBudgetFor_UnknownFallsBackToTierC — an unmapped model must
// return the conservative Tier C profile so a never-seen model still
// gets a working (if slimmed) catalog instead of an empty one.
func TestBudgetFor_UnknownFallsBackToTierC(t *testing.T) {
	got := BudgetFor("brand-new-vendor/model-x:v3")
	if got.Tier != TierC {
		t.Errorf("unknown should fall back to TierC; got %q", got.Tier)
	}
	if got.MaxCatalogBytes != 10000 {
		t.Errorf("fallback MaxCatalogBytes: want 10000, got %d", got.MaxCatalogBytes)
	}
	if got.Model != "brand-new-vendor/model-x:v3" {
		t.Errorf("Model should still echo for downstream logging; got %q", got.Model)
	}
}

// TestBudgetFor_EmptyModel — empty string is treated as unknown and
// falls back to Tier C. Avoids a nil-budget surprise upstream.
func TestBudgetFor_EmptyModel(t *testing.T) {
	got := BudgetFor("")
	if got.Tier != TierC {
		t.Errorf("empty model should fall back to TierC; got %q", got.Tier)
	}
}

// TestBudgetFor_LongestPrefixWins — when two prefix entries could
// match, the longer one wins. Guards against the bug where
// "openrouter/" would shadow "openrouter/anthropic/claude-" if order
// of iteration weren't stable.
func TestBudgetFor_LongestPrefixWins(t *testing.T) {
	// We don't have a generic "openrouter/" entry today, but we
	// do have "openrouter/anthropic/claude-" (TierA) and
	// "openrouter/openrouter/free" (TierC, exact). A model id that
	// starts with "openrouter/anthropic/claude-" must NOT fall
	// through to the conservative default.
	got := BudgetFor("openrouter/anthropic/claude-3-5-sonnet")
	if got.Tier != TierA {
		t.Errorf("longest prefix should win; got %q (model=%s)", got.Tier, got.Model)
	}
}
