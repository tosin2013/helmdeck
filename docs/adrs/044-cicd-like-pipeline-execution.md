---
description: "ADR-044: CI/CD-like Pipeline Execution: Resume, Retry, Re-run — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 44. CI/CD-like Pipeline Execution: Resume, Retry, Re-run

**Status**: Accepted (slice 1 shipped: failure attribution + re-run; resume + auto-retry pending)
**Date**: 2026-05-27
**Domain**: pipelines, pack-engine, api-design

## Context

Pipelines ([ADR 041](041-pipelines-as-first-class-resource.md)) today run once, fail-fast, and record per-step status/output/artifacts. When a step fails, the only recovery is to start a **fresh** run from step one — even if the failure was a transient blip or a fixable input on the last step, and even though every earlier step's output is already persisted. Operators expect pipelines to behave more like CI/CD: a failed run tells you *which* step failed and *why* (the actionable-error work in [ADR 043](043-actionable-gateway-model-errors.md) is the prerequisite — you can't usefully retry until the failure is legible), and you can **resume from the failed step**, **auto-retry transient failures**, and **re-run** with one call.

This ADR captures the agreed design so it isn't lost; **implementation is a separate PR.** Scope is the three behaviors the maintainer selected; `continue-on-error` / allow-failure was explicitly **not** requested and is out of scope.

## Decision (design)

### 1. Resume from the failed step

Add `POST /api/v1/pipelines/{id}/runs/{runId}/resume` (+ a `helmdeck__pipeline-resume` MCP tool). The runner rebuilds the in-memory `outputs` map from the persisted `RunStep.Output`s of already-succeeded steps and re-enters `RunSync`'s loop at the **first non-succeeded step index**, leaving the earlier steps untouched. This is tractable because successful step outputs are **already persisted** in `pipeline_runs.steps_json` (`internal/pipelines/store.go`, ADR 041). The only state that can't be replayed is a **session** threaded by a mid-pipeline `repo.fetch` (a resumed run may need to re-acquire it) — the design must detect a missing/expired `_session_id` and either re-run the session-producing step or fail with a clear `session_unavailable`.

### 2. Auto-retry transient failures

A per-step retry policy (N attempts, capped backoff) gated **only** on retryable typed codes — `timeout`, `rate_limited`, `session_unavailable` — and **never** `invalid_input` (caller must fix; ADR 043 ensures a bad model lands here, so it won't be retried into a loop). This overlaps ADR 005's fallback-chain rules (retry-with-a-different-*model*); the ADR must decide layering — proposal: the gateway owns model-fallback within a single dispatch, the pipeline runner owns step-level retry of the *same* step. Retry attempts are recorded on the `RunStep` (attempt count) for observability.

### 3. Re-run convenience + clearer status

`POST …/runs/{runId}/rerun` starts a **new** run of the same pipeline + inputs (distinct from resume, which continues the same run). Plus surface per-step `status`/`error`/`artifacts` prominently in run-status and the `/pipelines` UI — most of this data already exists (artifacts were added to `RunStep` in the #292 test work); this is largely a presentation change.

## Consequences

**Positive:**
- "Try again" stops meaning "redo everything"; a deck pipeline that failed at the render step re-runs just the render once the cause is fixed.
- Transient provider blips stop surfacing as hard pipeline failures.
- Reuses the persisted `steps_json` and the existing `RunSync` loop — no schema rewrite (a small additive column for attempt counts at most).

**Negative / open questions:**
- Resume + session lifetime is the sharp edge: a session from a `repo.fetch` step may have been reaped by the watchdog before resume; the design must define re-acquire-vs-fail.
- Retry/backoff interacts with the per-run timeout and with ADR 005 fallback — layering must be pinned down before coding.
- Idempotency: re-running a step that already produced an external side effect (e.g. `blog.publish` to Ghost, `email.send`) can duplicate it; resume must only re-run steps that did **not** succeed, and the docs must warn that a *manually* forced re-run of a side-effecting step is at the operator's risk.

**Out of scope:** continue-on-error / allow-failure per step (not requested); conditional steps; manual approval gates.

## Related PRD Sections

§6.6 Capability Packs, §19.7 Agent Memory and Session Persistence.

Related ADRs: [ADR 041](041-pipelines-as-first-class-resource.md) (the runner + persisted run history this extends), [ADR 043](043-actionable-gateway-model-errors.md) (legible failures — the prerequisite for useful retry), [ADR 005](005-openai-compatible-multi-provider-ai-gateway.md) (model-fallback rules the step-retry policy must layer with), [ADR 008](008-typed-error-codes-for-weak-model-reliability.md) (the retryable-vs-fixable code split the retry policy keys off).
