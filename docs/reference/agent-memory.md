---
title: Agent memory (ec.Memory)
description: The Universal Memory delivery layer — how pack handlers get transparent, namespace-scoped memory, how to opt a pack into the read-through cache, and how operators enable it.
---

# Agent memory (`ec.Memory`)

The Universal Memory delivery layer ([ADR 039](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/039-universal-memory-delivery-layer.md), shipped in v0.14.0) gives every pack handler an optional, namespace-scoped memory capability — and an engine seam that can cache a pack's output transparently. It is **additive and default-off**: a pack that doesn't opt in, and a deployment without a memory key, behave exactly as they did before.

It refines [ADR 029](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/029-four-tier-agent-memory-api.md): ADR 029 owns the agent-facing memory *API* and data model; this layer is the *pack-facing* half — the Working and Episodic tiers reaching handlers, backed by the storage primitives helmdeck already runs.

## What a pack handler sees

When a memory store is wired, `ExecutionContext.Memory` is a namespace-scoped handle. It is **`nil` when no store is configured** — handlers MUST nil-check:

```go
if ec.Memory != nil {
    if e, err := ec.Memory.Recall("last-run"); err == nil {
        // use e.Value (decrypted bytes)
    }
    _ = ec.Memory.Store("last-run", payload, memory.WithTTL(time.Hour))
}
```

The interface:

| Method | Purpose |
|---|---|
| `Store(key, value, opts...)` | Persist bytes under `key` in the caller's namespace. Opts: `WithTTL`, `WithCategory`, `WithTags`. |
| `Recall(key)` | Fetch a single entry (or an error if absent/expired). |
| `List(prefix)` | List entries under a key prefix, newest-first. |
| `Delete(key)` | Remove an entry. |
| `Namespace()` | The caller's namespace string. |
| `Context()` | The N most-recent non-expired entries, grouped by category — a structured payload an agent can inject as prompt context (#260). |

### Namespacing

The namespace is the **authenticated caller** (JWT subject, or `unknown` when unauthenticated). When the pack ran inside a session, it's further scoped to that session (`subject:sessionID`) so a caller's concurrent runs don't bleed into each other. Memory is never addressable across namespaces — a handler cannot read another caller's memory by mistake.

> Cross-*session* persistence keyed by the bare subject (a clone that outlives one session) is deliberately **not** a memory-layer feature — see [persistent repos](./packs/repo/fetch.md#persistent-clones-across-sessions-adr-040) (ADR 040). Memory records facts; it does not store filesystems.

## The read-through cache (declarative opt-in)

A pack opts into a transparent response cache by setting `Pack.Memory`:

```go
&packs.MemoryConfig{Cache: true, TTL: 5 * time.Minute, Category: "cache"}
```

With `Cache: true`, the engine keys on `sha256(input)` under the pack name. On a fresh hit it returns the stored output and **skips the handler entirely**; on a miss it runs the handler and stores the output with the TTL. The shipped exemplar is **`github.list_issues`** (5-minute TTL) — a one-line opt-in that takes pressure off the GitHub rate limit.

**Caching discipline (normative).** A pack that caches MUST be opt-in, MUST bound every entry with a TTL, and MUST NOT cache a response that carries credentials or per-call-volatile data. `Cache: true` with `TTL: 0` means "never expire" — almost never what you want.

## Enabling it (operators)

Set a 32-byte hex master key, distinct from the vault and keystore keys (a leak of one domain must not expose another):

```bash
HELMDECK_MEMORY_KEY=<64 hex chars>   # openssl rand -hex 32
```

- **Unset** ⇒ the control plane generates an *ephemeral* key with a warning; entries become unreadable after a restart. Pin the key to persist memory across restarts.
- The store is **SQLite on the `/data` volume, AES-256-GCM encrypted at rest** (reusing the vault crypto construction). Values are ciphertext in the row; losing `HELMDECK_MEMORY_KEY` orphans them.
- If the key can't be derived or the store can't open, the memory seam stays inert and every pack behaves as before.

Backends are pluggable behind the `MemoryStore` interface; SQLite is the first implementation. Redis-backed Episodic and the pgvector/Semantic tier remain deferred per ADR 039.

## See also

- [ADR 039 — Universal Memory delivery layer](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/039-universal-memory-delivery-layer.md)
- [ADR 029 — Four-tier agent memory API](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/029-four-tier-agent-memory-api.md) (the data model this refines)
- [Blog: Universal memory that's invisible until you opt in](/blog/memory-as-a-default-off-seam)
- Source: [`internal/memory`](https://github.com/tosin2013/helmdeck/tree/main/internal/memory), [`internal/packs/packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/packs.go) (the `ec.Memory` seam).
