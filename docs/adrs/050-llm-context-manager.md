# 50. Retrieval-Augmented Tool Selection for Catalog-Heavy Packs

**Status**: Accepted with revised roadmap (PR #1 shipped at #360; PR #2 wires `helmdeck.route` + adds operator visibility; PR #3 adds the cascading `Select()` entry point with lexical pre-filter + TopK + `helmdeck://my-plans` projection; PR #4 adds two-pass LLM cascade as opt-in escalation for the worst Tier C cases)
**Date**: 2026-06-01
**Domain**: gateway, packs, mcp, agent-integrations

## Context

ADR 049 PR #1 shipped `helmdeck.plan` — an LLM-backed meta-pack that returns ordered tool/pipeline sequences for multi-intent prompts. The pack is correct against frontier models and trivial-intent prompts against free models (a single-step plan resolves in ~13s on `openrouter/openrouter/free`). It is **not** correct against multi-intent prompts on free models. Live testing of a real chat prompt — a 1.5KB MiniMax M3 launch paste plus *"remember this, draft a blog, generate an illustration"* — reproducibly fails:

- `openrouter/nvidia/nemotron-3-super-120b-a12b:free` → 29.5s, `gateway returned an empty plan response`
- `openrouter/z-ai/glm-4.5-air:free` → 58.0s, same empty completion
- OpenClaw chat UI → 3× MCP 60s timeouts plus 1× the empty-plan error

The failure mode is the model returning a 200 with zero output content under prompt-size pressure. Measurement: helmdeck's catalog projection assembled by `buildCatalog()` in `internal/packs/builtin/route.go` is **35KB of JSON** for the current stack (52 packs × full metadata = 14KB, 21 pipelines × full metadata + step bodies = 21KB). Add the user's paste (~1.5KB), the system prompt (~1.5KB), the my-defaults block (~0–few KB), and the output ceiling (1500–3000 tokens), and free models with imperfect structured-output reliability bail.

This is not a `helmdeck.plan` bug — it is a **cross-cutting concern affecting every LLM-backed pack** that ships catalog or large input context: `helmdeck.plan`, `helmdeck.route`, `slides.outline`, `blog.rewrite_for_audience`, `content.ground` (rewrite mode), `swe.solve`, and future LLM-backed packs. ADR 047's goal of "free models gain reliable orchestration via helmdeck" and ADR 049's goal of "self-learning intent decomposition" are both gated on solving prompt size for weak models.

## Decision

A new module `internal/llmcontext` provides **per-model token budgets**, **catalog compaction**, and (later) **relevance ranking**. LLM-backed packs query the module before assembling their prompt; the module returns a budget-fitted catalog and a record of what was trimmed so handlers can surface that signal upstream.

Three-PR roadmap, each independently mergeable.

### PR #1 — `internal/llmcontext` module + budgets + `CompactCatalog`; wire into `helmdeck.plan`

The module exposes a narrow surface that every LLM-backed pack will adopt incrementally:

```go
// internal/llmcontext/llmcontext.go
package llmcontext

// Budget describes a model's effective input ceiling and structured-output
// reliability tier. Used by CompactCatalog to decide how aggressively
// to trim the catalog projection sent to the model.
type Budget struct {
    Model        string // canonical id, e.g. "openrouter/openrouter/free"
    InputTokens  int    // safe input ceiling (NOT the advertised context window)
    OutputTokens int    // recommended max_tokens for structured output
    Tier         Tier   // A | B | C — structured-output reliability under load
}

type Tier string
const (
    TierA Tier = "A" // frontier models: GPT-4-class, Claude Opus/Sonnet/Haiku
    TierB Tier = "B" // mid-tier: Llama-3-70B, Gemma-2-9B-it, GLM 4.5 air
    TierC Tier = "C" // weak / free models: NVIDIA Nemotron Nano, GLM 4.5 air free, smaller llamas
)

// BudgetFor returns the Budget for a model. Unknown models fall back
// to a conservative Tier-C default so the module never panics on a
// fresh model id.
func BudgetFor(model string) Budget { ... }

// CompactCatalog trims a routing-guide-shaped catalog projection until
// the marshaled JSON fits within budget.InputTokens (minus reserved
// headroom for the system prompt and user message). Returns the
// trimmed projection and a Trim record naming what was dropped so the
// caller can log it.
//
// Trim order, applied progressively until the size constraint is met:
//   1. Pack `intent_keywords[]`
//   2. Pack `typical_use`
//   3. Pack `limitations[]`
//   4. Pipeline `steps[]` bodies (keep only step count + names)
//   5. Pipeline input/output schemas (replace with field-name list)
//   6. Pack and pipeline `description` truncation (first sentence only)
// Pipeline `metadata.supersedes` is NEVER trimmed — it is the anchor
// for the planner's pipeline-aware rules.
func CompactCatalog(full RoutingGuide, budget Budget) (RoutingGuide, Trim) { ... }

type Trim struct {
    BeforeBytes int
    AfterBytes  int
    Dropped     []string // human-readable list of what was stripped, ordered
}
```

The initial `BudgetFor` table covers the models helmdeck callers actually use today. Unknown models fall back to a conservative Tier-C profile (16K input, 1500 output). The table lives in a `budgets.go` file and is extended by lookup, not generation — we will not auto-discover budgets from OpenRouter's API in PR #1.

`helmdeck.plan` calls `llmcontext.CompactCatalog(catalog, budget)` immediately after `buildCatalog(ctx, reg, pipes)`, before assembling the user message. The handler logs the Trim record at INFO when bytes drop more than 30% so operators see when free models are getting a slim catalog. `helmdeck.route` is **not** wired in PR #1 — keeping the surface narrow lets us validate the module against the most context-heavy pack first.

**Acceptance criteria.** PR #1 lands when the exact OpenClaw chat prompt that failed today (MiniMax M3 paste + multi-intent ask) returns a valid plan on `openrouter/openrouter/free` AND `openrouter/z-ai/glm-4.5-air:free` — measured by re-running the smoke script under both models with the live control-plane build.

### PR #2 — Wire `helmdeck.route`; add `helmdeck://context-budgets` MCP resource

PR #2 generalizes the integration. `helmdeck.route` calls `llmcontext.CompactCatalog` before its dispatch, sharing the same Trim logging the plan handler does. A new `helmdeck://context-budgets` MCP resource projects the budgets table so operators (and agents) can audit which model gets which budget without grepping source:

```json
{
  "budgets": [
    {"model": "openrouter/openrouter/free", "input_tokens": 24000, "output_tokens": 1500, "tier": "C"},
    {"model": "anthropic/claude-haiku-4-5", "input_tokens": 180000, "output_tokens": 4000, "tier": "A"},
    ...
  ],
  "policy": "Unknown models fall back to Tier-C (16K input, 1500 output). Tier C compacts the catalog aggressively; Tier A sends full metadata."
}
```

PR #2 also surfaces the Trim record on the wire — `helmdeck.plan`'s output gains an optional `compaction: {before_bytes, after_bytes, dropped: []}` field — so agents can see when a plan was made under a slim catalog and decide whether to escalate to a stronger model.

### PR #3 — Cascading `Select()` with lexical pre-filter; `helmdeck://my-plans` projection

After PR #1's metadata compaction lands, free models still see all 52 packs and 21 pipelines (just with less metadata). Live testing of PR #1 against the original motivating prompt (1.5KB MiniMax M3 paste + 3-action ask) confirmed empirically: even after stripping every metadata field, the irreducible catalog floor (names + ids + supersedes + truncated descriptions) is ~14KB. Some free models empty-complete on that combined with a long paste. **Metadata compaction alone can't fix the worst case**; the fix is loading only the catalog entries relevant to the intent.

PR #3 ships a cascading `Select(catalog, intent, budget) → (selected, Trim)` function as the public entry point, with `CompactCatalog` (PR #1) becoming the first stage and **lexical retrieval** becoming the second:

```
Select(catalog, intent, budget):
    if budget.Tier == TierA:
        return catalog                              # frontier: no work
    compacted, trim = CompactCatalog(catalog, budget)   # PR #1
    if fits(compacted, budget): return compacted        # most cases exit here
    ranked = LexicalRank(compacted, intent, history)    # PR #3
    top_n  = TopK(ranked, budget.MaxEntries)
    return top_n
```

`LexicalRank` scores catalog entries by:

1. **Keyword overlap** between intent and `intent_keywords[]`, `accepts[]`, `produces[]`, name, description (TF-IDF-style, with stop-word filtering).
2. **`plan_history` priors** — for prior plans with similar intent (grouped by `intentSHA` from ADR 049's audit rows), which tools did the planner actually pick? Per-caller, weighted by frequency. This is the **self-learning loop ADR 049 PR #2 promised**, now living inside the ranker.

PR #3 ships `helmdeck://my-plans` MCP resource (deferred from ADR 049 PR #2's original roadmap; consolidated here since the ranker is the natural consumer of that projection). The resource projects the caller's `plan_history` rows grouped by intent shape and reports most-used decompositions per intent class plus drift signals — useful both to the agent and to operators auditing the ranker's behavior.

**Public-API shift:** `helmdeck.plan` and (post-PR-#2) `helmdeck.route` switch from calling `CompactCatalog` directly to calling `Select`. The cascade is internal; callers don't pick between compaction and retrieval by hand.

**Acceptance criteria.** PR #3 lands when the exact OpenClaw chat prompt that motivated this ADR (MiniMax M3 paste + multi-intent ask) returns a valid plan on `openrouter/openrouter/free` AND `openrouter/z-ai/glm-4.5-air:free` — measured by re-running the smoke script under both models with the live control-plane build. (This is the acceptance gate PR #1 was originally scoped to meet; live testing surfaced that lexical pre-filter is necessary, hence the enlarged roadmap.)

### PR #4 — Two-pass LLM cascade as opt-in escalation

A small fraction of intents will defeat the lexical pre-filter — typically semantic asks where the keyword overlap signal is weak (*"summarize this PDF as bullet points"* has no obvious match to `slides.outline`). PR #4 adds a third escalation stage to `Select()`: when lexical ambiguity is high (top-N scores are clustered tightly) AND the budget allows it, dispatch a cheap-model first-pass that returns the candidate tool ids the second pass should consider, then plan with that filtered subset.

```
Select(...):
    ... (PR #1 + #3 stages)
    if HighConfidence(ranked): return top_n
    if budget.AllowsLLMFilter:
        return TwoPassLLMFilter(catalog, intent, budget.FilterModel)  # PR #4
    return top_n  # last resort: trust the lexical scorer
```

The LLM-filter pass is **opt-in per budget** because it doubles the latency cost for that call (two model dispatches instead of one). Tier A budgets won't reach this stage; Tier B may have `AllowsLLMFilter: false` by default; Tier C will have it enabled with a cheap filter model (e.g. `openrouter/openrouter/free` or a small Anthropic model) so the planning pass can use a more capable model with the trimmed subset.

PR #4 is **separate from PR #3** because it introduces a different latency profile (2× LLM calls) that benefits from being measurable and rollbackable independently. Some deployments will choose not to ship PR #4 at all — Tier B users with reliable lexical retrieval don't need it.

## Consequences

**Positive.**
- Free models become viable for catalog-heavy LLM-backed packs — the largest gap in helmdeck's "works with any MCP client" goal. Tier-C operators are no longer second-class.
- The Trim record gives operators concrete signal that they're running under a slim catalog. Today the same operator just sees "empty plan response" and can't diagnose.
- The budgets table is one place to update when a new model ships or a model's tier changes. Cross-cutting upgrade story for every LLM-backed pack.
- PR #3's relevance ranking layered on `plan_history` operationalizes ADR 049's self-learning promise — the planner gets sharper at intents it has seen, AND its history shrinks the catalog every other LLM-backed pack sees.

**Negative.**
- Adds a module every LLM-backed pack now depends on. The risk is that a `llmcontext` bug breaks several packs simultaneously. Mitigated by an exhaustive test suite at the module level (budget edge cases, compaction stability, deterministic Trim ordering) and by leaving each pack's integration narrow (one call site per pack).
- The budgets table needs maintenance as models ship and degrade. Not auto-detected, not crowd-sourced — explicit operator/maintainer call. Acceptable: budgets change slowly and the cost of being slightly conservative (smaller InputTokens than the model truly supports) is bounded — the model just sees a slimmer catalog than necessary.
- PR #3's relevance ranking is a behavior change. A planner that today sees all 52 packs may, after PR #3, see only the top 15 — and a user with a novel intent could find the right pack ranked outside the cutoff. Mitigated by: (a) ranking is only applied when compaction alone can't fit the budget, (b) the Trim record names how many entries were dropped by relevance, (c) the planner's gap_warning path still fires when nothing in the trimmed catalog fits.

**Out of scope of this roadmap.**
- Token counting via a real tokenizer (we use a byte-count heuristic at ~4 chars/token; close enough for the trim-until-fits loop, and avoids pulling a model-specific tokenizer into Go).
- Per-caller budget overrides. PR #2 surfaces the table; per-caller customization is a follow-up if operators ask for it.
- Auto-fetching budgets from OpenRouter or anthropic.com. Maintenance is by explicit edit.
- Streaming responses (separate concern; would help plan latency but not the empty-completion failure mode).
- Dense-retrieval (embedding-based) catalog ranking. PR #3 ships lexical retrieval because (a) keyword overlap is already strong against helmdeck's catalog metadata, and (b) embedding-based retrieval would add an inference dependency and a model-selection question that doesn't earn its complexity until lexical proves insufficient. Reconsider when lexical recall is measurably failing.

## Design evolution (2026-06-01)

PR #1 landed at #360 with `internal/llmcontext` + per-model budgets + `CompactCatalog`. Live testing of the merged code against the original motivating prompt produced this empirical result:

```
Catalog before compaction:  30,141 bytes
Catalog after compaction:   13,892 bytes (54% reduction; all 6 trim steps fired)
Free model on trivial intent:  ~23s success (was: empty completion)
Free model on motivating prompt: still empty completion (was: empty completion)
```

The infrastructure works as designed. **Metadata compaction alone does not close the acceptance gate** the original ADR set for PR #1 — that gate now belongs to PR #3 (lexical pre-filter + TopK) and PR #4 (LLM-filter escalation) as a layered cascade.

Two consequences for the roadmap:

1. **PR #1's acceptance criteria was softened in retrospect** to "infrastructure that makes trivial intents succeed on free models" (empirically met). The original "exact motivating prompt succeeds" criterion was moved to PR #3.

2. **PR #3 was enlarged** from a single relevance-ranking function to a public `Select()` cascade entry point. The cascade pattern is standard practice in production RAG systems (dense retrieval + cross-encoder re-ranker, HyDE + answer model) — we're applying it inside the helmdeck tool-selection domain rather than the open-text-QA domain it's usually demonstrated on. What's novel for helmdeck specifically is the **calibration loop** — empirical-failure-mode tiers feeding compaction with dispatch invariants feeding learned per-caller priors. The cascade itself is well-trod prior art.

3. **PR #4 was added** to the roadmap as the LLM-filter escalation for the worst Tier C cases that defeat lexical pre-filter. Separate PR because it introduces a different latency profile (2× LLM calls).

## See also (research grounding)

The cascade architecture borrows from well-established retrieval and agent-orchestration patterns:

- **MapReduce summarization over long contexts** — split-process-aggregate pattern, well-documented in LangChain and LlamaIndex.
- **RAG (Retrieval-Augmented Generation)** — Lewis et al. 2020, *Retrieval-Augmented Generation for Knowledge-Intensive NLP Tasks*. Original framing of dense retrieval before generation.
- **HyDE** — Gao et al. 2023, *Precise Zero-Shot Dense Retrieval without Relevance Labels*. Hypothetical document embeddings; relevant to PR #4's potential expansion.
- **ReAct** — Yao et al. 2022, *ReAct: Synergizing Reasoning and Acting in Language Models*. Iterative tool-selection pattern; informs how an agent might consume `Select`'s output across turns.
- **Cross-encoder re-rankers** — standard pattern in production RAG. PR #3's lexical scorer is the cheap version; PR #4's LLM filter is the expensive version.
- **Tool selection in agent frameworks** — LangChain's `tool_router`, LlamaIndex's `ToolMetadata`. Most existing implementations classify tools by name + description; helmdeck adds the dispatch-invariant + tier-aware compaction layers.

What we couldn't find published prior art for, and the bundled novelty this ADR claims:

- **Tier classification by empirical structured-output reliability** rather than vendor-advertised context window.
- **Domain-aware compaction with explicit dispatch invariants** (`supersedes`, names, ids never trimmed) — most published compaction techniques operate on free text without a schema to respect.
- **Self-learning per-caller priors from a packing-history audit category** (`plan_history` from ADR 049) used to weight retrieval ranking.
- **The cascade as a measurement instrument** — the Trim record and stage-escalation log produce data on where weak models actually fail, useful for future agent-framework research.

## See also

- ADR 047 — Catalog metadata + memory-driven routing. `helmdeck.route`'s catalog projection is the input `llmcontext.CompactCatalog` operates on.
- ADR 048 — Memory write surface. `plan_history` audit rows (ADR 049's category) feed PR #3's relevance ranker.
- ADR 049 — Intent decomposition. `helmdeck.plan` is PR #1's first integration target; its empty-completion failure on free models is the immediate motivation.
- `internal/packs/builtin/plan.go` — the pack that surfaces the gap most acutely.
- `internal/packs/builtin/route.go` — the pack that ships the catalog projection helper PR #1 calls.
