---
title: Models reference
description: Operator-facing tier table — which models helmdeck recognizes, what tier they're classified as, what tier-aware behavior applies, and how to override.
keywords: [helmdeck, models, tier, budget, prompt variant, routing, MCP]
---

# Models reference

The operator-facing surface for helmdeck's model-tier classification. Every chat completion model that hits the gateway falls into one of three tiers (A, B, C) plus an implicit "unknown" bucket that resolves to Tier C. The tier drives five tier-aware knobs simultaneously — catalog projection size, output token budget, prompt-template variant, strict-JSON mode, and prefix-cache routing. The full mechanism lives in [`internal/llmcontext/budgets.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/llmcontext/budgets.go) and is described architecturally in [ADR 050](../adrs/050-llm-context-manager.md), [ADR 051](../adrs/051-failure-mode-aware-dispatch.md), and [ADR 053](../adrs/053-tier-aware-plan-prompt-variants.md). This page is the operator's quick lookup.

## Recommended customization per tier

| Tier | Recommendation | What to do | Doc |
|---|---|---|---|
| **Tier A** (frontier) | **Works out of the box.** Generic SKILL.md as shipped. No per-model profile required. | Build your agent with the standard skill + your own SOUL/USER/IDENTITY/AGENTS for voice and operator persona. Verify behavior on your specific Tier A model — helmdeck assumes Tier A is reliable but doesn't claim certainty without your trace. | (no special doc) |
| **Tier B** (mid-tier paid or strong free) | **Unknown — experiment first.** Treat as a research question. | A/B test the same prompt with generic skill vs. a borrowed Tier C profile. Compare `artifact.put` calls + `verify_manifest` result + hallucination count. Decide based on YOUR trace. | [`docs/howto/experiment-with-tier-b-models.md`](../howto/experiment-with-tier-b-models.md) |
| **Tier C** (free / open-weight) | **Must customize.** Generic skill prose fails empirically (PR #462 + the [field reports](/blog)). | Use a model profile from `models/<provider>-<model>.yaml` as the starting point. Fork SOUL/USER/AGENTS per the profile's `prompt_template`. Encode use-case-specific success criteria. | [`docs/howto/add-free-models.md`](../howto/add-free-models.md) |

The library is a **starting point**, not a finished product. Operators MUST customize per (model × use-case). One library entry won't fit every use case.

**Per-model profiles available today**: [`openai/gpt-oss-120b:free`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml) (first entry; sourced from OpenAI Harmony + Together AI + IBM watsonx docs; empirically validated 2026-06-09). Planned per [issue #464](https://github.com/tosin2013/helmdeck/issues/464): `meta-llama/llama-3.3-70b-instruct:free`, `nvidia/nemotron-3-super-120b-a12b:free`, `google/gemma-2-9b-it:free`, `z-ai/glm-4.5-air:free` — each requires its own empirical validation before shipping.

## How tier affects behavior

| Knob | Tier A | Tier B | Tier C | Source ADR |
|---|---|---|---|---|
| Catalog projection | passthrough (no compaction) | trim metadata to ~22 KB | aggressive trim to ~10 KB + lexical pre-filter | [ADR 050](../adrs/050-llm-context-manager.md) |
| Output token budget | 4,000–8,000 | 2,000 | 1,500 | [ADR 051](../adrs/051-failure-mode-aware-dispatch.md) |
| `helmdeck.plan` prompt variant | `full_steps` — emit the complete pipeline JSON in one shot | `full_steps` | `single_pick` — emit ONE step at a time + `more_steps_likely` flag; agent re-calls plan for the next step | [ADR 053](../adrs/053-tier-aware-plan-prompt-variants.md) |
| Strict-JSON / `response_format` | enabled when the provider supports it | enabled for providers that support it | **disabled** — constrained-decoding deadlock risk on quantized inference | [ADR 051 PR #3](../adrs/051-failure-mode-aware-dispatch.md) |
| Prefix-cache routing (catalog block in system prompt) | enabled where supported | enabled on DeepSeek V4 Pro | disabled | [ADR 051 PR #4](../adrs/051-failure-mode-aware-dispatch.md) |
| LLM filter pass (two-pass cascade) | off by default | off | **on by default** (catalog won't fit otherwise) | [ADR 050 PR #4](../adrs/050-llm-context-manager.md) |

The asymmetry is deliberate: Tier C models get more tier-aware *protection* (smaller catalog, smaller output, simpler prompt, conservative encoder flags) because they have less raw capability to handle the unprotected path. Tier A models get fewer protections because they don't need them.

## When you'll see Tier C behavior

Three situations trigger the Tier C path:

1. **The model is explicitly in the Tier C list below** — e.g. `openrouter/nvidia/nemotron-3-super-120b-a12b:free`, `openrouter/moonshotai/kimi-k2.6`, or any other entry below.
2. **The model matches a Tier C prefix** — e.g. anything starting with `openrouter/qwen/qwen-2.5-` matches the broader `openrouter/qwen/qwen-2.5-` prefix entry.
3. **The model is unknown** — never seen before; helmdeck falls back to Tier C as the conservative default. This is intentional: when we don't know enough about a model to classify it, we assume it might struggle with the unprotected path.

If you observe Tier C behavior on a model you believe should be Tier A or B (you're hitting the smaller output budget, the simpler `single_pick` plan path, or the aggressive catalog trim), check that your model id matches an entry below verbatim or as a prefix. The model id `openrouter/nvidia/nemotron-3-super-120b-a12b:free` is technically a 120B model but is Tier C because the **free-tier inference quality** (quantization + reasoning-token leak + 50% multi-step plan success — see the [validation arc blog post](/blog/validation-arc-caught-its-own-first-bug)) doesn't match what parameter count alone would suggest.

## Tier A — frontier and frontier-relay models

Reliable on multi-step plans, structured output, and prefix caching. Catalog projection passes through unchanged; full output budget; `full_steps` plan variant; strict-JSON enabled.

| Model id (prefix or exact) | Input ceiling | Output budget | Strict JSON | Prefix cache | Hybrid reasoning | Notes |
|---|---|---|---|---|---|---|
| `anthropic/claude-opus-` | 200K | 8K | ✓ | ✓ | — | Anthropic flagship; extreme robustness under load |
| `anthropic/claude-sonnet-` | 200K | 8K | ✓ | ✓ | — | Primary working model class for helmdeck |
| `anthropic/claude-3.7-sonnet` | 200K | 8K | ✓ | ✓ | ✓ | Thinking mode; `<think>` blocks stripped by [ADR 051](../adrs/051-failure-mode-aware-dispatch.md) helper. BFCL 73.24%, Aider 84.2% |
| `anthropic/claude-haiku-` | 200K | 4K | ✓ | ✓ | — | Fastest Anthropic; used for `helmdeck.plan` filter pass when budget allows |
| `openai/gpt-4o` | 100K | 4K | ✓ | ✓ | — | BFCL 83.88%; stable; surfaces real HTTP errors rather than silent drops |
| `openai/gpt-5` | 1M | 4K | ✓ | ✓ | — | BFCL 72.92%, Aider 88.0%, 91.6% diff-format adherence — current frontier benchmark |
| `openai/o3-mini` | 200K | 4.4K | ✓ | ✓ | ✓ | BFCL 84.00%; reasoning model; `<think>` blocks stripped |
| `google/gemini-2.5-pro` | 1M | 4K | ✓ | ✓ | — | BFCL 85.04% (leaderboard top), Aider 99.6% edit-format |
| `google/gemini-2.5-flash` | 1M | 2.5K | ✓ | ✓ | — | BFCL 75.58%; fast + cheap; watch for safety-filter drops on code-execution prompts |
| `openrouter/anthropic/claude-` | 200K | 8K | ✓ | ✓ | — | OpenRouter relay of the Anthropic family |
| `openrouter/openai/gpt-4o` | 100K | 4K | ✓ | ✓ | — | OpenRouter relay |
| `openrouter/openai/gpt-5` | 1M | 4K | ✓ | ✓ | — | OpenRouter relay |
| `openrouter/openai/o3-mini` | 200K | 4.4K | ✓ | ✓ | ✓ | OpenRouter relay; reasoning model |
| `openrouter/google/gemini-2.5-` | 1M | 4K | ✓ | ✓ | — | Covers both `pro` and `flash` routes |

## Tier B — capable mid-tier and large open-weight models

Reliable on most tasks but with documented degradation modes — trailing-comma JSON at high temperature, narrower context windows, occasional timeout on long reasoning. Catalog trimmed to ~22 KB; output budget capped at 2K; `full_steps` plan variant; strict-JSON enabled where the provider supports it.

| Model id (prefix) | Input ceiling | Output budget | Strict JSON | Prefix cache | Hybrid reasoning | Notes |
|---|---|---|---|---|---|---|
| `openrouter/meta-llama/llama-3.1-70b` | 32K | 2K | — | — | — | Baseline mid-tier hosted |
| `openrouter/meta-llama/llama-3.3-70b` | 32K | 2K | — | — | — | Llama 3.3 family |
| `openrouter/google/gemma-2-` | 16K | 1.5K | — | — | — | Smaller-context Tier B route |
| `openrouter/mistralai/` | 32K | 2K | ✓ | — | — | Trailing-comma JSON degradation at high temperature; native API supports strict mode |
| `openrouter/deepseek/deepseek-v4-pro` | 1M | 2K | — | ✓ | ✓ | BFCL proxy 71.4%, Aider proxy 74.2%; HYBRID reasoning, **30× cache discount**, can hit 30-min serverless timeouts — keep filter cascade on |
| `openrouter/deepseek/deepseek-v3.2` | 128K | 2K | — | ✓ | — | Aider 74.2%; smaller context window than V4; same 30× cache discount |
| `openrouter/deepseek/deepseek-chat` | 128K | 2K | — | ✓ | — | Catches the broader `deepseek-chat-v3` / `v3.1` family |
| `openrouter/x-ai/grok-` | 256K | 2K | ✓ | — | — | BFCL proxy 61.38%, Aider 97.3% edit-format; price-tier bump past 128K context; xAI native supports strict mode |

## Tier C — free and weak open-weight models

Inconsistent on multi-step plans; chronic empty completions on the free tier; structured-output failures (constrained-decoding deadlock); hybrid-reasoning models that leak `<think>` content. **`helmdeck.plan` routes these to the `single_pick` variant by default** — one step per call, agent re-plans for the next — per [ADR 053](../adrs/053-tier-aware-plan-prompt-variants.md). The two-pass LLM filter cascade is on by default because the catalog won't fit a single-pass call.

| Model id (prefix or exact) | Input ceiling | Output budget | Strict JSON | Hybrid reasoning | Notes |
|---|---|---|---|---|---|
| `openrouter/openrouter/free` | 16K | 1.5K | — | — | OpenRouter's free routing tier — chronic empty completions due to infrastructure drops |
| `openrouter/nvidia/nemotron-` | 16K | 1.5K | — | — | Reasoning-trace models; the [validation arc post](/blog/validation-arc-caught-its-own-first-bug) measured 50% success at multi-step plan, ~71-char near-empty responses with 423 completion tokens (canonical reasoning leak). `single_pick` plan variant addresses both failure modes |
| `openrouter/z-ai/glm-` | 16K | 1.5K | — | — | BFCL 70.85%; infrastructure drops on the free routing tier |
| `openrouter/qwen/qwen-2.5-` | 16K | 1.5K | — | — | Aider 71.4%; injects ` ```json ` code fences even in strict mode — [ADR 051](../adrs/051-failure-mode-aware-dispatch.md) helper unwraps them |
| `openrouter/moonshotai/kimi-k2` | 256K | 1.5K | — | ✓ | HYBRID reasoning (large `<think>` output); observed 5-minute timeouts on long prompts |
| `openrouter/moonshotai/kimi-` | 256K | 1.5K | — | ✓ | Covers `kimi-latest` and future Kimi releases until empirically reclassified |
| `openrouter/tencent/` | 250K | 1.5K | — | — | `hy3-preview` + future Tencent routes; conservative until live-validated |

## Picking a model for your goal

| If you want | Use | Why |
|---|---|---|
| The most reliable structured output on any complexity intent | A Tier A model with strict-JSON enabled (`anthropic/claude-sonnet-`, `google/gemini-2.5-pro`, `openai/gpt-5`, or the `openrouter/auto` relay which Smart-routes among these) | Full output budget, full catalog, strict-JSON-enforced shape |
| Lowest cost per `helmdeck.plan` call AND consistent output | `openrouter/auto` | OpenRouter's complexity-aware router picks the cheapest model that can handle the intent; usually lands on Tier A |
| To exercise the `single_pick` agent-loop pattern | Any Tier C model — e.g. `openrouter/nvidia/nemotron-3-super-120b-a12b:free`, `openrouter/moonshotai/kimi-latest` | These default to the `single_pick` plan variant — see [ADR 053](../adrs/053-tier-aware-plan-prompt-variants.md). The agent re-calls `helmdeck.plan` after each step |
| Best price-per-token among Tier B | `openrouter/deepseek/deepseek-v3.2` or `openrouter/deepseek/deepseek-v4-pro` | DeepSeek native route has 30× prefix-cache discount; cached input ~$0.0145/M tokens |
| Maximum context (1M+ tokens) | `openai/gpt-5`, `google/gemini-2.5-pro`, `openrouter/deepseek/deepseek-v4-pro` | All three accept 1M+ input tokens |
| Reasoning model (the model "thinks" before answering) | Any `IsHybridReasoning: true` model — `anthropic/claude-3.7-sonnet`, `openai/o3-mini`, `openrouter/deepseek/deepseek-v4-pro`, the Kimi K2 family | `<think>` blocks are stripped automatically per [ADR 051 PR #1](../adrs/051-failure-mode-aware-dispatch.md); first-byte latency is higher; net output quality is often better on hard problems |

## Overriding the tier

When a model defies its tier classification — most commonly a Tier C model whose operator knows it handles multi-step plans reliably, or a Tier A model where the operator wants `single_pick` for cost reasons — the explicit override mechanism lives on the `Budget` entry in [`internal/llmcontext/budgets.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/llmcontext/budgets.go). Set `PromptVariant: PromptVariantFullSteps` to force `full_steps` despite a Tier C default, or `PromptVariant: PromptVariantSinglePick` to force `single_pick` despite a Tier A default. Same posture as the existing `IsHybridReasoning` / `WantsStrictJSON` / `SupportsPrefixCache` flags from [ADR 051 PR #2](../adrs/051-failure-mode-aware-dispatch.md) — operators override per-entry when their per-model knowledge contradicts the table.

To add a new model entry or revise an existing one, follow the methodology in [Calibrate model tiers](/howto/calibrate-model-tiers). The tier classifications are not guesses; each entry's trailing comment cites the benchmark (BFCL, Aider) or production observation that motivated the placement.

## Related

- [ADR 050 — Retrieval-augmented tool selection](../adrs/050-llm-context-manager.md) — the catalog projection mechanism the tier system extends.
- [ADR 051 — Failure-mode-aware dispatch](../adrs/051-failure-mode-aware-dispatch.md) — the architectural framing for the tier system + capability flags.
- [ADR 053 — Tier-aware prompt templates for `helmdeck.plan`](../adrs/053-tier-aware-plan-prompt-variants.md) — the `PromptVariant` mechanism + the empirical evidence (Nemotron 50% multi-step success) that motivated the `single_pick` path.
- [Calibrate model tiers](/howto/calibrate-model-tiers) — methodology for adding or revising a model entry.
- [Free models and context](/howto/free-models-and-context) — operating advice for the Tier C path specifically.
- [Validation arc blog post](/blog/validation-arc-caught-its-own-first-bug) — concrete numbers from the 2026-06-05 Nemotron testing window that surfaced the `single_pick` design.
- [Intent → prompt cookbook](/cookbook/intent-to-prompt) — recipes that work across tiers because the pipeline-direct path skips the planner.
