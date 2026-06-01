# 50. LLM Context Manager for Catalog-Heavy Packs

**Status**: Accepted — fully shipped in v0.22.0 (all PRs: per-model budgets + catalog compaction unblocking `helmdeck.plan` on free models, generalization to `helmdeck.route` + `helmdeck://context-budgets` operator visibility, cascading select + lexical rank + `helmdeck://my-plans`, and the two-pass LLM filter with JSON-decoder tolerance)
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

### PR #3 — Relevance ranking; learn priorities from `plan_history`

PR #3 is the innovative slice. After PR #1's flat compaction, free models still see all 52 packs and 21 pipelines (just with less metadata). For a prompt about *"write a blog from this brief"*, the catalog only needs to surface the blog/content/research packs and pipelines — not `vision.click_anywhere`, not `desktop.run_app_and_screenshot`, not `cmd.run`. PR #3 adds a `Rank(catalog, intent, history)` function that returns the catalog sorted by relevance, optionally with a `top_n` cap when the budget can't fit everything even after compaction.

Two signal sources feed `Rank`:

1. **Keyword overlap** — naive lexical matching between intent and each entry's `intent_keywords[]`, `accepts[]`, `produces[]`, name, description.
2. **`plan_history` mining** — for each prior plan with a similar intent (hashed via `intentSHA`, exposed by ADR 049's audit rows), which tools did the planner actually pick? This is the self-learning loop ADR 049 PR #2 promised, now used as a prior on relevance instead of only as a my-plans projection.

PR #3 ships `helmdeck://my-plans` (deferred from ADR 049 PR #2's roadmap) since the relevance-ranker is the natural consumer of that projection. The ranker reads `helmdeck.memory.List(ns, AuditKeyPrefixPlan)` once per call, groups by `intent_sha`, and weights candidates by tool frequency in similar intents.

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

## See also

- ADR 047 — Catalog metadata + memory-driven routing. `helmdeck.route`'s catalog projection is the input `llmcontext.CompactCatalog` operates on.
- ADR 048 — Memory write surface. `plan_history` audit rows (ADR 049's category) feed PR #3's relevance ranker.
- ADR 049 — Intent decomposition. `helmdeck.plan` is PR #1's first integration target; its empty-completion failure on free models is the immediate motivation.
- `internal/packs/builtin/plan.go` — the pack that surfaces the gap most acutely.
- `internal/packs/builtin/route.go` — the pack that ships the catalog projection helper PR #1 calls.
