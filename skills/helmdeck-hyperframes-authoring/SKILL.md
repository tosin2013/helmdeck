---
name: helmdeck-hyperframes-authoring
description: Author render-deterministic hyperframes compositions. Use when generating HTML/CSS/JS for `hyperframes.compose`, `hyperframes.render`, or any pipeline that produces a programmatic MP4 via hyperframes — including `builtin.scaffolded-narrated-video`, `builtin.prompt-video`, `builtin.prompt-narrated-video`. The framework's "render ≠ preview" bug class means many natural-looking compositions render as a near-blank canvas; this skill teaches the structural + asset rules that prevent it, plus the pre-render validation gate (lint → inspect → validate) that catches violations before burning the render budget.
metadata:
  openclaw:
    skillKey: helmdeck-hyperframes-authoring
    helmdeckVersion: "v0.29.4"
    source: https://github.com/tosin2013/helmdeck/blob/main/skills/helmdeck-hyperframes-authoring/SKILL.md
---

<!-- Canonical hyperframes-authoring skill. Use BEFORE writing any
     hyperframes composition HTML — the rules below are load-bearing
     for render-time correctness. Re-run scripts/configure-openclaw.sh
     (OpenClaw) or scripts/configure-claude.sh (Claude Code) after any
     helmdeck release to re-stamp helmdeckVersion. -->

# Hyperframes authoring — render-deterministic rules

When you write composition HTML/CSS/JS for hyperframes, the renderer **seeks to each frame index** rather than playing the timeline forward. Anything that depends on continuous wall-clock time, animation history, or post-paint DOM mutation **breaks during seek** — even if it looks perfect in `hyperframes preview`. Upstream tracks this as ["the hardest class of bug in agent-authored compositions"](https://github.com/heygen-com/hyperframes/issues/1437). The rules below collapse to one principle: **everything must be reconstructible from the timeline's current `totalTime()` alone.**

## Before you write any composition: the four rules to remember

1. **One GSAP timeline per composition, registered synchronously, keyed by `data-composition-id`.**
2. **Layout in HTML/CSS (final visible state); animate with `gsap.from()` into that state.**
3. **No `setTimeout`, `setInterval`, `requestAnimationFrame`, `repeat: -1`, or post-paint DOM mutation.**
4. **All assets local (no Google Fonts CDN, no S3 URLs, no jsdelivr except GSAP itself).**

## The structural contract

### Timeline registration

Every composition ends its `<script>` with this exact shape:

```js
const tl = gsap.timeline({ paused: true });
// ... synchronous tl.from() / tl.to() / tl.addLabel() calls ...
window.__timelines = window.__timelines || {};
window.__timelines["<composition-id>"] = tl;   // ← KEY MUST EQUAL data-composition-id ATTRIBUTE
```

The `<composition-id>` key MUST exactly match the `data-composition-id` attribute on the composition's root `<div>`. If they differ, the timeline is registered but never auto-attached to the parent — the composition renders as static last-frame.

**Do not write `mainTimeline.add(subTimeline, offset)` calls manually.** The runtime auto-sequences sub-compositions via their `data-composition-src` + `data-start` + `data-duration` attributes. Manual `tl.add()` triggers `gsap_studio_edit_blocked` warnings and conflicts with auto-discovery.

### Composition duration ≡ GSAP timeline duration

The composition's `data-duration="N"` and its GSAP timeline's intrinsic length MUST match. If they don't:

- timeline shorter than data-duration → composition holds final frame for the remainder (looks frozen)
- timeline longer than data-duration → renderer cuts before the animation ends

Decide the composition's total length FIRST, then build the timeline to fit exactly.

### Sub-composition sequencing is declarative

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

All sub-composition `data-duration` values should sum to the host's `data-duration` (5+20+5 = 30, matches the host's 30). Gaps render as black. Overlap renders as whichever has higher CSS z-index.

## The authoring-style contract

### Layout-before-animation

Define each element's **final visible state** in HTML/CSS (with absolute positioning + explicit z-index). Then animate FROM invisible/offscreen TO that final state with `gsap.from()`:

```css
.title { position: absolute; left: 50%; top: 40%;
         transform: translate(-50%, -50%);
         font-size: 96px; opacity: 1; }
```

```js
tl.from(".title",
  { opacity: 0, y: -50, duration: 1, ease: "power2.out" }, 0);
```

`gsap.from()` is the only animation pattern that handles seek correctly. The renderer can mathematically compute the element's state at any timestamp because the START state (provided to `from()`) and END state (in CSS) are both explicit.

**Avoid `gsap.to({ opacity: 0 })` for exits.** Use scene transitions (next scene covers current one) instead. Renderer seek to "halfway through an exit" lands at indeterminate state.

### Synchronous construction only

NO `async`/`await`, NO `setTimeout`, NO `setInterval`, NO `requestAnimationFrame`, NO `fetch().then(...)`. If timeline construction is deferred, the renderer seeks the frame BEFORE the animation registers and captures empty state.

```js
// ❌ BROKEN — registers timeline after first paint, renderer misses it
setTimeout(() => {
  const tl = gsap.timeline({ paused: true });
  // ...
  window.__timelines["scene-1"] = tl;
}, 100);

// ✅ CORRECT — synchronous, registered at script-execution time
const tl = gsap.timeline({ paused: true });
// ...
window.__timelines["scene-1"] = tl;
```

### No DOM mutation, no infinite loops

- `repeat: -1` (infinite GSAP repeat) — breaks the capture engine
- Runtime DOM mutation (`element.appendChild`, dynamic `<svg path d>` recalculation, `setInterval` typing animations) — frame N's state depends on N-1 prior mutations; seeking directly to N produces undefined state

For typewriter effects: `gsap.to({ text: "finalString", ease: "none" })` with the TextPlugin.
For SVG path drawing: `gsap.to(".path", { drawSVG: "100%" })` with DrawSVGPlugin.

## The asset contract

### Media needs `id`

Every `<audio>` and `<video>` MUST have an `id`. Without it, the renderer can't discover the element and the audio is **silent in the rendered MP4**.

```html
<!-- ❌ silent in renders -->
<audio src="assets/narration.mp3" data-start="0" data-duration="60"></audio>

<!-- ✅ renders correctly -->
<audio id="narration" src="assets/narration.mp3" data-start="0" data-duration="60"></audio>
```

helmdeck's `hyperframes.attach_audio` pack adds this automatically since v0.29.4 — `id="aroll-audio-<content-hash>"`.

### All assets local

External URLs fail in the sandbox. Fonts via local `@font-face` pointing at `.woff2` files (NOT `fonts.googleapis.com`). Video/audio bundled in `assets/` (NOT S3 URLs). GSAP via jsdelivr IS allowed (whitelisted).

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

### No CSS `transform` on GSAP-tweened elements

If GSAP tweens `x`, `y`, `scale`, `rotation`, or `transform` on an element, **DO NOT set `transform: ...` on that element in CSS**. GSAP overwrites the full CSS transform on first tween, discarding what the CSS set.

```js
// ✅ put the origin in GSAP, not CSS
tl.fromTo(".text", { y: 200 }, { y: 0, duration: 1 });
```

## The pre-render validation gate — REQUIRED before artifact upload

Three helmdeck packs catch most of the failures these rules prevent. **Always run them before `hyperframes.render`** in any pipeline that authors a composition. All three soft-surface by default; pass `strict:true` to abort downstream packs on any error-severity finding — that's the publish-gate setting.

| Pack | Catches | When |
|---|---|---|
| `helmdeck__pack-run` with `name: hyperframes.lint` | Static issues — missing media id, external font imports, CSS-vs-GSAP transform conflicts, manual `__timelines` registration | Always first. ~1s. |
| `helmdeck__pack-run` with `name: hyperframes.inspect` | Runtime layout — text/container overflow at specific timestamps; with `at_transitions:true` samples every tween boundary to catch transition-seam overlaps | After lint passes. ~10-30s. |
| `helmdeck__pack-run` with `name: hyperframes.validate` | Runtime errors — CORS-blocked assets (silent blank media), JS exceptions during composition load (blank canvas), WCAG AA contrast failures | After inspect passes. ~5-10s. |

Once all three pass, then run `hyperframes.render`. If you skip the gates and render fails, the typical failure mode is silent (blank canvas, no error) — the pipeline succeeds but the artifact is unwatchable. The gates exist to make those failures loud and pre-render-cost-cheap.

### Pipeline shape

```yaml
- helmdeck__pack-run:
    name: hyperframes.lint
    input:
      project_artifact_key: "${steps.previous.project_key}"
      strict: true   # abort if any error
- helmdeck__pack-run:
    name: hyperframes.inspect
    input:
      project_artifact_key: "${steps.previous.project_key}"
      at_transitions: true
      strict: true
- helmdeck__pack-run:
    name: hyperframes.validate
    input:
      project_artifact_key: "${steps.previous.project_key}"
      strict: true
- helmdeck__pack-run:
    name: hyperframes.render
    input:
      project_artifact_key: "${steps.previous.project_key}"
```

## Common LLM mistakes — pre-emptively avoid

When the agent is asked to write a composition, the LLM's training data is full of interactive web patterns that break in render. These are the most common ones — actively suppress them:

- "Animate text with `setTimeout` loops" → use `gsap.to({ text: "..." })` with TextPlugin
- "Draw SVG connectors at runtime" → pre-draw the path, animate with `drawSVG`
- "Use `requestAnimationFrame` for smooth motion" → GSAP already does this; just use `tl.to(...)`
- "Load fonts from Google Fonts" → use local `@font-face` with bundled `.woff2`
- "Embed YouTube/Vimeo player" → won't render; use local bundled video file
- "Make elements interactive on click" → renderer doesn't dispatch events; static-only

## Worked example skeleton — the smallest render-deterministic composition

When the user prompt asks for "a hyperframes composition that animates a title and subtitle over 8 seconds," the LLM should produce something with this exact skeleton:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=1920, height=1080" />
  <title>Hello World</title>
  <script src="https://cdn.jsdelivr.net/npm/gsap@3.14.2/dist/gsap.min.js"></script>
  <style>
    @font-face {
      font-family: "Inter";
      src: url("capture/assets/fonts/Inter-SemiBold.woff2") format("woff2");
      font-weight: 600;
    }
    html, body { margin: 0; width: 1920px; height: 1080px; overflow: hidden;
                 background: #0a0a0f; font-family: "Inter", sans-serif; color: white; }
    .title    { position: absolute; left: 50%; top: 40%;
                transform: translate(-50%, -50%);
                font-size: 96px; font-weight: 600; opacity: 1; }
    .subtitle { position: absolute; left: 50%; top: 56%;
                transform: translate(-50%, -50%);
                font-size: 36px; opacity: 1; color: #c0c0d0; }
  </style>
</head>
<body>
  <div id="root" data-composition-id="main" data-start="0" data-duration="8"
       data-width="1920" data-height="1080">
    <div class="title">Hello, World</div>
    <div class="subtitle">A render-deterministic composition</div>
  </div>
  <script>
    const tl = gsap.timeline({ paused: true });
    tl.from(".title",    { opacity: 0, y: -40, duration: 1.0, ease: "power2.out" }, 0.5);
    tl.from(".subtitle", { opacity: 0, y:  40, duration: 1.0, ease: "power2.out" }, 1.2);
    // Add a hold by addLabel; the data-duration=8 owns the timeline length.
    tl.addLabel("hold", 2.5);
    window.__timelines = window.__timelines || {};
    window.__timelines["main"] = tl;
  </script>
</body>
</html>
```

Key points: `data-composition-id="main"` ↔ `window.__timelines["main"]`. `data-duration="8"` matches the timeline's intent (animation + hold). All elements are positioned absolutely with CSS-resting states; GSAP animates from invisible (`opacity: 0` + `y` offset) to that resting state. No external font from CDN. No `setTimeout`. No infinite loops. Composition will render correctly.

## See also

- Explanation page (the "why" behind these rules): [/docs/explanation/authoring-render-deterministic-compositions](/docs/explanation/authoring-render-deterministic-compositions)
- Reference: [`hyperframes.lint`](/docs/reference/packs/hyperframes/lint), [`hyperframes.inspect`](/docs/reference/packs/hyperframes/inspect), [`hyperframes.validate`](/docs/reference/packs/hyperframes/validate), [`hyperframes.render`](/docs/reference/packs/hyperframes/render)
- Field report: [Render ≠ preview](/blog/child-composition-slot-lifetime)
- Upstream: [`heygen-com/hyperframes` skills](https://github.com/heygen-com/hyperframes/tree/main/skills) (motion-principles, beat-builder)
- The minimal upstream-only reproducer: [`scripts/hyperframes-bare-baseline.sh`](https://github.com/tosin2013/helmdeck/blob/main/scripts/hyperframes-bare-baseline.sh)
