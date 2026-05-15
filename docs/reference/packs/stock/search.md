---
title: stock.search
description: Search Pexels for stock photos and download the top results into the artifact store. Output chains into slides / blog / podcast / hyperframes packs the same way image.generate does.
keywords: [helmdeck, stock, pexels, photo, image, search, slides, blog, podcast, hyperframes, MCP]
---

# `stock.search`

Search Pexels for stock photography matching a query and download the top N results into the helmdeck artifact store. The downloaded photos chain into every content pack — `slides.render`, `slides.narrate`, `blog.publish`, `podcast.generate`, `hyperframes.render` — through the existing `*_artifact_key` chained inputs, **same contract as [`image.generate`](../image/generate.md)**. Downstream packs don't care whether the image was *generated* via fal.ai or *photographed* via Pexels — the artifact-store key is the only thing they need.

Use this when:

- You want real photography rather than AI-generated art (corporate decks, customer-facing blog feature images, podcast covers that need to feel "real").
- Generated images would be over-the-top for the use case.
- The cost or licensing of generated images is a problem (Pexels is free for commercial use).

## Setup prerequisite

Get a free API key at <https://www.pexels.com/api/>. Then either:

**Option A — env var (simplest)**

```sh
# deploy/compose/.env.local
HELMDECK_PEXELS_API_KEY=your_pexels_key_here
```

The env var auto-hydrates into the vault as `pexels-key` on startup.

**Option B — vault entry (preferred for multi-tenant)**

```sh
curl -X POST http://localhost:3000/api/v1/vault/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "name": "pexels-key",
    "type": "api_key",
    "host_pattern": "api.pexels.com",
    "plaintext": "your_pexels_key_here"
  }'
```

Pexels's default rate limit is 200 requests/hour (free tier). High-volume users can request an override at the API portal.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `query` | `string` | yes | — | Search terms. Plain English; Pexels does its own tokenization. |
| `engine` | `string` | no | `"pexels"` | Closed set; day 1 only `"pexels"`. Future: `"unsplash"`, `"pixabay"`. |
| `count` | `number` | no | `1` | Number of results to download. Range 1–4 (mirrors `image.generate`'s cap). |
| `orientation` | `string` | no | (any) | Pexels filter: `"landscape"` / `"portrait"` / `"square"`. |
| `size` | `string` | no | (any) | Minimum-size filter: `"large"` / `"medium"` / `"small"`. |
| `color` | `string` | no | (any) | Color filter. Hex like `"#ff0000"` or named like `"red"`, `"blue"`, etc. |
| `media_type` | `string` | no | `"photo"` | Day 1: `"photo"` only. `"video"` reserved for follow-up PR. |
| `credential` | `string` | no | `"pexels-key"` | Vault credential name override. |

### Validation

- `query` must be non-empty.
- `engine` must be `"pexels"` day 1. Unknown engines reject as `invalid_input`.
- `count` must be 1–4.
- `orientation` / `size`, when supplied, must be in their closed sets above.
- `media_type` must be `"photo"` day 1.
- Missing credential rejects as `invalid_input` with a message pointing at the setup instructions.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `engine` | `string` | Echo (`"pexels"`). |
| `artifact_keys` | `array<string>` | One key per downloaded photo. Pass these into downstream packs. |
| `results` | `array<object>` | Per-photo metadata. See below. |
| `query_used` | `string` | Echo of the input (helps the agent debug normalization differences). |

Each item in `results`:

| Field | Type | Notes |
|---|---|---|
| `id` | `number` | Pexels photo ID. Stable across the Pexels API surface. |
| `photographer` | `string` | Photographer's name (use in attribution UI). |
| `photographer_url` | `string` | Photographer's Pexels profile URL. |
| `source_url` | `string` | Pexels page for the photo. Link here for attribution. |
| `width` / `height` | `number` | Pixels. |
| `alt_text` | `string` | Pexels-provided alt text (use for accessibility). |
| `artifact_key` | `string` | Same key that appears in `artifact_keys[]`, duplicated here so callers walking metadata don't have to zip indices. |

## Examples

### Find one mountain sunrise photo

```sh
curl -X POST http://localhost:3000/api/v1/packs/stock.search \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"mountain sunrise"}'
```

```json
{
  "engine": "pexels",
  "artifact_keys": ["stock.search/photo-000.jpg"],
  "results": [
    {
      "id": 1545743,
      "photographer": "Eberhard Grossgasteiger",
      "photographer_url": "https://www.pexels.com/@eberhardgross",
      "source_url": "https://www.pexels.com/photo/mountain-1545743",
      "width": 1920,
      "height": 1080,
      "alt_text": "Sunrise over a mountain range",
      "artifact_key": "stock.search/photo-000.jpg"
    }
  ],
  "query_used": "mountain sunrise"
}
```

### Four portrait photos for a Shorts background

```sh
curl -X POST http://localhost:3000/api/v1/packs/stock.search \
  -d '{
    "query": "futuristic city night",
    "count": 4,
    "orientation": "portrait",
    "size": "large"
  }'
```

### Chain into a blog post's feature image

```sh
# 1. Find the photo.
KEY=$(curl -s -X POST http://localhost:3000/api/v1/packs/stock.search \
  -d '{"query":"office collaboration"}' | jq -r .artifact_keys[0])

# 2. Use it as blog.publish's feature_image_artifact_key.
curl -X POST http://localhost:3000/api/v1/packs/blog.publish \
  -d "{
    \"title\": \"How we ship\",
    \"body\": \"...\",
    \"format\": \"markdown\",
    \"destination\": \"ghost\",
    \"feature_image_artifact_key\": \"$KEY\"
  }"
```

### Chain into a podcast cover

```sh
KEY=$(curl -s -X POST http://localhost:3000/api/v1/packs/stock.search \
  -d '{"query":"vintage microphone", "orientation":"square"}' | jq -r .artifact_keys[0])

curl -X POST http://localhost:3000/api/v1/packs/podcast.generate \
  -d "{
    \"speakers\": {\"alice\":\"voice-001\"},
    \"prompt\": \"5-minute explainer on espresso machines\",
    \"model\": \"openrouter/openai/gpt-4o-mini\",
    \"cover_image_artifact_key\": \"$KEY\"
  }"
```

### Chain into a slide hero

```sh
KEY=$(curl -s -X POST http://localhost:3000/api/v1/packs/stock.search \
  -d '{"query":"abstract data visualization", "orientation":"landscape"}' | jq -r .artifact_keys[0])

# Use the artifact key as a slides.render input. The slide-level
# integration depends on which slides field the agent picks (see
# slides/render.md §hero-image-* chained inputs).
```

### Chain into a hyperframes composition

```sh
KEY=$(curl -s -X POST http://localhost:3000/api/v1/packs/stock.search \
  -d '{"query":"ocean waves", "orientation":"portrait"}' | jq -r .artifact_keys[0])
URL=$(curl -s http://localhost:3000/api/v1/artifacts/$KEY | jq -r .url)

curl -X POST http://localhost:3000/api/v1/packs/hyperframes.render \
  -d "{
    \"composition_html\": \"<!DOCTYPE html><html><body style='background: url(\\\"$URL\\\"); width:1080px;height:1920px;'></body></html>\",
    \"aspect_ratio\": \"9:16\"
  }"
```

## Errors

| Code | When | Recovery |
|---|---|---|
| `invalid_input` | Missing `query`; unknown `engine`; `count` out of 1–4 range; bad `orientation`/`size`; `media_type` other than `"photo"`; missing credential; pexels 401/403 (auth) | Fix the input or the credential. The error message names the actionable bit. |
| `handler_failed` | Pexels 429 (rate limit), 5xx (upstream), empty result set, photo download failed, photo > 32 MiB | Retry with a different query (empty results) or back off (rate limit). |
| `artifact_failed` | Artifact-store upload failed | Operator-level — check the artifact backend's health. |

## Scope cutoffs (day 1)

- **Photos only.** `media_type: "video"` reserved for a follow-up PR — Pexels videos are MP4 + thumbnails and need a separate artifact-handling pass.
- **Pexels engine only.** `engine: "unsplash"` and `engine: "pixabay"` are reserved field values; community PRs welcome.
- **Attribution metadata returned, but auto-credit-injection is the agent's job.** The pack surfaces photographer + source URL; how to credit them in the final deliverable (slide footer, blog credits, podcast description) is the agent's call. A future chained-input on the content packs could auto-credit; that's a separate issue.

## Licensing notes

Pexels photos are free for commercial use without attribution required, but the platform encourages crediting photographers and we surface the metadata to make it easy. See <https://www.pexels.com/license/> for the full terms — the spirit is "use freely, credit when you can." If you're shipping helmdeck output to a customer-facing surface (a podcast cover, a blog feature image), threading the `photographer` / `source_url` into the published artifact's metadata is the polite default.

## See also

- [`image.generate`](../image/generate.md) — sibling pack for *generated* art. Same chained-input contract.
- Source: [`internal/packs/builtin/stock_search.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/stock_search.go).
- Issue: [#217](https://github.com/tosin2013/helmdeck/issues/217).
- Pexels API docs: <https://www.pexels.com/api/documentation/>.
