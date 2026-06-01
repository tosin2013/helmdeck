// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package llmcontext provides per-model prompt budgets and deterministic
// catalog compaction for LLM-backed helmdeck packs (ADR 050).
//
// The problem: helmdeck.plan and helmdeck.route ship the full pack +
// pipeline catalog as a JSON projection in their prompts. At the
// current stack size (52 packs + 21 pipelines with full metadata, ~35
// KB of JSON), free models — `openrouter/openrouter/free`,
// `nvidia/nemotron-3-super-120b-a12b:free`, `z-ai/glm-4.5-air:free` —
// reproducibly return empty completions under prompt-size pressure.
// Live test on 2026-06-01 (PR #358 follow-up): 29.5 s and 58.0 s
// failures with `gateway returned an empty plan response`.
//
// The fix: BudgetFor(model) returns a Budget describing how much
// room the model can spare for the catalog projection;
// CompactCatalog trims metadata in a deterministic priority order
// until the marshaled catalog fits the budget, and returns a Trim
// record naming what got stripped. LLM-backed packs call these two
// helpers between buildCatalog and the dispatcher Dispatch.
//
// Tokenizer note: we use a 1 token ≈ 4 chars byte-count heuristic
// instead of a real tokenizer. The cost of being slightly
// conservative (sending a leaner catalog than the model can handle)
// is bounded — a hallucination guard catches the rare case where the
// trimmed catalog drops a pack the user asked for — while pulling a
// model-specific tokenizer into Go would expand the dependency
// surface for marginal gain. Reconsider when budgets get tighter.

package llmcontext

import "strings"

// Tier classifies a model by its observed reliability at producing
// structured JSON output under load. Independent of raw context-window
// size: a model with a 32K window that empty-completes at 20K of
// input is Tier C even though its window is larger than some Tier B
// models. Calibrated against live OpenClaw tests, not vendor specs.
type Tier string

const (
	// TierA: frontier models. Claude Opus/Sonnet/Haiku, GPT-4-class,
	// large Mistral. Reliable structured output even at 50K+ tokens
	// of catalog. Compaction usually skipped.
	TierA Tier = "A"
	// TierB: mid-tier hosted models. Llama 3 70B, Mistral 7B
	// Instruct, Gemma 2 9B. Reliable structured output up to ~25K of
	// catalog. Compaction trims aggressive metadata fields.
	TierB Tier = "B"
	// TierC: weak or free models. Sub-30B open models, free
	// OpenRouter routes. Empty-complete on 35KB catalogs.
	// Compaction must hit ~10KB to stay reliable.
	TierC Tier = "C"
)

// Budget describes the prompt budget for one model. InputTokens and
// OutputTokens are the safe ceilings (NOT necessarily the model's
// advertised maximums — we leave headroom). MaxCatalogBytes is the
// upper bound CompactCatalog targets when trimming; 0 disables
// compaction entirely (used for Tier A).
type Budget struct {
	Model           string
	InputTokens     int // safe input ceiling (1 token ≈ 4 chars heuristic)
	OutputTokens    int // recommended max_tokens for structured output
	MaxCatalogBytes int // 0 = no compaction; otherwise CompactCatalog trims until len(JSON) <= this
	Tier            Tier
}

// tierC is the conservative fallback for unknown models. Free
// OpenRouter routes inherit this profile because we treat unknown =
// untrusted.
var tierC = Budget{
	InputTokens:     16000,
	OutputTokens:    1500,
	MaxCatalogBytes: 10000,
	Tier:            TierC,
}

// budgetTable maps canonical model ids (provider/family/name as
// passed to gateway.ChatRequest.Model) to a Budget. Lookup matches
// EXACT id first, then prefix; first prefix-match wins. Keep prefixes
// SPECIFIC enough that adding a new model doesn't accidentally
// inherit the wrong tier — e.g. "openrouter/" alone would be too
// broad; "openrouter/anthropic/" is fine.
//
// Edit policy: when a new model lands or an existing model's
// reliability changes, edit this table. We do not auto-fetch budgets
// from provider APIs — operators see the budgets via
// `helmdeck://context-budgets` (PR #2) and can request a tier change
// when their model gets reclassified.
var budgetTable = []Budget{
	// --- Tier A (frontier; compaction off) ---
	{Model: "anthropic/claude-opus-", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA},
	{Model: "anthropic/claude-sonnet-", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA},
	{Model: "anthropic/claude-haiku-", InputTokens: 200000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA},
	{Model: "openai/gpt-4o", InputTokens: 100000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA},
	{Model: "openai/gpt-5", InputTokens: 100000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA},
	{Model: "openrouter/anthropic/claude-", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA},
	{Model: "openrouter/openai/gpt-4o", InputTokens: 100000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA},
	{Model: "openrouter/openai/gpt-5", InputTokens: 100000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA},

	// --- Tier B (mid-tier hosted) ---
	{Model: "openrouter/meta-llama/llama-3.1-70b", InputTokens: 32000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB},
	{Model: "openrouter/meta-llama/llama-3.3-70b", InputTokens: 32000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB},
	{Model: "openrouter/google/gemma-2-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 12000, Tier: TierB},
	{Model: "openrouter/mistralai/", InputTokens: 32000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB},

	// --- Tier C (free / weak; aggressive compaction) ---
	//
	// The :free suffix is OpenRouter's marker for zero-cost routes.
	// We treat ALL free routes as Tier C regardless of underlying
	// model — the failure mode that motivated ADR 050 was a free
	// route empty-completing on a 35KB catalog. The empirical limit
	// for these is around 10KB of catalog before structured output
	// gets unreliable.
	{Model: "openrouter/openrouter/free", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC},
	{Model: "openrouter/nvidia/nemotron-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC},
	{Model: "openrouter/z-ai/glm-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC},
	{Model: "openrouter/qwen/qwen-2.5-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC},
}

// BudgetFor returns the Budget for a model id. Lookup is exact-match
// first, then longest-prefix-wins. Unknown models fall back to the
// Tier C profile — we treat unknown as untrusted so a never-mapped
// model still gets a working (if conservative) compaction.
func BudgetFor(model string) Budget {
	model = strings.TrimSpace(model)
	// Exact match wins.
	for _, b := range budgetTable {
		if b.Model == model {
			return withModel(b, model)
		}
	}
	// Longest-prefix wins. Every table entry that isn't an exact
	// match is treated as a prefix; the operator chooses how
	// specific to make each entry, and the longest match takes
	// precedence so a generic "openrouter/" can coexist with a
	// specific "openrouter/anthropic/claude-" without ambiguity.
	// Iterating once and tracking the longest match is O(N) but
	// the table is tiny (under 20 rows); a sorted table or a trie
	// is unnecessary complexity until we have 100+ entries.
	var best Budget
	bestLen := -1
	for _, b := range budgetTable {
		if strings.HasPrefix(model, b.Model) && len(b.Model) > bestLen {
			best = b
			bestLen = len(b.Model)
		}
	}
	if bestLen >= 0 {
		return withModel(best, model)
	}
	return withModel(tierC, model)
}

func withModel(b Budget, model string) Budget {
	b.Model = model
	return b
}

// AllBudgets returns a copy of the budgets table so the
// helmdeck://context-budgets MCP resource (ADR 050 PR #2) can
// project it for operators and agents. The returned slice is
// safe to marshal directly — entries are immutable value types,
// and we deep-copy to keep callers from mutating the package's
// internal table by accident.
func AllBudgets() []Budget {
	out := make([]Budget, len(budgetTable))
	copy(out, budgetTable)
	return out
}

// TierCFallback returns the conservative Tier-C default that
// BudgetFor uses for unmapped models. Exported so the
// helmdeck://context-budgets MCP resource can surface the
// fallback alongside the explicit table — operators need to
// see what a "novel model" gets, not just what's in the table.
func TierCFallback() Budget { return tierC }
