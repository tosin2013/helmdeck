---
title: Authoring render-deterministic hyperframes compositions
description: The rules an LLM or human author must follow so a hyperframes composition renders correctly, not just previews correctly. Empirically derived from the v0.29.3 retest investigation where even upstream's own decision-tree example produced 2 distinct frames over 15s.
keywords: [helmdeck, hyperframes, render, GSAP, authoring, render ≠ preview]
---

# Authoring render-deterministic hyperframes compositions

A hyperframes composition can look perfect in `hyperframes preview` and produce a near-blank MP4 in `hyperframes render`. The framework calls this ["the hardest class of bug in agent-authored compositions"](https://github.com/heygen-com/hyperframes/issues/1437) — "render ≠ preview." Even upstream's curated `decision-tree` example trips over it: 2 distinct frames across its 15-second timeline when rendered, fully animated in preview.

This page documents the empirically-derived rules that make a composition render correctly. The corresponding [field report blog post](/blog/child-composition-slot-lifetime) tells the story of how we discovered them; this page is the canonical reference.

## The mental model

Preview is a normal browser. Render is **deterministic frame seeking** — the engine paused the timeline, teleported its playhead to `frame_index / fps`, captured the pixel buffer atomically, then moved on. Anything that depends on continuous wall-clock time, animation history, or post-paint DOM mutation **breaks** during seek.

The rules below collapse to one principle: **everything must be reconstructible from the timeline's current `totalTime()` alone**, with no dependency on what happened before.

## Structural rules

These map to upstream's runtime contract. Violating any of them produces a blank canvas or unregistered-element silent failure.

### Timeline registration

Every composition registers exactly one GSAP timeline at the end of its `<script>`, keyed by its `data-composition-id`:

```html
<div data-composition-id="my-scene" data-width="1920" data-height="1080" data-duration="12">
  <!-- composition content -->
  <script>
    const tl = gsap.timeline({ paused: true });
    // ... tl.from(), tl.to(), tl.addLabel() calls ...
    window.__timelines = window.__timelines || {};
    window.__timelines["my-scene"] = tl;   // ← key MUST match data-composition-id exactly
  </script>
</div>
```

The renderer reads `window.__timelines[id]` for every `[data-composition-id]` element it finds in the DOM and auto-sequences them into the main timeline via `data-start` attributes. **You do not write `tl.add(otherTimeline, offset)` calls manually** — that conflicts with auto-discovery and triggers `gsap_studio_edit_blocked` warnings.

### Composition duration ≡ GSAP timeline duration

The composition's `data-duration="N"` attribute and its GSAP timeline's intrinsic length **must match**. If `data-duration="12"` but the last `tl.to(...)` ends at t=8, the composition ends at t=8 and the renderer captures 4 seconds of frozen final-frame after. If `data-duration="12"` but the timeline goes to t=15, the renderer cuts at 12.

### Sub-composition sequencing is declarative

Sub-compositions are nested via `data-composition-src` attribute on a child `<div>` with `data-start` and `data-duration`:

```html
<div data-composition-id="main" data-start="0" data-duration="30">
  <div data-composition-id="intro"
       data-composition-src="compositions/intro.html"
       data-start="0" data-duration="5"></div>
  <div data-composition-id="body"
       data-composition-src="compositions/body.html"
       data-start="5" data-duration="20"></div>
  <div data-composition-id="outro"
       data-composition-src="compositions/outro.html"
       data-start="25" data-duration="5"></div>
</div>
```

The runtime fetches each composition file, extracts its template, executes its `<script>`, and calls `mainTimeline.add(subTimeline, dataStart)` automatically. **All three sub-compositions' data-duration values should sum to the host's data-duration** — gaps render as black, overlap renders as whichever has higher CSS z-index.

## Authoring-style rules

These map to GSAP's seek behavior. Violating them produces "preview works, render blanks" symptoms.

### Layout-before-animation

Define each element's **final visible state** in HTML/CSS, statically. Use absolute positioning with explicit z-index. Animate FROM invisible/offscreen TO that final state using `gsap.from()`:

```css
/* Final resting position — explicit, absolute */
.title { position: absolute; left: 50%; top: 40%;
         transform: translate(-50%, -50%);
         font-size: 96px; opacity: 1; }
```

```js
// Animation: enter FROM invisible state TO the CSS-defined resting state
tl.from(".title",
  { opacity: 0, y: -50, duration: 1, ease: "power2.out" }, 0);
```

This is the **only** pattern that handles deterministic seek correctly. `gsap.from()` defines an entrance; the renderer can mathematically compute the element's state at any timestamp because the start AND end are explicit.

**Avoid** `gsap.to({ opacity: 0 })` for exits — use scene transitions (the next scene covers the current one) instead. Renderer seek to "exit halfway through" lands at indeterminate state.

### Synchronous construction only

Timeline construction MUST be synchronous. NO `async`/`await`, NO `setTimeout`, NO `setInterval`, NO `requestAnimationFrame`, NO `fetch().then(timelineConstruction)`. If timeline construction is deferred, the renderer seeks the frame before the animation registers and captures empty state.

```js
// ❌ BROKEN — registers timeline after first paint, renderer misses it
setTimeout(() => {
  const tl = gsap.timeline({ paused: true });
  tl.from(".title", { opacity: 0 }, 0);
  window.__timelines["scene-1"] = tl;
}, 100);

// ✅ CORRECT — timeline constructed synchronously, registered at script-execution time
const tl = gsap.timeline({ paused: true });
tl.from(".title", { opacity: 0 }, 0);
window.__timelines["scene-1"] = tl;
```

### No infinite loops, no DOM mutation after paint

`repeat: -1` (infinite GSAP loop) and runtime DOM mutation (`element.appendChild`, dynamic SVG path calculation, typing-simulation `setInterval` loops) both break the seek contract. The renderer seeks to frame N expecting a specific DOM state; if frame N's state depends on the cumulative effect of N-1 prior mutations, seeking directly to N produces an undefined result.

For typewriter / typing effects: use a `gsap.to({ text: "finalString", ease: "none" })` with the GSAP TextPlugin, which is seek-friendly. NOT `setInterval` that appends characters.

For SVG path drawing: use `gsap.to(".path", { drawSVG: "100%" })` with the GSAP DrawSVGPlugin, which is seek-friendly. NOT `requestAnimationFrame` loops that recalculate `<path d>` attributes.

## Asset rules

These map to the headless Chromium sandbox the renderer uses. Violating them produces silent media or blank content.

### Media needs `id`

Every `<audio>` and `<video>` element MUST have an `id` attribute. The renderer uses `id` to discover media elements; without it, the audio is **silent** in the rendered MP4 and the video shows as blank.

```html
<!-- ❌ silent in renders -->
<audio src="assets/narration.mp3" data-start="0" data-duration="60"></audio>

<!-- ✅ renders correctly -->
<audio id="narration" src="assets/narration.mp3" data-start="0" data-duration="60"></audio>
```

helmdeck's `hyperframes.attach_audio` pack adds `id="aroll-audio-<content-hash>"` automatically since v0.29.4.

### All assets local — no CDNs

External URLs (Google Fonts CDN, S3-hosted video, jsdelivr scripts) fail in the sandbox. Fonts must use local `@font-face` declarations pointing at captured `.woff2` files. Videos and audio must be bundled in the project's `assets/` directory.

```css
/* ❌ blocked in sandbox */
@import url("https://fonts.googleapis.com/css2?family=Inter:wght@400;600");

/* ✅ uses local capture */
@font-face {
  font-family: "Inter";
  src: url("capture/assets/fonts/Inter-Regular.woff2") format("woff2");
  font-weight: 400;
}
```

GSAP itself is allowed via jsdelivr (`<script src="https://cdn.jsdelivr.net/npm/gsap@3.14.2/dist/gsap.min.js"></script>`) — that path is whitelisted in the sandbox configuration.

### No CSS `transform` on GSAP-animated elements

If GSAP tweens `y`, `x`, `scale`, `rotation`, or `transform` on an element, **do not set `transform: ...` on that element in CSS**. GSAP overwrites the full CSS transform on its first tween, discarding any rotation/scale/translate the CSS set. Move all positioning into GSAP:

```css
/* ❌ GSAP will discard this on first tween */
.text { transform: translateY(200px); }
```
```js
tl.fromTo(".text", { y: 200 }, { y: 0, duration: 1 });
```
```js
// ✅ origin in GSAP, animates correctly
tl.fromTo(".text", { y: 200 }, { y: 0, duration: 1 });
```

## The pre-render validation gate

Three packs catch the failures these rules prevent **before** the render burns. Run them in order, gating on each one passing strict:

```yaml
- pack: hyperframes.lint
  inputs:
    project_artifact_key: "${steps.previous.project_key}"
    strict: true   # any error → abort
- pack: hyperframes.inspect
  inputs:
    project_artifact_key: "${steps.previous.project_key}"
    at_transitions: true   # sample every tween boundary
    strict: true
- pack: hyperframes.validate
  inputs:
    project_artifact_key: "${steps.previous.project_key}"
    strict: true
- pack: hyperframes.render
  inputs:
    project_artifact_key: "${steps.previous.project_key}"
```

| Pack | Catches | Why it runs |
|---|---|---|
| [`hyperframes.lint`](/docs/reference/packs/hyperframes/lint) | Static source issues — missing media id, external font imports, CSS-vs-GSAP transform conflicts, manual `__timelines` registrations that conflict with auto-discovery | ~1s, file-system only — should always be the first gate |
| [`hyperframes.inspect`](/docs/reference/packs/hyperframes/inspect) | Runtime layout — text/container overflow at specific timestamps, transition-seam overlaps with `at_transitions:true` | Loads in headless Chrome + samples DOM, ~10–30s |
| [`hyperframes.validate`](/docs/reference/packs/hyperframes/validate) | Runtime errors — CORS-blocked assets (silent blank media), JS exceptions during composition load (blank canvas), WCAG AA contrast failures | Loads in headless Chrome + console audit, ~5–10s |

All three are soft-surface by default; pass `strict:true` to abort downstream packs on any error-severity finding.

## What this is NOT

- **Not a complete style guide** — see upstream's [SKILL.md](https://github.com/heygen-com/hyperframes/blob/main/skills/SKILL.md) and [motion-principles.md](https://github.com/heygen-com/hyperframes/blob/main/skills/motion-principles.md) for typography/color/timing guidance
- **Not a complete reference** — the validation packs encode the load-bearing checks; treat their `findings[].fixHint` field as the authoritative remediation guide
- **Not a guarantee** — even compositions that pass all three packs can render unexpectedly if the LLM violated a rule the linter doesn't yet check. Always render a 3-frame proof at draft quality before committing to a publish-quality render

## See also

- Field report: [Render ≠ preview](/blog/child-composition-slot-lifetime)
- Reference: [`hyperframes.lint`](/docs/reference/packs/hyperframes/lint), [`hyperframes.inspect`](/docs/reference/packs/hyperframes/inspect), [`hyperframes.validate`](/docs/reference/packs/hyperframes/validate)
- Upstream rules: [`heygen-com/hyperframes` skills](https://github.com/heygen-com/hyperframes/tree/main/skills)
- Upstream tracking: [`heygen-com/hyperframes#1437`](https://github.com/heygen-com/hyperframes/issues/1437) (the "render ≠ preview" bug class)
- Test bed: [`scripts/hyperframes-bare-baseline.sh`](https://github.com/tosin2013/helmdeck/blob/main/scripts/hyperframes-bare-baseline.sh) (run any scaffold + audio through the upstream-only path)
