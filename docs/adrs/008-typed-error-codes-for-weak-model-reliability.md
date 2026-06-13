---
description: "ADR-008: Closed-Set Typed Error Codes on All Pack Outputs — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 8. Closed-Set Typed Error Codes on All Pack Outputs

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
A weak model can branch reliably on `{"error": "auth_failed"}` but cannot reliably parse 12 lines of OpenSSH stderr or a git error trace. Untyped error surfaces are the primary cause of weak-model stalls observed in 2026-04-06 testing (PRD §3.1, §10).

## Decision
Every Capability Pack must define a closed set of error codes in its output schema. The Go control plane enforces that no pack returns an error outside that set — any uncategorized failure is mapped to the nearest defined code or to a generic `internal_error` with a redacted detail field. The baseline error vocabulary is: `auth_failed`, `rate_limited` (with `retry_after`), `not_found`, `timeout`, `schema_mismatch` (with `partial: true` payload), `network_error`. Additional pack-specific codes are permitted but must be enumerated in the pack's output schema.

## Consequences
**Positive:** weak models can branch on errors with high reliability; agent retry logic becomes deterministic; observability dashboards group failures by code.
**Negative:** pack authors lose flexibility to bubble raw errors; mapping logic adds work in every handler.

## Amendment (2026-06-05, [ADR 052](052-av-output-validation-post-step.md))

The `av.validate` pack and its default-on integration on `slides.narrate` / `podcast.generate` introduce a per-check **severity** axis (`pass` / `warn` / `fail`) that lives on the pack's `validation` output field, NOT on the pack's error code. The two surfaces stay deliberately separate. A failed check ("the script ran, the artifact has a 27.9-second duration mismatch") returns success at the runtime layer because the operation proceeded; the caller reads the `validation` field to decide what to do. A typed error code ("the script crashed because ffprobe was missing") returns `CodeHandlerFailed` because the operation didn't proceed. Pack handlers that need fail-fast semantics on validation findings opt in via `av.validate`'s `strict:true` input, which translates `fail`-severity check failures into `CodeArtifactFailed` — the bridge from the severity axis back to the typed-error vocabulary, used at CI publish gates. This keeps the closed-set error vocabulary closed while letting quality findings flow through as data.

## Related PRD Sections
§10 Security Model (Typed Error Codes table), §6.6 Capability Packs, §18 Success Metrics
