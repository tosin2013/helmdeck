---
description: "ADR-015: Pack: `slides.video` — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 15. Pack: `slides.video`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
Producing a narrated slide video requires Marp + Xvfb + ffmpeg + a TTS provider + audio/video muxing — five tools in sequence with brittle failure modes (PRD §6.6).

## Decision
Ship `slides.video` as a built-in pack.

**Input:** `{ markdown: string, voice_id: string, theme?: string, resolution?: "720p"|"1080p" }`
**Output:** `{ video_url: string, duration_seconds: number, page_count: integer }`
**Errors:** `auth_failed` (TTS key), `rate_limited`, `timeout`, `internal_error`

The handler renders frames via Marp + headless Chromium, calls the configured TTS provider (ElevenLabs by default; key from Credential Vault), generates per-slide audio, and muxes via ffmpeg inside an Xvfb-backed session. Resolution defaults to `1080p`.

## Consequences
**Positive:** narrated decks become a single API call; TTS provider is swappable via vault config.
**Negative:** longest-running pack (minutes); requires careful timeout and progress reporting.

## Amendment (2026-06-05, [ADR 052](052-av-output-validation-post-step.md))

The `slides.narrate` contract documented above (originally `slides.video`) gained a default-on validation post-step in Phase 3 of the validation arc ([PR #432](https://github.com/tosin2013/helmdeck/pull/432)). After the final `ConcatVideoMP4s` and the video artifact upload, the handler invokes `runAVValidation` (the shared core extracted from `av.validate`'s handler) against `/tmp/final.mp4` and the optional SRT path. The structured report — `{checks[], passed, failed, warnings, all_passed}` — lands in the pack output as a `validation` field; a `validation.json` sidecar artifact is also persisted alongside `engagement.json` and `captions.srt`. The new input `validate *bool` follows the pointer-bool default-on pattern (nil → run; `&false` → skip on benchmarks where the ~5–15-second null-muxer decode pass matters). Validation failures (script-exec failures, JSON-parse failures, or `fail`-severity check findings) NEVER fail the pack — the artifact is the value and the validation is a description of it. Operators wanting fail-fast call `av.validate` standalone with `strict:true`.

## Related PRD Sections
§6.6 Capability Packs, §14 Credential Vault
