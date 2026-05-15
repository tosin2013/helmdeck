---
title: hyperframes.render
description: Render an HTML/CSS/JS composition into a deterministic MP4 via Chromium BeginFrame + ffmpeg. Standard YouTube (16:9), vertical Shorts/TikTok/Reels (9:16), square Instagram (1:1), and portrait Instagram feed (4:5) all from one input surface.
keywords: [helmdeck, hyperframes, video, mp4, youtube, shorts, tiktok, instagram, reels, animation, MCP]
---

# `hyperframes.render`

Turn a self-contained HTML/CSS/JS composition into a deterministic MP4 video. The composition is anything a browser can render — CSS-keyframe animations, [Anime.js](https://animejs.com/) timelines, [GSAP](https://gsap.com/) scenes, [Lottie](https://airbnb.io/lottie/) animations embedded as `<lottie-player>` — and the pack drives Chromium frame-by-frame via the [HyperFrames CLI](https://github.com/heygen-com/hyperframes), then encodes the captured frames to MP4 with ffmpeg.

Two body modes work with **zero handler branching**:

- **Silent animation** — composition has no `<audio>` tag → MP4 is video-only.
- **Pre-mixed audio** — composition has an inline `<audio src="…">` → MP4 carries the audio track. Use this for chained `podcast.generate` → `hyperframes.render` workflows: the podcast pack returns a presigned audio URL, your composition embeds it as `<audio src>`, the render pipeline picks it up automatically.

**Sizing is composable**: pick a `resolution` and an `aspect_ratio` independently — the pack resolves them to a pixel-perfect `width × height` and threads those through both the Chromium viewport AND ffmpeg's encode pass.

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

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `composition_html` | `string` | yes | — | A complete self-contained HTML document. May reference external assets via http(s)/data URIs. |
| `resolution` | `string` | no | `"1080p"` | One of `"720p"`, `"1080p"`, `"4k"`. Controls the **shorter** dimension. |
| `aspect_ratio` | `string` | no | `"16:9"` | One of `"16:9"`, `"9:16"`, `"1:1"`, `"4:5"`. |
| `fps` | `number` | no | `30` | Frames per second. Cap: 60. |
| `duration_s` | `number` | no | (inferred) | Composition length in seconds. When omitted, HyperFrames reads the composition's `document.animationend` / total animation duration. |
| `quality` | `string` | no | `"high"` | HyperFrames encode preset (`"low"`, `"medium"`, `"high"`, `"max"`). Higher is slower + larger. |

### Resolution × aspect-ratio matrix

| | 16:9 (standard) | 9:16 (Shorts / TikTok / Reels) | 1:1 (IG feed) | 4:5 (IG feed-portrait) |
|---|---|---|---|---|
| **720p**  | 1280 × 720  | 720 × 1280  | 720 × 720   | 720 × 900    |
| **1080p** | 1920 × 1080 | 1080 × 1920 | 1080 × 1080 | 1080 × 1350  |
| **4k**    | 3840 × 2160 | 2160 × 3840 | 2160 × 2160 | 2160 × 2700  |

The "p" number is the shorter side, so `1080p + 9:16 = 1080×1920` matches what platform-aware operators expect from "1080p Shorts."

### Validation

- `composition_html` must be non-empty.
- `resolution` must be in the closed set above. Unknown values reject as `invalid_input`.
- `aspect_ratio` must be in the closed set above. Same handling.
- `fps` ≤ 60. Higher values reject as `invalid_input`.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `video_artifact_key` | `string` | `hyperframes.render/<rand>.mp4`. Resolve via `/api/v1/artifacts/<key>`. |
| `video_size` | `number` | Bytes. Cap: 512 MiB (oversize compositions reject before upload). |
| `width` | `number` | Resolved viewport width (pixels). |
| `height` | `number` | Resolved viewport height (pixels). |
| `fps` | `number` | Echo of the rate used (defaulted to 30 if unset). |
| `duration_s` | `number` | Echoed when supplied on input. When the producer pipeline infers from the composition, this field is omitted. |
| `aspect_ratio_used` | `string` | Echo of the resolved aspect ratio. |
| `resolution_used` | `string` | Echo of the resolved resolution preset. |

## Examples

### Silent 5-second CSS-keyframe animation (YouTube standard)

```sh
curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "composition_html": "<!DOCTYPE html><html><head><style>body{margin:0;background:#000;}.box{position:absolute;width:200px;height:200px;background:#0ea5e9;animation:slide 5s linear forwards;}@keyframes slide{from{left:0;}to{left:80%;}}</style></head><body><div class=\"box\"></div></body></html>",
    "duration_s": 5
  }'
```

Output: 1920×1080 (default 1080p + 16:9), ~5 seconds, no audio track.

### Vertical Shorts/TikTok (9:16)

```sh
curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -d '{
    "composition_html": "<!DOCTYPE html>...",
    "resolution": "1080p",
    "aspect_ratio": "9:16",
    "fps": 30
  }'
```

Output: 1080×1920. Drops straight into TikTok / YouTube Shorts / Instagram Reels.

### Square Instagram feed (1:1)

```sh
curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -d '{
    "composition_html": "<!DOCTYPE html>...",
    "aspect_ratio": "1:1"
  }'
```

Output: 1080×1080 (default 1080p + 1:1).

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
    \"composition_html\": \"<!DOCTYPE html><html><head></head><body><div class='title'>Mandelbrot</div><audio src='$AUDIO_URL' autoplay></audio></body></html>\",
    \"aspect_ratio\": \"9:16\"
  }"
```

The rendered MP4 carries the narration track without any glue code in the pack.

## Scope and limits

| Constraint | Value | Why |
|---|---|---|
| Max video size | 512 MiB | Enforced before artifact upload. Larger output rejects as `handler_failed` pointing at [#201](https://github.com/tosin2013/helmdeck/issues/201) (v1.x long-form streaming track). |
| Max composition length | ≤ 12 min @ 1080p (typical) | Bound by encode pass + memory; not a hard CLI limit. |
| Max fps | 60 | Pack-side cap. |
| Memory | 4 GiB session | Chromium baseline + ffmpeg encode peak. |
| Wall-clock timeout | 60 min | Generous; 1080p × 60 s typically finishes in 1-3 min. |
| Audio formats | Anything `<audio>` plays | The producer pipeline captures the realized audio track; you don't pre-mix on the host. |

## Errors

| Code | When | Recovery |
|---|---|---|
| `invalid_input` | Missing `composition_html`, unknown `resolution`/`aspect_ratio`, `fps > 60` | Fix the input. |
| `session_unavailable` | The control plane couldn't acquire the hyperframes sidecar (image not pulled, image-mode disabled) | `make sidecar-hyperframes-build` or pull `ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:latest`. |
| `handler_failed` | HyperFrames CLI exit ≠ 0, empty MP4 produced, oversize MP4 | Inspect the message — it surfaces the producer pipeline's stderr (truncated to 4 KiB). |
| `artifact_failed` | Artifact-store upload failed | Operator-level — check the artifact backend's health. |

## Async behavior

`hyperframes.render` is **async by default** — calls route through the SEP-1686 task envelope, so the JSON-RPC request returns a job ID immediately. Poll `pack.status` until `state == "completed"`, then `pack.result` to retrieve the output. For HTTP REST callers, follow the same `/api/v1/jobs/<id>` pattern documented in [`docs/integrations/webhooks.md`](../../../integrations/webhooks.md).

The render pipeline emits progress at:

- `0%` — writing composition to sidecar
- `10%` — beginning HyperFrames render
- `90%` — reading rendered MP4
- `95%` — uploading artifact
- `100%` — done

## Related

- [`podcast.generate`](../podcast/generate.md) — pairs naturally: podcast MP3 → embed presigned URL → narrated video.
- [`slides.narrate`](../slides/narrate.md) — different shape: Marp slide deck → narrated MP4. `slides.narrate` is the "slide presentation" pack; `hyperframes.render` is the "freeform animation" pack.
- [`image.generate`](../image/generate.md) — hero artwork for compositions (embed the resulting artifact's presigned URL as a `<img src>` in your HTML).
- [#200](https://github.com/tosin2013/helmdeck/issues/200) — the implementation issue this pack ships against.
- [#201](https://github.com/tosin2013/helmdeck/issues/201) — long-form streaming (>12 min, >512 MiB). v1.x track.
