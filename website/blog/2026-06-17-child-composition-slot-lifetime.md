---
slug: child-composition-slot-lifetime
title: "Render ≠ preview: what we learned shipping a hyperframes integration"
authors: [tosin]
tags: [friction, field-report]
description: A v0.29.2 pipeline produced 15 seconds of animation followed by 83 seconds of blank canvas. We assumed it was a slot-lifetime bug, filed upstream issues, shipped a fix, and tagged a release — then discovered that even upstream's own decision-tree example doesn't render at all (2 distinct frames over 15 seconds). The actual story: hyperframes has a known, documented "render ≠ preview" bug class, and the registry's own decision-tree trips over it. Upstream's own `hyperframes lint` was telling us this the whole time. We wrapped it as a helmdeck pack so the next agent catches it before burning the render budget.
image: /img/social-card.png
date: 2026-06-17
draft: false
---

## Hook

A v0.29.2 helmdeck pipeline produced a ~98-second narrated video with audio attached correctly and 83 seconds of blank canvas after t=15s. We assumed an upstream slot-lifetime bug, shimmed around it in PR #546, tagged v0.29.3, retested — and found the canvas still wasn't really animating. Even the *unmodified* upstream `registry/examples/decision-tree` produces only 2 distinct frames over its 15-second timeline. The compositions all have rich GSAP timelines. The framework has a renderer. The two don't connect for a class of compositions, and upstream documents this as ["the hardest class of bug in agent-authored compositions"](https://github.com/heygen-com/hyperframes/issues/1437). Upstream's own `hyperframes lint` flags every contributing issue.

The blog post isn't about the fix. It's about how easy it is to ship the *wrong* fix when you're staring at one symptom and not the whole architecture.

## Context

The pipeline run was `run_6f6cb0ea40a94dd1` against `builtin.scaffolded-narrated-video`: a `decision-tree`-flavored hyperframes scaffold, narration from `podcast.generate`, audio attached by the new `hyperframes.attach_audio` pack (v0.29.2 / [PR #542](https://github.com/tosin2013/helmdeck/pull/542)), rendered to MP4. Operator-visible symptom: 15 seconds of animation, then white for the rest.

The first hypothesis was an upstream slot-lifetime bug: a sub-composition whose `data-duration` ends before the host's blanks the canvas. Upstream had a closed issue ([#911](https://github.com/heygen-com/hyperframes/issues/911)) with our exact title. We shipped two fixes:

- **PR #546** — `attach_audio` rewrites the child's `data-duration` to match the root's when they started equal, eliminating the trigger
- **PR #548** — bump the sidecar pin `0.6.97` → `0.6.110` to pick up upstream's #911 fix

Both went out in v0.29.3. We tested. The canvas did not blank to pure white at 15s anymore. Done?

Not done.

## Finding

When we sampled frames evenly across the v0.29.3 render, we got only **2 distinct frames over 90 seconds**:

```text
t=2,7s   md5=e3e988…  17,897 B
t=14,17,22,45,70,89s   md5=e659a42c…  20,816 B  ← held for 75 seconds
```

PR #546 stopped the *blank* — but the underlying composition still wasn't animating. We wrote a minimal upstream-only reproducer ([`scripts/hyperframes-bare-baseline.sh`](https://github.com/tosin2013/helmdeck/blob/main/scripts/hyperframes-bare-baseline.sh)) that bypasses helmdeck entirely: it scaffolds via bare `npx hyperframes init`, embeds an audio file, matches durations by hand, renders. Same shape as our pipeline, no helmdeck Go code in the path. **Same result** — only 2 distinct frames.

Then we pulled the unmodified upstream registry example, byte-identical to what `npx hyperframes init --example=decision-tree` produces. Rendered at the example's intrinsic 15 seconds, no audio, no modifications. Sampled 10 frames:

```text
t=0s   d7cfaa…  17,301 B
t=1,2,3,5,7,9,11,13,14s   fc3407…  20,302 B  ← held for 13 of 15 seconds
```

**2 distinct frames over 15 seconds, on upstream's own example.** The bug isn't in helmdeck and isn't in PR #546 — it's that `decision-tree`, the example we chose, doesn't actually animate at render time. We confirmed by rendering `kinetic-type` the same way: **10 distinct frames over 10 samples**. Different example, fully animated.

| Example | Distinct frames over 10 samples | Verdict |
|---|---|---|
| `decision-tree` (curated registry) | **2** | Effectively static |
| `kinetic-type` (curated registry) | **10** | Fully animated |

And upstream's own `hyperframes lint --json` was telling us this the whole time:

```text
✗ [index.html] media_missing_id (error)
   <audio> has data-start but no id attribute. The renderer requires id
   to discover media elements — this audio will be SILENT in renders.

✗ [index.html] google_fonts_import (error)
   External font requests fail in sandboxed/offline renders.

⚠ [compositions/decision_tree.html] gsap_studio_edit_blocked (warning)
   Manual window.__timelines script — the runtime registers timelines
   automatically. Do not add a manual window.__timelines script unless
   GSAP intentionally controls element positions.
```

Two of those errors are operator-fixable. The third is upstream's own canonical example failing upstream's own linter. The pattern upstream calls ["render ≠ preview"](https://github.com/heygen-com/hyperframes/issues/1437) — and the decision-tree example trips over it because it relies on imperative DOM mutation (typing animations, dynamic SVG path calculations) that the headless renderer's deterministic frame-seek can't replay.

## What landed

Three changes in [this PR](https://github.com/tosin2013/helmdeck/pulls):

1. **`attach_audio` adds `id="aroll-audio-<content-hash>"`** to the injected `<audio>` element. Closes upstream's `media_missing_id` error. Audio no longer silent in renders. Content-addressed id mirrors the filename stem so the same audio bytes always produce the same id.

2. **A three-pack pre-render validation suite.** `hyperframes.lint` wraps `hyperframes lint --json` for static-source issues. `hyperframes.inspect` wraps `hyperframes inspect --json` to sample the DOM at every tween boundary in headless Chrome — catches text overflow and transition-seam overlaps that lint can't see. `hyperframes.validate` wraps `hyperframes validate --json` to load the project in Chrome and report DevTools console errors (CORS, missing assets, JS exceptions) plus WCAG AA contrast across timeline samples. All three share the same input shape, the same soft-surface default, and the same `strict:true` flag to gate downstream packs on a clean result. Combined with `av.validate` (post-render audio/video parity), pipelines now have symmetric validation on both sides of the render boundary.

3. **`scripts/hyperframes-bare-baseline.sh`** is now the minimal upstream-only diagnostic. Default `--example=kinetic-type` (verified render-deterministic). `--lint` enabled by default. The script becomes the "is this our bug or theirs?" test: identical pipeline shape with no helmdeck Go in the path.

## Why this matters to you

Three takeaways generalize beyond hyperframes.

**First, "did the test pass?" depends on what you sampled.** Our v0.29.2→v0.29.3 work fixed a real bug — the canvas no longer goes pure-white past 15s. If we'd defined "passed" as "no blank-color signature in the frames," we'd have shipped and walked away. What actually told us more was treating "how many *distinct* frames are in the rendered video?" as the load-bearing question. 2 distinct frames is functionally a slideshow, not a video. A one-line shell loop over md5sum is a binary signal that no amount of visual scrubbing matches.

**Second, the upstream's own lint is the cheapest diagnostic in the toolbox.** When a render goes wrong, the question "what does the upstream's own validator say about this project?" is often answered in <100ms and tells you exactly what to fix. The decision-tree example produces 2 errors and 21 warnings against upstream's own linter — including the literal text "this audio will be SILENT in renders." We were debugging an audio + animation symptom while upstream's linter was telling us we'd shipped an audio element guaranteed to be silent. The lint was already there. We just hadn't wired it in.

**Third, examples are not contracts.** When a framework ships a curated example in its registry, the natural assumption is "this is the canonical demo of how to use the framework." For hyperframes, that's true for `kinetic-type`, `swiss-grid`, `warm-grain` — all proven render-deterministic. It's not true for `decision-tree`, which the framework ships but its own renderer can't fully drive. The principle: before treating an example as your reference, render it bare and *verify it animates*. The 5-minute test would have saved us a week.

If you maintain a framework with examples, ship a smoke-test that renders each example and asserts >N distinct frames. If you wrap a framework in your own pipeline, lint upstream's output before you do anything else. The cost of either is far less than the cost of shipping a fix for the wrong bug.

## See also

- The shim (already merged): [PR #546](https://github.com/tosin2013/helmdeck/pull/546) — child-composition `data-duration` rewrite
- The pin bump + first version of this post: [PR #548](https://github.com/tosin2013/helmdeck/pull/548)
- The lint pack + audio id + baseline script: [PR #551](https://github.com/tosin2013/helmdeck/pull/551)
- Upstream issues we filed: [`heygen-com/hyperframes#1540`](https://github.com/heygen-com/hyperframes/issues/1540)
- The closed-but-adjacent upstream issue: [`heygen-com/hyperframes#911`](https://github.com/heygen-com/hyperframes/issues/911)
- The "render ≠ preview" bug class upstream tracks: [`heygen-com/hyperframes#1437`](https://github.com/heygen-com/hyperframes/issues/1437)
- helmdeck-side watch issue: [`helmdeck#547`](https://github.com/tosin2013/helmdeck/issues/547)
- The minimal reproducer: [`scripts/hyperframes-bare-baseline.sh`](https://github.com/tosin2013/helmdeck/blob/main/scripts/hyperframes-bare-baseline.sh)
- Pack reference: [`hyperframes.lint`](/docs/reference/packs/hyperframes/lint), [`hyperframes.attach_audio`](/docs/reference/packs/hyperframes/attach_audio)
- Earlier hyperframes friction story: [Pinning the wrong package](/blog/pinning-the-wrong-package)
