# 4. Ephemeral Stateless Browser Sessions

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: distributed-systems

## Context
Chromium leaks memory under sustained autonomous load and tends to OOMKill after ~20 h. Long-lived sessions accumulate cookie state, cache pollution, and DOM-mutation hazards that complicate per-tenant isolation (PRD §2, §6.1).

## Decision
Every browser session is a freshly spawned container with `restartPolicy: Never`, a configurable `timeout` (default 300 s), a `maxTasks` cap, and `/dev/shm` provisioned as a memory-backed `emptyDir` of at least 2 GiB. The Go control plane runs a watchdog that recycles any session exceeding its memory budget or idle timeout. Persistent state (cookies, auth) lives in the Credential Vault, not the session.

## Consequences
**Positive:** memory leaks become non-events; per-session security boundary; predictable resource accounting.
**Negative:** cold-start cost (mitigated by `browser-pool-warmup` Deployment); cookie/session reuse must round-trip the vault.

## Related PRD Sections
§6.1 Browser Session Management, §11.1 BrowserSession data model, §14 Credential Vault, §20.5 Session Pod Spec
