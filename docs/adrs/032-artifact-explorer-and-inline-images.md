# ADR 032 — Artifact Explorer & MCP Inline Image Content

**Status:** Accepted
**Date:** 2026-04-10
**Author:** Tosin Akinosho

## Context

Helmdeck packs produce artifacts (screenshots, PDFs, OCR source images, rendered slides) that are stored in the S3-compatible object store (Garage by default, per ADR 031). Today these artifacts are invisible to both operators and LLMs:

1. **Operators** have no UI for browsing, previewing, or downloading artifacts. The only way to see what an agent produced is to curl a signed URL extracted from the pack response JSON — and those URLs expire in 15 minutes.

2. **LLMs** receive artifact URLs as plain text in MCP tool results. A vision-capable model (GPT-4o, Claude, Gemini) cannot "see" an image from a URL string — it needs the actual bytes. This forces a wasteful two-step loop: the agent calls `browser.screenshot_url`, gets a URL, then needs a *second* tool call to download and display the image, assuming such a tool even exists in the client's toolkit. Most don't, so the agent just quotes the URL and the operator has to open it manually.

The MCP specification supports an `image` content type in tool results (https://modelcontextprotocol.io/specification/2025-11-05/server/tools#image-content) that embeds base64-encoded image bytes directly in the response. No MCP server in the ecosystem uses this today for real artifacts — helmdeck would be the first.

## Decision

Ship three capabilities as part of v0.6.0:

### 1. MCP inline image content (T302b)

When a pack produces image artifacts (`image/png`, `image/jpeg`, `image/webp`, `image/gif`) under a configurable size threshold (default 1 MB), the MCP PackServer includes them as `type: "image"` content blocks in the `tools/call` response alongside the existing `type: "text"` block:

```json
{
  "content": [
    { "type": "text", "text": "{\"artifact_key\":\"...\",\"size\":14879,...}" },
    { "type": "image", "data": "<base64>", "mimeType": "image/png" }
  ]
}
```

Artifacts over the threshold are URL-only (no inline). Non-image artifacts (PDFs, tarballs) are always URL-only. The threshold is per-pack configurable via `Pack.InlineImageThreshold` with a global default from `HELMDECK_MCP_INLINE_IMAGE_THRESHOLD`.

The REST API (`POST /api/v1/packs/<name>`) is not affected — it continues to return JSON with signed URLs. Only the MCP transport gains inline images because that's where the LLM consumes results.

### 2. Artifact Explorer UI panel (T613)

A new standalone panel in the Management UI sidebar (`/artifacts` route) that lists recent artifacts with:

- Pack name, timestamp, content type, and size per row
- Inline image preview (thumbnail) for image artifacts, rendered via a freshly-signed URL
- Download button that generates a fresh signed URL on click
- Filter by pack name and date range
- Pagination (default 50 per page)

Backed by a new `GET /api/v1/artifacts` endpoint that queries the S3 artifact index (or lists the bucket directly if the in-memory index doesn't have history from before the current process started) and returns metadata + fresh signed URLs.

### 3. Future extensions (tracked, not built in v0.6.0)

- **Bulk download** (ZIP of selected artifacts)
- **Sharing links** (longer-lived signed URLs or public links with an access token)
- **Retention policy UI** (surface the janitor config from T211b in the Artifacts panel)
- **Search by content** (OCR text search across screenshot artifacts)
- **Artifact diffing** (compare two screenshots of the same URL taken at different times)

## PRD Sections

§6.6 Capability Packs, §8.6 Capability Packs Panel, §14 Object Store

## Consequences

- Vision-capable LLMs can reason about screenshots in one round trip — this is a real differentiator for the sidecar pattern where the agent's quality of tool use directly depends on what it can see.
- Operators gain visibility into what their agents produced without leaving the Management UI.
- Base64 encoding inflates image size by ~33%. A 1 MB threshold means up to ~1.33 MB per image content block in the MCP response. Clients with tight context windows should configure a lower threshold.
- The Artifact Explorer endpoint generates fresh signed URLs on every request. High-frequency polling could create load on the S3 store's presign path — mitigated by the default 50-item page limit and no auto-refresh (manual refresh button only).
