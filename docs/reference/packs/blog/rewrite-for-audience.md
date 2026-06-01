---
title: blog.rewrite_for_audience
description: Translate a source document into an original blog post for a stated audience and angle.
keywords: [helmdeck, blog, blog.rewrite_for_audience, MCP, content]
---

# `blog.rewrite_for_audience`

`blog.rewrite_for_audience` translates a source document (markdown) into an original blog post for a stated audience and angle. It is **not** a summarizer — it leads with why-it-matters, de-jargons, connects to the audience's tools, and adds perspective, while staying grounded in `source_content` (no claims that aren't in the source). It is the generator pack at the heart of every `*-rewrite-blog` pipeline. It does not fetch sources (call `doc.parse` / `web.scrape` / `research.deep` first), does not insert inline citations (chain `content.ground` with `rewrite:false` after), and does not publish to a CMS (chain `blog.publish` to save the artifact).

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `source_content` | `string` | yes | — | The source markdown to rewrite. |
| `audience` | `string` | yes | — | Who the post is for (e.g. "platform engineers"). |
| `model` | `string` | yes | — | A routable `provider/model` id. |
| `angle` | `string` | no | — | The editorial angle / thesis. |
| `title` | `string` | no | — | Suggested title. |
| `persona` | `string` | no | `general` | Tone preset (`technical`, `marketing`, `executive`, `educational`, or freeform). |
| `max_tokens` | `number` | no | — | Cap on the LLM call. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `markdown` | `string` | The rewritten blog post. |
| `persona_used` | `string` | The persona that shaped the output. |
| `model` | `string` | The model used. |

## Vault credentials needed

- **None** directly — requires a configured AI gateway (gateway-gated pack).

## Use it from your agent (OpenClaw chat-UI worked example)

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/blog.rewrite_for_audience \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "source_content": "# Raw notes...",
    "audience": "platform engineers",
    "angle": "why this matters for CI",
    "model": "openrouter/auto"
  }'
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | `source_content`, `audience`, or `model` missing. |
| `handler_failed` | The model returned no usable output. |

## Session chaining

- **No session.** Stateless; chains via content (output `markdown` → `content.ground` / `blog.publish`).

## Async behavior

`Async: true` — one gateway LLM call; call through `pack.start` or a SEP-1686-aware SDK to avoid client timeouts.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/blog_rewrite_for_audience.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/blog_rewrite_for_audience.go).
- Companion packs: `content.ground`, `blog.publish`, `doc.parse`, `web.scrape`, `research.deep`.
- Pipelines: `builtin.brief-rewrite-blog`, `builtin.scrape-rewrite-blog`, `builtin.doc-rewrite-blog`, `builtin.research-rewrite-blog`.
