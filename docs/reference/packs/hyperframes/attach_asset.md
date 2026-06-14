---
title: hyperframes.attach_asset
description: Splice an A-roll asset (image or video) into a hyperframes scaffold project. Third (optional) step in the scaffolded-video pipeline.
keywords: [helmdeck, hyperframes, asset, image, video, a-roll, MCP]
---

# `hyperframes.attach_asset`

Take a project tarball (from [`hyperframes.scaffold`](./scaffold.md) or [`hyperframes.interpolate`](./interpolate.md)) plus an asset artifact key (from `image.generate`, `stock.search`, or any pack that uploaded an image/video to the artifact store), splice the asset into the target div in `index.html`, and re-upload the modified project. Third (optional) step in the four-pack chain:

```
hyperframes.scaffold     → scaffolded project (no A-roll)
       ↓
hyperframes.interpolate  → topic-specific text content
       ↓
hyperframes.attach_asset → THIS PACK: A-roll image / video spliced in
       ↓
hyperframes.render       → MP4
```

**Why optional**: many scaffolds work fine without A-roll content (typography-focused examples like `kinetic-type`, `morph-text`; data-viz examples like `nyt-graph`, `decision-tree`). Skip this pack and the target div renders empty — fine for those genres. Reach for it when the scaffold expects a main subject (`swiss-grid`, `product-promo`, `play-mode`) and you want the LLM-generated topic to be illustrated visually.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `project_artifact_key` | `string` | yes | — | Key from `hyperframes.scaffold` or `hyperframes.interpolate`. The pack downloads the tarball, modifies it, and re-uploads under a new key. |
| `asset_artifact_key` | `string` | yes | — | Key for the image / video bytes. Must be in the artifact store. **URL fetching is not supported in v1** — chain `http.fetch` upstream if your asset is URL-only. |
| `target_id` | `string` | no | `short_mag_cut_frame` | The HTML `id` of the target `<div>` in `index.html`. Accepts `"foo"` or `"#foo"` — the leading `#` is stripped. The default matches upstream's canonical A-roll slot id (visible in `swiss-grid`'s `index.html` as `<div id="short_mag_cut_frame">`). |

### Supported asset content types

The pack checks the artifact's `ContentType` field; unsupported types reject with `invalid_input`.

| Content type | Detected as | File extension in tarball |
|---|---|---|
| `image/png` | `image` | `.png` |
| `image/jpeg` | `image` | `.jpg` |
| `image/gif` | `image` | `.gif` |
| `image/webp` | `image` | `.webp` |
| `image/svg+xml` | `image` | `.svg` |
| `video/mp4` | `video` | `.mp4` |
| `video/webm` | `video` | `.webm` |
| `video/quicktime` | `video` | `.mov` |

### Size cap

Asset bytes are capped at **50 MiB**. Larger A-rolls usually mean the operator wanted a full video; in that case the asset should be encoded shorter, not bigger.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `project_artifact_key` | `string` | NEW key — the modified project tarball. Feeds `hyperframes.render`. |
| `original_project_artifact_key` | `string` | Echo of the input key. |
| `asset_kind` | `string` | `"image"` or `"video"` — drives the emitted HTML element. |
| `asset_filename` | `string` | `aroll-<hash><ext>` — content-addressed by SHA-256 prefix, so identical asset bytes always produce the same filename. |
| `asset_size` | `number` | Bytes. |
| `target_id_used` | `string` | The canonicalized target id (without `#`). |

## Behavior

### Asset embedded in the tarball, not inlined as data: URI
The asset bytes are added to the project tarball at `assets/aroll-<hash>.<ext>`, and the target div's content is replaced with `<img src="assets/aroll-...">` or `<video src="assets/aroll-..." muted></video>`. This matches upstream's `assets/` convention and keeps `index.html` small.

### Content-addressed filename for dedup
Asset filenames are derived from a SHA-256 prefix of the bytes. The same asset attached to different projects gets the same filename — convenient for caching and round-trip identity.

### Videos are emitted with `muted` per upstream convention
Per upstream's `AGENTS.md`: *"Videos use `muted` with a separate `<audio>` element for the audio track."* The narration audio (from `podcast.generate` and embedded elsewhere in the composition) does the actual sound; the A-roll video stays muted.

### URL fetch not supported in v1
The pack only accepts an artifact key, not a URL. If your asset is URL-only:
1. Use `http.fetch` (or chain through `image.generate` / `stock.search` which already produce keys).
2. Pass the resulting artifact key as `asset_artifact_key`.

This keeps the pack focused — egress validation and URL fetching are handled by the existing fetch packs.

## Validation & errors

| Failure | Code | Notes |
|---|---|---|
| Missing `project_artifact_key` / `asset_artifact_key` | `invalid_input` | Specific message per field. |
| Project / asset key not in store | `invalid_input` | Surfaces store error. |
| Empty asset bytes | `invalid_input` | Catches truncated uploads. |
| Asset > 50 MiB | `invalid_input` | Includes size in the error. |
| Unsupported content type | `invalid_input` | Lists supported types. |
| Project missing `index.html` | `invalid_input` | Not a valid scaffold. |
| Target div id not in `index.html` | `invalid_input` | Suggests trying a different `target_id` or scaffold. |
| Artifact upload failure | `artifact_failed` | Backend issue. |

## Chaining example

Hand-chained:

```sh
# After scaffold + interpolate returned KEY_PROJECT:
IMAGE=$(curl -X POST .../packs/image.generate/v1/execute \
  -d '{"prompt":"abstract neural network visualization, blue palette"}' | jq -r .output.image_artifact_key)

KEY_WITH_AROLL=$(curl -X POST .../packs/hyperframes.attach_asset/v1/execute \
  -d "{\"project_artifact_key\":\"$KEY_PROJECT\",\"asset_artifact_key\":\"$IMAGE\"}" | jq -r .output.project_artifact_key)

curl -X POST .../packs/hyperframes.render/v1/execute \
  -d "{\"project_artifact_key\":\"$KEY_WITH_AROLL\"}"
```

Pipeline-orchestrated (recommended): the `builtin.scaffolded-narrated-video` pipeline (PR 7) handles this — operators pass `aroll_prompt` (or omit it for no A-roll), the pipeline chains `image.generate` → `attach_asset` automatically.

## See also

- [`hyperframes.scaffold`](./scaffold.md) — produces the initial `project_artifact_key`.
- [`hyperframes.interpolate`](./interpolate.md) — rewrites text content; the typical predecessor to this pack.
- [`hyperframes.render`](./render.md) — consumes the final tarball, produces the MP4.
- [`image.generate`](../image/generate.md) — fal.ai-backed text-to-image; the common A-roll source.
- [`stock.search`](../stock/search.md) — Pexels-backed stock photos; the other common A-roll source.
- Issue [#503](https://github.com/tosin2013/helmdeck/issues/503) — architectural decision behind the four-pack split.
