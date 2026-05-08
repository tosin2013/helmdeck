---
title: doc.parse
description: Layout-aware document parsing via Docling — extract clean Markdown from PDFs (with table preservation), DOCX, PPTX, XLSX, HTML, and images. The heavyweight counterpart to doc.ocr.
keywords: [helmdeck, doc, parse, docling, PDF, markdown, table extraction, MCP]
---

# `doc.parse`

The layout-aware document parsing pack. Hands a PDF / DOCX / PPTX / XLSX / HTML / image to a self-hosted Docling service and returns the document's structure as clean Markdown — with **table layout preserved**, headings detected, lists recognized, and (optionally) OCR applied to embedded images. Where [`doc.ocr`](./ocr.md) is "image bytes in, text out", `doc.parse` is "real document in, structured Markdown out".

> ⚠️ **Known issue (2026-05-07)**: against current Docling builds the pack sends `http_sources` but the upstream `/v1/convert/source` API expects `sources`. URL inputs return `handler_failed` with a Docling 422 until the handler is updated. Inline base64 inputs (`source_b64` + `filename`) work today. Tracked as a `priority/P1` issue against the helmdeck repo.

## Setup prerequisite

`doc.parse` only works when the Docling overlay is running and the env-var toggle is set:

```bash
docker compose -f deploy/compose/compose.yaml \
               -f deploy/compose/compose.docling.yml \
               --env-file deploy/compose/.env.local up -d

# in deploy/compose/.env.local:
HELMDECK_DOCLING_ENABLED=true
```

When the toggle is off, the pack returns `invalid_input: doc.parse is disabled (set HELMDECK_DOCLING_ENABLED=true)`.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `source_url` | `string` | one of | — | Absolute http(s) URL. Egress-guarded. *(Currently broken; see Known Issue above.)* |
| `source_b64` | `string` | one of | — | Base64-encoded document bytes. Skips egress (never hits public network). |
| `filename` | `string` | when `source_b64` set | — | File extension picks the parser (`.pdf`, `.docx`, `.pptx`, `.xlsx`, `.html`, `.png`, `.jpg`). |
| `formats` | `array` | no | `["md"]` | Output formats. Accepts `md` (or `markdown`), `text` (or `plaintext`/`plain`), `html`. Aliases normalize on the way in (issue [#91](https://github.com/tosin2013/helmdeck/issues/91)). `md` is always force-included so the output schema's `markdown` field is non-empty. |
| `do_ocr` | `boolean` | no | `true` | Run OCR over embedded images during parsing. |
| `ocr_lang` | `array` | no | `["en"]` | Languages for embedded-image OCR (Docling's tesseract langs). |

Exactly one of `source_url` / `source_b64` must be set. Base64 payload capped at **32 MiB**.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `source` | `string` | URL or filename (echo of the input identity). |
| `markdown` | `string` | The parsed document as clean Markdown — always present. |
| `text` | `string` | Plain-text rendering. Only when `formats` requested it. |
| `html` | `string` | HTML rendering. Only when `formats` requested it. |
| `status` | `string` | Docling's status: `success`, `partial_success`, `failure`, `skipped`. |
| `processing_time` | `number` | Docling-reported seconds spent. |

## Vault credentials needed

**None.** The Docling service runs on the private `baas-net` and accepts unauthenticated calls from the control plane.

## Use it from your agent (OpenClaw chat-UI worked example)

> 📌 **The transcript below predates issue [#91](https://github.com/tosin2013/helmdeck/issues/91)'s fix.** The model emitted `formats: ["markdown"]` and got bounced. The pack now accepts `"markdown"` as an alias for `"md"` — fresh sessions converge on the first call. Captured here for historical evidence of the friction.

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Use the helmdeck__doc-parse tool with source_url=https://arxiv.org/pdf/2501.17887 to extract the document content as markdown. If it fails, tell me the exact error.

**Tool call** (1 call, no failures):

```json
{
  "name": "helmdeck__doc-parse",
  "arguments": {
    "source_url": "https://arxiv.org/pdf/2501.17887",
    "do_ocr": false,
    "formats": [
      "markdown"
    ]
  }
}
```

**Pack response** (truncated):

```json
{"error":"invalid_input","message":"unsupported format \"markdown\"; use md, text, or html"}
```

**Agent's final reply**:

> The tool returned an error:
> 
> **unsupported format “markdown”; use md, text, or html**

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0257).*

## Developer reference (`curl`)

```bash
# Inline PDF (base64) — known-good path
PDF_B64=$(base64 -w0 < /path/to/your.pdf)
curl -fsS -X POST http://localhost:3000/api/v1/packs/doc.parse \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d "{\"source_b64\":\"$PDF_B64\",\"filename\":\"input.pdf\",\"do_ocr\":true}"
```

URL path (currently returns the upstream-API mismatch error):

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/doc.parse \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{"source_url":"https://arxiv.org/pdf/2501.17887"}'
```

Captured response (URL path showing the current Docling API mismatch):

```json
{
  "error": "handler_failed",
  "message": "docling 422: {\"detail\":[{\"type\":\"missing\",\"loc\":[\"body\",\"sources\"],\"msg\":\"Field required\",\"input\":{\"options\":{\"to_formats\":[\"md\"],\"do_ocr\":true,\"image_export_mode\":\"placeholder\",\"abort_on_error\":false},\"http_sources\":[{\"url\":\"https://arxiv.org/pdf/2501.17887\"}]}}]}"
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | Both `source_url` and `source_b64` missing | `{"error":"invalid_input","message":"either source_url or source_b64 is required"}` |
| `invalid_input` | `source_b64` set without `filename` | `{"error":"invalid_input","message":"filename is required when source_b64 is set (extension picks the parser)"}` |
| `invalid_input` | `HELMDECK_DOCLING_ENABLED` is unset/false | `doc.parse is disabled (set HELMDECK_DOCLING_ENABLED=true)` |
| `invalid_input` | `formats` includes a value outside `md`/`text`/`html` | unknown format |
| `handler_failed` | Docling returns non-200 (incl. the current `http_sources` 422) | `docling NNN: <body>` |
| `handler_failed` | Docling reports `failure` or `skipped` | per-document errors surfaced from upstream |

## Session chaining

**Stateless.** No `_session_id` needed — Docling runs as its own service. Useful chains: `web.scrape` → save bytes via `fs.write` → `doc.parse` to convert a PDF an agent downloaded into structured Markdown for further processing.

## Async behavior

Synchronous. Heavy documents take time — a 100-page scanned PDF with OCR can hit 60+ seconds. The pack's timeout is 300 seconds; longer documents hit `timeout` rather than `handler_failed`.

## When to use which doc pack

| Pack | Use when |
|---|---|
| **`doc.parse`** | PDF, DOCX, PPTX, multi-page document with structure. Tables matter. Layout matters. Embedded images need OCR. |
| [`doc.ocr`](./ocr.md) | A single image. Plain text is enough. No layout. No Docling overlay running. |
| [`web.scrape`](/PACKS) | The document is rendered HTML on a webpage — Firecrawl handles fetch + clean-markdown extraction in one call. |

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `doc.parse`.
- Source: [`internal/packs/builtin/doc_parse.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/doc_parse.go).
- ADR 035 — MCP Server Hosting & Pack Evolution (Docling overlay rationale).
- T807c in [`TASKS.md`](/TASKS#phase-65--mcp-server-hosting--pack-evolution) — the work item that introduced this pack.
- Companion pack: [`doc.ocr`](./ocr.md).
