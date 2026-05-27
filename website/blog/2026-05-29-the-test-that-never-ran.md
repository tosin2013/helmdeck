---
slug: the-test-that-never-ran
title: "The test that never ran: a green check that asserted nothing, and a 39px clip"
authors: [tosin]
tags: [friction]
description: We shipped a CSS fix for clipped slides, wrote a headless-Chromium test that asserts no slide overflows, and blogged about it. The test had never run once — it skipped on a missing dependency every single time. When we finally wired it into CI, it caught a 39px clip in the "fixed" code.
image: /img/social-card.png
date: 2026-05-29
draft: true
---

Three days ago we [published a fix](/blog/a-pdf-slide-cannot-scroll) for mermaid diagrams getting clipped in PDF slide decks. The post even bragged about the test: *"there's an integration-tagged check that loads the rendered HTML in a headless Chromium and asserts no `<section>` overflows its own box."* That test had never run. Not once. And the fix it was supposed to guard still clipped tall diagrams by 39 pixels.

## Context

The original bug: a Marp slide is a fixed 1280×720 canvas, and PDF can't scroll, so an oversized mermaid diagram clips silently. The fix was a theme-independent auto-fit `<style>` — cap the diagram at `max-height: 70vh`, give tables `table-layout: fixed`. We backed it with two integration tests: a render-smoke check that the fit CSS reaches the renderer, and a geometric check (`TestSlidesFit_NoSectionOverflow`) that renders the deck in a headless Chromium and counts how many `<section>`s overflow their own bounds. The second one is the real proof — the only thing that actually answers "does it fit?"

Then this week we did something unrelated: we [added a CI job](https://github.com/tosin2013/helmdeck/pull/300) to run the `//go:build integration` suite, which — embarrassingly — had never run in CI at all. It ran. And it failed.

## Finding

The geometric test starts with a graceful escape hatch, the kind that looks responsible:

```go
const measure = `
const { chromium } = require('playwright');
...
`
// ...
if res.ExitCode == 42 || strings.Contains(string(res.Stderr), "MEASURE_UNAVAILABLE") {
    t.Skipf("headless measure unavailable in this sidecar image: %s", ...)
}
```

The sidecar image ships no `playwright` module. So `require('playwright')` threw, the script exited non-zero, and the test took its "harness unavailable, skip cleanly" path — every run, in every environment, since the day it was written. `go test` printed `--- SKIP`, the package went green, and nobody looked. A skip is indistinguishable from a pass at a glance, and this one had been quietly asserting nothing for its entire life.

The fix for the test was free: Marp prints its PDFs with a *bundled* `puppeteer-core` (and there's a `/usr/bin/chromium` in the image), so the measurement could use the exact browser that renders the real deliverable, with zero new dependencies. Point `NODE_PATH` at Marp's vendored copy, swap the Playwright API for Puppeteer's, and the test runs.

The moment it ran, it caught a real overflow the smoke test couldn't see — because "the CSS is present" and "the content fits" are different claims:

| mermaid cap | section scrollHeight | clientHeight | overflow |
|---|---|---|---|
| `70vh` (shipped) | 759px | 720px | **39px — clips** |
| `64vh` | 720px | 720px | exact |
| `60vh` | ≤720px | 720px | fits, with headroom |

`70vh` is 504px on a 720px slide — but the slide also carries its heading and Marp's ~255px of section padding. 504 + chrome > 720. The cap that was supposed to guarantee fit didn't account for everything else sharing the canvas. We lowered it to `60vh`, which leaves room even for a two-line title, and re-ran: zero overflow.

## Why this matters to you

A skipped test is worse than a missing one. A missing test is an honest gap. A skipped test is a green check with a tooltip nobody reads — it looks like coverage, it gets counted like coverage, and it actively discourages anyone from writing the test again because "we already have one." Ours was *designed* to skip gracefully, and that defensiveness is exactly what swallowed its entire reason to exist.

Three cheap habits would have caught this years sooner:

- **Audit your skip conditions like you audit your assertions.** A skip on a missing dependency is fine in a contributor's laptop; it is a silent hole in the one environment that's *supposed* to have the dependency. Make the test fail loud there, or assert the dependency is present before you allow the skip.
- **Count skips in CI, not just failures.** A run that skips the only test that matters is not a passing run. Surface the skip count; alert when a test that normally runs starts skipping.
- **Run your integration suite somewhere automated.** The deeper bug wasn't the `require('playwright')` — it was that the whole integration tier never executed in CI, so the skip had no audience. The day we gave it one, it paid for itself immediately.

When you write a guard for "if the harness isn't available," ask what happens if the harness is *never* available. If the answer is "the test silently passes forever," you haven't written a test — you've written a comment that compiles.

## See also

- The PR: <https://github.com/tosin2013/helmdeck/pull/300>
- The fix it was supposed to guard: [A PDF slide cannot scroll](/blog/a-pdf-slide-cannot-scroll)
- [`slides.render` reference](/reference/packs/slides/render)
