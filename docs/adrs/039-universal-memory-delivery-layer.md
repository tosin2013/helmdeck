# 39. Universal Memory Delivery Layer

**Status**: Proposed
**Date**: 2026-05-25
**Domain**: pack-engine, distributed-systems, api-design

> **First implementation: v0.14.0** — pluggable `MemoryStore` (SQLite default, AES-256-GCM at rest), the `ExecutionContext.Memory` engine seam + per-caller namespace, `Context()` aggregation, and the `github.list_issues` read-through cache exemplar landed default-OFF and additive (epic #254: #255/#256/#257/#258/#260). Redis-backed Episodic and the pgvector/Semantic tier remain deferred.

## Context

[ADR 029](029-four-tier-agent-memory-api.md) defines a four-tier agent memory model (Working / Episodic / Semantic / Procedural) exposed as an **explicit, agent-addressable API** (`GET/POST/DELETE /api/v1/memory/{agent_id}`). It answers *what* memory is and *who* may address it, but it leaves a gap: nothing makes memory available to the **pack handlers themselves**. Today a pack cannot cheaply remember anything — not a cached GitHub listing, not a parsed document structure — without the calling agent explicitly orchestrating the memory API around it.

The result is that the burden of memory falls entirely on the agent: it must learn the memory packs, call them in the right order, and thread keys through every invocation. Every pack that *could* avoid a redundant external call (rate-limited `github.*`, expensive `doc.parse`, slow `web.scrape`) instead repeats the work on every run.

A reality-check of the current pack engine establishes the constraints:

- `ExecutionContext` (`internal/packs/packs.go:140-161`) has no memory field. Packs receive `Input`, `Session`, `CDP`, `Artifacts`, `Exec`, `Logger`, `Progress` — nothing more.
- `Pack` (`internal/packs/packs.go:94-128`) has no memory configuration.
- `Engine.Execute` (`internal/packs/packs.go:256-436`) is a fixed straight-line path with **no pre/post interceptor seam**. There is nowhere to inject context before a handler runs or capture state after it returns.
- Sessions are keyed by raw UUID (`internal/session/types.go:60-81`); there is **no namespace, tenant, or project concept** to scope memory by.
- Storage primitives that *do* exist and should be reused: the `ArtifactStore` interface (`internal/packs/artifacts.go:30-36`) backed by Garage S3 (`internal/packs/s3store.go`), the SQLite store on a persistent `/data` volume, and the credential vault's AES-256-GCM encryption at rest (`internal/vault/vault.go`).
- Redis and PostgreSQL+pgvector — the backends ADR 029 names for the Episodic and Semantic tiers — are **not dependencies today**.

This ADR addresses the delivery gap without re-litigating ADR 029's data model.

## Decision

Introduce a **Universal Memory delivery layer**: memory as invisible infrastructure provided to every pack through its execution context, transparently backed by the platform's existing storage primitives.

### Relationship to ADR 029

This ADR **refines, and does not supersede, ADR 029.** The division of responsibility:

| Concern | Owned by |
| :--- | :--- |
| Memory data model (the four tiers) | ADR 029 |
| Agent/tenant-facing API (`/api/v1/memory/{agent_id}`) | ADR 029 |
| Semantic tier (pgvector + AI-gateway embeddings) | ADR 029 (deferred) |
| Procedural → Capability Pack promotion | ADR 029 |
| **Pack-facing access (`ec.Memory`)** | **This ADR** |
| **Engine middleware seam (context injection + auto-capture)** | **This ADR** |
| **First-implementation storage backend** | **This ADR** |

In short: ADR 029 says *what memory is and how agents address it*; this ADR says *how the Working and Episodic tiers reach pack handlers, and what backs them in the first implementation.* The Semantic tier and the procedural-promotion loop remain ADR 029's, unchanged.

### Pack-facing interface

Add an optional `Memory` field to `ExecutionContext` and an optional `Memory *MemoryConfig` to `Pack`:

```go
type MemoryInterface interface {
    Store(key string, value any, opts ...StoreOption) error
    Recall(key string) (*MemoryEntry, error)
    List(prefix string, opts ...ListOption) ([]MemoryKey, error)
    Delete(key string) error
    Namespace() string
}
```

A pack opts in by declaring a `MemoryConfig` (key prefix, default TTL, whether to auto-capture). **A pack with no `MemoryConfig` sees `ec.Memory == nil` and behaves exactly as today** — this layer is additive and default-off.

### Engine middleware seam

`Engine.Execute` gains a pre-execution and post-execution hook — the engine's **first general interceptor seam**, reusable beyond memory (e.g. tracing, quotas):

- **Pre-execution**: if the pack opts into context injection, populate `ec.Memory` and optionally attach aggregated context.
- **Post-execution**: if the pack opts into auto-capture, persist the successful output under the pack's key prefix.

Both hooks are no-ops unless the pack's `MemoryConfig` enables them. The straight-line behavior of the engine is unchanged for every pack that does not opt in.

### Storage — reuse, don't reinvent

The first implementation reuses existing primitives and adds **no new infrastructure**:

| ADR 029 tier | This-ADR backing | Reuses |
| :--- | :--- | :--- |
| Working | In-process Go map, session-scoped | — |
| Episodic | Garage S3 / SQLite | `ArtifactStore` interface (`artifacts.go:30-36`), the `/data` volume |
| (encryption at rest) | AES-256-GCM | vault crypto (`internal/vault/vault.go`) |

The `MemoryStore` interface mirrors `ArtifactStore` so the Garage S3 backend is shared. Configuration follows the established `HELMDECK_*` + `*_FILE` env convention (cf. `loadS3StoreFromEnv` in `cmd/control-plane/main.go`).

### Keying and namespace

Memory is keyed by a **namespace** = tenant/agent identity (from JWT claims, per ADR 029's scoping) plus optional session scope. No namespace concept exists today; building it is a **hard prerequisite** for any cross-session memory and is tracked as a distinct work item.

### Caching discipline (normative)

Any pack that caches external, freshness-sensitive, or credential-bearing results MUST:

- be **opt-in and default-off**,
- bound every cached entry with a **TTL**,
- **never** cache a response that carries credentials or secrets.

`http.fetch` and the `github.*` family are caching candidates only under these rules.

### Explicitly deferred

- **Redis-backed Episodic tier** and the **Semantic tier (pgvector + embeddings)** — these remain ADR 029's heavyweight path and are out of the first implementation.
- **`ec.Memory.Context()` aggregation** — the auto-built session-context payload is a later phase.

### Non-goal

Cross-session caching of cloned repositories for the `repo.*` family is **not** delivered by this layer. Clones live in ephemeral session containers ([ADR 004](004-ephemeral-stateless-browser-sessions.md)); persisting them across sessions requires a persistent repos volume and depends on resolving the session-reuse bug (#232). That is separate infrastructure, not a memory-layer benefit.

## Consequences

**Positive:**
- Memory becomes invisible infrastructure — packs gain it without the agent orchestrating memory calls.
- The engine acquires its first reusable pre/post interceptor seam, useful beyond memory.
- Reusing the `ArtifactStore` / Garage S3 / vault primitives means **zero new operational surface** in the first implementation.
- Default-off, per-pack opt-in allows a safe, incremental rollout across the pack catalog rather than a big-bang change.

**Negative:**
- Auto-capture risks staleness or data leakage if a pack opts in carelessly — mitigated by default-off plus the normative caching rules.
- The namespace model is a genuine prerequisite that touches the session model and JWT claims; cross-session memory is meaningless without it.
- Full value across the catalog is multi-release; the first implementation ships the seam plus a few exemplar packs, not the whole catalog.

## Related PRD Sections

§19.7 Agent Memory and Session Persistence, §6.6 Capability Packs.

Related ADRs: [ADR 029](029-four-tier-agent-memory-api.md) (the data model this refines), [ADR 031](031-object-store-garage-default-and-pluggable-s3.md) (the object store reused for the Episodic backing), [ADR 004](004-ephemeral-stateless-browser-sessions.md) (the ephemeral-session constraint behind the `repo.*` non-goal).
