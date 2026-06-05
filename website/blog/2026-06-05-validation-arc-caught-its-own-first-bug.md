---
slug: validation-arc-caught-its-own-first-bug
title: We shipped a 4-phase reliability arc. The first bug it caught was itself.
authors: [tosin]
tags: [friction, agent-architecture, weak-models]
description: A four-phase validation arc shipped across PRs #428–#433. The first time we ran it production-shaped, it caught a Dockerfile/runtime image mismatch that had been silently masking changes for months. Plus what a 120B free-tier model did to our planner.
image: /img/social-card.png
date: 2026-06-05
draft: true
---

## Hook

We shipped a four-phase validation arc for the AV-artifact packs in helmdeck — script, pack, default-on integration, ADR. The first time we triggered it in production-shaped use, the validation post-step couldn't find its own script. The Phase 3 soft-surface contract caught it, logged a clean warning, and shipped the artifact anyway. The bug was a compose-overlay regression that had been silently masking sidecar Dockerfile changes for months. **The arc demonstrated its load-bearing value by catching its own deployment bug — in the first run, in ~200 tokens, without blocking the artifact.**

## Context

The arc started with a real cost number. Every "the video has issues" diagnostic — the kind that happens when an operator reports a slides.narrate MP4 looks wrong — was costing ~3,000 LLM tokens of bash output, manual `ffprobe` analysis, and synthesis. We ran one such investigation on `slides.narrate/888de7b23142ba81-video.mp4` and discovered a 27.9-second audio/video duration mismatch that was eminently expressible as a JSON field on the producing pack's output. That investigation is captured in issue [#429](https://github.com/tosin2013/helmdeck/issues/429).

What followed was a four-phase arc, each phase provable against real artifacts before the next phase was built:

- **Phase 1 — [PR #428](https://github.com/tosin2013/helmdeck/pull/428):** `scripts/av-validate.sh`, a standalone bash + python3 + ffprobe + libavfilter validator. The executable spec. 13 checks across container/audio/video/SRT modalities with a `pass`/`warn`/`fail` severity model where `fail` is reserved for checks that match a shipped bug fix.
- **Phase 2 — [PR #430](https://github.com/tosin2013/helmdeck/pull/430):** `av.validate` pack — a thin handler that invokes the script and returns the structured report. Strict-mode opt-in for CI gates; soft-surface by default.
- **Phase 3 — [PR #432](https://github.com/tosin2013/helmdeck/pull/432):** default-on integration as a post-step on `slides.narrate` and `podcast.generate`. Every successful run now embeds the structured `validation` field in its output.
- **Phase 4 — [PR #433](https://github.com/tosin2013/helmdeck/pull/433) + [ADR 052](/adrs/052-av-output-validation-post-step):** the architecture record, plus focused amendments to [ADRs 008](/adrs/008-typed-error-codes-for-weak-model-reliability) / [015](/adrs/015-pack-slides-video) / [045](/adrs/045-pack-resource-sizing) / [051](/adrs/051-failure-mode-aware-dispatch).

We also shipped the apad fix for #429 itself ([PR #431](https://github.com/tosin2013/helmdeck/pull/431)) with same-PR coupling: the fix removed the demotion entry, the check returned to its natural `fail` severity, and the regression guard travelled with the upstream fix.

Then we tried the whole thing on a real repo.

## Finding 1 — the validation arc caught its own deployment bug

The plan: trigger `builtin.repo-presentation` against `https://github.com/tosin2013/helmdeck` from OpenClaw. The pipeline's terminal step is `slides.narrate`, which now embeds the `validation` field. The expected result was a `validation.checks[]` with `consistency:audio_video_duration: pass: true, severity: fail` proving the apad fix landed end-to-end against a real artifact.

What landed in the log instead:

```text
WARN  av.validate run failed; output ships without validation field
      pack: slides.narrate
      err:  handler_failed: parse av-validate.sh JSON:
            invalid character 'O' looking for beginning of value
            (stdout="OCI runtime exec failed:
                     stat /usr/local/bin/av-validate.sh:
                     no such file or directory")
```

The MP4 artifact still shipped. The pack returned success. The pipeline didn't break. But the validation report wasn't in the output — the soft-surface contract had fired exactly as designed by [ADR 052](/adrs/052-av-output-validation-post-step).

Root cause took ~200 tokens to identify because the log line was structured. The compose build overlay (`deploy/compose/compose.build.yaml`) only declared a `build:` directive for `control-plane`. The `sidecar-warm` service in the base `compose.yaml` ran:

```bash
docker pull ghcr.io/tosin2013/helmdeck-sidecar:${HELMDECK_VERSION:-latest}
```

at every `compose up`, populating the local Docker cache with the GHCR-published image (built from the last *release*, not the current source). The session runtime then defaulted to that same `:latest` tag. Net effect: `control-plane` source changes landed instantly during dev iteration, but `sidecar.Dockerfile` changes only took effect after a release to GHCR — which meant the [PR #430](https://github.com/tosin2013/helmdeck/pull/430) `COPY scripts/av-validate.sh /usr/local/bin/av-validate.sh` directive was in the Dockerfile, baked into our local `helmdeck-sidecar:dev` image, and **invisible to the running stack**. The bug had been silently masking sidecar Dockerfile changes since the overlay shipped in [PR #134](https://github.com/tosin2013/helmdeck/pull/134).

The fix ([PR #434](https://github.com/tosin2013/helmdeck/pull/434)) was 47 lines of compose YAML. Two complementary overrides: `HELMDECK_SIDECAR_IMAGE` on the control-plane pointed at a local tag, and `sidecar-warm` got repurposed to BUILD that tag instead of PULL. The runtime override mechanism (`HELMDECK_SIDECAR_IMAGE`) had been in the code at `internal/session/docker/runtime.go:40-47` the whole time; it was the compose-level wiring that was missing.

| Diagnostic on this class of bug | Cost |
|---|---|
| Manual: `docker exec` + `docker image inspect` + `compose config` archaeology | ~3,000 tokens, 20–40 minutes |
| Via the structured `validation` field + control-plane WARN log | **~200 tokens, 3 minutes** |

## Finding 2 — what a 120B free-tier model did to our planner

While testing, we ran the planning step on `openrouter/nvidia/nemotron-3-super-120b-a12b:free`. Six calls in five minutes against the same intent class ("create a narrated presentation about this repo"):

```text
14:41:03  stop    1535 tokens   743 chars   90s   ✓  (clean stop)
14:39:33  length   600 tokens  2627 chars   15s   ✗  (truncated mid-JSON)
14:39:17  stop     710 tokens   791 chars   29s   ✓
14:38:49  stop     423 tokens    71 chars   15s   ✗  (near-empty after reasoning leak)
14:38:34  stop    1547 tokens   685 chars   95s   ✓
14:36:59  length   600 tokens  2549 chars   34s   ✗  (truncated again)

Effective success rate: 3/6 — 50%
Average successful latency: 71 seconds
```

Two failure modes, both textbook: `finish_reason: length` hit at the 600-token output cap, and "reasoning leak" — the canonical 423-token-completion / 71-char-visible pattern that TokenMix [^1] measures at 40% on DeepSeek R1 with `max_tokens=200`.

The same intent class on `openrouter/auto` worked cleanly: 2 calls, 2 stops, 15–34s latency, 776–1782 completion tokens. Same prompt. Same catalog. Different model class. **The architectural finding isn't that Nemotron is bad. It's that Nemotron's failure profile is the wrong tool for the *output shape* of a multi-step plan, and our planner has one prompt template for every tier.**

Inside `helmdeck.plan`, the catalog projection is already tier-aware (Tier C gets the aggressive trim per [ADR 050](/adrs/050-retrieval-augmented-tool-selection)). The output token budget is tier-aware (600 tokens for Tier C). Strict JSON mode is gated on tier ([ADR 051 PR #3](/adrs/051-failure-mode-aware-dispatch)). Prefix-cache routing is gated on tier ([ADR 051 PR #4](/adrs/051-failure-mode-aware-dispatch)). **The prompt template itself is not.**

Portkey ships this as a first-class feature in their "Smart Fallback with Model-Optimized Prompts" [^2] — different `prompt_id` per entry in a fallback `targets` array. DSPy goes further: it compiles a different prompt per LM from one signature [^3]. The research that fed our cost-savings thesis (BFCL multi-turn collapse — xLAM-2-1B at 8.38% multi-turn vs 53.97% overall [^4]; PLAN-TUNING [^5]; the "small models benefit from decomposed planning" Pre-Act result [^6]) all converges on the same point: small models can't reliably emit multi-step plans in one shot, but they can reliably make one pack-pick decision per turn.

The next architectural move, captured as a planned follow-up, is two prompt strategies inside `helmdeck.plan`:

- **`full_steps`** for Tier A — emits the full pipeline JSON in one shot (today's behavior).
- **`single_pick`** for Tier C — picks the single most-relevant pack with a short reason string; the agent runs steps sequentially.

The selection lives in the `Budget` entry per model in `internal/llmcontext/budgets.go`. Same code path as the existing tier-aware projection knobs. ~80 LOC + the new template.

## Why this matters to you

Two takeaways that survive outside this codebase.

**1. Soft-surface failure makes structured signal possible.** The validation arc shipped with explicit posture: failed checks land in the output as data, not as a runtime error. That posture is what let the missing-script bug surface as a *structured warning in the log* instead of a pipeline failure. If we'd shipped strict-mode-by-default, the first run would have been a red CI failure, and we'd have spent the same 20 minutes on it. Soft-surface didn't hide the bug — it surfaced it in a shape the agent could read in 200 tokens. **Design your failure modes for the diagnostic loop, not just for the success path.**

**2. Model size is the wrong primitive. Output shape is the right one.** A 120B free-tier model that can't reliably emit 1,500 tokens of nested JSON isn't a "bad model" — it's a model whose effective output shape doesn't match the task. The Portkey / DSPy / Pre-Act result is real: small models can make one decision well, but multi-step decomposition in one shot is past their reliable output budget. If you're building agent systems against mixed-tier model pools, **route by output shape, not by parameter count.** The `single_pick` strategy isn't a workaround for weak models — it's a more honest interface to what those models can actually do.

The deeper move is to make the planner *itself* tier-aware about its own output. We did that for the catalog (smaller catalog for smaller models) and the budget (smaller budget for smaller models). The prompt template is the last knob, and it's the one that closes the loop on the Nemotron-class observation. That PR is the natural next ship.

The PRs are linked above. The cookbook of intent → prompt recipes that helps users skip the planner entirely shipped alongside the docs refresh in [PR #435](https://github.com/tosin2013/helmdeck/pull/435).

## See also

- The full validation arc: PRs [#428](https://github.com/tosin2013/helmdeck/pull/428), [#430](https://github.com/tosin2013/helmdeck/pull/430), [#431](https://github.com/tosin2013/helmdeck/pull/431), [#432](https://github.com/tosin2013/helmdeck/pull/432), [#433](https://github.com/tosin2013/helmdeck/pull/433)
- The deployment-bug fix the arc caught: [PR #434](https://github.com/tosin2013/helmdeck/pull/434)
- Architecture: [ADR 052 — AV output validation as a default-on post-step](/adrs/052-av-output-validation-post-step)
- The tier model: [ADR 051 — failure-mode-aware dispatch for mixed-tier deployments](/adrs/051-failure-mode-aware-dispatch)
- Cookbook: [Intent → prompt](/cookbook/intent-to-prompt) — recipes that skip the planner when your model can't be trusted with one
- Reference: [Why helmdeck](/explanation/why-helmdeck) — token-cost comparisons, validation arc as worked example

## References

[^1]: TokenMix. *Thinking Tokens Billing Trap (2026)*. <https://tokenmix.ai/blog/thinking-tokens-billing-trap-2026>. Measured 40% empty-response rate on DeepSeek R1 with `max_tokens=200`.

[^2]: Portkey. *Smart Fallback with Model-Optimized Prompts*. <https://portkey.ai/docs/guides/use-cases/smart-fallback-with-model-optimized-prompts>. First-class fallback API with per-model `prompt_id` binding.

[^3]: DSPy. *Signatures and Optimizers*. <https://dspy.ai/learn/programming/signatures/>. Compiles a different prompt per LM from a single signature.

[^4]: TinyLLM. *Small Language Models for Agentic Systems* (arXiv 2511.22138). <https://arxiv.org/abs/2511.22138>. xLAM-2-1B = 53.97% BFCL overall, 8.38% multi-turn; Qwen3-1.7B = 55.49% overall, 16.88% multi-turn.

[^5]: Liu et al. *PLAN-TUNING: Post-Training Language Models to Learn Step-by-Step Planning* (arXiv 2507.07495). <https://arxiv.org/pdf/2507.07495>.

[^6]: Sharma et al. *Pre-Act: Multi-Step Planning and Reasoning Improves Acting in LLM Agents* (arXiv 2505.09970). <https://arxiv.org/pdf/2505.09970>.
