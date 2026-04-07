# 21. Pack: `browser.screenshot_url`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
The simplest possible browser task — "take a screenshot of this URL" — still requires session creation, navigation, render wait, capture, and cleanup. It is the most-called pack and the canonical example of progressive disclosure (PRD §6.6, §19.10).

## Decision
Ship `browser.screenshot_url` as a built-in pack.

**Input:** `{ url: string, full_page?: boolean (default true), wait_ms?: integer (default 1000), viewport?: { width: integer, height: integer } }`
**Output:** `{ screenshot_url: string, dimensions: { width: integer, height: integer } }`
**Errors:** `not_found`, `timeout`, `network_error`

The handler acquires a session from the warm pool, navigates, waits the requested duration after network idle, captures, uploads, and recycles. Default viewport is 1280×800.

## Consequences
**Positive:** the smallest possible weak-model-friendly call; the pack used to validate the entire substrate end-to-end.
**Negative:** none material — this is the reference pack.

## Related PRD Sections
§6.6 Capability Packs, §19.10 Progressive Disclosure
