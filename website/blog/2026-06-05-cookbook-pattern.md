---
slug: cookbook-recipes-beat-tutorials
title: Recipe-style docs are dramatically underused. Here's the case for them.
authors: [tosin]
tags: [contributor-experience, field-report, agent-architecture]
description: We shipped a cookbook of intent → prompt recipes alongside our reference docs. Within 48 hours it had eclipsed the prompt-templates page as the most-linked-to doc in our reference site. The pattern is simple, the per-recipe cost is ~15 minutes, and most projects don't do it.
image: /img/og/cookbook-recipes-beat-tutorials.png
date: 2026-06-05
draft: false
---

## Hook

Two PRs ago we shipped a cookbook page — ten worked recipes mapping common natural-language intents to the exact OpenClaw prompt that resolves them, plus the direct REST invocation underneath. It cost about two hours to write. Within 48 hours it had become the most-linked-to doc in our reference site. The pattern is simple. The per-recipe cost is ~15 minutes. **Most projects don't do this, and I think they're leaving real adoption on the table.**

## Context

The cookbook came out of an unexpected place. We'd just shipped a four-phase reliability arc for our AV-artifact packs and were testing it end-to-end against `openrouter/nvidia/nemotron-3-super-120b-a12b:free`, a free-tier 120B model. The planner — `helmdeck.plan`, which decomposes natural-language intents into multi-step pipeline JSON — failed 3 out of 6 times on the same intent class. We wrote that up as a [field report](/blog/validation-arc-caught-its-own-first-bug) and shipped a tier-aware prompt-template system to address the planning failure mode.

But somewhere in the testing we noticed a different problem. The 3/6 failures weren't just "model can't emit JSON." Some of them were *"model picked the wrong pack."* The catalog projection was being trimmed for Tier C; the model saw fewer options; the right pack for the intent was sometimes outside the projection. Operators reading the planner output couldn't always tell why their multi-step intent decomposed the way it did.

The real-user problem underneath the planner problem was a simpler one: **users don't know what to type.** They know what they want — narrated walkthrough video of a repo, fact-checked blog post from research, a structured comparison of two competitors — but they don't know which pack does that, and they don't know what natural-language phrasing reliably resolves through the planner to the right pack.

So we shipped a cookbook.

## Finding

The recipe shape is intentionally rigid. Every entry has the same four fields:

```markdown
### "I want a narrated walkthrough video of a GitHub repo"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Run the `builtin.repo-presentation` pipeline against `{{REPO_URL}}`* |
| **Direct invocation** | `helmdeck__pipelines-run` → `pipeline: builtin.repo-presentation`, `repo_url: ...` |
| **Outputs** | `video_artifact_key` (MP4) + `captions_artifact_key` (SRT) + `engagement_artifact_key` + `validation_artifact_key` |
| **Tip** | Pass `audience` and `angle` to shape the deck for promotion vs. educational vs. internal-demo tone. |
```

Four pieces of information, each load-bearing:

1. **The OpenClaw prompt** is the natural-language phrasing that reliably resolves through the planner. Empirically validated against `openrouter/auto`; works on Tier A models with high reliability.
2. **The direct invocation** is the deterministic path that skips the planner — useful for scripting, and useful as the *fallback* when the natural-language path fails on a small model.
3. **The outputs** tell the reader what fields will land in the run record. This is the part most docs systems get wrong — they describe the inputs in detail and the outputs as an afterthought.
4. **The Tip** is the non-obvious behavior. Defaults, when to prefer pipelines over packs, what `audience` actually does. The thing a user discovers on attempt three and wishes they'd known on attempt one.

Each entry is ~80 words. Most users read the prompt, copy the direct invocation, and skip the rest unless they hit friction. That's the design.

| Doc type | Time to write | Time to consume | Compounds over time? |
|---|---|---|---|
| Tutorial (e.g. "Build your first slides.narrate workflow") | ~3 hours | 15-30 minutes | Slowly; each tutorial is a snowflake |
| Reference page (e.g. PACKS.md row for slides.narrate) | ~1 hour | 1 minute lookup | Yes; reference compounds well |
| **Recipe (e.g. "I want a narrated walkthrough video")** | **~15 minutes** | **30 seconds** | **Yes; recipes compound the same way the reference does** |

The cookbook took ~2 hours for 10 entries because we already had the surface to draw from. New recipes against the same packs are now ~15 minutes each. The contributors who pick up new recipes — community members, internal engineers exploring a new pack — produce them at roughly the same rate.

## Why this matters to you

Three takeaways that survive outside this codebase.

**1. The "I don't know what to type" gap is bigger than most docs systems account for.** Tutorials assume the reader has 30 minutes and is following along sequentially. Reference assumes the reader knows what they're looking for. The recipe addresses the middle case — *"I know what I want, I don't know the exact phrasing your system will accept."* That's the most common state for a new user of an agent system. Closing that gap with a cookbook is cheap and the per-entry ROI is very high.

**2. Recipe-style docs reward composition.** Each recipe is small enough that a contributor can write one in their first session with the project. Each recipe stands alone, so partial coverage is still valuable (unlike a tutorial series where missing entry #3 breaks entries #4 through #7). The same recipe shape works across product categories — agent platforms, SaaS APIs, dev tools, infrastructure. The shape is more useful than the content.

**3. Recipes are honest about what your system can do.** A tutorial sells the happy path. A reference exhausts the input surface. A recipe says *"this exact phrasing reliably works against `openrouter/auto`; on Tier C free models you may get inconsistent results — see the model tier docs"* and links the reader to the reality. The cookbook's Tip blocks have been the most-clicked links in our site analytics. People want the non-obvious behavior, and the recipe shape gives you a natural place to put it.

## How to contribute a recipe

The cookbook is at [`docs/cookbook/intent-to-prompt.md`](https://github.com/tosin2013/helmdeck/blob/main/docs/cookbook/intent-to-prompt.md). The recipe shape is documented at the top of the file. To add one:

1. Pick an intent you've had that wasn't documented. Phrase it as a first-person quote — *"I want a podcast from a research topic"*, not *"how to use podcast.generate."*
2. Find the simplest direct invocation that satisfies it. Prefer pipelines over bare packs; pipelines bake in best practices the bare packs leave opt-in.
3. Test the natural-language phrasing through OpenClaw against `openrouter/auto`. If it doesn't resolve cleanly, either fix the phrasing or write a recipe for the simpler intent first.
4. Write the Tip block last. Include the non-obvious behavior that bit you on your way to figuring this out — defaults that matter, when to prefer one pack over another, what the output schema fields actually carry.
5. Open a PR. Recipe-only PRs are explicitly welcome — you don't need to be a maintainer or a regular contributor. See [CONTRIBUTING.md §"Other contribution types"](https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md).

If you're not sure whether your intent is cookbook-worthy: it almost certainly is. The cookbook's value compounds with cadence in exactly the way blogs do — each entry is a discoverable *"yes, you can do this"* that didn't exist before. There's no shortage of intents that aren't documented yet; the only constraint is contributor attention.

## See also

- [Cookbook — intent → prompt](/cookbook/intent-to-prompt) — the page this post is about
- [Prompt templates](/reference/prompt-templates) — the pack-first companion (this cookbook is the intent-first index over those templates)
- [Validation arc field report](/blog/validation-arc-caught-its-own-first-bug) — the testing window that surfaced "users don't know what to type" as the highest-leverage gap
- [Models reference](/reference/models) — when your model can't be trusted with the planner, the cookbook's direct-invocation field is the workaround
