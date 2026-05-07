---
title: browser.screenshot_url
description: Navigate a headless browser to a URL and capture a PNG screenshot. Returns the artifact key + a signed S3 URL the agent (or operator) can fetch directly.
keywords: [helmdeck, browser, screenshot, headless, chromedp, MCP]
---

# `browser.screenshot_url`

The reference pack for the helmdeck pack substrate. Drives a headless Chromium session via CDP to navigate to a URL and capture a PNG screenshot, then uploads the PNG to the artifact store and returns both an `artifact_key` reference and a **signed S3 URL** (in the response's top-level `artifacts` block) that operators can fetch directly. Smoke-test target on every release; first pack to ship in any new MCP integration.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | `string` | yes | — | Absolute URL. Validated by the egress guard (RFC 1918, metadata IP, loopback all blocked). |
| `fullPage` | `boolean` | no | `false` | Capture the entire scrollable page rather than just the viewport. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `output.url` | `string` | Echo of the input URL (helps when batching). |
| `output.artifact_key` | `string` | `browser.screenshot_url/<rand>-screenshot.png`. The stable handle that follow-up packs can reference. |
| `output.size` | `number` | PNG size in bytes. |
| `artifacts` | `array` (top-level) | Each entry has `key`, **`url`** (signed S3 URL valid 15 min), `size`, `content_type`, `created_at`. The `url` is what an agent or operator actually opens. |

## Vault credentials needed

**None.** Pure unauthenticated GET → screenshot. For pages behind a login, use [`web.login_and_fetch`](/PACKS) (vault-backed) or [`web.scrape_spa`](/PACKS) with a vault session-cookie credential.

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste an OpenClaw chat-UI transcript. Suggested prompt:

  "Take a full-page screenshot of https://example.com and tell me what's on
   the page."

The agent should call browser.screenshot_url, then optionally use
doc.ocr or vision.extract_visible_text on the resulting artifact to read
the rendered text.
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/browser.screenshot_url \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com","fullPage":true}'
```

Real captured response (signed-URL portions truncated):

```json
{
  "pack": "browser.screenshot_url",
  "version": "v1",
  "output": {
    "url": "https://example.com",
    "artifact_key": "browser.screenshot_url/22228b5ede04b9b0-screenshot.png",
    "size": 16491
  },
  "artifacts": [
    {
      "key": "browser.screenshot_url/22228b5ede04b9b0-screenshot.png",
      "url": "http://garage:3900/helmdeck-artifacts/browser.screenshot_url/22228b5ede04b9b0-screenshot.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&...&X-Amz-Expires=900&X-Amz-Signature=...",
      "size": 16491,
      "content_type": "image/png",
      "created_at": "2026-05-07T19:36:25.507708975Z"
    }
  ],
  "duration_ms": 3236
}
```

The signed URL expires in 15 minutes (`X-Amz-Expires=900`). For longer-lived access, fetch the bytes directly:

```bash
curl -fsS -H "Authorization: Bearer $JWT" \
  "http://localhost:3000/api/v1/artifacts/browser.screenshot_url/22228b5ede04b9b0-screenshot.png" \
  -o screenshot.png
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `url` field missing | `{"error":"invalid_input","message":"missing required field \"url\""}` |
| `handler_failed` | URL is malformed (chromedp can't parse) | `{"error":"handler_failed","message":"navigate not-a-real-url: Cannot navigate to invalid URL (-32000)"}` |
| `handler_failed` | Page navigation fails (DNS, TLS, timeout) | wrapped Go error |
| `session_unavailable` | Engine has no CDP factory wired (sidecar image absent) | runtime not configured |
| `artifact_failed` | Garage / S3 wouldn't accept the upload | check Garage health, disk pressure |

The egress guard runs **after** URL parsing, so a syntactically-malformed URL hits `handler_failed`, while a parseable URL pointing at a blocked range hits `invalid_input`. Document both for completeness; agents react differently to each.

## Session chaining

`needs_session: true`. The engine acquires an ephemeral session per call; sessions are transparent to this pack (no `_session_id` field). For chained workflows where you want the same browser session across multiple `browser.*` calls, use [`browser.interact`](./interact.md) instead — its `actions` array gives you N steps in one call.

## Async behavior

Synchronous only. Typical latency 1–4 seconds against a warm sidecar. Heavy pages bounded by the per-session timeout (default 60s) → past that, `handler_failed` with `context deadline exceeded`.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `browser.screenshot_url`.
- Source: [`internal/packs/builtin/screenshot_url.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/screenshot_url.go).
- ADR 021 — pack-browser-screenshot-url.
- Companion: [`browser.interact`](./interact.md) — multi-step automation.
- For pages behind auth: [`web.login_and_fetch`](/PACKS), [`web.scrape_spa`](/PACKS).
