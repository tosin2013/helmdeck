---
slug: empirical-tier-context-management
title: "Free models empty-completed our 35KB tool catalog. So we tier-classified them by failure mode, not vendor spec."
authors: [tosin]
tags: [weak-models, agent-architecture, friction]
description: A live test exposed that some free LLMs return empty completions when a tool catalog exceeds their effective working set. We responded by classifying models by their observed structured-output reliability — not their advertised context windows — and compacting the catalog with explicit dispatch invariants.
image: /img/social-card.png
date: 2026-06-01
draft: true
---

We shipped `helmdeck.plan` (ADR 049 PR #1) — an LLM-backed meta-pack that decomposes multi-intent user prompts into ordered tool/pipeline calls. It worked on frontier models. It worked on trivial intents against free models. Then we tested the actual scenario that motivated the pack: a real OpenClaw chat prompt with a 1.5KB launch announcement paste and *"remember this, draft a blog about it, generate an image."*

Three of four attempts hit OpenClaw's MCP 60-second timeout. The fourth returned `{"error":"handler_failed","message":"gateway returned an empty plan response"}` after 29.5 seconds — our own error string for *the model returned a 200 with no content*.

<!-- truncate -->

The same prompt against `openrouter/z-ai/glm-4.5-air:free` took 58 seconds and produced the same empty completion. Two different free models, both with advertised 32K context windows, both reproducibly emptying out when the prompt got busy.

## Measuring what was actually too big

The diagnosis took ten minutes once we instrumented properly. `helmdeck.plan` ships the full catalog projection — every pack and pipeline with full metadata — to give the model enough context to pick the right tools. We measured the projection:

```
packs full metadata:     14,187 bytes  (52 packs)
pipelines full metadata: 21,092 bytes  (21 pipelines)
total catalog payload:   35,279 bytes
```

Add the user's 1.5KB paste, the 1.5KB system prompt, and the 3000-token structured-output ceiling, and free models with imperfect structured-output reliability give up entirely. Not a timeout, not a refusal — a 200 OK with zero output.

A trivial intent (`"take a screenshot of github.com"`) on the same model with the same catalog worked in 13 seconds. The failure wasn't the catalog alone — it was the interaction between catalog size, intent complexity, and the model's working set for producing structured JSON.

## Tiers calibrated by failure mode, not context window

The standard pattern in agent frameworks is to classify models by their advertised context window. LangChain's model registry, LlamaIndex's `LLMMetadata`, Anthropic's model card spec — all of them lead with "what's the max input." Useful for cost estimation, mostly useless for predicting where structured output breaks.

We tier helmdeck-known models differently. Three tiers, calibrated against observed failures:

- **Tier A — frontier.** Claude Opus / Sonnet / Haiku, GPT-4-class. Reliable structured output even at 50K+ tokens of catalog. Compaction skipped.
- **Tier B — mid-tier hosted.** Llama 3 70B, Mistral 7B Instruct, Gemma 2 9B. Reliable up to ~25K of catalog. Compaction trims aggressively.
- **Tier C — weak or free.** Free OpenRouter routes, sub-30B open models. Empty-complete on 35KB catalogs. Compaction targets ~10KB.

`z-ai/glm-4.5-air:free` and `nvidia/nemotron-3-super-120b-a12b:free` both have 32K context windows. Both are Tier C in our table because at 14KB of input — well within window — they emptied out on the structured-output task.

The takeaway: vendor specs describe maximums, not reliability under load. We had to learn this by reproducing the failure, and the tier system encodes what we learned.

## Compaction with dispatch invariants

Once we had a tier in hand, the question became *what to throw away.* Standard summarization or arbitrary truncation would have broken the pack — `helmdeck.plan`'s system prompt teaches the model three pipeline-aware rules, and rule P2 depends on a specific field in the pipeline metadata:

> **Honor `supersedes`.** A pipeline whose `metadata.supersedes` lists packs the user mentioned by name wins automatically.

If compaction drops `supersedes`, the planner stops emitting pipeline-direct decompositions and falls back to chaining the constituent packs by hand. The pipeline's curation guarantee — *"this sequence works because maintainers proved it"* — silently regresses.

So we wrote `CompactCatalog` with explicit dispatch invariants. Six trim steps applied in priority order:

```
1. pack.intent_keywords[]
2. pack.typical_use
3. pack.limitations[]
4. pipeline.steps[] bodies (kept: id/name/pack)
5. pipeline inputs/outputs schemas (replaced with field-name lists)
6. description truncation to first sentence
```

Pipeline `metadata.supersedes` is **never trimmed.** Pack names and pipeline ids are **never trimmed.** Those three fields are the dispatch graph — the planner needs them to emit valid step shapes the agent can actually call.

After all six passes, the live test runs like this:

```
{"msg":"helmdeck.plan: catalog compacted to fit model budget",
 "model":"openrouter/openrouter/free", "tier":"C",
 "before_bytes":30141, "after_bytes":13892,
 "dropped":["pack.intent_keywords[]","pack.typical_use",
            "pack.limitations[]","pipeline.steps[].body",
            "pipeline.inputs/outputs.schema",
            "description.firstSentence",
            "still_over_budget(13892>10000)"]}
```

Trivial intents on `openrouter/openrouter/free` post-compaction succeed in ~23 seconds. The 30KB → 13.9KB reduction is enough to unblock simple cases.

The complex multi-paragraph intent still empty-completes. The 14KB irreducible floor — names, ids, supersedes, plus trimmed descriptions — is still too much for the model when combined with a long paste and a structured-output ceiling. The honest answer is that metadata compaction alone can't fix the worst case; the real fix is **retrieval-augmented tool selection**: send only the catalog entries relevant to the intent, scoped as a follow-up PR.

## What's standard, what's actually different

We considered framing this post as "helmdeck builds RAG for tool selection." That would be misleading. RAG, two-pass cascades, dense retrieval + cross-encoder re-rankers — these are well-known patterns in agent frameworks. The cascade architecture we're building toward is standard practice.

What's less standard about our approach:

- **Tier classification by structured-output reliability, not context window.** A 32K-window model that empty-completes at 20K on structured output is Tier C even though its window is "larger" than some Tier B models.
- **Domain-aware compaction with explicit dispatch invariants.** Generic summarization doesn't know which tokens are load-bearing. Helmdeck's compaction operates inside a known schema and treats `supersedes`, names, and ids as untouchable.
- **Self-learning per-caller priors** — designed for the next PR. Future retrieval ranking will mine the `plan_history` audit category we shipped with `helmdeck.plan` (intent SHA, complexity classifier, step tool names + arg hashes — 30-day TTL, namespaced per caller). Per-caller priors based on what the planner actually picked for similar past intents.

The bundled novelty isn't the cascade machinery. It's the **calibration loop**: empirical-failure-mode tiers → compaction with dispatch invariants → learned per-caller priors → measurement of where retrieval depth had to escalate. The cascade is standard; calibrating it against observed failures and feeding the observations back into the system is the part we couldn't find published prior art for.

## Why this matters beyond helmdeck

Three takeaways that generalize to anyone building agent frameworks over a mixed-capability model fleet:

1. **Don't trust vendor specs for structured output.** Run your actual prompt on the model and look at what comes back at the failure boundary. We were two PRs into ADR 050 before we had the actual failing prompt in hand; in hindsight it should have been the first thing we ran.
2. **Compaction needs a schema, not a summarizer.** If you ship a catalog to the model and let it decide which tokens are load-bearing, the model will sometimes throw away the dispatch graph. Compaction inside a known schema lets you encode invariants the model can't choose to violate.
3. **Empty completions are a real failure mode.** They look like success at the HTTP layer (`200 OK`) but produce no usable output. Build for them — catch the empty response before it propagates and surface it as a typed error so downstream callers can retry, escalate, or degrade. We log the trim record on every call so operators can correlate "model returned empty" with "catalog was compacted to N% of original" in the audit trail.

If you've hit a related failure on a free or mid-tier model — empty completions, partial JSON, structured-output collapse on a long prompt — we'd love a reproduction PR with your prompt + model + observed bytes. The tier table is calibrated against what we've seen; it gets sharper the more failures we have data for.

## Read the design

- **ADR 050 — Retrieval-Augmented Tool Selection** (design doc): [PR #359](https://github.com/tosin2013/helmdeck/pull/359)
- **PR #1 — `internal/llmcontext` module + budgets + compaction**: [PR #360](https://github.com/tosin2013/helmdeck/pull/360)
- **ADR 049 — `helmdeck.plan` intent decomposer** (motivating context): [`docs/adrs/049-intent-decomposition.md`](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/049-intent-decomposition.md)
