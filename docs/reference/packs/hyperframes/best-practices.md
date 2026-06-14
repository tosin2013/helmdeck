---
title: HyperFrames composition guide (upstream-sourced)
description: helmdeck's integration guide for the HyperFrames renderer — what the upstream HyperFrames project documents as hard rules and conventions, what hyperframes.compose enforces locally, and what's helmdeck-specific. Sourced from the upstream AGENTS.md / SKILL.md and hyperframes-student-kit, not synthesized.
keywords: [helmdeck, hyperframes, video, composition, GSAP, upstream, agents, SKILL.md]
---

# HyperFrames composition guide

This page is helmdeck's integration guide for the [upstream HyperFrames project](https://github.com/decision-crafters/hyperframes). It documents what HyperFrames itself enforces as hard rules, where those rules are sourced from upstream, and where [`hyperframes.compose`](./compose) layers its own validation. **The canonical reference is the upstream `AGENTS.md` and `SKILL.md` files**; this guide is a helmdeck-side mirror so the [Tier A/B system prompt](./compose#tier-aware-system-prompt) can reference it from an MCP / Docusaurus URL.

A previous version of this page synthesized rules from training-data knowledge ("one focal element per 3 seconds", "minimum font size 60px", etc.) without citation. That content has been replaced. See the [2026-06-14 epistemic-discipline blog post](/blog/upstream-spec-drift) for the lesson and how this rewrite came about.

## Where HyperFrames fits in the helmdeck pipeline

The upstream HyperFrames project documents a **seven-step production pipeline** for generating full videos from a brief:

| Step | Artifact | Upstream-documented owner |
|---|---|---|
| 1. Capture | `capture/` directory | Source-of-truth ingestion (URL / PDF / brand reference) |
| 2. Design | `DESIGN.md` | Brand palette / typography / do's & don'ts |
| 3. Script | `SCRIPT.md` | Narrative — hook, beats, proof points, CTA |
| 4. Storyboard | `STORYBOARD.md` | Per-beat creative direction |
| 5. VO + Timing | `narration.wav` + `transcript.json` | TTS + word-level timestamps |
| 6. Build | `compositions/*.html` | One animated HTML per beat |
| 7. Validate | snapshot PNGs + `npx hyperframes lint` pass | Visual + layout verification |

helmdeck's `hyperframes.compose` covers **step 6 only** — the build phase. Steps 1-5 are operator-driven (or driven by other helmdeck packs: `podcast.generate` covers VO + timing for narrated chains). Step 7 is partially covered: the pack does its own [timeline-coverage check](./compose#timeline-coverage) and [track-index collision check](./compose#track-index-collision-check) before returning. The upstream `npx hyperframes lint` is more comprehensive and runs on rendered output — see [Failure modes](#failure-modes) for the full picture.

## Layout first, animation second (upstream hard rule)

The upstream `SKILL.md` is explicit: agents must write the **static "hero frame"** — the moment the composition rests in its final, steady-state layout — using standard CSS (`display: flex`, `gap`, padding inside elements) **before** any GSAP logic is applied.

```html
<!-- HERO FRAME (static) — must be readable BEFORE any animation runs -->
<div id="bg" class="clip" data-start="0" data-duration="60" data-track-index="0"
     style="background: #1a1a2e; position: absolute; inset: 0;"></div>
<div id="title" class="clip" data-start="0" data-duration="60" data-track-index="1"
     style="display: flex; align-items: center; justify-content: center; padding: 8% 6%;
            color: #fff; font-size: 96px; line-height: 1.2;">
  How tracepoint observability works
</div>
```

Only after the hero frame is structurally sound does the agent add motion:

- `tl.from()` for entrances — animate **TO** the established CSS position **FROM** an invisible/off-screen offset (e.g. `opacity: 0`, `y: -50`).
- `tl.to()` for exits — animate **AWAY FROM** the steady state.

**Why this matters**: if the timeline fails to execute, gets paused at an arbitrary playhead, or hits a deterministic-capture edge case, the visual elements instantly revert to a structurally sound layout. Compositions designed with `position: absolute; top: Npx` on content containers fail this property — content overflows uncontrollably when taller than the remaining viewport, and the upstream layout auditor flags it.

**Upstream source**: HyperFrames `skills/hyperframes/SKILL.md` (the layout-first rule).

## `data-track-index` is temporal, not spatial (upstream hard rule)

This is the rule most commonly conflated with `z-index`. The upstream documentation is unambiguous:

- **`data-track-index` is a non-linear-editor "row" index.** Clips on the **same integer track** MUST NOT temporally overlap. The upstream auditor and helmdeck's [`composeTrackCollision`](./compose#track-index-collision-check) check both reject overlapping clips on the same track.
- **Visual stacking is CSS `z-index` entirely.** A clip on `data-track-index="0"` does NOT render below a clip on `data-track-index="9"` — the renderer doesn't read track-index for stacking. Spatial layering happens via standard `z-index` on absolutely / relatively positioned elements.

**Upstream convention** for track allocation:

| Track-index range | Purpose |
|---|---|
| `0` | Background plates, atmospheric overlays |
| `1`–`5` | Primary scenes, typographical elements, foreground visuals |
| `9`+ | Audio elements (`<audio data-start data-duration data-track-index>`) |

This separation lets the upstream auditor cleanly distinguish "intentional spatial overlap" (z-stacked design layers) from "unintentional temporal overlap" (broken composition).

**Upstream source**: HyperFrames `AGENTS.md` (track semantics + layout auditor distinction).

## Paused-timeline contract (upstream hard rule)

`hyperframes.compose` adds the scaffolding for you; you should not emit it. But understanding **why** the scaffolding exists clarifies why several other rules are non-negotiable.

The HyperFrames capture engine halts the native browser clock and injects a synthetic timing state, then programmatically seeks the GSAP timeline to precise sub-millisecond decimal values calculated as `frame = floor(time * fps)`. For this to work:

```html
<script>
  window.__timelines = window.__timelines || {};
  const tl = gsap.timeline({ paused: true });   // ← MUST be paused
  tl.from("#title", { opacity: 0, y: -50, duration: 1 }, 0);
  window.__timelines["my-video"] = tl;            // ← MUST register globally
</script>
```

`hyperframes.compose` writes this script block for you wrapping the `===TIMELINE===` content the LLM produces. If a model tries to emit `gsap.timeline()` (no `paused: true`), it overrides the contract and the capture engine produces static frames or hits a 45,000ms registration timeout. The Tier C system prompt forbids emitting the scaffolding for exactly this reason.

**Upstream source**: HyperFrames engine documentation (`packages/engine/`).

## Attribute vocabulary

The full set of `data-*` attributes the upstream engine recognizes:

### Structural / temporal (required)

| Attribute | Target | Purpose |
|---|---|---|
| `data-composition-id` | Root container | Identifies the registered timeline (must match `window.__timelines[<id>]`) |
| `data-width` / `data-height` | Root container | Explicit canvas pixel dimensions (e.g. 1920×1080 for landscape, 1080×1920 for portrait) |
| `data-start` / `data-duration` | Root + every `class="clip"` | Temporal bounds. On root, `data-start="0"` and `data-duration` defines total MP4 length |
| `data-track-index` | Every `class="clip"` | NLE row index — see [the temporal-not-spatial rule above](#data-track-index-is-temporal-not-spatial-upstream-hard-rule) |

### Media-control

| Attribute | Target | Purpose |
|---|---|---|
| `data-media-start` | `<audio>` / `<video>` | Trim offset INTO the source media (e.g. `data-media-start="5.5"` starts playback 5.5s into the file) |
| `data-volume` | `<audio>` / `<video>` | **Static, immutable** amplitude (0.0–1.0). Set once; volume tweens are silently ignored — see [Failure modes](#failure-modes) |
| `data-composition-src` | Sub-composition host `<div>` | Relative/absolute file path to an external HTML sub-composition payload — enables modular composition |

### Layout-auditor control

| Attribute | Target | Purpose |
|---|---|---|
| `data-layout-allow-overflow` | Any visible DOM element | Tells the layout auditor to ignore boundary breach for this element (use for elements that intentionally slide off-screen during entry/exit) |
| `data-layout-ignore` | Any visible DOM element | Excludes the element from the layout auditor entirely (use for decorative grain, light leaks, intentional vignette overlap) |

### Parameterized batch rendering

| Attribute | Target | Purpose |
|---|---|---|
| `data-variable-values` | Sub-composition host | Stringified JSON object with per-instance variable overrides. Variables accessed in the sub-composition via `window.__hyperframes.getVariables()`. Enables hyper-personalized batch rendering from a single master template |

**Upstream source**: HyperFrames `packages/core/` type definitions + schema parsers.

## Reference templates (upstream)

Upstream ships canonical reference compositions via the CLI registry (`npx hyperframes init my-video --example <name>`):

| Template | Aspect | Visual intent | Architectural purpose |
|---|---|---|---|
| `blank` | landscape | Minimum viable scaffolding | Agent-driven generation starting point |
| `warm-grain` | landscape | Organic, textured, cream-toned + overlay grain | Nested sub-compositions + kinetic captions |
| `swiss-grid` | landscape | International Typographic Style, strictly structured | Corporate / technical demonstrations |
| `play-mode` | landscape | Energetic elastic animations | Rapid easing curves, bounce dynamics, social hooks |
| `vignelli` | **portrait** | High contrast, bold type, deep red accent | Mobile-first 9:16 baseline architecture |
| `product-promo` | landscape | Multi-scene product showcase | Complex shader transitions, ≥20s pacing, elaborate staging |
| `nyt-graph` | landscape | Editorial data visualization | Statistical animation, chart races |
| `decision-tree` | landscape | Diagrammatic flowcharts | Branching explainers, educational tutorials |
| `kinetic-type` | landscape | Aggressive kinetic typography | Promotional intros, title cards |

Additionally, the [`hyperframes-student-kit`](https://github.com/nateherkai/hyperframes-student-kit) repo ships **12 fully-finished, production-grade reference projects** with a `MOTION_PHILOSOPHY.md` ("10 Laws") documenting the framework's aesthetic governance. This is the closest upstream gets to a "design best practices" doc — and the canonical place to source design rules from.

**helmdeck note**: today `hyperframes.compose` generates compositions from scratch via the LLM. [Issue #503](https://github.com/tosin2013/helmdeck/issues/503) proposes a `template.fetch` pack pattern that would let operators seed compositions from these upstream templates instead.

## WebGL shader transitions

For scene-to-scene transitions, upstream provides **14 highly-optimized WebGL shaders** via `HyperShader.init()`. These execute on the GPU within the browser page context (not Node-side compositing — that pipeline was deprecated in a 20× performance refactor). Document them in the body the LLM writes; the engine renders them deterministically.

A representative subset:

| Shader name | Visual mechanism | Optimal duration |
|---|---|---|
| `chromatic-split` | R/B radial channel shift outward; channels separate then rejoin | 0.3 – 0.5 s |
| `gravitational-lens` | Pinch-pull motion toward center + R/B chromatic separation | 0.6 – 1.0 s |
| `cinematic-zoom` | 12 RGB-offset radial zoom blur passes | 0.4 – 0.6 s |
| `sdf-iris` | SDF circle expands from center with accent-tinted glow rings | 0.5 – 0.7 s |
| `ripple-waves` | Radial standing-wave UV displacement crossfade | 0.6 – 1.0 s |

**Upstream source**: HyperFrames `packages/shader-transitions/`.

## Audio handling

HyperFrames decouples audio entirely from the visual capture path:

1. **Visual capture is muted.** The Headless Chromium browser captures the pure visual frame sequence with audio output silenced.
2. **FFmpeg multiplexes audio post-capture.** The `<audio>` element's `src`, `data-start`, `data-duration`, `data-track-index`, `data-volume`, and `data-media-start` attributes serve purely as **metadata**. The audio file is never "played" live in the DOM during capture; FFmpeg merges it algorithmically after the visual MP4 is assembled.

**Consequence — volume is static, not animatable.** Any JavaScript-driven attempt to change volume during the timeline (`gsap.to(audio, { volume: 0.5 })`, `audio.volume = 0.3`, etc.) is **silently ignored** by the engine. Fades, crossfades, and ducking must be baked into the audio file upstream of `hyperframes.compose` (e.g. via `ffmpeg afade`).

### Audio-reactive animation (advanced — pre-computed FFT)

When visual elements must react to audio frequency content (a logo pulsing to a bass drum, a glow intensifying with treble peaks), the standard Web Audio API is **categorically unusable** — it relies on a continuous live processing clock that shatters under the deterministic frame-capture loop.

Upstream documents the workaround: **pre-extract FFT data offline** using a Python script (`extract-audio-data.py`) leveraging `numpy` + `ffmpeg`. Decode to mono float32 samples; FFT-window at 4096 samples (a per-frame 30fps window is too small to cleanly resolve low frequencies); output a JSON object with pre-computed amplitude per-frame across normalized frequency bands.

```javascript
var AUDIO_DATA = {
  fps: 30,
  totalFrames: 900,
  frames: [
    { bands: [0.82, 0.45, 0.31, 0.10, 0.05] },
    { bands: [0.84, 0.43, 0.30, 0.11, 0.05] },
    // ...
  ]
};
```

Then in the timeline, **iterate exhaustively** with discrete `tl.call()` invocations at exact temporal offsets (`frame_index / fps`) — NOT a single continuous interpolated tween. This guarantees the deterministic capture engine evaluates the pre-computed amplitude at every frame slice without depending on a live clock.

This pattern is out of scope for `hyperframes.compose`'s current LLM-generated content (the agent would have to handle the offline pre-extraction step). Document it here for completeness; consider a future `audio.extract_fft` pack.

**Upstream source**: HyperFrames `extract-audio-data.py` reference script + audio-reactive documentation.

## Failure modes

Catalog of documented failure modes with cause + mitigation. Most-common-first:

| Failure | Cause | Mitigation |
|---|---|---|
| Blank-screen gap | `class="clip"` elements don't cover `[0, duration_seconds)`; reset CSS shows through | `hyperframes.compose` rejects at compose-time. Add a permanent background `class="clip"` covering the full duration. See [timeline-coverage check](./compose#timeline-coverage). |
| Track-index collision | Two clips on same integer `data-track-index` temporally overlap | `hyperframes.compose` rejects at compose-time. Put overlapping clips on DIFFERENT tracks; use CSS `z-index` for spatial stacking. |
| Audio volume tween silently ignored | `gsap.to(audio, {volume:...})` or `audio.volume = X` during timeline | FFmpeg multiplexes audio post-capture; runtime DOM changes have no effect. Bake fades into the audio file upstream. |
| Static frames captured | `gsap.timeline()` without `paused: true` | Don't emit the timeline scaffolding — the pack writes it correctly. If your `===TIMELINE===` content tries to override the registration, the engine produces static frames. |
| 45 000 ms registration timeout | Sub-composition timelines not registered to `window.__timelines["<composition-id>"]` | Don't emit the scaffolding (pack handles it). For sub-compositions, the pack's `data-composition-src` flow registers them correctly. |
| `TypeError: Illegal invocation` in sub-composition | `document.getElementById()` inside a sub-composition script | The engine's `wrapScopedCompositionScript` bundles scripts as Function-constructor strings, stripping the global document scope. Use the `window.__hyperframes` namespace for element access instead. |
| ARM64 + Chromium 147 — `beginFrame` cascade error | Hardware/Chromium version mismatch on Linux ARM64 | Set `PRODUCER_FORCE_SCREENSHOT=true` in the render execution context. Bypasses `beginFrame` for a slower but stable screenshot path. |
| Render hits `Runtime.callFunctionOn timed out` | Legacy Node-side WebGL transition compositor under parallel workers | Already fixed upstream — current version uses page-side GPU WebGL compositing (20× speedup). Update the upstream HyperFrames version if you hit this. |
| Studio sub-composition stamping bleeds into render | Old `packages/studio/` regression | Already fixed upstream. Update if you see hidden sub-composition bounding boxes in the final MP4. |

**Upstream source**: HyperFrames changelog + release notes.

## React (Remotion) migration constraints

If you're porting an existing Remotion project to HyperFrames, the following Remotion patterns do NOT translate and must be refactored:

- **`useState` / `useReducer` for animation state** — violates the imperative pre-calculated GSAP timeline. Express state changes as discrete `tl.call()` operations at specific timestamps.
- **`useEffect` / `useLayoutEffect` with non-empty dependency arrays** — initiates asynchronous calculations outside the deterministic capture loop.
- **`<Img crossOrigin="anonymous">`** — headless HyperFrames enforces CORS differently; cross-origin images frequently fail to load.
- **`<Loop>` with internal state increments or cross-iteration data seeding** — stateful loops destroy the deterministic sequence. Purely visual repetition (CSS `animation-iteration-count: infinite`, GSAP `repeat: -1` without state) is fine.

## What `hyperframes.compose` enforces (helmdeck-side)

Two validations layered on top of the upstream contract — both run at compose-time before any render cost is incurred:

1. **Timeline-coverage check** — every `class="clip"` element's `[data-start, data-start+data-duration)` union must cover `[0, duration_seconds)` within `min(2.0s, duration * 0.05)` tolerance. Rejects with `CodeInvalidInput` and cites the gap range + suggested fix.
2. **Track-index collision check** — clips sharing the same integer `data-track-index` must not temporally overlap. Rejects with `CodeInvalidInput` and cites the colliding track + intervals + reminds the operator that CSS `z-index` is for spatial layering.

These are helmdeck-side replications of upstream's `npx hyperframes lint` rules so the failure surfaces at compose-time rather than after the render bill.

## See also

- [`hyperframes.compose`](./compose) — the pack reference, with input/output schema, error codes, and Tier-aware system prompt note
- [`hyperframes.render`](./render) — the renderer consuming the composition
- [Upstream HyperFrames](https://github.com/decision-crafters/hyperframes) — `AGENTS.md`, `SKILL.md`, packages
- [hyperframes-student-kit](https://github.com/nateherkai/hyperframes-student-kit) — 12 production-grade reference projects + `MOTION_PHILOSOPHY.md`
- [Issue #503](https://github.com/tosin2013/helmdeck/issues/503) — proposal to surface upstream templates as a `template.fetch` pack
- [Blog: When agent-instruction docs drift from upstream spec](/blog/upstream-spec-drift) — the epistemic lesson behind this rewrite
