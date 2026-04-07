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

## Related PRD Sections
§6.6 Capability Packs, §14 Credential Vault
