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
//
// AllowsLLMFilter + FilterModel control the ADR 050 PR #4 two-pass
// cascade: when set and the lexical retrieval result is ambiguous
// (low HighConfidence), the caller may dispatch a separate "filter"
// LLM call to narrow the catalog before the real planning call. The
// trade is one extra round-trip for usable structured output on the
// hardest Tier C cases. Tier A budgets never need it (full catalog
// fits); Tier B often doesn't either (lexical alone produces
// confident picks). Empty FilterModel means "use the caller's
// planning model for both passes" — the simplest setup, works because
// the filter prompt is small enough that even weak models handle it
// reliably.
type Budget struct {
	Model           string
	InputTokens     int // safe input ceiling (1 token ≈ 4 chars heuristic)
	OutputTokens    int // recommended max_tokens for structured output
	MaxCatalogBytes int // 0 = no compaction; otherwise CompactCatalog trims until len(JSON) <= this
	Tier            Tier
	AllowsLLMFilter bool   // ADR 050 PR #4: opt-in to the two-pass filter cascade
	FilterModel     string // model id for the filter pass; "" → reuse the planning model

	// ADR 051 PR #2 capability flags. Populated from the research
	// synthesis cited in ADR 051 § "Context" and the methodology in
	// docs/howto/calibrate-model-tiers.md. Each field defaults to
	// false on unmapped models — conservative for "we don't know" —
	// so behavior on unknown models is unchanged from before PR #2.

	// IsHybridReasoning is true when the model emits <think> /
	// <reasoning> blocks before its structured output. PR #1's
	// StripReasoningTokens always handles them in the JSON-decode
	// path; the flag tells PR #2's typed-error layer to bucket
	// "model timed out reasoning" failures correctly and tells PR
	// #3/#4 work to expect extended latency on the first byte.
	IsHybridReasoning bool

	// WantsStrictJSON is true when the provider supports a request-
	// time strict-JSON / response_format flag (OpenAI's
	// response_format.json_object, Anthropic's tool-call contract,
	// Gemini's responseMimeType, Mistral's json_object). PR #3
	// will pass through when this is set on Tier A models;
	// contraindicated on Tier C weak open-weights routes where the
	// constrained-decoding deadlock the report describes is more
	// likely than not.
	WantsStrictJSON bool

	// SupportsPrefixCache is true when the provider offers prompt-
	// prefix caching (Gemini, Anthropic, OpenAI, DeepSeek native
	// routes). PR #4 will restructure the two-pass filter to
	// preserve the catalog prefix across both calls when this is
	// set, enabling near-100% cache hit rate on the static catalog
	// tokens.
	SupportsPrefixCache bool

	// CachedInputCostUSDPerMTok is the cached-input rate per million
	// tokens, surfaced via helmdeck://context-budgets so operators
	// (and future cost-aware routing logic) can weigh providers.
	// 0 means "no caching" or "unknown." Populated from Artificial
	// Analysis and per-provider pricing pages.
	CachedInputCostUSDPerMTok float64

	// PromptVariant overrides the tier-default prompt strategy used
	// by helmdeck.plan. Empty string defaults to the tier mapping:
	// Tier A/B → PromptVariantFullSteps (emit the complete pipeline
	// in one shot); Tier C → PromptVariantSinglePick (emit one step
	// at a time; agent re-calls helmdeck.plan for the next step).
	// See ADR 053 for the architectural rationale + the empirical
	// data from the 2026-06-05 Nemotron-3-super-120b-a12b:free
	// observation that motivated the split.
	//
	// Set explicitly on a budget entry to override the tier default
	// — useful when a model defies its tier (e.g. a Tier B model
	// trained specifically for tool calling that handles multi-step
	// plans reliably and should get FullSteps despite the tier
	// default suggesting otherwise).
	PromptVariant PromptVariant
}

// PromptVariant selects which helmdeck.plan system-prompt template
// fires for a given model. The variant controls what the model is
// ASKED to produce, not the output schema — both variants emit the
// same {steps:[], complexity, reasoning} shape so the handler doesn't
// need to parse two different response formats. SinglePick instructs
// the model to emit ONE step at a time + a more_steps_likely flag;
// the agent re-calls helmdeck.plan for the next step. FullSteps is
// today's behavior — the complete pipeline emitted in one shot.
//
// The architectural rationale is in ADR 053: small models that
// reliably make ONE pack-pick decision in 50–200 tokens can fail at
// emitting a 1,500-token multi-step plan. SinglePick matches the
// output shape to what those models can reliably produce; the agent
// loop pattern (call plan → run step → call plan again) naturally
// composes with the per-step decisions.
type PromptVariant string

const (
	// PromptVariantUnset means "use the tier default" via
	// Budget.ResolvePromptVariant. Acts as the zero value so
	// existing Budget literals without an explicit PromptVariant
	// continue to work and inherit tier-default behavior.
	PromptVariantUnset PromptVariant = ""

	// PromptVariantFullSteps emits the complete pipeline JSON in
	// one shot — today's behavior, used by Tier A/B models that
	// can reliably produce 500–2000 tokens of nested JSON.
	PromptVariantFullSteps PromptVariant = "full_steps"

	// PromptVariantSinglePick emits ONE step at a time with a
	// more_steps_likely flag. Tier C default. Output schema is the
	// same {steps:[], complexity, reasoning} as FullSteps but the
	// steps array contains exactly one element; the agent re-calls
	// helmdeck.plan with updated context to plan the next step.
	PromptVariantSinglePick PromptVariant = "single_pick"
)

// ResolvePromptVariant returns the prompt-variant to use for this
// budget. When PromptVariant is explicitly set on the entry, that
// wins. When unset, falls back to tier defaults: Tier A/B →
// FullSteps; Tier C (and unknown) → SinglePick.
//
// The fallback for unknown models (Tier C default) is the
// conservative choice — if we don't know enough about the model to
// classify it, assume it might struggle with multi-step output and
// route to the safer single-pick path. Operators who know better
// override on the per-entry PromptVariant field.
func (b Budget) ResolvePromptVariant() PromptVariant {
	if b.PromptVariant != PromptVariantUnset {
		return b.PromptVariant
	}
	switch b.Tier {
	case TierA, TierB:
		return PromptVariantFullSteps
	default:
		return PromptVariantSinglePick
	}
}

// tierC is the conservative fallback for unknown models. Free
// OpenRouter routes inherit this profile because we treat unknown =
// untrusted. AllowsLLMFilter=true: when lexical retrieval can't
// confidently narrow the catalog, callers may dispatch a filter
// pass with the planning model itself (FilterModel="") to get a
// usable subset before the real planning call.
var tierC = Budget{
	InputTokens:     16000,
	OutputTokens:    1500,
	MaxCatalogBytes: 10000,
	Tier:            TierC,
	AllowsLLMFilter: true,
	FilterModel:     "",
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
	//
	// Tier-A models reliably produce structured JSON on prompts up to
	// ~50KB of catalog. Empirical scores cited from the ADR 051 research
	// synthesis: BFCL (Berkeley Function-Calling Leaderboard), Aider
	// polyglot edit-format adherence, and Artificial Analysis pricing.
	// Each entry's classification source is named in its trailing
	// comment so future operators can trace the call to its evidence.
	{Model: "anthropic/claude-opus-", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 7.5},                               // ADR 050; Anthropic flagship, extreme robustness under load
	{Model: "anthropic/claude-sonnet-", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 1.5},                             // ADR 050; primary working model class for helmdeck
	{Model: "anthropic/claude-3.7-sonnet", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA, IsHybridReasoning: true, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 1.5}, // BFCL 73.24%, Aider 84.2%; hybrid thinking mode — emits <think>, stripped by ADR 051 helper
	{Model: "anthropic/claude-haiku-", InputTokens: 200000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.3},                              // ADR 050; fastest Anthropic, used for the helmdeck.plan filter pass when budget allows
	{Model: "openai/gpt-4o", InputTokens: 100000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 1.25},                                       // BFCL 83.88%; stable, surfaces real HTTP errors rather than silent drops
	{Model: "openai/gpt-5", InputTokens: 1050000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.46},                                       // BFCL 72.92%, Aider 88.0%, 91.6% diff-format adherence — current frontier benchmark
	{Model: "openai/o3-mini", InputTokens: 200000, OutputTokens: 4400, MaxCatalogBytes: 0, Tier: TierA, IsHybridReasoning: true, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.55},             // BFCL 84.00%; reasoning model — emits <think>, stripped by ADR 051 helper
	{Model: "google/gemini-2.5-pro", InputTokens: 1050000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.125},                             // BFCL 85.04% (leaderboard top), Aider 99.6% edit-format
	{Model: "google/gemini-2.5-flash", InputTokens: 1050000, OutputTokens: 2500, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.075},                           // BFCL 75.58%; fast + cheap, watch for silent safety-filter drops on code-execution prompts
	{Model: "openrouter/anthropic/claude-", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 1.5},                         // ADR 050; OpenRouter relay of the Anthropic family
	{Model: "openrouter/openai/gpt-4o", InputTokens: 100000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 1.25},                            // ADR 050
	{Model: "openrouter/openai/gpt-5", InputTokens: 1050000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.46},                            // ADR 050
	{Model: "openrouter/openai/o3-mini", InputTokens: 200000, OutputTokens: 4400, MaxCatalogBytes: 0, Tier: TierA, IsHybridReasoning: true, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.55},  // mirrors openai/o3-mini above
	{Model: "openrouter/google/gemini-2.5-", InputTokens: 1050000, OutputTokens: 4000, MaxCatalogBytes: 0, Tier: TierA, WantsStrictJSON: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.125},                     // covers both pro and flash routes

	// --- Tier B (mid-tier hosted) ---
	//
	// Tier-B models handle structured JSON reliably up to ~25KB of
	// catalog but show empirical wobbles documented in the ADR 051
	// research synthesis. Compaction trims metadata; lexical fallback
	// fires when the trim isn't enough.
	{Model: "openrouter/meta-llama/llama-3.1-70b", InputTokens: 32000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB},                                                                                                                 // ADR 050; baseline mid-tier hosted
	{Model: "openrouter/meta-llama/llama-3.3-70b", InputTokens: 32000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB},                                                                                                                 // ADR 050
	{Model: "openrouter/google/gemma-2-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 12000, Tier: TierB},                                                                                                                          // ADR 050
	{Model: "openrouter/mistralai/", InputTokens: 32000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB, WantsStrictJSON: true},                                                                                                        // ADR 050; trailing-comma JSON degradation at high temperature; native API supports strict mode
	{Model: "openrouter/deepseek/deepseek-v4-pro", InputTokens: 1000000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB, AllowsLLMFilter: true, IsHybridReasoning: true, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.0145}, // BFCL proxy 71.4%, Aider proxy 74.2%; HYBRID reasoning, 30× cache discount, can hit 30-min serverless timeouts — keep filter cascade on
	{Model: "openrouter/deepseek/deepseek-v3.2", InputTokens: 128000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.0145},                                                    // Aider 74.2%; smaller context window than V4; same cache discount
	{Model: "openrouter/deepseek/deepseek-chat", InputTokens: 128000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB, SupportsPrefixCache: true, CachedInputCostUSDPerMTok: 0.0145},                                                    // catches the broader deepseek-chat-v3/v3.1 family
	{Model: "openrouter/x-ai/grok-", InputTokens: 256000, OutputTokens: 2000, MaxCatalogBytes: 22000, Tier: TierB, WantsStrictJSON: true},                                                                                                       // BFCL proxy 61.38%, Aider 97.3% edit-format; price-tier bumps past 128K context; xAI native supports strict mode

	// --- Tier C (free / weak; aggressive compaction + filter cascade) ---
	//
	// The :free suffix is OpenRouter's marker for zero-cost routes.
	// We treat ALL free routes as Tier C regardless of underlying
	// model — the failure mode that motivated ADR 050 was a free
	// route empty-completing on a 35KB catalog. The empirical limit
	// for these is around 10KB of catalog before structured output
	// gets unreliable. Hybrid-reasoning entries here (kimi-k2 series)
	// rely on ADR 051's <think>-stripping to be usable at all.
	{Model: "openrouter/openrouter/free", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC, AllowsLLMFilter: true},
	{Model: "openrouter/nvidia/nemotron-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC, AllowsLLMFilter: true},
	{Model: "openrouter/z-ai/glm-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC, AllowsLLMFilter: true},                                    // BFCL 70.85%; infrastructure drops on the free routing tier (chronic empty completions)
	{Model: "openrouter/qwen/qwen-2.5-", InputTokens: 16000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC, AllowsLLMFilter: true},                               // Aider 71.4%; injects ```json fences even in strict mode — ADR 051 helper unwraps
	{Model: "openrouter/moonshotai/kimi-k2", InputTokens: 256000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC, AllowsLLMFilter: true, IsHybridReasoning: true}, // HYBRID reasoning (large <think> output); observed 5-minute timeouts on long prompts; ADR 051 helper strips think blocks
	{Model: "openrouter/moonshotai/kimi-", InputTokens: 256000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC, AllowsLLMFilter: true, IsHybridReasoning: true},   // covers kimi-latest and future Kimi releases until empirically reclassified
	{Model: "openrouter/tencent/", InputTokens: 250000, OutputTokens: 1500, MaxCatalogBytes: 10000, Tier: TierC, AllowsLLMFilter: true},                                    // hy3-preview + future Tencent routes; conservative until live-validated
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
