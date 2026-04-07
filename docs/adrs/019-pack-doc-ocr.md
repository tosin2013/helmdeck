# 19. Pack: `doc.ocr`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
OCR pipelines require image preprocessing, Tesseract invocation, and language-pack management — boilerplate that should not be in agent code (PRD §6.6).

## Decision
Ship `doc.ocr` as a built-in pack.

**Input:** `{ file_url: string, language?: string (ISO 639-2, default "eng"), preprocess?: boolean }`
**Output:** `{ text: string, confidence: number, page_count: integer }`
**Errors:** `not_found`, `timeout`, `internal_error`

The handler downloads the file, optionally runs preprocessing (deskew, denoise, contrast), invokes Tesseract with the requested language pack, and returns extracted text plus an aggregate confidence score. PDFs are split per-page automatically.

## Consequences
**Positive:** OCR becomes a single typed call; language packs are bundled in the session image.
**Negative:** image bloat from extra languages; quality varies by source document.

## Related PRD Sections
§6.6 Capability Packs
