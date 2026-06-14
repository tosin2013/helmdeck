---
title: hyperframes.compose
description: Generate a HyperFrames HTML/CSS/JS video composition from a plain-language description — the pack guarantees the render contract (sized canvas, data-* scaffolding, paused GSAP window.__timelines), the LLM writes only the visuals. Feed straight into hyperframes.render.
keywords: [helmdeck, hyperframes, compose, video, mp4, gsap, generate, prompt, MCP]
---

# `hyperframes.compose`

`hyperframes.compose` turns a plain-language **description** into a HyperFrames
HTML/CSS/JS composition that [`hyperframes.render`](./render) can turn into an MP4.
It's the "describe it, the LLM writes it" half of video generation — the same split
[`slides.outline`](../slides/outline) provides for decks: the model does the
creative work, the pack does the production-contract work.

## Why this pack exists

`hyperframes.render` only *renders* an author-supplied composition. Writing that
composition by hand means reproducing the HyperFrames contract exactly — a sized
canvas, a root `<div>` with `data-composition-id` / `data-width` / `data-height`,
and a **paused** GSAP timeline registered on `window.__timelines["main"]`. A dropped
attribute makes the render fail. So callers ended up pasting raw HTML.

This pack closes that gap **without trusting the model to get the boilerplate
right**. The model returns only three creative pieces — extra CSS, the visible
`class="clip"` elements, and the GSAP timeline body — and the pack **assembles** the
final document around the guaranteed scaffolding (canvas sized to the chosen
aspect ratio, root `data-*`, the `window.__timelines` registration, an optional
narration `<audio>`). The structural contract holds regardless of model quality.

## Timeline coverage

Every second of the composition's `[0, duration_seconds)` range must be covered by at least one `class="clip"` element. The body's reset CSS sets `background: #000;` so any uncovered range renders as visible black in the final MP4. The pack rejects compositions whose foreground elements leave a gap longer than `min(2.0s, duration_seconds * 0.05)` — added in [PR #502](https://github.com/tosin2013/helmdeck/pull/502) after the 2026-06-13 concept-animator session surfaced a 2+ second black run in an 8-second rendered video.

The canonical pattern is a **permanent background element** plus foreground content on separate tracks:

```html
<div id="bg" class="clip" data-start="0" data-duration="60" data-track-index="0"
     style="background: #1a1a2e; position: absolute; inset: 0"></div>
<div id="title" class="clip" data-start="0" data-duration="6" data-track-index="1">...</div>
<div id="diagram" class="clip" data-start="6" data-duration="54" data-track-index="1">...</div>
```

## Track-index collision check

Per the upstream HyperFrames hard rule documented in [the composition guide](./best-practices#data-track-index-is-temporal-not-spatial-upstream-hard-rule): clips sharing the same integer `data-track-index` MUST NOT temporally overlap. Track-index is a non-linear-editor row index (temporal layout), NOT a CSS `z-index` (spatial stacking). The pack rejects compositions where two clips on the same track overlap — added in [PR #504](https://github.com/tosin2013/helmdeck/pull/504) after sourcing the rule directly from the upstream `AGENTS.md`.

Upstream convention for track allocation:

- `data-track-index="0"` — backgrounds, atmospheric overlays
- `data-track-index="1"`–`"5"` — primary scenes, typographical elements
- `data-track-index="9"`+ — audio elements

To stack visuals at the same moment in time, put them on DIFFERENT tracks and use CSS `z-index` for spatial layering.

For the full upstream-sourced rule set — seven-step pipeline, layout-first pattern, attribute vocabulary, reference template catalog, audio constraints, documented failure modes, React migration constraints — see [HyperFrames composition guide](./best-practices).

## Tier-aware system prompt

The system prompt the pack sends to the gateway LLM adapts to the caller-supplied model's tier (per the existing `models/*.yaml` profile registry — also added in [PR #502](https://github.com/tosin2013/helmdeck/pull/502)):

- **Tier C** (free / weak open models) — verbose, constraint-heavy prompt with the hard rules and TIMELINE COVERAGE explanation inlined verbatim. Tier C models reliably follow explicit rules but unreliably honor external references.
- **Tier A/B** (frontier / mid-tier hosted models) — leaner prompt that trusts the model on creative latitude and references the [best-practices guide](./best-practices) for deeper guidance.

The contract rules (canvas size, deterministic-only, timeline coverage) are identical across tiers; only the depth of guidance differs.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `description` | `string` | yes | — | What the video should show/say. |
| `model` | `string` | yes | — | Gateway model id (`provider/model`; see `helmdeck://models`). |
| `aspect_ratio` | `string` | no | `16:9` | `16:9`, `9:16`, or `1:1` — drives the exact canvas size (reuses `hyperframes.render`'s preset matrix). |
| `resolution` | `string` | no | `1080p` | `1080p` or `4k` — sets the canvas pixel dimensions. |
| `duration_seconds` | `number` | conditional | `8` (silent only) | Video length (cap 720s). **Required when `audio_url` is provided** — set to the audio's length (e.g. `podcast.generate`'s `duration_s` output, rounded up). The 8s default applies ONLY to silent compositions. |
| `audio_url` | `string` | no | — | A presigned audio URL (e.g. `podcast.generate`'s `audio_url`). When set, the pack embeds an `<audio>` element so the rendered MP4 carries narration. Empty → a silent video. **When set, `duration_seconds` is required** (issue [#498](https://github.com/tosin2013/helmdeck/issues/498)). |
| `style` | `string` | no | — | Freeform visual style hint (e.g. "dark, minimal, bold type"). |
| `max_tokens` | `number` | no | derived | Completion-token budget (clamped to [2048, 8192]). |
| `metadata_model` | `string` | no | `openrouter/auto` | Gateway model id for the engagement-metadata generation step. Pass `""` to opt out (no second LLM call, no `engagement` output). Pass any model id (e.g. `openrouter/openai/gpt-oss-120b:free`) to pin the chain to free tier. String-ptr-shaped: omitted → use default. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `composition_html` | `string` | The assembled, render-ready composition. Pass to `hyperframes.render`'s `composition_html`. |
| `model` | `string` | The model used. |
| `aspect_ratio` | `string` | Echo of the aspect ratio. |
| `width` / `height` | `number` | The canvas pixel dimensions. |
| `duration_seconds` | `number` | The video length baked into the composition. |
| `has_audio` | `boolean` | Whether a narration `<audio>` element was embedded. |
| `duration_source` | `string` | `"audio"` when synced to an embedded track, else `"timeline"`. |
| `engagement` | `object` | Duration-band-aware engagement payload (see [Engagement metadata](#engagement-metadata) below). Absent when `metadata_model: ""` or when generation failed (composition is still produced — engagement is best-effort). |
| `engagement_artifact_key` | `string` | Stable artifact key to the JSON sidecar with the same payload. Useful for chaining downstream packs. Absent when engagement is absent or artifact storage failed. |

## Engagement metadata

When `metadata_model` is non-empty (default: `openrouter/auto`), `hyperframes.compose` makes a second gateway LLM call to produce video-shaped engagement metadata aligned with the rendered MP4's duration band. The shape is selected from `duration_seconds`:

| Band | Duration | Output shape | Target distribution |
|---|---|---|---|
| `short_form` | < 60s | `{title, hook, hashtags, caption, thumbnail_prompt}` | TikTok / YouTube Shorts / Reels |
| `mid_form` | 60–179s | `{title, hook, hashtags, caption, social_blurb, thumbnail_prompt}` | Shorts (long edge) / Twitter / LinkedIn-native |
| `long_form` | ≥ 180s | `{title, description, chapters, hashtags, tags, hook_30s, category, language, thumbnail_prompt}` | YouTube proper |

All bands include a `format` field naming the band (defense against the model returning a different shape than the prompt asked for) and a `thumbnail_prompt` ready to feed `image.generate` for hero artwork.

**Cost discipline**: the default `openrouter/auto` routes to a paid model. To keep the chain on free tier (e.g. when the agent itself runs on `openai/gpt-oss-120b:free`), pass `metadata_model: "openrouter/openai/gpt-oss-120b:free"`. This mirrors `podcast.generate`'s `metadata_model` pattern.

**Failure handling**: if the engagement LLM call returns an error or unparseable JSON, the pack logs a warning and returns the composition without `engagement` / `engagement_artifact_key`. The composition itself is the load-bearing output; engagement is value-add.

## Async behavior

**Asynchronous** (`Async: true`) — one gateway LLM call; no session needed (it only
calls the gateway, then `hyperframes.render` does the session-bound rendering).

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | Missing `description`/`model`; `audio_url` provided without `duration_seconds > 0` (issue [#498](https://github.com/tosin2013/helmdeck/issues/498)); composition's `class="clip"` elements don't cover `[0, duration_seconds)` within tolerance ([PR #502](https://github.com/tosin2013/helmdeck/pull/502)); two clips on the same `data-track-index` temporally overlap ([PR #504](https://github.com/tosin2013/helmdeck/pull/504)); unsupported `aspect_ratio`/`resolution`; the model returned an unparseable spec or no visible elements. |
| `internal` | Registered without a gateway dispatcher. |
| `handler_failed` | Gateway returned no choices. |

## See also

- [`hyperframes.render`](./render) — renders the composition to MP4.
- [HyperFrames composition best practices](./best-practices) — design guidance for what makes a HyperFrames composition genuinely good (referenced by the Tier-A/B system prompt).
- [`podcast.generate`](../podcast/generate) — its presigned `audio_url` drops into `audio_url` for a narrated video.
- [Pipeline prompt templates](/reference/prompt-templates/pipelines) — `builtin.prompt-video` and `builtin.prompt-narrated-video` chain this before `hyperframes.render`.
- Source: [`internal/packs/builtin/hyperframes_compose.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/hyperframes_compose.go).
