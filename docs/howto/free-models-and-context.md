---
title: Run orchestration packs on free models
description: Understand the LLM context manager (ADR 050) — per-model budgets, catalog compaction, the compaction output field, and diagnosing empty plans.
keywords: [helmdeck, free models, context budget, llmcontext, compaction, context-budgets, ADR 050]
---

# Run orchestration packs on free models

`helmdeck.plan` and `helmdeck.route` give the model a projection of the catalog (52 packs + 21 pipelines, each with metadata) to reason over. On a frontier model that fits comfortably. On a small free model with an 8K–16K context window, the full projection can blow the budget, and the model returns an empty or garbage plan.

The **LLM context manager** (`internal/llmcontext`, ADR 050) fixes that: it compacts the catalog projection to fit each model's budget before the packs call the gateway. This guide explains how to read what it did and what to do when a plan still comes back thin.

## The per-model budgets

Budgets are global engine policy, surfaced as a read-only MCP resource:

```json
resources/read  { "uri": "helmdeck://context-budgets" }
```

```json
{
  "budgets": [
    { "model": "openrouter/auto", "input_tokens": 128000, "max_catalog_bytes": 120000, "tier": "A" },
    { "model": "openrouter/openrouter/free", "input_tokens": 8192, "max_catalog_bytes": 6000, "tier": "C" }
  ],
  "fallback": { "input_tokens": 16000, "max_catalog_bytes": 12000, "tier": "B" },
  "policy": "exact model match, then provider prefix, then fallback"
}
```

- **Tier A** — generous budget, no compaction; the model sees the full catalog.
- **Tier B / C** — progressively aggressive metadata trimming (drop `limitations`, then `typical_use`, then collapse to name + one-line summary), plus a cascading select + lexical rank that keeps the entries most relevant to the user's intent.

Unmapped models use the `fallback` entry. To give a new model a bigger budget, add its id to your deployment's budget config (see ADR 050) rather than editing source.

## Reading the `compaction` field

When `helmdeck.plan` compacts, it reports it in the output:

```json
{
  "steps": [ ... ],
  "rewritten_prompt": "...",
  "complexity": "pack-chain",
  "compaction": {
    "tier": "C",
    "catalog_entries_total": 73,
    "catalog_entries_kept": 18,
    "bytes_before": 41000,
    "bytes_after": 5800
  },
  "model": "openrouter/openrouter/free"
}
```

If `catalog_entries_kept` is small and the plan missed an obviously-relevant pack, the relevant entry was ranked out. Two fixes:

1. **Sharpen the intent.** The lexical ranker keys off the user's words; "make a slide deck from this PDF" keeps the slides/doc packs that a vague "do something with this file" drops.
2. **Use a bigger model for the planning call.** Pass a Tier-A `model` (e.g. `openrouter/auto`) for the `helmdeck.plan`/`helmdeck.route` call specifically, then execute the resulting steps on whatever model you like.

## Diagnosing an empty plan

| Symptom | Likely cause | Fix |
|---|---|---|
| `steps: []`, `complexity: single-action` | The model decided one tool suffices. | Check `recommendation`/`rewritten_prompt` — often correct. |
| `steps: []` on a free model, no `compaction` | Model returned unparseable JSON. | Retry; the two-pass filter tolerates minor JSON drift but not total failure. Use a Tier-A model. |
| Plan omits a relevant pack, low `catalog_entries_kept` | Ranked out under a tight budget. | Sharpen the intent or raise the model tier (above). |
| `internal: registered without a gateway dispatcher` | No AI gateway configured. | Orchestration packs are gateway-gated; configure a provider key. |

## Related

- [Intent decomposition](./intent-decomposition.md) and [Routing & gap analysis](./routing-and-gap-analysis.md).
- [Models reference](/reference/models) — operator-facing tier table; the per-model entries this guide refers to abstractly.
- [`helmdeck.plan` reference](../reference/packs/helmdeck/plan.md); [MCP resources](../reference/mcp-resources.md) — `context-budgets`, `my-plans`.
- ADR 050 — LLM Context Manager for Catalog-Heavy Packs.
- [ADR 053](/adrs/053-tier-aware-plan-prompt-variants) — `PromptVariantSinglePick` for Tier C models; why `helmdeck.plan` now emits one step at a time on Tier C.
