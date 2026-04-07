# 14. Pack: `slides.render`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
Rendering Markdown decks to PDF/PPTX/HTML normally requires Marp CLI, a headless Chromium, temp directories, and format-specific flags — multi-step shell knowledge that weak models cannot reliably drive (PRD §6.6).

## Decision
Ship `slides.render` as a built-in pack.

**Input:** `{ markdown: string, format: "pdf"|"pptx"|"html", theme?: string }`
**Output:** `{ artifact_url: string, format: string, page_count: integer }`
**Errors:** `schema_mismatch`, `timeout`, `internal_error`

The handler runs Marp inside a session container, captures the artifact, uploads it to the configured object store, and returns a signed URL. Theme defaults to `default`. Marp and Chromium are baked into the browser sidecar image.

## Consequences
**Positive:** any agent gets slide rendering as one typed call.
**Negative:** Marp/Chromium added to base image (~120 MB).

## Related PRD Sections
§6.6 Capability Packs
