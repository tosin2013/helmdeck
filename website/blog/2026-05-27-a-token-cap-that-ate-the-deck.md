---
slug: a-token-cap-that-ate-the-deck
title: "A 2048-token cap was silently eating half your slide deck"
authors: [tosin]
tags: [friction]
description: A grounded-deck pipeline kept returning decks with the back half missing — no error, no warning. The renderer got the blame. The real culprit was a fixed 2048-token cap on an upstream rewrite step that truncated any document larger than the test fixtures.
image: /img/social-card.png
date: 2026-05-27
draft: true
---

A user ran the `grounded-deck` pipeline on a hand-built 20–25 slide markdown deck — fact-check the claims, render to PDF — and got back a deck with roughly the first third of the slides. The rest were just gone. No error, no warning, a clean exit. The obvious suspect was the renderer. The renderer was innocent.

## The renderer can't drop what it never received

`builtin.grounded-deck` is two steps: `content.ground` adds citations to the markdown, then `slides.render` turns the grounded markdown into a PDF. `slides.render` shells out to Marp — it splits on `---` separators and renders whatever it's handed. It has no model, no summarizer, nothing that could "decide" to drop slides. If the PDF has twelve slides, twelve slides arrived as input.

So the content disappeared *before* the render step. That points at `content.ground`, and specifically at the part of it that nobody suspected because it's optional and usually helpful: the rewrite.

## A full-document rewrite on a fixed budget

When `rewrite: true`, `content.ground` doesn't just append `[source](url)` links. After inserting citations it makes one more LLM call that hands the model the **entire document** plus the grounding report and asks it to rewrite weak claims into stronger, source-backed prose. The model returns the whole document, rewritten.

That call was capped at a fixed budget:

```go
maxTokens := 2048
```

2048 output tokens is plenty for a blog post. A 20–25 slide deck is several thousand tokens. So the model did exactly what it was told: it rewrote from the top and stopped when it hit the ceiling — mid-document, partway through the deck. The API flagged it (`finish_reason: "length"`), and the pack ignored the flag and shipped the truncated text downstream as `grounded_text`. Marp rendered the surviving slides faithfully. The cap, not the renderer, ate the deck.

This is the quiet failure mode of any fixed output-token limit: it's invisible until someone hands you an input larger than your test fixtures. The 2048 was even commented as a deliberate, cost-conscious default. It was correct for every document the tests exercised and wrong for the first real deck.

## The fix is three guards and a default

**Read the truncation signal.** The gateway already surfaces `finish_reason`. If the rewrite came back `"length"`, the document is incomplete, so we discard it and fall back to the citation-only version — which preserves every slide, just with `[source]` links added rather than reworded prose:

```go
if resp.Choices[0].FinishReason == "length" {
    return "", errRewriteTruncated   // caller keeps the citation-only text
}
```

**Scale the budget to the input.** A rewrite that returns the whole document needs a budget sized to the whole document, not a constant. We estimate from input length (~4 chars/token) with headroom, clamped to a sane ceiling:

```go
maxTokens := estimatedTokens(text) * 5 / 4   // clamped to [2048, 8192]
```

**Tell the model it might be a deck.** The rewrite prompt now says: if this is a slide deck, preserve every `---` separator and keep the slide count — never merge or reorder slides.

And the one that matters most for decks specifically: **the deck pipelines no longer rewrite at all.** `grounded-deck` and `research-ground-deck` now ground with `rewrite: false`. A prose rewrite is a *blog* affordance — it makes flowing text more authoritative. On a slide deck it reflows structure even when it isn't truncated. Citation-only grounding adds the sources and leaves the slide boundaries exactly where the author put them. Blog pipelines keep `rewrite: true`, now protected by the truncation guard.

## What to take from it

Two things generalize past this one pack.

First, a fixed output-token cap on a step that returns variable-length content is a silent truncation waiting for a bigger input. If a step can return "the whole thing, transformed," its budget has to track the size of the whole thing — and you have to check `finish_reason`, because that field is the cheapest truncation detector you'll ever get and ignoring it is precisely how truncation goes silent.

Second, in a multi-step pipeline, "the output is missing content" almost never points at the step you'd blame first. The renderer was the visible end of the chain, so it looked guilty; the damage was done two steps upstream by an optional enhancement. When data goes missing across a pipeline, walk it backwards from the symptom and ask each step what it actually received — not what it produced.

The fix shipped in the [content.ground reference](/reference/packs/content/ground) and the built-in pipeline definitions; see the [changelog](/changelog) for the full entry.
