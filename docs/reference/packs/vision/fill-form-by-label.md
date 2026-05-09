---
title: vision.fill_form_by_label
description: Fill a form on the visible XFCE4 desktop by matching field labels to their values. The pack iterates one field at a time, asking a vision model to locate each label, then xdotool-types the value into the matched field.
keywords: [helmdeck, vision, form, xdotool, computer use, MCP]
---

# `vision.fill_form_by_label`

The "fill in this form" pack. Caller supplies a `fields` map of `{label: value}` pairs and a vision-capable `model`; the pack iterates each label in alphabetical order, screenshots the desktop, asks the model to locate the matching field, types the value via xdotool, and moves to the next field. Returns the list of fields that were successfully filled and the total step count.

This is the messiest of the three vision packs because the action loop must track per-field progress. Pairs naturally with [`vision.click_anywhere`](./click-anywhere.md) (to submit afterward) and [`vision.extract_visible_text`](./extract-visible-text.md) (to verify the post-submit state).

> ⚠️ **Same known limitation as `vision.click_anywhere`** — see [issue #102](https://github.com/tosin2013/helmdeck/issues/102). The loop doesn't visually verify the typed text actually landed in the right field before moving on. For high-stakes forms, follow with `vision.extract_visible_text` to confirm the field values.

## Setup prerequisite

Vision packs run on a **desktop-mode** session. The Chromium window must already be at the form URL — typically achieved by `vision.click_anywhere` to focus the URL bar, then `desktop.type` + `desktop.key` Enter to navigate.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `fields` | `object` | yes | — | `{label: value}` map. Labels are alphabetized internally for deterministic iteration. |
| `model` | `string` | yes | — | Vision-capable provider/model. |
| `max_steps` | `number` | no | `12` | Cap on the **total** step count (across all fields). Forms with N fields typically take ~N+1 steps. |
| `_session_id` | `string` | yes (chained) | — | Pass the session id from the upstream desktop-mode pack. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `completed` | `boolean` | `true` only when **every** field was filled within `max_steps`. |
| `fields_filled` | `array` | Labels successfully filled, in alphabetical order. |
| `steps` | `number` | Total steps used. |

## Vault credentials needed

**None** — the AI key for `model` resolves through the *AI Providers* UI panel.

## Use it from your agent (OpenClaw chat-UI worked example)

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> First navigate the visible desktop's Chromium to https://httpbin.org/forms/post by using helmdeck__desktop-screenshot, then helmdeck__vision-click_anywhere to click the URL bar with model openrouter/anthropic/claude-haiku-4.5, then xdotool-type the URL via helmdeck__desktop-type, then press Enter via helmdeck__desktop-key. Once the form is loaded, use helmdeck__vision-fill_form_by_label with fields={"customer name":"Alice","telephone":"555-0100"} and model=openrouter/anthropic/claude-haiku-4.5. Tell me which fields_filled and the steps used.

**Tool call** (5 calls, no failures):

```json
{
  "name": "helmdeck__desktop-run_app_and_screenshot",
  "arguments": {
    "command": "xdotool",
    "args": [
      "search",
      "--class",
      "Chromium"
    ]
  }
}
```

**Pack response** (truncated):

```json
{"pack":"desktop.run_app_and_screenshot","version":"v1","output":{"artifact_key":"desktop.run_app_and_screenshot/b9741fbf798ac639-screen.png","command":"xdotool","size":27275},"artifacts":[{"key":"desktop.run_app_and_screenshot/b9741fbf798ac639-screen.png","url":"http://garage:3900/helmdeck-artifacts/desktop.run_app_and_screenshot/b9741fbf798ac639-screen.png?X-Amz-Algorithm=AWS4-HMAC-SHA256\u0026X-Amz-Credential=GKaef177ebe9fd814dc103b84b%2F20260508%2Fgarage%2Fs3%2Faws4_request\u0026X-Amz-Date=20260508T133135Z\u0026X-Amz-Expires=900\u0026X-Amz-SignedHeaders=host\u0026X-Amz-Signature=795270d222126ba82290d29e96aec63b1539e2aa28fa97ac0503c2e7a647d978","size":27275,"content_type":"image/png","created_at":"2026-05-08T13:31:35.938794922Z","pack":"desktop.run_app_and_screenshot"}],"duration_ms":11
  ...[truncated]
```

```json
{
  "name": "helmdeck__vision-click_anywhere",
  "arguments": {
    "goal": "click the URL bar at the top of the Chromium window to focus it",
    "model": "openrouter/anthropic/claude-haiku-4.5"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"vision.click_anywhere","version":"v1","output":{"completed":false,"final_action":{"action":"click","x":377,"y":69,"reason":"Click on the URL bar to focus it for user input"},"steps":6},"artifacts":[{"key":"vision.click_anywhere/d6b1e0922577b5e6-step-000.png","url":"http://garage:3900/helmdeck-artifacts/vision.click_anywhere/d6b1e0922577b5e6-step-000.png?X-Amz-Algorithm=AWS4-HMAC-SHA256\u0026X-Amz-Credential=GKaef177ebe9fd814dc103b84b%2F20260508%2Fgarage%2Fs3%2Faws4_request\u0026X-Amz-Date=20260508T133204Z\u0026X-Amz-Expires=900\u0026X-Amz-SignedHeaders=host\u0026X-Amz-Signature=ad00bd187a4aa9e65ccc93df6180322dea14c7dcd1945d4a1a602a11e428504f","size":27275,"content_type":"image/png","created_at":"2026-05-08T13:32:04.976622197Z","pack":"vision.click_anywhere"},{"key":"vision.click_a
  ...[truncated]
```

```json
{
  "name": "helmdeck__desktop-run_app_and_screenshot",
  "arguments": {
    "command": "xdotool",
    "args": [
      "type",
      "https://httpbin.org/forms/post"
    ]
  }
}
```

**Pack response** (truncated):

```json
{"pack":"desktop.run_app_and_screenshot","version":"v1","output":{"artifact_key":"desktop.run_app_and_screenshot/f7651dffeb5a0868-screen.png","command":"xdotool","size":27275},"artifacts":[{"key":"desktop.run_app_and_screenshot/f7651dffeb5a0868-screen.png","url":"http://garage:3900/helmdeck-artifacts/desktop.run_app_and_screenshot/f7651dffeb5a0868-screen.png?X-Amz-Algorithm=AWS4-HMAC-SHA256\u0026X-Amz-Credential=GKaef177ebe9fd814dc103b84b%2F20260508%2Fgarage%2Fs3%2Faws4_request\u0026X-Amz-Date=20260508T133239Z\u0026X-Amz-Expires=900\u0026X-Amz-SignedHeaders=host\u0026X-Amz-Signature=21c633607c473e565e6c5e8e40da203de7a08f403094b89bb9ae131e40fa0fc8","size":27275,"content_type":"image/png","created_at":"2026-05-08T13:32:39.576996584Z","pack":"desktop.run_app_and_screenshot"}],"duration_ms":12
  ...[truncated]
```

```json
{
  "name": "helmdeck__desktop-run_app_and_screenshot",
  "arguments": {
    "command": "xdotool",
    "args": [
      "key",
      "Return"
    ]
  }
}
```

**Pack response** (truncated):

```json
{"pack":"desktop.run_app_and_screenshot","version":"v1","output":{"artifact_key":"desktop.run_app_and_screenshot/d72ec656ff2b8a0a-screen.png","command":"xdotool","size":27275},"artifacts":[{"key":"desktop.run_app_and_screenshot/d72ec656ff2b8a0a-screen.png","url":"http://garage:3900/helmdeck-artifacts/desktop.run_app_and_screenshot/d72ec656ff2b8a0a-screen.png?X-Amz-Algorithm=AWS4-HMAC-SHA256\u0026X-Amz-Credential=GKaef177ebe9fd814dc103b84b%2F20260508%2Fgarage%2Fs3%2Faws4_request\u0026X-Amz-Date=20260508T133254Z\u0026X-Amz-Expires=900\u0026X-Amz-SignedHeaders=host\u0026X-Amz-Signature=26a08c65cc642de4affabe16fc992a19211b74b8017cde91ec69c713e4d21395","size":27275,"content_type":"image/png","created_at":"2026-05-08T13:32:54.749119218Z","pack":"desktop.run_app_and_screenshot"}],"duration_ms":11
  ...[truncated]
```

```json
{
  "name": "helmdeck__vision-fill_form_by_label",
  "arguments": {
    "fields": {
      "customer name": "Alice",
      "telephone": "555-0100"
    },
    "model": "openrouter/anthropic/claude-haiku-4.5"
  }
}
```

**Pack response** (truncated):

```json
{"error":"handler_failed","message":"parse action: no parseable JSON object found"}
```

**Agent's final reply**:

> I couldn’t complete the form‑filling step because the vision‑fill‑form‑by‑label call failed (invalid JSON payload).

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.1946).*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/vision.fill_form_by_label \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "fields": {"customer name":"Alice","telephone":"555-0100"},
    "model":  "openrouter/anthropic/claude-haiku-4.5"
  }'
```

Response shape:

```json
{
  "pack": "vision.fill_form_by_label",
  "version": "v1",
  "output": {
    "completed":     true,
    "fields_filled": ["customer name","telephone"],
    "steps":         3
  },
  "duration_ms": …,
  "session_id": "…"
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `model` empty | `{"error":"invalid_input","message":"model must not be empty"}` |
| `invalid_input` | `fields` map empty | `{"error":"invalid_input","message":"fields must contain at least one entry"}` |
| `session_unavailable` | Engine has no session executor | `{"error":"session_unavailable","message":"engine has no session executor"}` |
| `handler_failed` | Vision step failed (screenshot, model call, parse) | `{"error":"handler_failed","message":"…"}` |

When `completed: false` is returned without a top-level error, the pack ran out of steps before all fields were filled. Inspect `fields_filled` to see how far it got and consider raising `max_steps`.

## Session chaining

**Required (creates if absent).** Typical chain:

```
desktop.screenshot → vision.click_anywhere (focus URL bar) → desktop.type (URL) →
desktop.key (Enter) → vision.fill_form_by_label → vision.click_anywhere (Submit) →
vision.extract_visible_text (verify success)
```

Always pass `_session_id` through every step — see the [Session chaining contract](/integrations/SKILLS#session-chaining-contract--read-before-chaining-fs--cmdrun--git).

## Async behavior

Synchronous. Wall-clock = `len(fields) × (screenshot + model_latency + xdotool)`. A 5-field form on a Haiku-tier model is typically 15–25 seconds.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `vision.fill_form_by_label`.
- Source: [`internal/packs/builtin/vision_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/vision_packs.go).
- ADR 027 — Vision pipeline.
- Companion packs: [`vision.click_anywhere`](./click-anywhere.md), [`vision.extract_visible_text`](./extract-visible-text.md).
