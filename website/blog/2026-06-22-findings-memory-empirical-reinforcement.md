---
slug: findings-memory-empirical-reinforcement
title: "The agent that learns from its own lint errors"
authors: [tosin]
tags: [agent-architecture, weak-models, friction]
description: "Our authoring skill TELLS the LLM not to register manual __timelines or reference non-existent local files. The LLM did both anyway. Adding the same rule in the system prompt didn't help. What did help: showing the LLM, on every subsequent compose call, the exact codes its prior runs had produced — 'missing_local_asset (seen 2 times), gsap_studio_edit_blocked (seen 1 time)'. Concrete empirical reinforcement beat abstract rules. The architecture is small: every validation finding lands in per-caller memory; the compose pack reads + injects them as a system-prompt suffix on the next run. Zero token cost for clean callers; auto-tunes per agent."
image: /img/social-card.png
date: 2026-06-22
draft: true
---

## Hook

We shipped a "hyperframes authoring" skill that told the LLM, in 13 KiB of in-context rules, not to manually register `window.__timelines["x"] = tl` and not to reference local files like `<img src="google-logo.svg">` that don't exist in the project. The LLM did both anyway. The first empirical run of the validation suite (`hyperframes.lint`) produced three errors: `missing_local_asset` for two hallucinated logo files, `gsap_studio_edit_blocked` for the manual timeline registration, and `timeline_track_too_dense` because the LLM put six elements on a single track. Each of those is **explicitly forbidden by name** in the skill.

The fix that worked wasn't a stricter skill or a better validator. It was telling the LLM, on its NEXT compose call, exactly what codes its prior runs had produced — `"missing_local_asset (seen 2 times, severity=error)"`. The empirical signal closed the gap that the abstract rule didn't.

## Context

Helmdeck's hyperframes pipeline produces narrated MP4s through an LLM-authored HTML/CSS/JS composition step (`hyperframes.compose`) → a pre-render validation suite (`hyperframes.lint` → `inspect` → `validate`) → a Chromium-based render (`hyperframes.render`). The validation suite — three packs that wrap upstream's own `hyperframes lint`, `hyperframes inspect`, and `hyperframes validate` — was designed as a publish gate: catch render-killing issues before burning ~5 minutes of headless-Chrome render budget.

That gate worked. The first end-to-end run of the bring-your-own-audio pipeline (operator uploads MP3 → LLM authors composition timed to it → render) hit lint with three findings:

```
✗ missing_local_asset (error)
  <img> element references local file(s) not found in the project:
  google-logo.svg, antigravity-logo.svg. The renderer will silently
  skip these and produce a video with missing visuals.

⚠ gsap_studio_edit_blocked (warning)
  GSAP tweens target "#title .content", ..., "#bg" in a registered
  timeline. The hyperframes runtime registers timelines automatically.
  Do not add a manual window.__timelines script.

⚠ timeline_track_too_dense (warning)
  Track 1 has 6 timed elements in this HTML file.
```

Each of these violations is documented in the in-context skill the agent loads at conversation start. The agent had the rules. The agent ignored them.

## Finding

The gap isn't in the validator (it caught the issues correctly), and it isn't in the skill (the rules are present and explicit). The gap is **between abstract instruction and concrete behavior** for a Tier C model. The LLM reads "don't reference local image files unless they exist" and proceeds to write `<img src="google-logo.svg">` because its training corpus is full of HTML with logo references, and the abstract constraint doesn't compete with that prior.

What we shipped: **findings-memory**. Three small architectural pieces:

1. **Every pack audit row now carries structured findings.** The engine extracts `{code, severity, file}` triples from the pack's output JSON, recognizing the lint/inspect/validate output shapes plus a generic top-level `findings: []` array. Capped at 50 per row, verbose `message` and `fix_hint` left in the sidecar artifact. The audit row stays bounded; the codes go in.

2. **`BuildDefaults` aggregates findings by code across the caller's history.** Same projection that already surfaces "most-used inputs" — group by `code`, count occurrences, track `last_seen`. Sorted busiest-first, capped at top-20. The result is the per-caller "common findings" projection: a frequency-ranked list of every concrete antipattern the agent has actually produced.

3. **The compose pack injects the top-10 findings into its system prompt on every run.**

```
FINDINGS FROM YOUR PRIOR RUNS — concrete antipatterns you have
produced before. ...

- missing_local_asset (seen 2 time(s), severity=error)
- gsap_studio_edit_blocked (seen 1 time(s), severity=warning)
- timeline_track_too_dense (seen 1 time(s), severity=warning)

Do not produce HTML, CSS, or JS that would trigger any of the
codes above.
```

Empty findings → empty prefix → zero token cost. A new caller sees no overhead. An experienced caller sees exactly the rules it has personally violated. The architecture auto-tunes per agent.

## Why this matters to you

Three takeaways generalize beyond hyperframes compositions.

**First, the empirical signal does the heavy lifting for weak models.** A capable closed model (Claude, GPT-4) reads `"don't reference local files"` and mostly complies — the abstract instruction is enough. A weak model on the order of `gpt-oss-120b:free` reads the same instruction and ignores it under the weight of its training prior. What changes the behavior is the change in framing: `"don't do X"` becomes `"you did X 3 times — really stop"`. The second sentence cites concrete evidence the model can ground itself against; the first is a rule the model can rationalize past. If you're building agents on weak open-weight models, your prompt should not just contain rules — it should contain **rules + their personal violation count**.

**Second, the loop closes at the prompt layer, not at fine-tune time.** This is the design choice the architecture rests on. We could have collected lint findings into a dataset and fine-tuned the open-weight model on examples that avoid those patterns. That works, but it's slow (training cycle), per-deployment (the model serves many callers), and lossy (a fine-tune averages across all callers' findings). The findings-memory approach lives entirely at inference time: the audit log already exists for other purposes; the projection runs in milliseconds; the prompt injection is a string concat. It learns from a single failed run, scopes per caller, costs ~300 tokens, and adapts as the model's failure modes shift. No retraining required.

**Third, the contract you write for a validator should also be the contract you teach the LLM against.** Our validation suite's rule codes (`missing_local_asset`, `gsap_studio_edit_blocked`, `text_box_overflow`) are now load-bearing in two places: as validation gates that block the render, AND as in-prompt feedback that prevents the same code from firing on the next run. The codes themselves are the contract. This is true any time you have an automated checker upstream of a generator: lint your code, audit your data, validate your output — and feed the violation codes back into the generator's prompt so the next generation has empirical reinforcement of what to avoid.

The thing not to do: ship a validator + a documentation page describing what it catches, and call it done. The validator catches; the documentation describes; but the LLM still needs to be TOLD what it personally has produced — that's the gap findings-memory closes.

## See also

- The pre-render validation suite this builds on: [`hyperframes.lint`](/docs/reference/packs/hyperframes/lint), [`hyperframes.inspect`](/docs/reference/packs/hyperframes/inspect), [`hyperframes.validate`](/docs/reference/packs/hyperframes/validate)
- The render-deterministic authoring rules ([`docs/explanation/authoring-render-deterministic-compositions`](/docs/explanation/authoring-render-deterministic-compositions)) — what the agent is supposed to know already; findings-memory is what makes the agent personally accountable for it
- The architecture issue: [#570](https://github.com/tosin2013/helmdeck/issues/570) (full design + slice breakdown)
- The motivating empirical sidecar: `hyperframes.lint/290e8344a4bf2934-lint.json` (3 findings from the 2026-06-22 BYO test cycle)
- ADR 047 (Universal Memory + per-caller scoping) — the substrate findings-memory writes to
- The empirical-iteration discipline that produced this: blog [Render ≠ preview: what we learned shipping a hyperframes integration](/blog/child-composition-slot-lifetime)
