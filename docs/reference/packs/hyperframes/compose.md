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

## Async behavior

**Asynchronous** (`Async: true`) — one gateway LLM call; no session needed (it only
calls the gateway, then `hyperframes.render` does the session-bound rendering).

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | Missing `description`/`model`; `audio_url` provided without `duration_seconds > 0` (issue [#498](https://github.com/tosin2013/helmdeck/issues/498)); unsupported `aspect_ratio`/`resolution`; the model returned an unparseable spec or no visible elements. |
| `internal` | Registered without a gateway dispatcher. |
| `handler_failed` | Gateway returned no choices. |

## See also

- [`hyperframes.render`](./render) — renders the composition to MP4.
- [`podcast.generate`](../podcast/generate) — its presigned `audio_url` drops into `audio_url` for a narrated video.
- [Pipeline prompt templates](/reference/prompt-templates/pipelines) — `builtin.prompt-video` and `builtin.prompt-narrated-video` chain this before `hyperframes.render`.
- Source: [`internal/packs/builtin/hyperframes_compose.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/hyperframes_compose.go).
