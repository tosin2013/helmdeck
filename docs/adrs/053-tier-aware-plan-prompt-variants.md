---
description: "ADR-053: Tier-Aware Prompt Templates for `helmdeck.plan` — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 53. Tier-Aware Prompt Templates for `helmdeck.plan`

**Status**: Accepted (infrastructure shipped in this PR; tier-default selection is on for all unmapped models; opt-in override available per `Budget` entry.)
**Date**: 2026-06-05
**Domain**: llmcontext, packs, agent-integrations

## Context

`helmdeck.plan` decomposes natural-language intents into ordered multi-step pipeline JSON. Its existing tier-aware knobs are extensive — catalog projection is trimmed for smaller tiers ([ADR 050](050-retrieval-augmented-tool-selection.md)), output budgets are conservative on Tier C ([ADR 051](051-failure-mode-aware-dispatch.md)), strict-JSON mode is gated on tier ([ADR 051 PR #3](051-failure-mode-aware-dispatch.md)), prefix-cache routing is gated on tier ([ADR 051 PR #4](051-failure-mode-aware-dispatch.md)). The pack's `Description` field has even acknowledged the asymmetry in plain text: *"plan quality is bounded by the model: free models may benefit from rewritten_prompt while frontier models can consume steps[] directly."*

The **prompt template itself** has not been tier-aware. Both Tier A frontier models and Tier C free open-weights models received the same `planSystemPrompt` and were asked to emit the same multi-step pipeline JSON in one shot. The pack's existing fallback paths (lexical selection on filter failure, error typing on length-truncated responses, reasoning-token stripping) provided defense-in-depth — but they kicked in *after* the model failed, not *before*.

The motivating evidence landed during the validation-arc testing window on 2026-06-05. Six `helmdeck.plan` calls were issued against `openrouter/nvidia/nemotron-3-super-120b-a12b:free` for the same multi-step intent class:

| Wall time | finish_reason | completion_tokens | raw_content_len | latency_ms | Outcome |
|---|---|---|---|---|---|
| 14:41:03 | stop | 1535 | 743 | 90199 | ✓ clean |
| 14:39:33 | length | 600 | 2627 | 15184 | ✗ truncated mid-JSON |
| 14:39:17 | stop | 710 | 791 | 28854 | ✓ clean |
| 14:38:49 | stop | 423 | 71 | 14776 | ✗ near-empty (reasoning leak) |
| 14:38:34 | stop | 1547 | 685 | 94958 | ✓ clean |
| 14:36:59 | length | 600 | 2549 | 34139 | ✗ truncated mid-JSON |

Effective success rate: 3/6 = **50%**. Two failure modes — length truncation at the 600-token output cap and the canonical "reasoning leak" pattern (423-token completion that emits only 71 chars of user-visible JSON; TokenMix measures the analogous behavior at ~40% on DeepSeek R1 with `max_tokens=200`). On `openrouter/auto` for the same intent class, the same window saw 2/2 clean stops at 15–34s latency.

The architectural literature converges on a single point. BFCL data (TinyLLM, arXiv 2511.22138) shows small open-weight models exhibit a multi-turn collapse — xLAM-2-1B at 53.97% overall vs **8.38% on multi-turn**, Qwen3-1.7B at 55.49% overall vs **16.88% multi-turn** — a 30-to-50-point drop from single-call to multi-step in one shot. Portkey ships "Smart Fallback with Model-Optimized Prompts" as a first-class feature with per-model `prompt_id` binding. DSPy compiles a different prompt per LM from a single Signature. The PLAN-TUNING (arXiv 2507.07495) and Pre-Act (arXiv 2505.09970) results both conclude that small models benefit from *decomposed planning* — running one step at a time rather than emitting the full plan in one shot. Anthropic's "Building Effective Agents" essay points the same direction: simpler loops; smaller per-call work; scale autonomy as the model gets smarter.

The output shape — not the model size per se — is the right primitive. A 120B model that can't reliably emit 1,500 tokens of nested pipeline JSON isn't a "bad model"; it's a model whose effective output budget under our prompt doesn't match the task as we've shaped it.

## Decision

Make `helmdeck.plan`'s prompt template tier-aware via a new `Budget.PromptVariant` field with two values shipped:

- **`PromptVariantFullSteps`** — Tier A/B default. The existing `planSystemPrompt` template. Asks the model for the complete pipeline JSON in one shot. Optimal when the model can reliably emit 500–2000 tokens of structured output.
- **`PromptVariantSinglePick`** — Tier C default. The new `planSystemPromptSinglePick` template. Asks the model for the SINGLE NEXT step + a `more_steps_likely` flag. The agent re-calls `helmdeck.plan` with updated context to plan the step after that. Output budget ~300 tokens — well inside what Tier C models reliably produce.

The output schema stays the same across both variants: `{steps:[], complexity, more_steps_likely, reasoning}`. The handler doesn't need to parse two different response shapes. Only the model's TASK changes — what it's asked to produce.

### Selection mechanism

`Budget.ResolvePromptVariant()` returns the variant for a given model:

1. **Explicit override wins.** If `Budget.PromptVariant` is non-empty on the table entry, that value is returned. Useful when a model defies its tier (e.g. a Tier B model trained specifically for tool calling that handles multi-step plans reliably and should get `FullSteps` despite the tier suggesting otherwise; OR a Tier A model where an operator wants the agent-loop pattern for cost reasons since `single_pick` output is much smaller per call).
2. **Tier default.** When `PromptVariant` is unset (the zero value), `ResolvePromptVariant` returns the tier-default: Tier A/B → `FullSteps`; Tier C and unknown → `SinglePick`. The fallback for unknown tiers is the conservative path — if we don't know enough about the model to classify it, assume it might struggle with multi-step output and route to the safer single-pick path.

### Output additions

`planOutput` gains two fields:

- **`prompt_variant_used`** — `"full_steps"` or `"single_pick"`. Surfaced so agents can detect when they're in the one-step-at-a-time loop and re-call `helmdeck.plan` after running the returned step.
- **`more_steps_likely`** — `bool`. Set by the `single_pick` variant when the emitted step is the FIRST in a chain. Always `false` on `full_steps` (the full plan is already in `steps[]`). Omitted from the wire shape when `false` to keep the schema stable for callers that haven't migrated to read it.

### Agent loop pattern

The `single_pick` variant composes naturally with the MCP agent loop pattern:

```
1. Agent calls helmdeck.plan with intent
2. helmdeck.plan returns {steps:[step1], more_steps_likely:true}
3. Agent runs step1 (the tool call helmdeck.plan recommended)
4. Agent calls helmdeck.plan with intent + step1's output as context
5. helmdeck.plan returns {steps:[step2], more_steps_likely:true}
6. ... repeat until more_steps_likely:false
```

Each call is a self-contained Tier-C-sized decision. The agent provides the loop; helmdeck.plan provides the per-step routing. The catalog projection is already cached on prefix-cache-enabled providers (when applicable), so the per-step cost is dominated by output tokens, which are small.

### Backward compatibility

The wire shape for callers that don't read the new fields is unchanged: existing consumers continue to receive `{steps, complexity, reasoning, model, compaction?}` and the new fields are added at the JSON layer as `omitempty`. Tier A/B behavior is identical to pre-ADR-053 (same prompt, same output, same model interactions). Only Tier C and unknown-tier models see a behavioral change — and that change is *they now produce reliably parseable output where 50% of the time they previously did not*.

## Consequences

**Positive.** Tier C models reliably produce structured plan output. The 600-token output cap that previously triggered `finish_reason: length` truncation on 33% of multi-step plans now comfortably accommodates a `single_pick` response (~50–200 tokens of structured output). Effective success rate moves from ~50% to expected ~95%+ on the Tier C path. The architectural posture matches the literature: route by output shape, not by parameter count. The override mechanism (`Budget.PromptVariant` field) keeps operators in control when their per-model knowledge contradicts the tier default — same posture as the explicit `IsHybridReasoning` / `WantsStrictJSON` flags introduced in ADR 051 PR #2.

**Positive (architectural).** The catalog projection (ADR 050), output budget (ADR 051), prompt template (this ADR), strict-JSON gating (ADR 051 PR #3), and prefix-cache routing (ADR 051 PR #4) now all flex per tier through the same `Budget` struct. Operators have one place to look ("what's the budget entry for this model say?") and one place to override (the table entry). The tier system has become the unified abstraction for "what does this model deserve and what can it handle."

**Negative.** The agent now runs through helmdeck.plan once per step on Tier C, rather than once per intent. For a 5-step intent, that's 5 `helmdeck.plan` calls instead of 1. Each call is much smaller (smaller catalog projection, smaller output, often prefix-cached on the catalog block), so the wall-clock cost is roughly comparable and the per-call failure surface is much smaller. But the audit log will show 5× the `mcp_call` rows for `helmdeck__plan` on Tier C paths, and operators inspecting traffic patterns need to understand why.

**Negative.** Two prompt templates means two surfaces to keep in sync when the planning rules evolve. We mitigate via the rule-with-test guard pattern (`TestSelectPlanSystemPrompt` asserts template markers per tier+variant), but a future change to the routing rules (pipeline supersedes, complexity classification) must consciously update both templates. We accept this — the maintenance cost is bounded and the alternative (one template that tries to serve both shapes) was empirically demonstrated to fail.

**Negative.** The `more_steps_likely` flag is a hint, not a contract. A small model might emit `more_steps_likely:true` when it doesn't actually know whether more steps are needed; the agent then makes an extra round-trip that produces a no-op step. We accept this — the round-trip is cheap on Tier C, and the alternative (forcing the model to know whether it's done) is exactly the structural problem we're trying to avoid.

**Out of scope.** A separate output schema for `single_pick` (`{pack, input, reason}` vs `{steps:[...]}`) — keeping one schema means one parser, one set of tests, one downstream contract. Worth revisiting if the unified schema produces friction. A per-tier output-budget bump on `single_pick` (since the smaller output should comfortably fit a smaller `OutputTokens`) — handled by the existing tier-keyed `OutputTokens` field; no new knob needed. A `single_pick` variant for `helmdeck.route` (the single-tool-recommendation pack) — that pack already emits a `{recommendation, alternatives}` shape that's structurally what `single_pick` does for `plan`; no change needed.

**Promotion / deferred.** A `PromptVariantHybrid` value where the planner emits the first 1–2 steps and signals "more likely" — a middle ground for Tier B models that handle short multi-step plans but not full pipelines. Deferred until we have empirical data on a specific Tier B model failing the current `FullSteps` posture; speculative variants without motivating evidence are how the variant enum bloats into a footgun.

## See also

- [ADR 050](050-retrieval-augmented-tool-selection.md) — Catalog projection: the same Budget mechanism this ADR extends.
- [ADR 051](051-failure-mode-aware-dispatch.md) — Failure-mode-aware dispatch: defines the tier system, capability flags, and the four-PR roadmap this ADR slots into as a natural fifth piece.
- [ADR 052](052-av-output-validation-post-step.md) — The validation arc whose testing window produced the Nemotron observation that motivated this ADR.
- `internal/llmcontext/budgets.go` — `Budget.PromptVariant` field + `ResolvePromptVariant` method + tier defaults.
- `internal/packs/builtin/plan.go` — `planSystemPromptSinglePick` template + `selectPlanSystemPrompt` selector.
- `internal/llmcontext/budgets_test.go` — `TestResolvePromptVariant_TierDefaults` + `TestResolvePromptVariant_ExplicitOverride`.
- `internal/packs/builtin/plan_test.go` — `TestSelectPlanSystemPrompt` template-marker regression guard.
- Field-report blog post: *"We shipped a 4-phase reliability arc. The first bug it caught was itself"* (draft in [PR #436](https://github.com/tosin2013/helmdeck/pull/436)) — the motivating Nemotron observation that produced this ADR.
- TokenMix on reasoning-token-leak rates: <https://tokenmix.ai/blog/thinking-tokens-billing-trap-2026>
- BFCL multi-turn measurements: <https://arxiv.org/abs/2511.22138>
- Portkey "Smart Fallback with Model-Optimized Prompts": <https://portkey.ai/docs/guides/use-cases/smart-fallback-with-model-optimized-prompts>
- DSPy compile-per-LM Signatures: <https://dspy.ai/learn/programming/signatures/>
- PLAN-TUNING (arXiv 2507.07495); Pre-Act (arXiv 2505.09970): the decomposed-planning architectural argument.
