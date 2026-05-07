---
title: browser.interact
description: Deterministic multi-step browser automation against a headless Chromium via CDP. The building block for AI-driven web.test.
---

# `browser.interact`

Executes a user-supplied ordered array of browser actions (navigate / click / type / wait / screenshot / extract / assert_text) against a headless Chromium session via CDP. **No LLM in the loop** — the caller specifies every step explicitly. This is the deterministic, fast option when speed and reproducibility matter and nobody is watching the screen.

It's also the building block for the AI-driven [`web.test`](/PACKS) pack, which decomposes natural-language test instructions into `browser.interact` action sequences via Playwright MCP's accessibility tree.

> **Headless ≠ visible.** This pack runs against an off-screen Chromium. Operators watching a session via noVNC will see *nothing* when this pack runs. When the user *is* watching, or the task is "drive a browser so I can see it", reach for the [`desktop.*`](/PACKS) REST primitives against a desktop-mode session instead — Chromium there is pre-launched on the XFCE4 display.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | `string` | yes | — | Absolute starting URL. Validated by the egress guard. |
| `actions` | `array` | yes | — | Ordered array of action objects (see schema below). |

### Action schema

Each entry in `actions` is one of:

| `action` | Required fields | Optional fields | Behavior |
|---|---|---|---|
| `navigate` | `url` | — | Navigate to a new URL mid-flow. Egress-guarded. |
| `click` | `selector` | — | CSS selector click via CDP. |
| `type` | `selector`, `value` | — | Type into an input. Supports placeholders if vault credentials are scoped (rare for browser.interact; usually use `web.login_and_fetch` instead). |
| `wait` | — | `ms` (default 1000) | Sleep for `ms` milliseconds. |
| `screenshot` | — | — | Capture a base64 PNG, append to `output.screenshots`. |
| `extract` | `selector` | `format` (`text`\|`html`, default `text`) | Read content from an element, append to `output.extractions`. |
| `assert_text` | `text` | — | Assert that `text` appears anywhere in the current page body. Failure → `assertions_passed: false`, but the pack continues running subsequent actions. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `url` | `string` | Echo of the input URL. |
| `steps_completed` | `number` | How many actions ran before completion or first non-fatal failure. |
| `screenshots` | `array` | Base64-encoded PNGs in the order they were captured. Each one is small (viewport-sized); use [`browser.screenshot_url`](./screenshot-url.md) for full-page captures. |
| `extractions` | `object` | Map of selector → extracted content. |
| `assertions_passed` | `boolean` | True iff every `assert_text` action matched. |

## Vault credentials needed

**None for the pack itself.** If individual `type` actions reference `${vault:NAME}` placeholders in `value`, those credentials must exist in the vault and be granted to the calling actor — but typically credential-driven workflows belong in [`web.login_and_fetch`](/PACKS) which is purpose-built for them.

## CLI invocation

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/browser.interact \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "url": "https://example.com",
    "actions": [
      {"action": "click", "selector": "#login-btn"},
      {"action": "type",  "selector": "#username", "value": "demo"},
      {"action": "type",  "selector": "#password", "value": "demo"},
      {"action": "click", "selector": "#submit"},
      {"action": "wait",  "ms": 2000},
      {"action": "screenshot"},
      {"action": "assert_text", "text": "Welcome"},
      {"action": "extract", "selector": "h1", "format": "text"}
    ]
  }'
```

Response (truncated):

```json
{
  "url": "https://example.com",
  "steps_completed": 8,
  "screenshots": ["iVBORw0KGgoAAAANSUhEUg..."],
  "extractions": {"h1": "Welcome, demo"},
  "assertions_passed": true
}
```

## UI invocation

**Not yet — read-only catalog** (same status as [`browser.screenshot_url`](./screenshot-url.md); tracked as [T606a](/TASKS#phase-6--management-ui-weeks-1720)). The action-array schema makes this pack a particularly nice candidate for an in-UI builder; the future Test Runner will likely render an action-by-action visual editor.

## Error codes

| Code | When | Operator action |
|---|---|---|
| `invalid_input` | Malformed `actions` array; unknown `action` value; `selector` missing for an action that requires it. | Re-check the action schema above. |
| `session_unavailable` | Engine has no CDP factory wired (sidecar image missing). | See [troubleshooting](/howto/troubleshoot-install). |
| `handler_failed` | A non-assertion CDP call failed mid-sequence (selector not found within the action's wait window, navigate timed out, …). The response includes `steps_completed` so the caller can see how far the sequence got. | Inspect the audit log for the wrapped chromedp error; widen waits with explicit `wait` actions. |

`assert_text` failures are **not** error codes — they leave `assertions_passed: false` and let the rest of the sequence run. The pack only short-circuits on hard CDP failures.

## Session chaining

`needs_session: true`. Today the engine creates an ephemeral session per pack call. If you want to share a browser session across multiple `browser.interact` calls (e.g. login once, then run several action sequences), the cleanest path is one large `actions` array; persistent session pinning across multiple `browser.interact` calls is on the roadmap as part of the broader pack-session story.

## Async behavior

**Synchronous only.** A 10-step action array against a typical SaaS site runs in 5–15 seconds. The per-session timeout still applies — heavy `wait` actions count against it.

## When to use which browser pack

| Pack | When to use |
|---|---|
| [`browser.screenshot_url`](./screenshot-url.md) | One-shot snapshot of a public URL. No interaction. Smallest input footprint. |
| **`browser.interact`** (this pack) | Deterministic multi-step automation: known selectors, known order. Fast, no LLM, no human watching. |
| [`web.scrape_spa`](/PACKS) | Single page, structured extraction by JSON-Schema. The model gives helmdeck the schema; helmdeck does the extraction. |
| [`web.scrape`](/PACKS) | Firecrawl-backed clean-markdown scrape. No selectors. Heavy but high-quality output. |
| [`web.test`](/PACKS) | Natural-language testing via Playwright MCP. The LLM plans each step; this pack gates the assertions. |
| [`vision.*`](/PACKS) | When selectors don't exist or the page is non-DOM (Canvas, video, PDF). Visual-only loop. |
| [`desktop.*`](/PACKS) primitives | When a human is watching the session and you want them to see the actions happen. |

## See also

- Catalog row: [`PACKS.md`](/PACKS)
- Source: [`internal/packs/builtin/browser_interact.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/browser_interact.go)
- Agent guidance: [`SKILLS.md`](/integrations/SKILLS)
- Companion pack: [`browser.screenshot_url`](./screenshot-url.md)
- AI-driven sibling: [`web.test`](/PACKS) (T807e)
- Roadmap: [T621 in TASKS.md](/TASKS#phase-3--mcp-registry--bridge--client-integrations-weeks-910)
