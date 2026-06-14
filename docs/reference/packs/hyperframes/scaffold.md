---
title: hyperframes.scaffold
description: Scaffold a hyperframes composition from one of the upstream framework's 140+ pre-built examples (swiss-grid, decision-tree, code-snippet-dark-modern, etc.). First link in the scaffolded-video pipeline.
keywords: [helmdeck, hyperframes, scaffold, video, mp4, swiss-grid, decision-tree, kinetic-type, MCP]
---

# `hyperframes.scaffold`

Pick one of upstream HyperFrames' 140+ pre-built [example compositions](https://hyperframes.heygen.com/examples) and get back a packaged project tarball ready to be customized and rendered. This is the first pack in the four-pack scaffold-video pipeline:

```
hyperframes.scaffold  → scaffolded project (generic upstream content)
       ↓
hyperframes.interpolate → LLM rewrites visible text content (titles, stats, transcript)
       ↓
hyperframes.attach_asset → splice in an A-roll image / video (from image.generate or stock.search)
       ↓
hyperframes.render → produces the final MP4
```

Each pack composes individually. The `builtin.scaffolded-narrated-video` pipeline chains them automatically.

**Why this exists**: Tier C models (free / weak open-weight, `gpt-oss-120b:free`, `gemma`, etc.) reliably wire packs together but struggle to author HTML/CSS/GSAP from scratch — they produce structurally-valid but visually-flat output. Borrowing the visual creativity from upstream's example catalog moves the creative burden from the model to the framework; the LLM only needs to interpolate content, not invent design. See [`upstream-spec-drift.md`](/blog/upstream-spec-drift) for the empirical session that drove this design.

## Sidecar prerequisite

Runs inside `helmdeck-sidecar-hyperframes` — same image as `hyperframes.render`. Pulls `npx hyperframes init` from the upstream-pinned version (currently `0.6.97`).

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `example` | `string` | yes | — | Upstream example name. Run with an invalid name to surface the full registry in the error message. Common picks: `swiss-grid`, `decision-tree`, `code-snippet-dark-modern`, `kinetic-type`, `vignelli`, `warm-grain`, `tiktok-follow`, `caption-pill-karaoke`. |
| `resolution` | `string` | no | `"1080p"` | One of `"1080p"`, `"4k"`. |
| `aspect_ratio` | `string` | no | `"16:9"` | One of `"16:9"`, `"9:16"`, `"1:1"`. |

### Resolution × aspect_ratio matrix

Same as `hyperframes.render` — the scaffold pack forwards the resolved CLI preset (`landscape` / `portrait` / `square` ± `-4k`) so the scaffolded composition matches the eventual render dimensions.

### Picking an example

Upstream's registry spans every short-form-video genre. Some recommendations by intent:

| Intent | Common picks |
|---|---|
| Technical explainer with code | `code-snippet-dark-modern`, `swiss-grid`, `decision-tree`, `nyt-graph` |
| Data viz / charts / maps | `data-chart`, `world-map`, `flowchart`, `us-map-flow` |
| Social platforms | `tiktok-follow`, `instagram-follow`, `yt-lower-third`, `x-post`, `reddit-post`, `spotify-card` |
| Caption / typography focus | `kinetic-type`, `morph-text`, `caption-pill-karaoke`, `caption-glitch-rgb` |
| Storytelling / narrative | `warm-grain`, `vignelli`, `play-mode`, `product-promo` |
| Quick fallback / minimal | `blank` (empty canvas, no template content) |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `project_artifact_key` | `string` | `hyperframes.scaffold/<rand>-<example>-scaffold.tar.gz`. Feeds `hyperframes.interpolate`, `hyperframes.attach_asset`, or `hyperframes.render` directly. |
| `example_used` | `string` | Echo of the input. |
| `cli_preset_used` | `string` | Resolved upstream `--resolution` preset name. |
| `width` | `number` | Resolved viewport width (pixels). |
| `height` | `number` | Resolved viewport height (pixels). |
| `aspect_ratio_used` | `string` | Echo of the resolved aspect ratio. |
| `resolution_used` | `string` | Echo of the resolved resolution. |
| `editable_slots` | `object` | Manifest naming the editable text files in the scaffold. Shape: `{ compositions: [{path, size}, ...], other_files: [...] }`. `hyperframes.interpolate` consumes this. |

## Validation & errors

| Failure | Code | Notes |
|---|---|---|
| `example` empty or missing | `invalid_input` | Friendly error suggesting common picks. |
| `resolution` × `aspect_ratio` not in matrix | `invalid_input` | Lists supported combinations. |
| `example` not in upstream registry | `invalid_input` | Init script's exit 1; full registry listed on stderr is surfaced in the error message. |
| Init script other failures (exit 2-5) | `handler_failed` | Real pack/script failure (missing deps, scaffold malformed, init crashed, tar failed). |
| Empty tarball after init | `handler_failed` | Pack bug — file an issue. |
| Artifact upload failure | `artifact_failed` | Backend issue (store full, network). |

## Chaining example

Hand-chained:

```sh
# 1. Scaffold from upstream's swiss-grid example.
SCAFFOLD=$(curl -X POST .../packs/hyperframes.scaffold/v1/execute \
  -d '{"example": "swiss-grid"}' | jq -r .output.project_artifact_key)

# 2. Interpolate user content into the scaffolded slots.
INTERP=$(curl -X POST .../packs/hyperframes.interpolate/v1/execute \
  -d "{\"project_artifact_key\":\"$SCAFFOLD\", \"description\":\"eBPF observability — survey of 200 SREs\", \"model\":\"openrouter/openai/gpt-oss-120b:free\"}" | jq -r .output.project_artifact_key)

# 3. Render to MP4.
curl -X POST .../packs/hyperframes.render/v1/execute \
  -d "{\"project_artifact_key\":\"$INTERP\"}"
```

Pipeline-orchestrated (recommended):

```sh
curl -X POST .../pipelines/builtin.scaffolded-narrated-video/run \
  -d '{"description":"...", "example":"swiss-grid", "model":"openrouter/openai/gpt-oss-120b:free"}'
```

## Sizing the sidecar

| Field | Default | Notes |
|---|---|---|
| Image | `ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:latest` | Override with `HELMDECK_SIDECAR_HYPERFRAMES`. |
| Memory | `1g` | Init step is light I/O. |
| Timeout | 5 min | Covers worst-case init for big examples. |

## See also

- [`hyperframes.interpolate`](./interpolate.md) — LLM-driven text content rewriting.
- [`hyperframes.attach_asset`](./attach_asset.md) — A-roll image / video splicing.
- [`hyperframes.render`](./render.md) — Project tarball → MP4.
- Pipeline: `builtin.scaffolded-narrated-video` — full chain.
- Issue [#503](https://github.com/tosin2013/helmdeck/issues/503) — the architectural decision behind Path B / Option C.
- Upstream [HyperFrames](https://github.com/decision-crafters/hyperframes) — framework docs.
