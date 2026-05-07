---
title: browser.interact
description: Deterministic multi-step browser automation against a headless Chromium via CDP. Navigate, click, type, scroll, screenshot, extract, assert in a single call. The deterministic alternative to LLM-driven web.test.
keywords: [helmdeck, browser, interact, chromedp, automation, MCP]
---

# `browser.interact`

Executes a user-supplied ordered array of browser actions against a headless Chromium session. **No LLM in the loop** — the caller specifies every step explicitly. Returns base64 screenshots taken during the run, extracted element content, and an `assertions_passed` flag so the agent can branch on success.

This is the deterministic, fast option when speed and reproducibility matter. It's also the building block for the AI-driven [`web.test`](/PACKS), which decomposes natural-language test instructions into `browser.interact` action sequences via Playwright MCP's accessibility tree.

> **Headless ≠ visible.** Runs against an off-screen Chromium. Operators watching a session via noVNC see *nothing* when this pack runs. When the user *is* watching — or the task is "drive a browser so I can see it" — reach for the [`desktop.*`](/PACKS) REST primitives instead.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | `string` | yes | — | Absolute starting URL. Egress-guarded. |
| `actions` | `array` | yes | — | Ordered array of action objects (schema below). |

### Action schema

Each entry in `actions` is one of:

| `action` | Required fields | Optional | Behavior |
|---|---|---|---|
| `navigate` | `url` | — | Navigate to a new URL mid-flow. Egress-guarded. |
| `click` | `selector` | — | CSS-selector click via CDP. |
| `type` | `selector`, `value` | — | Type into an input. Supports `${vault:...}` placeholders. |
| `wait` | — | `ms` (default `1000`) | Sleep N milliseconds. |
| `screenshot` | — | — | Capture a viewport-sized base64 PNG → appended to `output.screenshots`. |
| `extract` | `selector` | `format` (`text`\|`html`, default `text`) | Read content from an element → `output.extractions[selector]`. |
| `assert_text` | `text` | — | Assert `text` appears in the current page body. Failure → `assertions_passed: false`, but subsequent actions still run. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `output.url` | `string` | Echo of the input URL. |
| `output.steps_completed` | `number` | Actions that ran before completion or first non-fatal failure. |
| `output.screenshots` | `array` | Base64 viewport PNGs in capture order. Use [`browser.screenshot_url`](./screenshot-url.md) for full-page captures. |
| `output.extractions` | `object` | Map of selector → extracted content. |
| `output.assertions_passed` | `boolean` | True iff every `assert_text` matched. |

## Vault credentials needed

**None for the pack itself.** If `type` actions reference `${vault:NAME}` in `value`, those credentials must exist + be granted to the calling actor. Credential-driven workflows usually belong in [`web.login_and_fetch`](/PACKS) which is purpose-built for them.

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste an OpenClaw chat-UI transcript. Suggested prompt:

  "Use browser.interact to visit https://example.com, take a screenshot,
   extract the text of the h1, and assert that 'Example Domain' is on the page.
   Tell me whether the assertion passed."
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/browser.interact \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "url": "https://example.com",
    "actions": [
      {"action": "screenshot"},
      {"action": "extract", "selector": "h1", "format": "text"},
      {"action": "assert_text", "text": "Example Domain"}
    ]
  }'
```

Real captured response (screenshot bytes truncated):

```json
{
  "pack": "browser.interact",
  "version": "v1",
  "output": {
    "url": "https://example.com",
    "steps_completed": 3,
    "screenshots": ["/9j/4AAQSkZJRgABAQAAAQABAAD/...<base64 PNG truncated>..."],
    "extractions": {
      "h1": "Example Domain"
    },
    "assertions_passed": true
  },
  "session_id": "9cd1d6d5-246a-4a13-bdb6-9f6f16fcf8e4"
}
```

For chained login + scrape workflows:

```json
{
  "url": "https://app.example.com/login",
  "actions": [
    {"action": "type",  "selector": "#username", "value": "demo"},
    {"action": "type",  "selector": "#password", "value": "${vault:demo-app-pw}"},
    {"action": "click", "selector": "#submit"},
    {"action": "wait",  "ms": 2000},
    {"action": "assert_text", "text": "Welcome"},
    {"action": "extract", "selector": ".user-id", "format": "text"}
  ]
}
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | Malformed `actions` array; unknown `action`; `selector` missing for an action that requires it |
| `session_unavailable` | Engine has no CDP factory (sidecar absent) |
| `handler_failed` | A non-assertion CDP call fails mid-sequence (selector not found, navigate timed out). The response still reports `steps_completed` so the caller sees how far it got |

`assert_text` failures are **not** error codes — they leave `assertions_passed: false` and let the rest of the sequence run.

## Session chaining

`needs_session: true`. The engine creates an ephemeral session per pack call. To share state across multiple `browser.interact` calls, fold them into one larger `actions` array (cheaper than multiple session spin-ups).

## Async behavior

Synchronous. A 10-step action array against a typical SaaS site runs in 5–15 seconds. The per-session timeout still applies — heavy `wait` actions count against it.

## When to use which browser pack

| Pack | Use when |
|---|---|
| [`browser.screenshot_url`](./screenshot-url.md) | One-shot snapshot of a public URL. No interaction. |
| **`browser.interact`** | Deterministic multi-step automation: known selectors, known order. Fast, no LLM, no human watching. |
| [`web.scrape_spa`](/PACKS) | Single page, structured extraction by JSON-Schema. The model gives helmdeck the schema; helmdeck does the extraction. |
| [`web.scrape`](/PACKS) | Firecrawl-backed clean-markdown scrape. No selectors. |
| [`web.test`](/PACKS) | Natural-language testing. The LLM plans each step; this pack underlies the assertions. |
| [`vision.*`](/PACKS) | When selectors don't exist or the page is non-DOM (Canvas, video, PDF). |
| [`desktop.*`](/PACKS) primitives | When a human is watching the session and you want them to see the actions happen. |

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/browser_interact.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/browser_interact.go).
- Companion: [`browser.screenshot_url`](./screenshot-url.md).
- AI-driven sibling: [`web.test`](/PACKS) (T807e).
- Roadmap: [T621 in TASKS.md](/TASKS#phase-3--mcp-registry--bridge--client-integrations-weeks-910).
