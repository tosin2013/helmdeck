---
description: "ADR-049: Intent Decomposition and the Self-Learning Plan Pack — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 49. Intent Decomposition and the Self-Learning Plan Pack

**Status**: Accepted — shipped in v0.22.0: `helmdeck.plan` pack with pipeline-aware decomposition (PR #1), and the `helmdeck://my-plans` self-learning projection (delivered via ADR 050 PR #3). Frontier-gap detection (`expert_baseline` / `gap_signal`, PR #3) remains deferred.
**Date**: 2026-06-01
**Domain**: pack-engine, pipelines, mcp, agent-integrations, memory

## Context

ADR 047 PR #3 shipped `helmdeck.route` to answer *"given an intent, which ONE tool is best?"* — and that's enough when the user's ask maps to a single pack or pipeline. But real conversational prompts often span multiple intents. A live test in OpenClaw made this concrete:

A user pasted a MiniMax M3 launch email and asked *"do you have memory using helmdeck for [paste]... we can use the memory to create a blog to test the memory."* Three intents: **check memory**, **store the launch info as a fact**, **draft a blog from it**. A free model (`nvidia/nemotron-3-super-120b-a12b:free`) picked the most-obvious-first-tool — image generation — and stopped. It never called `helmdeck.memory_store`, never invoked the blog pipeline, never used the corpus bridge ADR 048 shipped. The bridge worked; the agent simply didn't reach for it.

This is the gap between frontier and free models: frontier models decompose multi-intent prompts into ordered tool calls naturally; free models collapse to the most-obvious-first-tool. Routing alone can't fix it because the question isn't *"which tool?"* — it's *"which sequence of tools, with their arguments, in what order?"*

`helmdeck.plan` answers that question. The pack returns:
- An **ordered `steps[]` array** — each step naming a concrete tool/pipeline + its arguments + a rationale.
- A **`rewritten_prompt` string** — the same plan rendered as a step-by-step natural-language instruction explicit enough that a free model can execute it without further reasoning. This is the "send the prompt back to the calling LLM" idea the user articulated, materialized as an output field instead of an out-of-band mechanism.
- A **`complexity` classifier** — `single-action`, `pipeline-direct`, or `pack-chain` — so downstream consumers know which decomposition shape they got.

And it **learns**: every plan is written to memory under a new category `plan_history`, so future plans can mine past decompositions as priors. Over time the pack gets sharper at intents it has seen before and surfaces measurement signals about where even frontier models needed help.

## Decision

Three-PR roadmap, each independently mergeable.

### PR #1 — `helmdeck.plan` pack (this PR)

A new LLM-backed meta-pack mirroring `helmdeck.route`'s scaffolding (`internal/packs/builtin/route.go`). Reuses `buildCatalog()` for the catalog projection (packs **and** pipelines) and `defaultsFromAdapter()` for the caller's learned defaults. The system prompt teaches the model three pipeline-aware rules:

1. **Pipeline wins when one fits.** If a pipeline's `metadata.accepts` + `produces` covers the user's intent end-to-end, emit ONE step calling `helmdeck__pipeline-run` with that pipeline ID. Do NOT re-decompose what the pipeline already does internally.
2. **Honor `supersedes`.** A pipeline whose `metadata.supersedes` lists packs the user mentioned by name wins automatically — same policy `helmdeck.route` already enforces.
3. **Decompose only when no pipeline fits.** Fall back to a pack-by-pack chain only when no pipeline matches. The `complexity` output field distinguishes the three shapes for downstream consumers.

The hallucination guard rejects any step whose `tool` doesn't resolve to a registered pack or pipeline — replaced with `"tool": "unknown"` + a populated `rationale` explaining the gap. `helmdeck.plan` can't call itself (recursive-call guard).

Every successful plan writes a `PlanAudit` row to memory under category `plan_history` (new). The category is added to the reserved set in `internal/packs/facts.go` so agents can't poison the projection via the write surface.

SKILL.md gains one paragraph teaching the agent: *"For multi-action user prompts, call `helmdeck__plan` FIRST. Execute the returned `steps` in order, or treat `rewritten_prompt` as your next system-prompt-style instruction if the structured form is unwieldy."*

### PR #2 — Self-learning via `plan_history` projection

A new `helmdeck://my-plans` MCP resource projects the caller's recent plans grouped by intent shape (tokenized + hashed) and surfaces:

- Most-used decompositions per intent class
- Steps that consistently appear together (co-occurrence learned over time)
- **Plan drift** — when the agent's actual `pack_history` audit diverged from the recommended steps. Honest signal that the recommendation was wrong, not just unused.

The handler reads this projection as a prior on subsequent plan requests: for an intent that resembles past ones, the LLM gets *"here are the steps you produced for similar intents in the past; refine if needed"* alongside the catalog + defaults. Plans get sharper as the pack runs.

### PR #3 — Frontier-model gap detection

A new optional input `expert_baseline` on `helmdeck.plan` accepts the model's unaided attempt at the intent (or an explicit *"frontier model would have done X"*). The pack compares it to its own decomposition and emits a `gap_signal` field — empty when they agree, structured when they diverge:

```json
"gap_signal": {
  "frontier_missed": ["helmdeck__helmdeck-memory_store"],
  "plan_added": ["persistence step"],
  "category": "memory-aware orchestration",
  "notes": "Frontier model skipped persistence; this plan added it because the user said 'remember'."
}
```

Aggregated across calls, this is a dataset of tool-orchestration gaps even frontier models miss — useful for SKILL.md refinements, pack metadata improvements, and (innovative) public research on agent orchestration limits.

## Consequences

**Positive.**
- Free models gain reliable multi-step orchestration through an explicit decomposition step — closing the largest gap to frontier-model behavior in helmdeck's documented integration matrix.
- The `rewritten_prompt` output gives agents a fallback path when structured `steps[]` execution is brittle.
- Pipeline awareness keeps the pack honest: `builtin.brief-rewrite-blog` is the right answer for *"draft a blog from this brief"* — not a hand-decomposed three-pack chain — and the system prompt enforces that.
- `plan_history` joins `pack_history` and `pipeline_history` as a learning signal; PR #2's projection turns helmdeck into a measurement instrument for agent orchestration quality.

**Negative.**
- Adds an LLM round-trip before tool execution. ~1–3s latency cost per multi-action turn at PR #1; PR #2's cached-prior path brings repeat intents under ~100ms once warmed.
- Two coupled outputs (`steps[]` + `rewritten_prompt`) can drift. Mitigated by deriving `rewritten_prompt` from `steps` in the handler post-LLM, not asking the LLM to produce both independently.
- Self-learning data (PR #2) accumulates even when the agent ignored the plan. Naive frequency counts would encode prompt-engineering noise. Mitigated by explicit plan_drift tracking — the projection separates "plan was followed" from "plan was generated but ignored" signals.

**Out of scope of this roadmap.**
- Plan execution. PR #1 only RETURNS a plan; the agent (or a future executor) runs the steps. Keeping the contract narrow lets chat UIs, CLI agents, and future automation consume the plan output differently.
- Cross-caller plan sharing. Each caller's `plan_history` is namespaced like the rest of the memory layer. PR #2 may surface a curated subset; broad sharing is a separate ADR.
- Real-time plan adaptation (re-planning mid-execution when a step fails). The current contract is fire-once, agent-executes; mid-flight re-planning is a research-grade follow-up.

## See also

- ADR 047 — Catalog metadata + memory-driven routing. `helmdeck.plan` reuses `helmdeck.route`'s scaffolding and respects the same pipeline-supersedes policy.
- ADR 048 — Memory write surface + OpenClaw memory-corpus bridge. The `plan_history` audit category extends the same memory infrastructure; the corpus bridge will surface plan summaries alongside pack history in PR #3 of ADR 048's roadmap once landed.
- `internal/packs/builtin/route.go` — the scaffold the new pack near-clones.
