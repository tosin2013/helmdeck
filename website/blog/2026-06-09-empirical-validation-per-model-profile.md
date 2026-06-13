---
slug: empirical-validation-per-model-profile
title: "Empirical validation: the audit-callback pattern fires (and the profile only gets you partway)"
authors: [tosin]
tags: [weak-models, agent-architecture, field-report, reproduction]
description: A profile-aware Tier C agent ran the audit-callback pattern end-to-end on openai/gpt-oss-120b:free — real artifacts, real verify_manifest with all_present:true. It also simplified the skill's 9-platform table to 2 variations. The library is a starting point, not a finished product.
image: /img/og/empirical-validation-per-model-profile.png
date: 2026-06-09
draft: false
---

## Hook

We ran the same prompt twice on `openai/gpt-oss-120b:free` — baseline agent with generic skill prose, then a custom agent shaped by a per-model prompting profile. The profile-aware agent deposited **2 real artifacts**, called **`artifact.verify_manifest`** with `all_present: true, 2 of 2 verified`, and hallucinated **zero** manifest entries. It also produced only **2** platform variations when the skill table listed 9. The library helps. It does not finish the job.

## Context

This is the third post in [a series](./plausibility-shaped-output) [that started](./the-audit-callback-pattern) with an honest reckoning: even after [three architectural fixes](https://github.com/tosin2013/helmdeck/pulls?q=is%3Apr+merged%3A2026-06-09) closed the most common Tier C failure modes (skill-prose ignored, required arg missing, multi-step chain hallucinated), the *underlying* problem — that small open-weight models behave very differently from frontier models on the same skill text — wasn't going to be fixed by more pack-layer work alone. The next thing to test was at the **input layer**: shape the prompt to match what the model actually responds to, per its training docs.

So we shipped the first entry in a model-profile library: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/experiment/gpt-oss-120b-prompting-profile/models/openai-gpt-oss-120b-free.yaml), sourced from [OpenAI's Harmony response-format docs](https://developers.openai.com/cookbook/articles/openai-harmony), [Together AI's GPT-OSS guide](https://docs.together.ai/docs/gpt-oss), and [IBM watsonx's GPT-OSS behavior guidelines](https://www.ibm.com/docs/en/watsonx/watson-orchestrate/base?topic=models-gpt-oss-model-behavior-instruction-guidelines). The profile encodes one specific prompting shape: **Objective → Source priority → Constraints → Output format → Success criteria.** Not "step 1, step 2, step 3."

Then we set up two OpenClaw agents pointed at the same skill, both on the same free model, differing only in their `AGENTS.md`. Baseline used the categorical four-modes-and-decision-rules prose we ship by default. Profile-aware used the Harmony-shaped success-criteria framing the YAML profile prescribes.

## Finding

Same prompt, same model, two agents. The trace counts say everything:

| Metric | Baseline agent (generic prose) | Profile-aware agent (Harmony-shaped) |
|---|---|---|
| `helmdeck.plan` calls | 1 | 1 |
| `pipeline-run` calls | 0 | **2** |
| Real blog artifacts in store | 0 | **2** |
| `artifact.verify_manifest` calls | 0 | **1** |
| `verify_manifest` result | n/a | **`all_present: true, 2 of 2 verified`** |
| Hallucinated manifest entries in chat | 6 (earlier session) or 0 (later, skipped manifest) | **0** |
| 6-section structured output | partial | **complete** |
| Platform variations actually produced | 4 in chat, 0 deposited | **2 deposited**, skill table listed ~9 |

This is the first time we've watched the **audit-callback pattern** ([PR #462](https://github.com/tosin2013/helmdeck/pull/462)) fire end-to-end from a real Tier C trace. The profile-aware agent called `pipeline-run` twice (one per source URL), polled `pack-status` until completion, listed the resulting artifacts, called `verify_manifest` with the actual keys, got `all_present: true` back, and only then composed its final response. The verification result landed in the model's context window before the text reply was written; the response honestly reports `verified: 2 of 2`.

We have the audit pattern. We have empirical proof it fires. **And we still got 2 platform variations instead of 9.**

The agent reasoned about the *objective* (artifacts in the store) and picked the most efficient path: one `pipeline-run` per source URL produces a finished blog artifact via the built-in `builtin.scrape-rewrite-blog` pipeline (which internally calls `blog.publish` to deposit). That's two real artifacts, both verified, both downloadable. Per the operator's USER.md the skill table called for ~9 platform-native variations. The agent chose 2.

This isn't a bug. It's [exactly the behavior the Together AI docs describe](https://docs.together.ai/docs/gpt-oss): GPT-OSS "performs best when given clear objectives while avoiding over-prompting or micromanaging the method." We gave it an objective; it picked a method we hadn't anticipated.

## The strategic truth this validates

The profile library is **necessary but not sufficient** for non-frontier models.

| Tier | What the profile does | What's left to the operator |
|---|---|---|
| Tier A (frontier) | Probably nothing — verify on your own model | Generic skill prose works out of the box (helmdeck assumption; please verify) |
| Tier B (mid-tier) | **Unknown — your experiment is the data we need** | Open research question |
| Tier C (free open-weight) | Raises floor of structural compliance — 6-section output, audit-callback fires | **Per-use-case customization** — the AGENTS.md success criteria must encode YOUR use case's specific commitments (N platforms, N deposits, N variations), because the model will optimize for the objective and may simplify when the criteria don't pin a specific N |

The profile gets you reliability of the *audit-callback shape*. It does not get you a specific *use-case implementation*. Operators adopting helmdeck on Tier C models will need to:

1. Use the model profile from `models/<provider>-<model>.yaml` as the starting point
2. Fork SOUL.md, USER.md, AGENTS.md for their specific operator persona
3. **Encode use-case-specific success criteria** that pin the exact commitments (N=9 platform variations, not "platform variations") so the model can't simplify them away
4. Run a verification trace on their own prompt before relying on the agent

The library is a starting point. Operators must finish the job.

## Why this matters to you

If you're shipping an agent on a free model, three principles fall out of today's work:

1. **Profile your model with its official docs.** Generic skill prose is wrong-fit for at least two of every three free models we've tested. Each model's training harness wants a specific prompting shape (Harmony-style for GPT-OSS, plain-English step-by-step for Llama, explicit ordered procedures for Nemotron). The first cuts of a per-model library now live in helmdeck's [`models/`](https://github.com/tosin2013/helmdeck/tree/main/models) directory, but the more useful artifact is the methodology: read the model's official docs, encode the prompting shape, and verify with an A/B trace.

2. **Make verification a typed tool call, not advisory prose.** The `artifact.verify_manifest` audit-callback pattern fired on Tier C only because the AGENTS.md success criteria framed it as a *definition of validity*, not as a separate "step 4b" advisory. Tier C ignores advisory prose; it executes objectives. Frame verification as part of the objective.

3. **Don't expect one skill to fit every use case.** The library is a starting point. Even with the profile applied, the model will simplify the skill's pluggable specifics (number of platforms, number of variations, number of deposits) toward its own efficient interpretation of the objective. If your use case has hard counts, pin them in the operator's AGENTS.md success criteria — not in skill prose, which the model treats as guidance rather than contract.

## Share your findings

Every operator running a custom Tier C agent is producing data the rest of the community needs. Three contribution paths:

- **Profile contribution**: if you customize a profile for a new model (or refine an existing one), open a PR to `models/<provider>-<model>.yaml` with your trace evidence in the `community_traces[]` field
- **Use-case contribution**: if you used an existing profile on a new use case (research summarizer, code reviewer, etc.) with different results, open an issue with the trace excerpt and comparison metrics
- **Failure-mode contribution**: if you hit a new failure mode (not skipped / hallucinated / simplified), file an issue tagged `field-report` with the trace data. We're building a vocabulary of Tier C failure modes; novel ones strengthen the whole community's understanding

See [`docs/howto/add-free-models.md`](/howto/add-free-models) for the detailed workflow.

## See also

- The PR that shipped the audit-callback pattern: [#462 — artifact.verify_manifest](https://github.com/tosin2013/helmdeck/pull/462)
- The model profile YAML: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml)
- Issue tracking the rest of the library: [#464](https://github.com/tosin2013/helmdeck/issues/464)
- Companion posts: [Plausibility-shaped output](./plausibility-shaped-output) (what motivated the audit pack) and [The audit-callback pattern](./the-audit-callback-pattern) (the architectural framing)
- How to add free models to your own agent: [`docs/howto/add-free-models.md`](/howto/add-free-models)
- How to A/B test a Tier B candidate: [`docs/howto/experiment-with-tier-b-models.md`](/howto/experiment-with-tier-b-models)
