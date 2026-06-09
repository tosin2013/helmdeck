---
slug: tier-a-empirical-baseline
title: "Tier A is structurally better. The deposit-step failure is universal."
authors: [tosin]
tags: [agent-architecture, weak-models, field-report, reproduction]
description: We ran the same prompt on Claude Sonnet 4.6 that we ran on gpt-oss-120b:free. Tier A handles parallel tool use, 8-platform fanout, the InfoQ 6-criterion fit check, and the "one clarifying question" rule. It also skips the mandatory artifact.put step the same way Tier C does. The deposit-step failure is tier-invariant.
image: /img/social-card.png
date: 2026-06-09
draft: true
---

## Hook

`anthropic/claude-sonnet-4.6` ran 8 real `blog.rewrite_for_audience` calls in parallel, executed a full 6-criterion InfoQ fit check with per-criterion grades, stated a 5-step execution plan upfront, asked exactly one clarifying question per the AGENTS.md rule, and produced zero hallucinated manifest entries. Then it skipped the mandatory `artifact.put` deposit step entirely — same as both Tier C variants. The deposit-step skipping is **tier-invariant**, not a Tier C failure mode we can patch with a per-model profile.

## Context

The 2026-06-09 morning's [three architectural fixes](https://github.com/tosin2013/helmdeck/pulls?q=is%3Apr+merged%3A2026-06-09) + [the audit-callback pattern](./the-audit-callback-pattern) + [the per-model profile library](./empirical-validation-per-model-profile) all targeted Tier C reliability. We assumed Tier A "works out of the box" because frontier models handle generic skill prose. We never empirically tested it.

[Issue #466](https://github.com/tosin2013/helmdeck/issues/466) tracked the gap. This post closes it.

The methodology: take the existing `tech-blog-publisher` agent (already on `openrouter/auto`, which routes to Tier A models), run the same mcp-adr-analysis-server prompt we used on Tier C all day, and watch the trace. Same skill prose. Same workspace files (SOUL / IDENTITY / USER / AGENTS already layered per [OpenClaw's canonical model](https://docs.openclaw.ai/concepts/agent-workspace)). No per-model profile injected. Tier A or it isn't.

The router picked `anthropic/claude-sonnet-4.6` for this run.

## Finding

The trace produced two distinct results — one that supports the "Tier A is better at structural compliance" claim, and one that doesn't.

### What Tier A handled that Tier C didn't

| Behavior | Tier C baseline | Tier C w/ profile | Tier A (Sonnet 4.6) |
|---|---|---|---|
| Parallel tool use at startup | ✗ | ✗ | **✓ 3 simultaneous** (read SKILL.md + 2 web-scrapes) |
| Real `blog.rewrite_for_audience` calls | 4 in chat | 0 (used `pipeline-run`) | **✓ 8** (matched the skill table) |
| InfoQ 6-criterion fit check | skipped | skipped | **✓ per-criterion grades, "Possible fit" verdict** |
| Multi-step plan acknowledged | partial | partial | **✓ 5-step plan stated upfront** |
| "Ask at most ONE clarifying question" | ✗ (hedged with "let me know") | ✗ | **✓ one specific question + stated default** |

Every structural row swung Tier A's way. The model honored the SKILL.md's required structure end to end. The InfoQ fit check is particularly notable — Tier C agents on the same prompt have either skipped it entirely or produced a vague "Possible fit" without specifics. Tier A returned a full 6-row grade table with concrete gaps to close before submission.

The "one clarifying question" rule is the cleanest signal of skill obedience. Tier C agents either hedge ("let me know how you'd like to proceed") or skip the question and improvise. Tier A asked one question, gave a sharp default, and committed to executing the default if the operator stayed silent. That's exactly the SOUL.md voice.

### What Tier A *also* didn't handle

| Mandatory rule from SKILL.md | Tier C baseline | Tier C w/ profile | Tier A (Sonnet 4.6) |
|---|---|---|---|
| `artifact.put` after each variation | **✗** 0 calls | **✗** 0 calls (used auto-deposit) | **✗** 0 calls |
| `artifact.verify_manifest` after manifest | **✗** 0 calls | **✓** 1 call (`all_present: true`) | **✗** 0 calls |
| New artifacts in store from session | 0 | 2 (via pipeline auto-deposit) | **0** |

Tier A's text at the moment of truth (17:08:32 in the trace):

> *"Now appending CTAs and depositing to artifacts — all in parallel."*

Its actual parallel tool calls were 8 invocations of `blog.append_cta` (a CTA-appender that returns markdown, not a deposit). **The model conflated "append CTA" with "deposit to artifacts."** Even when those 8 calls all failed (the cause was an [unrelated pack-contract gap](https://github.com/tosin2013/helmdeck/pull/468)), the agent didn't pivot to call `artifact.put` directly. The mandatory deposit step was never executed.

Reading the agent's text reveals the misunderstanding: it treated the entire workflow as "rewrite → append CTA → done," with "depositing" living somewhere inside the pack pipeline rather than as an explicit step the agent must invoke. The SKILL.md says §4 is "MANDATORY, NOT ADVISORY" with the exact tool name `helmdeck__artifact-put`. Tier A ignored it.

## Naming the pattern

This is **tier-invariant deposit-step skipping**: the agent reads the mandatory-deposit rule, acknowledges in text that it's depositing, but never invokes the actual `artifact.put` tool. It's distinct from the [plausibility-shaped output](./plausibility-shaped-output) we documented earlier — Tier C *fabricated* a manifest; Tier A *truthfully says* it's depositing but doesn't.

Both failure modes have the same root cause: skill prose alone is insufficient to drive a typed tool call. Mandatory-by-prose is treated as advisory by every model tier we've tested.

The implication is uncomfortable: **the layered architectural work isn't done.** [PR #450](https://github.com/tosin2013/helmdeck/pull/450) (typed deposit), [PR #462](https://github.com/tosin2013/helmdeck/pull/462) (audit callback), and the [per-model profile library](./empirical-validation-per-model-profile) all assume the agent will call the typed pack when the skill says to. Today's data says: it won't, regardless of tier.

## What this changes architecturally

[Phase 3 of issue #461](https://github.com/tosin2013/helmdeck/issues/461) — engine-level post-call hook that fires the registered auditor *without* skill-prose dependency — was originally framed as "deferred until Phase 1 + 2 prove the pattern is generally useful." Today's trace flips that justification: the pattern is necessary *because* skill prose can't carry the mandatory-call weight on any tier, not just Tier C.

The architectural shape that closes this loop:

1. **Producer pack registers a paired auditor** (e.g., `blog.publish` → `blog.verify-published`)
2. **Engine intercepts the producer's completion** and auto-invokes the auditor with the producer's output
3. **Auditor result is attached to the producer's response envelope** — the LLM sees both in its next-turn context
4. **No skill-prose dependency** — the agent doesn't need to remember to call the auditor, because the engine fired it

This removes "the agent will read the skill and call the verify pack" from the trust chain. It's the same architectural shape as [ADR 052](/adrs/av-output-validation-post-step)'s av-validate post-step, applied at the artifact-deposit layer instead of the video-encoding layer.

## Why this matters to you

If you're building an agent on any tier, three principles fall out of today's three-trace comparison:

1. **Don't ship "MANDATORY, NOT ADVISORY" skill prose and expect it to work.** Every tier treats prose mandates as advisory. Architectural enforcement is the only durable answer.

2. **Tier A is better at structural compliance, not at typed-tool dispatch.** Frontier models handle 8-step chains, parallel tool use, structured output, and clarifying-question discipline beautifully. They still skip explicit deposit calls if the skill describes "deposit" as part of a chained workflow without making the tool call the explicit terminal step.

3. **Engine-level post-call hooks are the answer.** Pack the producer + auditor pair into the engine's contract so the agent can't choose to skip the audit. Both PR #462's pattern and the planned Phase 3 generalize across producer/auditor pairs.

## See also

- The issue tracking this experiment: [#466](https://github.com/tosin2013/helmdeck/issues/466)
- Phase 3 of the audit-callback pattern (engine-level hook — strengthened by today's evidence): [#461](https://github.com/tosin2013/helmdeck/issues/461)
- The PR fixing the `blog.append_cta` rejection: [#468](https://github.com/tosin2013/helmdeck/pull/468)
- The companion posts that motivated this experiment: [Plausibility-shaped output](./plausibility-shaped-output), [The audit-callback pattern](./the-audit-callback-pattern), [Empirical validation per-model profile](./empirical-validation-per-model-profile)
- The model docs revised with this finding: [`docs/reference/models.md`](/reference/models)
