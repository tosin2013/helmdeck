---
title: doc.ocr
description: Run Tesseract OCR over an image and return the extracted plaintext. Lightweight option when you only need text-from-image; for layout-aware document parsing reach for `doc.parse` instead.
keywords: [helmdeck, doc, ocr, tesseract, image to text, MCP]
---

# `doc.ocr`

Pipes an image through Tesseract inside the browser sidecar's session container and returns the extracted text. Accepts either a remote URL (helmdeck fetches the bytes) or a base64-encoded inline payload. Tesseract supports multi-language recognition via the `language` parameter; the sidecar ships only the English pack by default.

For PDF tables, multi-format documents, or layout-aware extraction (paragraphs, headings, columns), reach for [`doc.parse`](./parse.md) instead — `doc.ocr` is the simple "image bytes in, text out" function.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `source_url` | `string` | one of `_url` / `_b64` | — | Absolute `http://` or `https://` URL. Fetched in the control plane (not in the session container) so the egress allowlist applies. |
| `source_b64` | `string` | one of `_url` / `_b64` | — | Base64-encoded image bytes. Skips the egress check (the bytes never leave helmdeck). |
| `language` | `string` | no | `eng` | Tesseract language code(s). Multiple via `+`: `eng+spa`. Only `eng` ships in the sidecar by default — see [SIDECAR-EXTENDING](/SIDECAR-EXTENDING) to add language packs. |

Exactly one of `source_url` / `source_b64` must be set. Image bytes are capped at **32 MiB**.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `text` | `string` | Extracted text, trailing whitespace trimmed. Empty string when Tesseract finds nothing recognizable (e.g. blank or low-resolution input). |
| `language` | `string` | Echo of the language used (default `eng`). |
| `bytes` | `number` | Source image size in bytes (after base64 decode if applicable). |

## Vault credentials needed

**None.** Pure local Tesseract; no upstream API.

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste an OpenClaw chat-UI transcript. Suggested prompt:

  "I have a screenshot of a recipe at https://example.com/recipe.png — use
   doc.ocr to read what's in it and summarize the ingredients."

Capture and paste:
  1. The chat prompt sent.
  2. The tool call OpenClaw emits.
  3. The agent's text reply (it should narrate what it OCR'd).
  4. Footer: "Verified via OpenClaw 2026.4.18 + helmdeck v0.9.0 on <date>."
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

Generate a small inline test PNG (text rendered with Pillow) and OCR it:

```python
# generate-test-png.py — produces a small base64 PNG with known text
from PIL import Image, ImageDraw, ImageFont
import io, base64
img = Image.new("L", (600, 80), 255)
draw = ImageDraw.Draw(img)
font = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf", 36)
draw.text((20, 20), "Hello helmdeck OCR demo", fill=0, font=font)
buf = io.BytesIO()
img.save(buf, format="PNG")
print(base64.b64encode(buf.getvalue()).decode())
```

```bash
B64=$(python3 generate-test-png.py)
curl -fsS -X POST http://localhost:3000/api/v1/packs/doc.ocr \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d "{\"source_b64\":\"$B64\"}"
```

Real captured response:

```json
{
  "pack": "doc.ocr",
  "version": "v1",
  "output": {
    "text": "Hello helmdeck OCR demo",
    "language": "eng",
    "bytes": 2852
  },
  "session_id": "a703f819-efa4-48ec-b8bd-995a65a755b1"
}
```

The `session_id` field appears on every session-coupled pack response — useful for the agent to chain follow-up calls to the same sidecar (though OCR rarely benefits from chaining).

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | Both `source_url` and `source_b64` missing | `{"error":"invalid_input","message":"either source_url or source_b64 is required"}` |
| `invalid_input` | Both `source_url` and `source_b64` set | `{"error":"invalid_input","message":"set either source_url or source_b64, not both"}` |
| `invalid_input` | `source_b64` is not valid base64 | `{"error":"invalid_input","message":"source_b64 is not valid base64"}` |
| `invalid_input` | `source_url` doesn't start with `http://` or `https://` | `{"error":"invalid_input","message":"source_url must be http or https"}` |
| `invalid_input` | Source image bytes exceed 32 MiB | `source image N bytes exceeds 33554432 byte cap` |
| `session_unavailable` | Engine has no session executor (sidecar runtime down) | runtime not configured |
| `handler_failed` | Tesseract exits non-zero (corrupt image, unsupported format) | `tesseract exit N: <stderr>` |
| `handler_failed` | `source_url` HTTP fetch returns non-200 | `fetch <url>: HTTP NNN` |

## Session chaining

`needs_session: true`. The engine acquires a sidecar session per call and runs Tesseract inside it. Pass `_session_id` to reuse an existing session — useful when an agent has already created a session via `repo.fetch` and wants to OCR a screenshot it just saved into the clone path.

## Async behavior

Synchronous only. Tesseract on a 600×80 PNG runs in ~50ms; on a full A4 scanned page ~1–3 seconds. The pack's wall-clock latency is dominated by container exec round-trip plus the actual OCR — typically 2–4 seconds end-to-end on first call (sidecar warmup), 1–2 seconds on warm sessions.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `doc.ocr`.
- Source: [`internal/packs/builtin/doc_ocr.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/doc_ocr.go).
- ADR 019 — sidecar OCR + Tesseract bundling.
- Companion pack: [`doc.parse`](./parse.md) for layout-aware document parsing (Docling-backed; covers PDFs, DOCX, tables).
- [SIDECAR-EXTENDING.md](/SIDECAR-EXTENDING) — adding additional Tesseract language packs.
