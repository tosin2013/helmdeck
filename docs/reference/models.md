---
title: Models reference
description: Operator-facing tier table — which models helmdeck recognizes, what tier they're classified as, what tier-aware behavior applies, and how to override.
keywords: [helmdeck, models, tier, budget, prompt variant, routing, MCP]
---

# Models reference

The operator-facing surface for helmdeck's model-tier classification. Every chat completion model that hits the gateway falls into one of three tiers (A, B, C) plus an implicit "unknown" bucket that resolves to Tier C. The tier drives five tier-aware knobs simultaneously — catalog projection size, output token budget, prompt-template variant, strict-JSON mode, and prefix-cache routing. The full mechanism lives in [`internal/llmcontext/budgets.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/llmcontext/budgets.go) and is described architecturally in [ADR 050](../adrs/050-llm-context-manager.md), [ADR 051](../adrs/051-failure-mode-aware-dispatch.md), and [ADR 053](../adrs/053-tier-aware-plan-prompt-variants.md). This page is the operator's quick lookup.

## Recommended customization per tier

| Tier | Structural compliance | Mandatory deposit-step compliance | What to do | Doc |
|---|---|---|---|---|
| **Tier A** (frontier) | **Works out of the box.** Generic SKILL.md as shipped. No per-model profile required. Handles parallel tool use, full N-platform fanout, multi-criterion fit checks, one-clarifying-question discipline. | **Skips the mandatory deposit step in single-response workflows.** Empirically observed (2026-06-09): Claude Sonnet 4.6 ran 8 platform variations via `blog.rewrite_for_audience` but **never called `artifact.put` or `artifact.verify_manifest`** — conflated "append CTA" with "deposit to artifacts." This is **workflow-shape-dependent, not tier-specific** (revised — see [empirical findings](#empirical-findings-from-2026-06-09) below). Iterative multi-turn workflows succeed even on Tier C. The architectural answer is the audit-callback pattern ([PR #462](https://github.com/tosin2013/helmdeck/pull/462)) wired as a typed call the agent invokes, with engine-level enforcement ([#461 Phase 3](https://github.com/tosin2013/helmdeck/issues/461)) as the durable fix. | Build your agent with the standard skill + your own SOUL/USER/IDENTITY/AGENTS for voice and operator persona. For multi-deposit workflows, structure the skill as a multi-turn workflow with explicit operator handoffs rather than asking the agent to do everything in one response. Verify behavior on your specific Tier A model. | (no special doc — but see [empirical findings](#empirical-findings-from-2026-06-09) below) |
| **Tier B** (mid-tier paid or strong free) | **Unknown — experiment first.** Treat as a research question. | **Unknown.** Run the same A/B and count `artifact.put` vs claimed deposits. | A/B test the same prompt with generic skill vs. a borrowed Tier C profile. Compare `artifact.put` calls + `verify_manifest` result + hallucination count. Decide based on YOUR trace. | [`docs/howto/experiment-with-tier-b-models.md`](../howto/experiment-with-tier-b-models.md) |
| **Tier C** (free / open-weight) | **Must customize.** Generic skill prose fails empirically — three failure modes documented ([PR #462](https://github.com/tosin2013/helmdeck/pull/462) + the [field reports](/blog)). | **Profile + iterative workflow shape makes mandatory tool calls reliable.** Profile-aware Tier C agent in single-response mode fires `verify_manifest` via auto-deposit pipelines (2026-06-09 trace). **Three-turn iterative workflow** (outline → draft → operator-triggered deposit+verify) successfully calls explicit `artifact.put` + `artifact.verify_manifest` with `all_present: true` — even on a free `gpt-oss-120b:free` route. **Latency is the trade-off**: ~3-5 min per turn on free Tier C routes; feels like hanging in the UI but completes correctly. | Use a model profile from `models/<provider>-<model>.yaml` as the starting point. Fork SOUL/USER/AGENTS per the profile's `prompt_template`. **For multi-deposit workflows, split the work into explicit operator-triggered turns** rather than asking for everything in one response — each turn small enough that chain-call reliability holds. Encode use-case-specific success criteria with hard counts. | [`docs/howto/add-free-models.md`](../howto/add-free-models.md) |

The library is a **starting point**, not a finished product. Operators MUST customize per (model × use-case). One library entry won't fit every use case. **The mandatory deposit step needs engine-level enforcement to be reliable across tiers** — that's [#461 Phase 3](https://github.com/tosin2013/helmdeck/issues/461).

### Empirical findings from 2026-06-09

Same prompt (`tech-blog-publisher` on the mcp-adr-analysis-server source), same skill, two model tiers — three traces captured:

| Behavior | Tier C baseline (no profile) | Tier C w/ profile (`gpt-oss-120b:free`) | Tier A (`claude-sonnet-4.6`) |
|---|---|---|---|
| Parallel tool use at startup | ✗ | ✗ | **✓ 3 simultaneous** |
| Real `blog.rewrite_for_audience` calls | 4 (in chat, not deposited) | 0 (used pipeline shortcut) | **✓ 8** |
| Real `pipeline-run` calls (auto-deposit producer) | 0 | **✓ 2** | 0 |
| Real `blog.append_cta` calls | 0 | 0 | 8 (all REJECTED — see [PR #468](https://github.com/tosin2013/helmdeck/pull/468)) |
| InfoQ 6-criterion fit check executed | skipped | skipped | **✓ per-criterion grades** |
| Multi-step plan acknowledged upfront | partial | partial | **✓ full 5-step plan stated** |
| Honored "ask at most ONE clarifying question" rule | ✗ (hedged) | ✗ (hedged) | **✓ one question + stated defaults** |
| **`artifact.put` calls** | **0** | **0** | **0** |
| **`artifact.verify_manifest` calls** | **0** | **1 (`all_present: true, 2 of 2 verified`)** | **0** |
| Hallucinated manifest entries | 6 (earlier session) | 0 | 0 |

**Reading the table**: Tier A handles every *structural* aspect of the skill better than either Tier C variant — parallel tool use, full fanout, fit-check rigor, clarifying-question discipline. But on the load-bearing "deposit step is mandatory" rule, **all three traces above (single-response workflows) skipped explicit `artifact.put`**. Only the profile-aware Tier C variant fired `verify_manifest` — and it did so via the `pipeline-run` shortcut, where the audit was implicit in the pipeline contract rather than an explicit agent decision.

**Refined finding from a fourth trace (later 2026-06-09)**: a Tier C agent running an **iterative three-turn workflow** (outline → draft → operator-triggered deposit+verify) on the same `gpt-oss-120b:free` route successfully called BOTH `artifact.put` AND `artifact.verify_manifest`, returning `all_present: true`. Latency was significant (~5 minutes for the deposit-and-verify turn on the free route), but the mandatory tool calls executed correctly.

**Corrected conclusion**: deposit-step skipping is **workflow-shape-dependent, not tier-invariant**. Single-response workflows asking the agent to do classify-outline-draft-deposit-verify-checklist in one go fail on every tier. Multi-turn iterative workflows with explicit operator handoffs (each turn small enough that 1-2 pack calls suffices) drive the mandatory calls reliably even on cheap Tier C. **Engine-level enforcement ([#461 Phase 3](https://github.com/tosin2013/helmdeck/issues/461)) remains the durable architectural answer** because it removes the workflow-shape dependency entirely — but well-shaped iterative skill prose CAN drive the mandatory call on every tier we've tested.

### Iterative workflow pattern (recommended for Tier C and multi-deposit cases on any tier)

When a skill produces N artifacts that must be deposited, structure it as:

- **Turn 1**: agent classifies + outlines + asks at most one clarifying question + stops with explicit handoff (`Reply 'proceed' to write the draft`)
- **Turn 2**: agent writes the full draft + stops with explicit handoff (`Reply 'deposit' to save and verify`)
- **Turn 3**: agent calls `artifact.put` + `artifact.verify_manifest` + reports the result

Each turn is small enough that `chain_call_reliability: high` (1-2 pack calls per turn) actually applies. The mandatory tool calls fire reliably. Trade-off is operator effort (three message exchanges instead of one) and latency on free Tier C routes (~3-5 min per turn).

The handoff line at the end of each turn IS load-bearing — if the skill prose says "produce a handoff line" but doesn't list missing-handoff as an invalidation condition, the model will drop it. Pin handoff lines as success-criteria invalidation conditions in AGENTS.md (`A response is invalid unless it ends with the literal text 'Reply with deposit ...'`).

**Per-model profiles available today** (per [issue #464](https://github.com/tosin2013/helmdeck/issues/464) Phase 1):

- [`openai/gpt-oss-120b:free`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml) — sourced from OpenAI Harmony + Together AI + IBM watsonx docs; empirically validated 2026-06-09.
- [`google/gemma-4-26b-a4b-it:free`](https://github.com/tosin2013/helmdeck/blob/main/models/google-gemma-4-26b-a4b-it-free.yaml) — *profile stub*; 26B-total / 3.8B-active MoE Gemma 4 (256K context, multimodal); sourced from Google AI + Hugging Face + DeepMind docs; baseline empirical trace pending — Google AI Studio shared free pool gates upstream (2026-06-10 429 finding), BYOK recommended for sustained empirical work.
- [`meta-llama/llama-3.3-70b-instruct:free`](https://github.com/tosin2013/helmdeck/blob/main/models/meta-llama-llama-3.3-70b-instruct-free.yaml) — *profile stub*; 70B dense Llama 3.3; sourced from Meta Llama 3.3 docs; baseline empirical trace pending.
- [`nvidia/nemotron-3-super-120b-a12b:free`](https://github.com/tosin2013/helmdeck/blob/main/models/nvidia-nemotron-3-super-120b-a12b-free.yaml) — *profile stub*; 120B/12B hybrid Mamba-Transformer MoE (1M context); sourced from Nvidia NIM + agentic-coding cookbook + technical blog; baseline empirical trace pending.
- [`qwen/qwen3-coder:free`](https://github.com/tosin2013/helmdeck/blob/main/models/qwen-qwen3-coder-free.yaml) — *profile stub*; 480B-total / 35B-active MoE coder-specialized variant (1M context via YaRN, 256K native); sourced from Alibaba Qwen HF card + GitHub README + announcement blog; baseline empirical trace pending. Substituted for the originally-listed `z-ai/glm-4.5-air:free`, which was deprecated from OpenRouter on or before 2026-06-10.

**Non-OpenRouter profiles (alternative routing)** — per [issue #482](https://github.com/tosin2013/helmdeck/issues/482):

- [`huggingface/openai/gpt-oss-120b`](https://github.com/tosin2013/helmdeck/blob/main/models/huggingface-openai-gpt-oss-120b.yaml) — first non-OpenRouter template; reuses the gpt-oss prompting guidance from the OpenRouter sibling above (model behavior is provider-agnostic); routes through HuggingFace Inference Providers (`router.huggingface.co/v1`) with `:fastest` / `:cheapest` / `:preferred` provider-selection policies. Empirical sections empty — community contributions invited. See [`docs/howto/configure-non-openrouter-providers.md`](../howto/configure-non-openrouter-providers.md) for routing setup.

Stub profiles ship the schema scaffold with docs-sourced prompting guidance. They invite community contributions per [`docs/howto/add-free-models.md` § 7](../howto/add-free-models.md): operators running custom Tier C agents can submit trace excerpts to populate `community_traces[]` or open issues against any per-model YAML.

**See also**: [`docs/reference/model-profiles-schema.md`](model-profiles-schema.md) for the canonical YAML schema reference; [`docs/howto/configure-non-openrouter-providers.md`](../howto/configure-non-openrouter-providers.md) for routing setup on HuggingFace / Together / Groq / self-hosted.

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

The tier table rows below describe **routing-level** behavior — input ceiling, output budget, strict-JSON support, and the like. For **prompting-level** behavior — what prompt shape this model expects, what reasoning-effort knob it exposes, what failure modes are documented — see the per-model profile library linked in the previous section. Per-model profiles override the row-level Notes column with specific best practices, anti-patterns, and prompt templates sourced from official model docs.

| Model id (prefix or exact) | Input ceiling | Output budget | Strict JSON | Hybrid reasoning | Notes |
|---|---|---|---|---|---|
| `openrouter/openrouter/free` | 16K | 1.5K | — | — | OpenRouter's free routing tier — chronic empty completions due to infrastructure drops |
| `openrouter/nvidia/nemotron-` | 16K | 1.5K | — | — | Reasoning-trace models; the [validation arc post](/blog/validation-arc-caught-its-own-first-bug) measured 50% success at multi-step plan, ~71-char near-empty responses with 423 completion tokens (canonical reasoning leak). `single_pick` plan variant addresses both failure modes. Profile: [`nvidia/nemotron-3-super-120b-a12b:free`](https://github.com/tosin2013/helmdeck/blob/main/models/nvidia-nemotron-3-super-120b-a12b-free.yaml) (stub) |
| `openrouter/z-ai/glm-` | 16K | 1.5K | — | — | BFCL 70.85%; infrastructure drops on the free routing tier. Note: `z-ai/glm-4.5-air:free` was deprecated from OpenRouter on or before 2026-06-10; only the paid `z-ai/glm-4.5-air` remains. Prefix row covers future GLM `:free` variants if Z.ai restores any. |
| `openrouter/google/gemma-4-` | 16K | 1.5K | — | — | 26B-A4B MoE Gemma 4; 256K context window, multimodal; binary thinking-mode toggle (no graded knob). Google AI Studio shared free pool gates upstream (2026-06-10 429 finding); BYOK recommended. Profile: [`google/gemma-4-26b-a4b-it:free`](https://github.com/tosin2013/helmdeck/blob/main/models/google-gemma-4-26b-a4b-it-free.yaml) (stub) |
| `openrouter/meta-llama/llama-3.3-70b-instruct:free` | 16K | 1.5K | — | — | Free routing of Llama 3.3 70B; the non-`:free` route is Tier B. Profile: [`meta-llama/llama-3.3-70b-instruct:free`](https://github.com/tosin2013/helmdeck/blob/main/models/meta-llama-llama-3.3-70b-instruct-free.yaml) (stub) |
| `openrouter/qwen/qwen3-coder` | 16K | 1.5K | — | — | 480B/35B MoE coder-specialized variant; 1M context via YaRN, 256K native; Agent RL post-trained for multi-turn tool use; non-thinking-mode only. Profile: [`qwen/qwen3-coder:free`](https://github.com/tosin2013/helmdeck/blob/main/models/qwen-qwen3-coder-free.yaml) (stub) |
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
