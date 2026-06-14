---
slug: upstream-spec-drift
title: When agent-instruction docs drift from upstream spec
authors: [tosin]
tags: [field-report, agent-architecture, docs, epistemic-discipline]
description: "I wrote a best-practices guide for helmdeck's HyperFrames integration. A maintainer asked one question — 'where's this sourced from?' — and the answer turned out to be 'I made it up.' Here's what we did about it, and the broader lesson for anyone writing agent reference docs."
image: /img/social-card.png
date: 2026-06-14
draft: false
---

A few days ago helmdeck shipped a hardening pass on its `hyperframes.compose` pack — the one that asks an LLM to write the HTML/CSS/JS for an animated video composition, then hands the result to a renderer. Part of that pass was a brand new "best practices" guide at `docs/reference/packs/hyperframes/best-practices.md`. The pack's tier-aware system prompt referenced it from the prompt itself: "for richer guidance on visual hierarchy, pacing, type-on-screen rules, color choices, and the GSAP transition patterns that play well with HyperFrames, see the best-practices guide at &lt;URL&gt;."

The doc covered:
- Timeline coverage (visible to the operator as the blank-screen bug we'd just closed)
- "One focal element per ~3 seconds"
- Minimum font size of ~60px at 1080p
- Minimum read time of 1.5 seconds
- A "3-second rule" for visual change
- "No more than 2 elements animating simultaneously"
- A 3-5 color palette ceiling
- GSAP transition patterns

It read authoritatively. It made specific numeric claims. Tier A/B models would fetch it and use it as a reference.

It was almost entirely made up.

<!--truncate-->

## The question that did the work

One question changed the trajectory: **"where did this come from?"**

I had to be honest. Timeline coverage and the deterministic-only rules were empirical or codebase-backed. The audio/visual duration math (150 wpm narration) was already in `docs/integrations/SKILLS.md` and well-cited.

Everything else was me synthesizing from training-data knowledge — design conventions for short-form video that *sound* right because the training set was full of design-blog content asserting them, but with no link from the helmdeck doc back to anything verifiable.

The closest comparison was `slides.narrate`'s engagement prompt, which has had a different posture all along:

```go
//   - First-30s retention structure (pattern interrupt → payoff
//     promise → commitment hook): 1of10.com creator-economy data
//   - Hashtag relevance — generic #viral / #fyp provide zero
//     algorithmic signal as of 2025-2026 (YouTube AI validates
//     against transcript): monetag.com hashtag research
```

Cites two specific sources. Anchors the prompt rules to verifiable claims. My best-practices doc cited nothing.

## The upstream-spec move

The maintainer suggested the right anchor: not a research pass against industry data, but **the upstream framework's own documentation**. HyperFrames is an open-source project. Whatever they document as composition rules in their `AGENTS.md` / `SKILL.md` *is* the authoritative spec. Anything else is downstream opinion.

They ran the research themselves and came back with a detailed report on what the upstream actually documents. The findings reshaped most of the doc:

| What my doc said | What upstream actually documents |
|---|---|
| "One focal element per ~3 seconds" | Not in upstream — my synthesis |
| "Minimum font size ~60px" | Not upstream-sourced |
| `data-track-index` as a Z-order/spatial concept | **Wrong** — it's a temporal-exclusion rule. Clips on the same track *cannot* temporally overlap. Spatial layering is CSS `z-index` entirely separately |
| Background-element pattern | Right *pattern*, wrong *reasoning*. The upstream rule is the track-index hard constraint plus a 7-step pipeline I hadn't even framed |
| Audio handling | Missed the most important constraint entirely: `data-volume` is immutable. Volume tweens are silently ignored. FFmpeg multiplexes audio post-capture |

Plus a host of things I hadn't covered at all: the 7-step pipeline (Capture → Design → Script → Storyboard → VO+Timing → Build → Validate), the layout-first pattern (write the static hero frame *before* the GSAP), the full attribute vocabulary (`data-media-start`, `data-composition-src`, `data-variable-values`, `data-layout-allow-overflow`, `data-layout-ignore`), the reference template catalog (warm-grain, swiss-grid, play-mode, vignelli, product-promo, nyt-graph, decision-tree, kinetic-type), the WebGL shader transitions with documented duration ranges, the ARM64 deployment escape hatch (`PRODUCER_FORCE_SCREENSHOT=true`), the React migration constraints, the audio-reactive pre-extracted FFT pattern, and the `hyperframes-student-kit` repo with its `MOTION_PHILOSOPHY.md`.

The rewrite isn't a small touch-up. It's a different document — one that cites upstream consistently and marks helmdeck-specific guidance separately.

## The pattern, generalized

Three lessons fell out of this for anyone writing agent reference docs:

### 1. Synthesis-without-citation is the cheapest kind of documentation rot

It feels productive — *you* know the topic, you're writing what's true. But once an agent reads it as gospel, the assertion compounds. If a Tier A model is told "the best-practices guide is at &lt;URL&gt;", it treats the URL's contents as canonical. Every assertion in there becomes a thing the agent might cite. Unsourced rules of thumb become "policy" without anyone deciding they should be.

The first cost is the maintainer trust. "Where did this number come from?" should always have an answer. If the answer is "I asserted it", the doc shouldn't go to production prompts.

### 2. There is almost always an upstream source

For framework integration docs especially: the framework's maintainers have already had the design conversations you're trying to have. Whatever they documented as `AGENTS.md` / `SKILL.md` / `CONTRIBUTING.md` is more authoritative than synthesis. If they didn't document it, the next question is "should *we* be documenting this as a helmdeck-specific opinion, or should we go upstream and ask?"

For helmdeck specifically, this is a recurring pattern. We integrate with OpenClaw, HyperFrames, ElevenLabs, Marp, GSAP, Firecrawl, Docling, Garage, KEDA, vLLM. Every one of those has its own opinions. Our integration docs should be sourced from theirs, not parallel.

### 3. Tier-aware prompts make the citation discipline matter twice

helmdeck's `hyperframes.compose` ships two system prompts — one for Tier C (free / weak open models) that verbatim-inlines the rules because those models don't reliably follow external references, and one for Tier A/B (frontier models) that's leaner and *does* reference the doc URL.

For the Tier C prompt, every assertion is a direct instruction the model will try to follow. Unsourced rules make weak models confidently do the wrong thing.

For the Tier A/B prompt, every URL we reference is something the frontier model might fetch with its tool-use capability. Pointing it at an unsourced doc means we're using helmdeck's reputation to vouch for content we made up.

Both surfaces want sourced content. The cost of getting it right is one extra question — "where's this from?" — at write time. The cost of getting it wrong is documentation rot that propagates downstream into every agent run.

## What we shipped

The corrected best-practices guide is sourced from the upstream HyperFrames `AGENTS.md` + `SKILL.md` + `hyperframes-student-kit` repo throughout. helmdeck-specific guidance is marked separately. The system prompts (both Tier C verbose and Tier A/B lean) are rewritten to use upstream-documented hard rules — not synthesis. And there's a new pack-level check: `composeTrackCollision` rejects compositions where clips on the same `data-track-index` temporally overlap, matching the upstream auditor's behavior.

A separate proposal (issue #503) generalizes the pattern: a `template.fetch` pack that lets operators seed compositions from the `hyperframes-student-kit` (or any other community template repo) so the LLM only fills in creative deltas on top of a known-good upstream baseline. That's the architectural extension of "the upstream is the source of truth" — let operators *consume* upstream templates directly, not rebuild from scratch every time.

## TL;DR for anyone writing agent reference docs

- Every numeric claim or design rule needs a citation.
- For framework integrations, the upstream's `AGENTS.md` / `SKILL.md` is the canonical source. Source from it explicitly.
- When you don't have a source, mark the claim as "rule of thumb, not strictly validated" rather than asserting it as policy.
- Test your doc by asking: "if a maintainer asked where each line came from, could I answer?" If no — fix it before any agent reads it.

The agent's confidence is downstream of your doc's confidence. Calibrate accordingly.

## Related

- [PR #504](https://github.com/tosin2013/helmdeck/pull/504) — the upstream-aligned rewrite (this post ships with it)
- [Issue #503](https://github.com/tosin2013/helmdeck/issues/503) — proposal to surface upstream templates as a `template.fetch` pack
- [PR #502](https://github.com/tosin2013/helmdeck/pull/502) — the original doc (the one this rewrite supersedes)
- Upstream [HyperFrames](https://github.com/decision-crafters/hyperframes) and the [`hyperframes-student-kit`](https://github.com/nateherkai/hyperframes-student-kit) reference repo
