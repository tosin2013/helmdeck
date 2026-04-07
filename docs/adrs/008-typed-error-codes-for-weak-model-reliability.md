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

## Related PRD Sections
§10 Security Model (Typed Error Codes table), §6.6 Capability Packs, §18 Success Metrics
