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
| `model` | `string` | no | resolves via `defaultPackModel()` | A routable `provider/model` id. |
| `angle` | `string` | no | — | The editorial angle / thesis. |
| `title` | `string` | no | — | Suggested title. |
| `persona` | `string` | no | `general` | Tone preset (`technical`, `marketing`, `executive`, `educational`, `academic`, or freeform). |
| `max_tokens` | `number` | no | derived from target | Cap on the LLM call. When set, must be at least the target × 1.7 tokens/word; otherwise the pack raises it so the chosen target isn't silently truncated. |
| `length_intent` | `string` | no | `thorough` | JIT length sizing — one of `summary` / `thorough` / `exhaustive`. Pack measures `source_content`, picks a target word range from the heuristic table below. See [#525](https://github.com/tosin2013/helmdeck/issues/525) for the convention. |
| `inspect` | `boolean` | no | `false` | When `true`, pack returns measurements + suggestion and does NOT call the model. Useful when an agent wants to negotiate length before committing. |
| `target_words_min` | `number` | no | — | Explicit numeric override. Both `min` and `max` must be set (and `max ≥ min`) to be honored; partial falls through to `length_intent`. |
| `target_words_max` | `number` | no | — | Explicit numeric override. See `target_words_min`. |

### Length intent heuristic

The pack picks a chosen target by multiplying `source_words × ratio`, then clamping to the row's floor/ceiling. The reported range brackets the chosen target ±15%, re-clamped to the row bounds.

| Intent | Compression ratio | Floor | Ceiling |
|---|---|---|---|
| `summary` | 0.10 | 300 | 1200 |
| `thorough` (default) | 0.30 | 800 | 2500 |
| `exhaustive` | 0.55 | 1500 | 6000 |

**Precedence**: `inspect:true` short-circuits everything > both `target_words_min` and `target_words_max` set > `length_intent` > default (`thorough`). The chosen range is injected into the system prompt as an explicit override of the persona's word-count guidance.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `markdown` | `string` | The rewritten blog post. Empty when `inspect:true`. |
| `model` | `string` | The model used (always populated; reflects the resolved default when caller omitted). |
| `persona_used` | `string` | The persona that shaped the output. |
| `source_words` | `number` | Whitespace-delimited word count of `source_content`. |
| `target_words_chosen` | `number` | Midpoint of the chosen range, what the model was told to aim for. |
| `target_words_min` | `number` | Lower bound of the chosen range. |
| `target_words_max` | `number` | Upper bound of the chosen range. |
| `output_words` | `number` | Whitespace-delimited word count of the produced `markdown`. |
| `compression_ratio` | `number` | `output_words / source_words`. |
| `length_intent_applied` | `string` | Where the chosen size came from — `intent:summary` / `intent:thorough` / `intent:exhaustive` / `explicit`. |
| `truncated` | `boolean` | `true` when the model hit `max_tokens`. Strong signal is `finish_reason=length`; fallback heuristic fires when output is within 95% of `target_words_max` AND ends without sentence-terminating punctuation. Re-run with a smaller `length_intent` or a larger `max_tokens`. |
| `inspect` | `boolean` | `true` when this was an inspect-mode call (no generation). |
| `suggested_target` | `number` | Inspect mode only — what the pack would aim for if called for real. |
| `suggested_target_min` / `_max` | `number` | Inspect mode only — bracket around `suggested_target`. |
| `reason` | `string` | Inspect mode only — human-readable explanation of the suggestion. |

### Inspect mode worked example

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/blog.rewrite_for_audience \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "source_content": "# Long source...\n\n[~7000 words]",
    "audience": "platform engineers",
    "inspect": true,
    "length_intent": "exhaustive"
  }'
```

Response (no model call, no token cost):

```json
{
  "inspect": true,
  "markdown": "",
  "model": "openrouter/auto",
  "source_words": 7000,
  "suggested_target": 3850,
  "suggested_target_min": 3272,
  "suggested_target_max": 4427,
  "length_intent_applied": "intent:exhaustive",
  "reason": "source is 7000 words; applying intent:exhaustive for a target near 3850 words (range 3272-4427)"
}
```

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
    "model": "openrouter/auto",
    "length_intent": "thorough"
  }'
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | `source_content` or `audience` missing/empty. |
| `internal` | Pack registered without a gateway dispatcher AND the call is not `inspect:true`. Inspect mode works without a dispatcher. |
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
