---
title: HyperFrames composition best practices
description: Design guidance for hyperframes.compose authors — timeline coverage, visual hierarchy, type-on-screen rules, pacing, color, and GSAP transition patterns that play well with the HyperFrames renderer.
keywords: [helmdeck, hyperframes, video, best practices, GSAP, composition, design]
---

# HyperFrames composition best practices

[`hyperframes.compose`](./compose) accepts a plain-language description and returns a render-ready HTML/CSS/JS composition. The pack enforces a strict structural contract (sized canvas, root `data-*` scaffolding, paused `window.__timelines` registration) and a [timeline-coverage check](./compose#timeline-coverage) that rejects compositions whose `class="clip"` elements leave a meaningful blank-screen gap. Those guards prevent **broken** compositions, not **mediocre** ones.

This page is the design guidance for what makes a HyperFrames composition genuinely good. The pack's Tier-A/B system prompts reference this doc explicitly; Tier-C prompts inline the most important rules verbatim because weak models don't reliably follow external references.

## 1. Timeline coverage

Every second of the composition's `[0, duration_seconds)` range needs at least one visible `class="clip"` element behind it. The body's reset CSS sets `background: #000;` so any uncovered range renders as visible black in the final MP4. `hyperframes.compose` rejects compositions with gaps longer than `min(2.0s, duration * 0.05)`.

The canonical pattern is a **permanent background element** plus foreground content that comes and goes:

```html
<!-- background: solid color (or simple gradient) covering the full duration -->
<div id="bg" class="clip" data-start="0" data-duration="60" data-track-index="0"
     style="background: linear-gradient(135deg, #1a1a2e, #16213e);
            position: absolute; top: 0; left: 0; width: 100%; height: 100%"></div>

<!-- foreground: title fades in for the first 6 seconds -->
<div id="title" class="clip" data-start="0" data-duration="6" data-track-index="1"
     style="color: #fff; font-size: 96px; position: absolute; top: 40%; left: 6%">
  How tracepoint observability works
</div>

<!-- foreground: animation plays from 6s to end -->
<div id="diagram" class="clip" data-start="6" data-duration="54" data-track-index="2"
     style="...">...</div>
```

The background covers the whole timeline; the foreground elements overlap and replace each other freely. **Without the background**, any second where the title's animated out and the diagram's animated in but hasn't faded up yet renders as black.

## 2. Visual hierarchy — one focal element per ~3 seconds

Animated explainers are not infographics. The viewer is watching a moving image at 30 fps; they can only process one new piece of information at a time. Plan for **one focal element per ~3-second beat** — a heading, a single diagram, a single number, a single icon — and let everything else either fade back or stay as supporting background.

Concretely:

- A 60s video has ~20 beats. That's 20 distinct visual moments, not 60 different elements competing for attention.
- A 12-minute video (720s) has ~240 beats. Group them into 6–12 chapters of related beats; the chapters become the YouTube `engagement.chapters` payload.
- **Avoid stacking 5 elements with overlapping `data-start`/`data-duration` and animating them in sequence.** GSAP can do it; the viewer can't track it.

## 3. Type-on-screen rules

Video is not a web page. Type that's legible on a 1920×1080 desktop browser is unreadable on a phone in a feed.

- **Minimum font size: ~60px** at 1080p (~3.5% of canvas height). 80–96px is more typical for headings.
- **Minimum on-screen duration for a complete read: 1.5 seconds.** A 5-word heading needs ~2.5 seconds; a sentence needs 4–6 seconds. Compositions that flash text for under a second lose viewers.
- **Plain background contrast.** White on dark, dark on light. Avoid type on photographic / gradient backgrounds without an explicit contrast guard.
- **One typeface.** HyperFrames disallows external font loading (it's a determinism guarantee — `font-family: ui-sans-serif, system-ui, ...` is the safe path). Don't try to mix three weights of three families.

## 4. Pacing

For explainer / how-to / educational content (the primary `hyperframes.compose` use case):

- **Short-form (<60s)**: target one explainer beat. A single concept, a single hook, a single payoff. Don't try to teach two things; the audience clicks away. The duration-band-aware engagement payload uses `format: short_form` for these.
- **Mid-form (60–179s)**: 2–4 beats. Hook, problem, solution, takeaway. Each beat = ~20–60 seconds.
- **Long-form (≥180s)**: chapter-aware. Each chapter is a self-contained beat with its own intro / problem / payoff. The `engagement.chapters` payload should reflect this structure.

Pacing rules of thumb:

- **3-second rule**: every 3 seconds, *something visible* should change. A pulse, a fade, a scale, a position shift. Static frames feel broken.
- **No more than 2 elements animating simultaneously.** Multi-element choreographed sequences are GSAP's strength but a viewer-experience minefield.
- **Hook by 5 seconds.** Whatever the payoff promise is, signal it within the first 5 seconds — viewers who don't see the promise leave.

## 5. Color choices

HyperFrames is deterministic — no external image / font / network resources — so all visual richness has to come from CSS. Practical guidance:

- **Solid colors > complex gradients.** A two-stop linear gradient is fine; a 5-color radial gradient is rendering risk and visually noisy.
- **Limit the palette to 3–5 colors.** A dark base (`#0f172a`, `#1a1a2e`, `#16213e`), a contrast highlight (`#22d3ee`, `#f59e0b`), a text color (`#f1f5f9`), an accent (`#94a3b8`).
- **Avoid full-saturation primaries on saturated backgrounds.** They induce vibration; the audience reads it as "amateur".
- **Background animations: subtle.** A slow `gsap.to(bg, { rotation: 360, duration: 60, repeat: -1 })` works. Anything jittery competes with the foreground.

## 6. GSAP transition patterns that play well

The pack's contract requires a single paused `window.__timelines["main"]` and `class="clip"` elements with `data-start` / `data-duration` for the upstream HyperFrames CLI's clock. Inside that contract, GSAP is fully available. Patterns that play well:

- **`tl.from(...)` for entrances** — start at `opacity: 0` or `y: 50` and let GSAP animate to the final state. Pair with `data-start` so the element exists in the DOM at the moment the timeline reaches it.
- **`tl.to(...)` for exits** — animate to `opacity: 0` toward the end of `data-duration`. Avoid animating past the duration boundary; the renderer cuts at the `data-duration` mark.
- **Tween chains** — `tl.from(...).from(...)` chains tweens after the previous one finishes; useful for "title fades in, then diagram fades in".
- **Stagger groups** — `tl.from(".bullet", { opacity: 0, y: 30, stagger: 0.5 })` is the canonical "reveal bullet list" pattern.
- **Position parameters** — `tl.from(target, props, position)` where `position` is an absolute second offset gives you fine control. Use this for syncing to narration moments when you know the audio timing.

Anti-patterns:

- **Imperative animation loops** (`setInterval`, `requestAnimationFrame`) — break determinism and HyperFrames frame capture.
- **DOM mutation during the timeline** — adding / removing nodes mid-render. Set up everything in the static body; animate in/out with `gsap.set` / `tl.from` / `tl.to`.
- **External resource loading at runtime** — `<img src="...">` with a network URL, `@font-face` with a URL, `fetch(...)`. The pack rejects them at the LLM prompt level; including them anyway makes the render fail.

## 7. Audio-aware pacing

When `audio_url` is provided (the narrated-video case), the composition's pacing should serve the narration:

- **`duration_seconds` must match the audio's actual length** — see [issue #498](https://github.com/tosin2013/helmdeck/issues/498). The pack rejects mismatches.
- **Plan visual beats around narration cues.** ~150 wpm narration means ~3 words per beat in a 3-second pacing window. The visual change should land on a sentence break, not mid-clause.
- **Title cards before the narration starts.** Reserve the first 1–2 seconds for a silent title card; viewers haven't engaged with the audio yet.
- **Outro slowdown.** The final 5–10 seconds should reduce motion — let the viewer settle on a final state with the conclusion or CTA visible.

## 8. Common failure modes (and how to avoid them)

| Failure | Symptom | Fix |
|---|---|---|
| Blank-screen gap | `hyperframes.compose` returns `CodeInvalidInput` citing a gap range, or `av.validate` flags a `video:black_runs` warn | Add a permanent background `class="clip"` with `data-start="0"` covering the full duration |
| Duration mismatch | Rendered MP4 is shorter than the narration audio; audio truncated at timeline end | Pass `duration_seconds` matching `podcast.generate`'s `duration_s` output (rounded up) — see [#498](https://github.com/tosin2013/helmdeck/issues/498) |
| Loudness out of range | `av.validate` warns `audio:loudness_lufs` outside the −14±2 target | Normalize the audio source upstream of `podcast.generate`, or accept the warn as a known limitation of free-tier TTS routing |
| `data-start + data-duration > duration_seconds` | Render fails caller_fixable | Each element's interval must stay within `[0, duration_seconds)` |
| Type unreadable on mobile | Final MP4 plays fine on desktop but viewers in social feeds can't read it | Bump minimum font size to ~60px; check contrast against the actual background, not the design canvas |
| Too many simultaneous animations | Composition reads as visually noisy; viewers click away | One focal element per ~3 seconds; max 2 animating elements at any moment |

## See also

- [`hyperframes.compose`](./compose) — the pack reference
- [`hyperframes.render`](./render) — the renderer the composition feeds
- [`podcast.generate`](../podcast/generate) — narration audio source
- [Issue #498](https://github.com/tosin2013/helmdeck/issues/498) — `duration_seconds` enforcement (silent-truncation foot-gun closed)
- [PR #502](https://github.com/tosin2013/helmdeck/pull/502) — timeline-coverage check + tier-aware system prompts (this guide ships with that PR)
- [The HyperFrames upstream `blank` template + AGENTS.md](https://github.com/decision-crafters/hyperframes) — the structural contract `hyperframes.compose` assembles around
