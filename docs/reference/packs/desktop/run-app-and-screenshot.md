---
title: desktop.run_app_and_screenshot
description: Launch an app on the visible XFCE4 desktop and capture a screenshot after a settle delay. Chromium is pre-launched; this pack is for everything else (xterm, file manager, GIMP, etc.).
keywords: [helmdeck, desktop, xfce4, screenshot, xdotool, scrot, MCP]
---

# `desktop.run_app_and_screenshot`

The "launch this app and show me what happens" pack. Caller supplies a `command` (and optional `args`); the pack launches it detached on the session's visible XFCE4 desktop (`DISPLAY=:99`), waits a settle delay, then captures the full desktop with `scrot`. Returns the screenshot as an artifact key + size.

**Chromium is already running** on every desktop-mode session. Don't call this pack to "launch chromium" — the window is already there. Use this for anything else: xterm for shell work an operator can watch, the file manager, GIMP for image edits, LibreOffice for document work.

The session must be in **desktop mode** (`HELMDECK_MODE=desktop`). The pack's `SessionSpec` sets that automatically when it creates a fresh session, so no extra wiring needed.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `command` | `string` | yes | — | Executable name. Resolved via `PATH` inside the sidecar. e.g. `xterm`, `gimp`, `xdg-open`. |
| `args` | `array` | no | `[]` | Argument list. Each item is one argv element; never shell-expanded. e.g. `["-geometry", "120x40"]` for xterm. |
| `wait_ms` | `number` | no | `1500` | Settle delay (ms) between launching and screenshotting. Capped at 60000. Bigger windows / slower apps benefit from `3000`–`5000`. |
| `_session_id` | `string` | yes (chained) | — | Standard chained input. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `artifact_key` | `string` | `desktop.run_app_and_screenshot/<rand>-screen.png`. Resolve via `/api/v1/artifacts/<key>`. |
| `size` | `number` | PNG bytes. |
| `command` | `string` | Echo of the command that was launched. |

## Vault credentials needed

**None.**

## Use it from your agent (OpenClaw chat-UI worked example)

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Use helmdeck__desktop-run_app_and_screenshot to launch xterm with args ["-geometry", "120x40"] on the visible desktop with wait_ms=2000. Tell me the artifact_key and the size in bytes.

**Tool call** (1 call, no failures):

```json
{
  "name": "helmdeck__desktop-run_app_and_screenshot",
  "arguments": {
    "args": [
      "-geometry",
      "120x40"
    ],
    "command": "xterm",
    "wait_ms": 2000
  }
}
```

**Pack response** (truncated):

```json
{"pack":"desktop.run_app_and_screenshot","version":"v1","output":{"artifact_key":"desktop.run_app_and_screenshot/67fd9a5791e5ade8-screen.png","command":"xterm","size":27275},"artifacts":[{"key":"desktop.run_app_and_screenshot/67fd9a5791e5ade8-screen.png","url":"http://garage:3900/helmdeck-artifacts/desktop.run_app_and_screenshot/67fd9a5791e5ade8-screen.png?X-Amz-Algorithm=AWS4-HMAC-SHA256\u0026X-Amz-Credential=GKaef177ebe9fd814dc103b84b%2F20260508%2Fgarage%2Fs3%2Faws4_request\u0026X-Amz-Date=20260508T152307Z\u0026X-Amz-Expires=900\u0026X-Amz-SignedHeaders=host\u0026X-Amz-Signature=47aa8156e589b180d473e23b5cd65e15537663423880e11ba75ff770a54336e4","size":27275,"content_type":"image/png","created_at":"2026-05-08T15:23:07.542045078Z","pack":"desktop.run_app_and_screenshot"}],"duration_ms":1242
  ...[truncated]
```

**Agent's final reply**:

> artifact_key: desktop.run_app_and_screenshot/67fd9a5791e5ade8-screen.png  
> size: 27,275 bytes

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0013).*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/desktop.run_app_and_screenshot \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "command":  "xterm",
    "args":     ["-geometry", "120x40"],
    "wait_ms":  2000
  }'
```

Response shape:

```json
{
  "pack": "desktop.run_app_and_screenshot",
  "version": "v1",
  "output": {
    "artifact_key": "desktop.run_app_and_screenshot/abc123-screen.png",
    "size":         185432,
    "command":      "xterm"
  },
  "session_id": "..."
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `command` empty | `command is required` |
| `invalid_input` | `wait_ms` negative | `wait_ms must be ≥ 0` |
| `handler_failed` | `command` not found in sidecar PATH | `nohup: failed to run …: No such file or directory` |
| `handler_failed` | `scrot` exit non-zero (Xvfb crashed) | `scrot exit N: <stderr>` |
| `session_unavailable` | Engine has no session executor | `engine has no session executor` |

## Session chaining

**Required (creates if absent).** Sets `HELMDECK_MODE=desktop` on session creation so the sidecar boots with Xvfb + XFCE4 + noVNC. Compatible chains:

- **Upstream**: typically the first call in a desktop-mode workflow.
- **Downstream**: `vision.click_anywhere` (click on something the screenshot revealed), `vision.extract_visible_text` (OCR the screenshot), or any of the [16 `desktop.*` REST primitives](../desktop-rest-primitives.md) (deterministic click/type/scroll once you know the coordinates).

Always pass `_session_id` to follow-on calls — see [SKILLS.md §"Session chaining contract"](/integrations/SKILLS#session-chaining-contract--read-before-chaining-fs--cmdrun--git).

## Async behavior

Synchronous. Wall-clock = sidecar boot (cold session ~10–20s for desktop mode) + `wait_ms` + screenshot (~1s).

## What the screenshot looks like

The session's Xvfb display (`DISPLAY=:99`) starts with a **blank gray desktop** — XFCE4 with no wallpaper, a minimal panel at the bottom, and Chromium pre-launched somewhere on the canvas. A capture taken immediately after launching xterm shows that mostly-empty desktop with the new xterm window in the corner. **This is the expected baseline**, not a rendering bug.

If you see a screenshot that looks "blank" — flat gray, with maybe a thin window border — that's likely Xvfb's default background plus the launched app's window in a position you didn't expect. The PNG itself is fine; the desktop just doesn't have a richer baseline.

If you want a visually richer baseline (wallpaper, larger panel, themed window decorations), an operator can install one into the sidecar entrypoint at [`deploy/docker/sidecar-entrypoint.sh`](https://github.com/tosin2013/helmdeck/blob/main/deploy/docker/sidecar-entrypoint.sh) — drop a `feh --bg-scale /path/to/wallpaper.png &` line right after `startxfce4` and rebuild the sidecar image. Helmdeck does not ship a wallpaper to keep the sidecar lean.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `desktop.run_app_and_screenshot`.
- Source: [`internal/packs/builtin/desktop_run_app.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/desktop_run_app.go).
- Companion: [`desktop.* REST primitives`](../desktop-rest-primitives.md) — the 16 deterministic screenshot/click/type/key/etc. endpoints.
- Companion: [`vision.*` packs](../vision/click-anywhere.md) — the AI-driven counterparts that drive the same desktop session.
- Agent decision table: [SKILLS.md §"Driving the visible desktop"](/integrations/SKILLS#driving-the-visible-desktop-when-the-operator-is-watching).
