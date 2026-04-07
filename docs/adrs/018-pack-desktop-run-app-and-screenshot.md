# 18. Pack: `desktop.run_app_and_screenshot`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
Capturing a native GUI application normally requires Xvfb + xdotool + scrot + window focus management — four tools that must run in the right order (PRD §13, §14).

## Decision
Ship `desktop.run_app_and_screenshot` as a built-in pack.

**Input:** `{ command: string, args?: string[], wait_for: { window_title?: string, ms?: integer } }`
**Output:** `{ screenshot_url: string, window_title: string, exit_code?: integer }`
**Errors:** `timeout`, `not_found` (binary missing), `internal_error`

The handler launches the application inside an Xvfb-backed XFCE4 session, waits for the named window via `xdotool search --sync` or a fixed delay, focuses it, captures via `scrot`, and uploads the PNG.

## Consequences
**Positive:** native-app screenshots in one call; works for any X11-compatible binary.
**Negative:** binary must be present in the session image; non-X11 apps unsupported.

## Related PRD Sections
§13 Desktop Actions, §6.6 Capability Packs
