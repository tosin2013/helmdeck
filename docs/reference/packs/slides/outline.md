---
title: slides.outline
description: Restate prose/markdown as a structured Marp slide deck — --- separated slides with titles, bullets, and speaker notes — ready for slides.render or slides.narrate.
keywords: [helmdeck, slides, outline, deck, marp, pipelines, MCP]
---

# `slides.outline`

`slides.outline` takes prose (a README, a `research.deep` synthesis, `content.ground`
output, scraped markdown) and asks the gateway LLM to restate it as a **structured
Marp deck**: slides separated by a line containing only `---`, each with a short
title, concise bullets, and a `<!-- speaker note -->` for narration. The output
markdown is ready to hand to [`slides.render`](/reference/packs/slides/render) or
[`slides.narrate`](/reference/packs/slides/narrate).

## Why this pack exists

`slides.render` / `slides.narrate` split a deck into slides **only on `---`** (the
standard Marp delimiter). Prose has no `---`, so feeding a raw README or synthesis
straight into them collapsed the whole document onto **one slide** — a degenerate
~7-second video that still reported success. `slides.outline` is the missing
transform: it turns prose into an actual multi-slide deck. The built-in deck/narrate
pipelines (`grounded-deck`, `research-deck`, `research-narrate`, `research-ground-deck`,
`scrape-deck`, `repo-readme-narrate`) now insert it before rendering.

## Deterministic bounds

The output is bounded, not open-ended:

- **`max_slides`** is clamped to a hard ceiling (30).
- The **completion-token budget** is clamped (≈300 tokens/slide, floor 2048, ceil 8192).
- The result is **validated to be a real deck**: it must parse to at least 2 slides
  (using the same `---` splitter `slides.render`/`slides.narrate` use). If the model
  returns fewer — almost always because the input was too thin — the pack returns
  **`invalid_input`** (`caller_fixable`: "content too thin; provide more material"),
  **not** a silent one-slide deck. A pipeline then fails legibly instead of emitting a
  7-second blob.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `text` | `string` | yes | — | The prose/markdown to restructure into slides. |
| `model` | `string` | yes | — | Gateway model id (`provider/model`; see `helmdeck://models`). |
| `max_slides` | `number` | no | 18 (cap 30) | Upper bound on slide count. |
| `title` | `string` | no | — | Prepended as a deck title hint for the model. |
| `narration` | `boolean` | no | `true` | Emit a `<!-- … -->` speaker note per slide (needed by `slides.narrate`; harmless for `slides.render`). |
| `max_tokens` | `number` | no | derived | Completion-token budget (clamped to [2048, 8192]). |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `markdown` | `string` | The Marp deck — `---`-separated slides with titles, bullets, and (when `narration`) notes. |
| `slide_count` | `number` | Number of slides the deck parses to (≥ 2). |
| `model` | `string` | The model used. |

## Async behavior

**Asynchronous** (`Async: true`) — one gateway LLM call. The initial call returns a
SEP-1686 task envelope; poll for the result.

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | Missing `text`/`model`; the model produced fewer than 2 slides (content too thin). |
| `internal` | Registered without a gateway dispatcher. |
| `handler_failed` | Gateway returned no choices or an empty deck. |
| `invalid_output` | (Gateway errors are mapped to `invalid_input` with a `helmdeck://models` hint when the model is unroutable.) |

## See also

- [`slides.render`](/reference/packs/slides/render) and [`slides.narrate`](/reference/packs/slides/narrate) — the consumers of this deck.
- [Pipeline prompt templates](/reference/prompt-templates/pipelines) — the deck/narrate pipelines that chain `slides.outline`.
- Source: [`internal/packs/builtin/slides_outline.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/slides_outline.go).
