---
slug: content-packs-grow-images
title: "Content packs grow images: one prompt, four packs, zero round-trips"
authors: [tosin]
tags: [field-report, agent-architecture, content-packs]
description: v0.12.0 chains image.generate into podcast covers, slide hero artwork, and blog feature images. Each chained pack saves a registry round-trip and an audit-log entry per call — and the agent never has to remember which model to use.
date: 2026-05-12
---

## The friction

Through v0.11.0, the canonical recipe for a podcast cover was:

```text
agent → podcast.generate (with generate_cover_prompt:true)
     → reads cover_image_prompt out of the response
     → image.generate(prompt: that-string)
     → reads image_artifact_key
     → pastes URL into the publish step
```

Four pack calls, two registry round-trips, two audit-log entries, two LLM cost-per-tool-call decisions on the agent's side. And the agent has to *remember* which model to use for the cover — fal.ai has a dozen, all with different cost/quality trade-offs.

<!-- truncate -->

For one cover, that's fine. For an agent generating ten podcast episodes in a workflow, the per-cover overhead becomes the dominant cost.

## The fix

v0.12.0's #146 chains `image.generate` directly into the four content packs that need imagery:

| Pack | New input | What it does |
|---|---|---|
| `podcast.generate` | `cover_image: bool` | auto-generates cover from the script |
| `slides.render` | `hero_image_prompt: string` | inlines an `<img data:image/png;base64,…>` before slide 1 |
| `slides.narrate` | `hero_image_prompt: string` | inlines INTO slide 1 (so the per-slide TTS pipeline still sees content) |
| `blog.publish` | `feature_image_artifact_key` OR `hero_image: bool` | for Ghost, uploads to `/images/upload/` then stamps `feature_image` |

Now the recipe is one call:

```bash
curl -X POST /api/v1/packs/podcast.generate -d '{
  "speakers": {"Alex": "v1"},
  "script":   [...],
  "theme":    "solo-essay",
  "cover_image": true
}'
```

Output gains `cover_image_artifact_key` alongside `audio_artifact_key`. The cover artifact lands under the **chained pack's** namespace (`podcast.generate/image-000.png`) rather than `image.generate/`'s — the chained pack owns its own outputs, which matters for retention and audit.

## How the chain stays cheap

The first commit of #146a (PR #165) extracts `RunImageGen` out of `image.generate`'s handler into a reusable Go function. The four chaining PRs call it directly:

```go
res, perr := RunImageGen(ctx, ec, vault, eg, ImageGenRequest{
    Prompt: coverPrompt,
    Model:  imageGenDefaultModel,  // fal-ai/flux/schnell, $0.003/image
})
if perr != nil { return nil, perr }
out["cover_image_artifact_key"] = res.ArtifactKeys[0]
```

No HTTP round-trip back through `/api/v1/packs/image.generate`. No JSON serialise-then-parse of the input/output schemas. No second audit-log entry. The chained call is a function invocation, not a registry dispatch.

The artifact still lands in the durable artifact store (fal.ai signed URLs expire ~1h), so the agent can fetch it later through `/api/v1/artifacts/<key>` without any extra plumbing.

## Sibling: `helmdeck://image-models`

The other half of v0.12.0's image-gen polish is #158: a new MCP resource at `helmdeck://image-models` that mirrors v0.11.0's `helmdeck://voices`. An agent's MCP client lists 7 curated fal.ai models with cost, p50 latency, supports-seed, supports-image-size, max resolution, and capability tags:

```json
{
  "model_id": "fal-ai/flux/schnell",
  "display_name": "FLUX schnell",
  "approx_cost_per_image_usd": 0.003,
  "p50_latency_s": 2,
  "supports_seed": true,
  "capabilities": ["photorealistic", "fast", "default"],
  "notes": "Fastest and cheapest in the FLUX family. helmdeck's default."
}
```

Before #158 an agent picking a model had to leave helmdeck and browse fal.ai's site. Now it lists `mcp://helmdeck/image-models` and picks the right one based on the budget for that specific job.

## When chaining is the wrong call

A few cases where you still want to call `image.generate` separately:

- **Iterating on the prompt.** If you're tuning the cover artwork, you don't want every iteration to also synthesise audio. Generate cover via `image.generate` standalone, accept it, then call `blog.publish` with `feature_image_artifact_key=<the-good-one>`.
- **Reusing one image across packs.** A multi-tenant workflow that pairs the same cover with five different podcast episodes wants one image and five podcast calls, not five generations of the same image. Cache the artifact key, pass it explicitly.
- **Different model per pack.** A workflow that wants `fal-ai/flux-pro/v1.1` for blog covers (production quality) but `fal-ai/flux/schnell` for slide hero art (fast preview) can pass `cover_image_model` / `hero_image_model` per call to override.

The chain isn't a replacement for `image.generate` — it's a convenience for the 80% case where the cover-prompt-to-image step is exactly what the agent would have done manually anyway.

## What's in the box

v0.12.0 ships:

- The four chained inputs across the content packs (#146)
- `helmdeck://image-models` MCP resource + `fal-key` env-hydrate (#158)
- ~20 new tests covering each chain with stubbed fal.ai/ElevenLabs/Ghost endpoints

Try it:

```bash
git pull && ./scripts/install.sh   # or --image-mode (see the next post)
# Then:
curl -X POST $HELMDECK_URL/api/v1/packs/blog.publish \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"destination":"artifact","format":"markdown",
       "title":"My first auto-covered post",
       "body":"# Hello\n\nThis post has a hero image without the agent ever calling image.generate explicitly.",
       "hero_image": true}'
```

The response will include `feature_image_artifact_key`. Fetch via `/api/v1/artifacts/<key>` — that's your cover.
