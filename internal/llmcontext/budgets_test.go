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

// --- ADR 051 PR #2: capability-flag tests --------------------------

// TestBudgetFor_HybridReasoningFlag — models that emit <think>
// blocks have IsHybridReasoning=true. The flag tells downstream
// telemetry to bucket "model timed out reasoning" failures
// correctly.
func TestBudgetFor_HybridReasoningFlag(t *testing.T) {
	hybrid := []string{
		"anthropic/claude-3.7-sonnet",
		"openai/o3-mini",
		"openrouter/openai/o3-mini",
		"openrouter/deepseek/deepseek-v4-pro",
		"openrouter/moonshotai/kimi-k2.6",
		"openrouter/moonshotai/kimi-latest",
	}
	for _, m := range hybrid {
		if got := BudgetFor(m); !got.IsHybridReasoning {
			t.Errorf("%q should have IsHybridReasoning=true; got %+v", m, got)
		}
	}
	directOnly := []string{
		"openai/gpt-4o",
		"anthropic/claude-sonnet-4.6",
		"google/gemini-2.5-flash",
		"openrouter/openrouter/free",
	}
	for _, m := range directOnly {
		if got := BudgetFor(m); got.IsHybridReasoning {
			t.Errorf("%q should have IsHybridReasoning=false; got %+v", m, got)
		}
	}
}

// TestBudgetFor_StrictJSONFlag — models from providers with native
// strict-JSON / response_format support have WantsStrictJSON=true.
// Tier A native APIs + Mistral + Grok set this; weak open-weights
// routes do NOT (report describes constrained-decoding deadlock as
// the strict-mode failure mode on quantized inference).
func TestBudgetFor_StrictJSONFlag(t *testing.T) {
	strictCapable := []string{
		"anthropic/claude-haiku-4-5",
		"openai/gpt-4o",
		"openai/o3-mini",
		"google/gemini-2.5-pro",
		"openrouter/mistralai/mistral-large",
		"openrouter/x-ai/grok-4",
	}
	for _, m := range strictCapable {
		if got := BudgetFor(m); !got.WantsStrictJSON {
			t.Errorf("%q should have WantsStrictJSON=true; got %+v", m, got)
		}
	}
	openWeightsNoStrict := []string{
		"openrouter/openrouter/free",
		"openrouter/qwen/qwen-2.5-coder",
		"openrouter/moonshotai/kimi-k2.6",
		"openrouter/meta-llama/llama-3.1-70b-instruct",
	}
	for _, m := range openWeightsNoStrict {
		if got := BudgetFor(m); got.WantsStrictJSON {
			t.Errorf("%q should have WantsStrictJSON=false (constrained-decoding deadlock risk); got %+v", m, got)
		}
	}
}

// TestBudgetFor_PrefixCacheFlag — Anthropic / OpenAI / Google /
// DeepSeek native routes have prefix caching. PR #4 will exploit
// the flag to restructure the two-pass filter for cache reuse.
func TestBudgetFor_PrefixCacheFlag(t *testing.T) {
	cachedProviders := []string{
		"anthropic/claude-opus-4-7",
		"openai/gpt-5",
		"google/gemini-2.5-flash",
		"openrouter/deepseek/deepseek-v4-pro",
		"openrouter/deepseek/deepseek-v3.2",
	}
	for _, m := range cachedProviders {
		got := BudgetFor(m)
		if !got.SupportsPrefixCache {
			t.Errorf("%q should have SupportsPrefixCache=true; got %+v", m, got)
		}
		if got.CachedInputCostUSDPerMTok <= 0 {
			t.Errorf("%q should have CachedInputCostUSDPerMTok > 0 when SupportsPrefixCache; got %v", m, got.CachedInputCostUSDPerMTok)
		}
	}
	uncachedProviders := []string{
		"openrouter/openrouter/free",
		"openrouter/qwen/qwen-2.5-coder",
		"openrouter/meta-llama/llama-3.3-70b-instruct",
	}
	for _, m := range uncachedProviders {
		if got := BudgetFor(m); got.SupportsPrefixCache {
			t.Errorf("%q should have SupportsPrefixCache=false; got %+v", m, got)
		}
	}
}

// TestBudgetFor_FallbackCapabilityFlagsConservative — the tierC
// fallback profile (used for unmapped models) has all capability
// flags off. Conservative — we don't make affirmative claims about
// a model we haven't classified.
func TestBudgetFor_FallbackCapabilityFlagsConservative(t *testing.T) {
	got := BudgetFor("brand-new-vendor/never-seen:v0")
	if got.IsHybridReasoning {
		t.Errorf("unmapped model should have IsHybridReasoning=false (conservative); got %+v", got)
	}
	if got.WantsStrictJSON {
		t.Errorf("unmapped model should have WantsStrictJSON=false (conservative); got %+v", got)
	}
	if got.SupportsPrefixCache {
		t.Errorf("unmapped model should have SupportsPrefixCache=false (conservative); got %+v", got)
	}
	if got.CachedInputCostUSDPerMTok != 0 {
		t.Errorf("unmapped model should have CachedInputCostUSDPerMTok=0; got %v", got.CachedInputCostUSDPerMTok)
	}
}
