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
`scrape-deck`, `repo-presentation`) now insert it before rendering.

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
| `title` | `string` | no | — | Deck title. Passed to the model AND used by the title-slide guarantee (see below). |
| `author` | `string` | no | — | Author byline placed on the title slide. |
| `persona` | `string` | no | `general` | Audience persona — shapes tone + the closing slide. Known: `general`, `technical`, `marketing`, `executive`, `educational`; any other string is a freeform audience hint. |
| `narration` | `boolean` | no | `true` | Emit a `<!-- … -->` speaker note per slide (needed by `slides.narrate`; harmless for `slides.render`). |
| `max_tokens` | `number` | no | derived | Completion-token budget (clamped to [2048, 8192]). |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `markdown` | `string` | The Marp deck — `---`-separated slides with titles, bullets, and (when `narration`) notes. |
| `slide_count` | `number` | Number of slides the deck parses to (≥ 2). |
| `model` | `string` | The model used. |
| `has_title_slide` | `boolean` | Whether the deck opens with a title slide (always `true` when `title` was provided). |
| `persona_used` | `string` | The persona applied (canonical key, or the freeform hint). |

## Title-slide guarantee & personas

The system prompt requires the model to open with a **title slide** and end with a
**closing slide**, but weak models skip them. So when you pass **`title`**, the
pack *guarantees* the title slide: it prepends `# <title>` (with the `author` as a
byline) when the model didn't already lead with a matching title, and won't
duplicate one it did write. Without a `title` input the pack doesn't invent one —
it relies on the prompt. The **closing** slide is strongly prompted (and shaped by
the persona) rather than force-appended, since a good closing needs the model's
content.

**`persona`** injects an audience-appropriate style directive and tells the model
what the closing slide should do — `marketing` → a call-to-action, `executive` →
the decision/ask, `technical` → next steps, etc. Agents should ask the user for
the title, author, and persona (or propose + confirm) before generating. To bake a
persona into a built-in pipeline, clone it and set a literal `"persona"`/`"author"`
in its `slides.outline` step (built-ins don't expose these as run inputs).

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
