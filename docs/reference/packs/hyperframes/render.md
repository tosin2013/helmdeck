---
title: hyperframes.render
description: Render an HTML/CSS/JS composition into a deterministic MP4 via Chromium BeginFrame + ffmpeg. Standard YouTube (16:9), vertical Shorts/TikTok/Reels (9:16), and square Instagram (1:1) at 1080p or 4K.
keywords: [helmdeck, hyperframes, video, mp4, youtube, shorts, tiktok, instagram, reels, animation, MCP]
---

# `hyperframes.render`

Turn a self-contained HTML/CSS/JS composition into a deterministic MP4 video. The composition is anything a browser can render — CSS-keyframe animations, [Anime.js](https://animejs.com/) timelines, [GSAP](https://gsap.com/) scenes, [Lottie](https://airbnb.io/lottie/) animations embedded as `<lottie-player>` — and the pack drives Chromium frame-by-frame via the upstream [HyperFrames CLI](https://github.com/heygen-com/hyperframes), then encodes the captured frames to MP4 with ffmpeg.

Two body modes work with **zero handler branching**:

- **Silent animation** — composition has no `<audio>` tag → MP4 is video-only.
- **Pre-mixed audio** — composition has an inline `<audio src="…">` → MP4 carries the audio track. Use this for chained `podcast.generate` → `hyperframes.render` workflows: the podcast pack returns a presigned audio URL, your composition embeds it as `<audio src>`, the render pipeline picks it up automatically.

Two input modes work (mutually exclusive — pass exactly one):

- **`composition_html`** — a complete self-contained HTML document, scaffolded as `<projectDir>/index.html` (this pack's v0.13.0 mode; unchanged).
- **`project_artifact_key`** — a key into the artifact store referencing a gzipped tarball of a hyperframes project directory (`index.html` + `compositions/*.html` + `assets/` + `hyperframes.json` + …). Produced by `hyperframes.compose`'s scaffold mode when the caller picks an upstream `--example=<name>` instead of authoring HTML from scratch. Render downloads the tarball, extracts it in-sidecar, and runs `hyperframes render <project-dir>` against the multi-file shape the framework natively expects.

**Sizing is composable**: pick a `resolution` (1080p or 4K) and an `aspect_ratio` (16:9 landscape, 9:16 vertical, 1:1 square) independently — the pack resolves them to one of the upstream CLI's [resolution presets](https://hyperframes.heygen.com/packages/cli) and threads it through.

## Sidecar prerequisite

The pack runs inside the dedicated `helmdeck-sidecar-hyperframes` image. The control plane pulls it on first use; operators can pre-pull or pin a fork via env var:

```sh
export HELMDECK_SIDECAR_HYPERFRAMES=ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:latest
# or a forked image
export HELMDECK_SIDECAR_HYPERFRAMES=registry.internal/our-hyperframes:v1
```

To build locally:

```sh
make sidecar-hyperframes-build
```

Same convention as the Python / Node language sidecars — see [`docs/SIDECAR-LANGUAGES.md`](../../../SIDECAR-LANGUAGES.md).

## Composition expectations

The upstream HyperFrames CLI is **project-oriented** — it expects a directory containing an `index.html` plus optional metadata. The pack scaffolds this for you: your `composition_html` lands at `/tmp/helmdeck-hf/index.html` inside the sidecar, and that directory is passed as the CLI's project argument.

**Author the composition at the target aspect ratio.** Upstream's `--resolution` flag is an integer-multiple upscale knob (1080p → 4K via Chrome DPR), not a dimension setter. A composition authored at 1920×1080 with `aspect_ratio: "9:16"` will fail at the CLI level because the aspect ratios don't match. Match the composition's `<body>` / canvas dimensions to the aspect ratio you pass to the pack.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `composition_html` | `string` | one of | — | A complete self-contained HTML document. Scaffolded as `/tmp/helmdeck-hf/index.html`. Mutually exclusive with `project_artifact_key`. |
| `project_artifact_key` | `string` | one of | — | Key into the artifact store referencing a gzipped tarball of a hyperframes project (multi-file scaffold from `hyperframes.compose`'s scaffold mode). Mutually exclusive with `composition_html`. Render extracts the tarball under `/tmp/helmdeck-hf/` and runs the CLI against the project directory. |
| `resolution` | `string` | no | `"1080p"` | One of `"1080p"`, `"4k"`. (`720p` not supported — upstream CLI has no 720p preset.) |
| `aspect_ratio` | `string` | no | `"16:9"` | One of `"16:9"`, `"9:16"`, `"1:1"`. (`4:5` not supported — upstream CLI has no 4:5 preset.) |
| `fps` | `number` | no | `30` | Frames per second. Pack-side cap: 60. |
| `quality` | `string` | no | `"high"` | Upstream CLI preset: `"draft"`, `"standard"`, or `"high"`. |

### Resolution × aspect-ratio matrix

| | 16:9 (YouTube standard) | 9:16 (Shorts / TikTok / Reels) | 1:1 (Instagram feed) |
|---|---|---|---|
| **1080p** | 1920 × 1080 (`landscape`) | 1080 × 1920 (`portrait`) | 1080 × 1080 (`square`) |
| **4k**    | 3840 × 2160 (`landscape-4k`) | 2160 × 3840 (`portrait-4k`) | 2160 × 2160 (`square-4k`) |

The parenthesized name is the CLI preset the pack maps to. The pack's response includes `cli_preset_used` so you can trace what argument was sent to the subprocess.

### Validation

- Exactly one of `composition_html` or `project_artifact_key` must be non-empty. Both missing → `invalid_input` with the message naming both alternatives; both set → `invalid_input` with "mutually exclusive".
- For `project_artifact_key`: key must resolve in the artifact store, content must be non-empty, must extract via `tar -xzf` (malformed tarballs reject as `invalid_input` with the tar stderr surfaced), and the extracted root must contain `index.html`.
- `resolution` × `aspect_ratio` must be one of the six combinations above; unsupported tuples reject as `invalid_input` with a list of what's allowed.
- `fps` ≤ 60. Higher values reject as `invalid_input`.
- `quality` must be `"draft"`, `"standard"`, or `"high"`.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `video_artifact_key` | `string` | `hyperframes.render/<rand>.mp4`. Resolve via `/api/v1/artifacts/<key>`. |
| `video_size` | `number` | Bytes. Cap: 512 MiB (oversize compositions reject before upload). |
| `width` | `number` | Resolved viewport width (pixels). |
| `height` | `number` | Resolved viewport height (pixels). |
| `fps` | `number` | Echo of the rate used (defaulted to 30 if unset). |
| `aspect_ratio_used` | `string` | Echo of the resolved aspect ratio. |
| `resolution_used` | `string` | Echo of the resolved resolution preset. |
| `cli_preset_used` | `string` | The upstream CLI preset name (`landscape` / `portrait` / `square` ± `-4k`). |

## Examples

### Silent 5-second CSS-keyframe animation (YouTube standard)

```sh
curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "composition_html": "<!DOCTYPE html><html><head><style>body{margin:0;background:#000;width:1920px;height:1080px;}.box{position:absolute;width:200px;height:200px;background:#0ea5e9;animation:slide 5s linear forwards;}@keyframes slide{from{left:0;}to{left:80%;}}</style></head><body><div class=\"box\"></div></body></html>"
  }'
```

Output: 1920×1080 (default 1080p + 16:9 → `landscape` preset), no audio track.

### Vertical Shorts/TikTok (9:16)

```sh
curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -d '{
    "composition_html": "<!DOCTYPE html><html><head><style>body{margin:0;width:1080px;height:1920px;}</style></head><body>...</body></html>",
    "resolution": "1080p",
    "aspect_ratio": "9:16",
    "fps": 30
  }'
```

Output: 1080×1920 (`portrait` preset). Drops straight into TikTok / YouTube Shorts / Instagram Reels.

### Square Instagram feed (1:1)

```sh
curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -d '{
    "composition_html": "<!DOCTYPE html><html><head><style>body{margin:0;width:1080px;height:1080px;}</style></head><body>...</body></html>",
    "aspect_ratio": "1:1"
  }'
```

Output: 1080×1080 (`square` preset).

### Chained podcast → narrated video

```sh
# 1. Generate a podcast MP3.
POD=$(curl -s -X POST http://localhost:3000/api/v1/packs/podcast.generate \
  -d '{"speakers":{"alice":"voice-001"},"prompt":"60-second explainer about the Mandelbrot set","model":"openrouter/openai/gpt-4o-mini","duration_target_min":1}')
AUDIO_KEY=$(echo "$POD" | jq -r .audio_artifact_key)
AUDIO_URL=$(curl -s http://localhost:3000/api/v1/artifacts/$AUDIO_KEY | jq -r .url)

# 2. Embed the presigned URL in a composition and render.
curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -d "{
    \"composition_html\": \"<!DOCTYPE html><html><head><style>body{margin:0;width:1080px;height:1920px;}</style></head><body><div class='title'>Mandelbrot</div><audio src='$AUDIO_URL' autoplay></audio></body></html>\",
    \"aspect_ratio\": \"9:16\"
  }"
```

The rendered MP4 carries the narration track without any glue code in the pack.

## Scope and limits

| Constraint | Value | Why |
|---|---|---|
| Max video size | 512 MiB | Enforced before artifact upload. Larger output rejects as `handler_failed` pointing at [#201](https://github.com/tosin2013/helmdeck/issues/201) (v1.x long-form streaming track). |
| Supported resolution × aspect tuples | 6 (see matrix above) | Pack-side surface aligned with upstream CLI's preset set. |
| Max fps | 60 (pack-side cap) | Upstream CLI itself accepts up to 240; helmdeck caps at 60 because higher rates roughly linearly increase encode cost without obvious benefit for short-form/social content. File an issue if you need higher. |
| Memory | 4 GiB session | Chromium baseline + ffmpeg encode peak. |
| Wall-clock timeout | 60 min | Generous; 1080p × 60s typically finishes in 1-3 min. |

## Errors

| Code | When | Recovery |
|---|---|---|
| `invalid_input` | Missing `composition_html`; unsupported resolution × aspect tuple; `fps > 60`; `quality` not in {draft,standard,high} | Fix the input. |
| `session_unavailable` | Control plane couldn't acquire the hyperframes sidecar (image not pulled, image-mode disabled) | `make sidecar-hyperframes-build` or pull `ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:latest`. |
| `handler_failed` | HyperFrames CLI exit ≠ 0 (composition aspect mismatch, malformed HTML, encode failure), empty MP4 produced, or oversize MP4 | Inspect the message — it surfaces the upstream CLI's stderr (truncated to 4 KiB). |
| `artifact_failed` | Artifact-store upload failed | Operator-level — check the artifact backend's health. |

## Async behavior

`hyperframes.render` is **async by default** — calls route through the SEP-1686 task envelope, so the JSON-RPC request returns a job ID immediately. Poll `pack.status` until `state == "completed"`, then `pack.result` to retrieve the output. For HTTP REST callers, follow the same `/api/v1/jobs/<id>` pattern documented in [`docs/integrations/webhooks.md`](../../../integrations/webhooks.md).

The render pipeline emits progress at:

- `0%` — scaffolding hyperframes project (write composition to sidecar)
- `10%` — beginning HyperFrames render
- `90%` — reading rendered MP4
- `95%` — uploading artifact
- `100%` — done

## Related

- [`hyperframes.compose`](./compose.md) — generate the `composition_html` from a plain-language description (so you don't hand-author the `data-*`/`window.__timelines` contract). `builtin.prompt-video` chains compose → render.
- [`podcast.generate`](../podcast/generate.md) — pairs naturally: podcast MP3 → embed presigned URL → narrated video.
- [`slides.narrate`](../slides/narrate.md) — different shape: Marp slide deck → narrated MP4. `slides.narrate` is the "slide presentation" pack; `hyperframes.render` is the "freeform animation" pack.
- [`image.generate`](../image/generate.md) — hero artwork for compositions (embed the resulting artifact's presigned URL as an `<img src>` in your HTML).
- [#200](https://github.com/tosin2013/helmdeck/issues/200) — the implementation issue this pack ships against.
- [#201](https://github.com/tosin2013/helmdeck/issues/201) — long-form streaming (>12 min, >512 MiB). v1.x track.
- Upstream CLI: [github.com/heygen-com/hyperframes](https://github.com/heygen-com/hyperframes)
