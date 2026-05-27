---
slug: a-pdf-slide-cannot-scroll
title: "A PDF slide cannot scroll: why your mermaid diagrams were getting clipped"
authors: [tosin]
tags: [friction]
description: slides.render quietly cut the edges off big mermaid diagrams and wide tables in PDF decks. The CSS that was supposed to handle it — overflow-x:auto — is a no-op in a paginated format. The fix was four lines of theme-independent CSS, but the lesson is about where the bug actually lived.
image: /img/social-card.png
date: 2026-05-26
draft: false
---

A user asked helmdeck to build a slide deck with a mermaid diagram and a comparison table, render it to PDF — and the diagram ran off the right edge and the table's last columns were simply gone. No error, no warning. The deck looked fine in the HTML preview and broke silently in the PDF. The fix was four lines of CSS, but finding *where* the bug lived took longer than writing it.

## A slide is a fixed canvas

`slides.render` turns a Marp markdown deck into PDF, PPTX, or HTML. Mermaid fences are pre-rendered to inline SVG; the whole thing is handed to `marp`. The catch nobody had internalized: a Marp slide is a **fixed 1280×720 canvas**, and the PDF and PPTX codecs **cannot scroll**. Whatever doesn't fit isn't shrunk and isn't paged — it's clipped at the slide edge. HTML happens to scroll, which is exactly why the preview looked fine and the deliverable didn't.

## Where the bug actually lived

There were two culprits, and the second is the instructive one.

The mermaid diagrams were emitted as `<img class="mermaid-svg" src="data:image/svg+xml;…">` at the SVG's **natural size**, with no CSS constraining them. A dense graph renders large, so it overflowed. Obvious enough.

The tables were the trap. The curated themes *did* have a rule for them:

```css
table { … overflow-x: auto; }
```

That looks like it handles wide tables. It doesn't — `overflow-x: auto` means "show a scrollbar when content overflows," and **a PDF has no scrollbar**. In a paginated render it's a no-op; the table just clips. The rule had been there long enough to look load-bearing, but it only ever did anything in the HTML preview — the one format where overflow wasn't a problem in the first place. The CSS was solving the bug exactly where the bug didn't exist.

The fix is a theme-independent auto-fit `<style>` injected into every render. Marp hoists an inline `<style>` in the markdown to global CSS that layers *after* the selected theme, so it applies to the curated themes and the built-in ones (gaia/default) alike:

```css
section img { max-width: 100%; height: auto; }
section img.mermaid-svg { max-height: 70vh; object-fit: contain; }
section table { max-width: 100%; table-layout: fixed; }
section table th, section table td { overflow-wrap: anywhere; }
```

Diagrams scale down to fit instead of clipping; tables lay out to the slide width and wrap their cells instead of running off the edge. The `section …` selectors out-specify a theme's bare `table {}`, so the fit always wins. It's applied in both `slides.render` and `slides.narrate` — the latter exports per-slide PNGs, which clip identically.

The part I'd flag for anyone touching this code: it's almost impossible to unit-test "it fits" without rendering. We test that the fit CSS reaches the renderer, and there's an integration-tagged check that loads the rendered HTML in a headless Chromium and asserts no `<section>` overflows its own box — measuring `scrollWidth` vs `clientWidth`, which is a pre-transform layout value and so survives Marp's fit-to-viewport scale transform. For a visual bug, the honest verification is still a rendered-PDF eyeball.

## Mind the medium

When output looks right in one format and wrong in another, the bug usually isn't in your content — it's in an assumption about the *medium*. `overflow: auto` is a perfectly good rule that silently means nothing the moment the medium can't scroll. The same trap waits anywhere a "responsive" web instinct meets a fixed canvas: print stylesheets, PDF export, fixed-size video frames, e-ink. Ask what the target medium can actually *do* with overflow before you trust a rule that assumes it can scroll. Ours couldn't, and a CSS property that had looked like a guardrail for months turned out to be decoration.

## See also

- [`slides.render` reference](/reference/packs/slides/render) — the `format` options and mermaid handling
- Issue [#280](https://github.com/tosin2013/helmdeck/issues/280) — the overflow bug; shipped in v0.15.0
