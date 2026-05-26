# 40. Persistent Repos Volume and Cross-Session Clone Reuse

**Status**: Proposed
**Date**: 2026-05-26
**Domain**: distributed-systems, pack-engine, session-runtime

## Context

[ADR 039](039-universal-memory-delivery-layer.md) closed with an explicit **non-goal**: cross-session caching of cloned repositories for the `repo.*` family is *not* a memory-layer benefit. The Universal Memory proposal's flagship "`repo.fetch` remembers the clone and just `git pull`s next time" example reads like free memory, but it is not — clones live in **ephemeral session containers** ([ADR 004](004-ephemeral-stateless-browser-sessions.md)) and die with them. Persisting a clone across sessions requires real infrastructure. ADR 039 deferred that to issue [#259](https://github.com/tosin2013/helmdeck/issues/259); this ADR is its design.

A reality-check of the current runtime establishes the constraints:

- **Clones are ephemeral.** `repo.fetch` clones into `mktemp -d /tmp/helmdeck-clone-XXXXXX` *inside* the session sidecar (`internal/packs/builtin/repo_fetch.go:298,330`). When the session is terminated — which `Terminate()` does with `RemoveVolumes: true` (`internal/session/docker/runtime.go:305`) — the clone is gone.
- **Sessions mount no persistent storage.** `session.Spec` (`internal/session/types.go:32-58`) has no volume field; `buildHostConfig()` (`internal/session/docker/runtime.go:471-494`) mounts nothing persistent into a sidecar. `ec.PersistentReposPath` does not exist.
- **Same-session reuse already works.** The #259 blocker, [#232](https://github.com/tosin2013/helmdeck/issues/232), is fixed (#236): `repo.fetch` now surfaces `session_id` in its output (`repo_fetch.go:245-252`) so a caller can thread `_session_id` and a follow-up `fs.*` / `cmd.run` / `repo.map` reuses the same live container (`internal/packs/packs.go:323-354`, gated by `PreserveSession`). That is *within* one session's lifetime; it does nothing across sessions.
- **The memory layer can record state, not store bytes.** ADR 039's `ec.Memory` (`Store`/`Recall`/`List`/`Delete`/`Namespace`) is an encrypted key-value tier keyed by caller namespace. It is the right place to record *which* clone exists for a repo (path, last-pulled SHA, age) — but it is a metadata store, not a filesystem; the working tree itself must live somewhere durable.
- **The volume pattern already exists.** Compose declares named volumes `helmdeck-data`, `helmdeck-garage-{meta,data,credentials}` (`deploy/compose/compose.yaml:170-178`); the control-plane mounts `helmdeck-data:/data` plus the Docker socket (`:65-72`). Session sidecars are spawned on demand by the control-plane via the Docker SDK, not by Compose.

The tension to resolve: ADR 004 mandates ephemeral, per-session-isolated containers because **Chromium** leaks memory and accumulates cookie/DOM state. A persistent repos volume must not re-open that wound.

## Decision

Introduce a **persistent repos volume** mounted into session sidecars, plus a thin `repo.*` reuse path that records clone state in the ADR 039 memory layer. The working trees live on the volume; the memory layer records where they are and how fresh.

### Scope of ADR 004, restated

ADR 004's ephemerality is about **browser state** — the Chromium process, its `/dev/shm`, cookies, cache, and DOM. None of that is persisted here. **Git working trees are source artifacts, not browser state.** The session container, the browser, and `/dev/shm` remain fully ephemeral and `RemoveVolumes: true` on terminate; only a *separate, explicitly-mounted* repos volume survives, re-attached into each fresh session that needs it. ADR 004's invariant — "persistent state lives outside the session container" — is upheld, not violated: the clone now lives on a named volume instead of leaking into the sidecar's `/tmp`.

### Storage model — shared named volume, namespaced by subject and repo

A single named volume `helmdeck-repos` is mounted into every session sidecar at `/repos` (read-write). Layout:

```
/repos/<subject>/<repo-hash>/          # the git working tree (clone)
/repos/<subject>/<repo-hash>/.hdcache/ # optional per-language dependency cache
```

- `<subject>` is the JWT subject — the **same namespace the memory layer uses** (ADR 039), so a tenant's repos and a tenant's memory share one identity model.
- `<repo-hash>` is `sha256(normalized-repo-url)[:16]`. A clone is keyed by *repo*, not by language: a git working tree is language-agnostic source, so one subtree per repo serves any toolchain.
- The big cross-session win is **not** the clone bytes (cloning is cheap) but the **dependency/build cache** — `node_modules`, `.venv`, Go module cache, `target/`, `~/.m2`. These are language-specific and large, so they get a dedicated sibling `.hdcache/` subtree per repo. Persisting them is what turns a multi-minute `npm install` / `go mod download` into a no-op on the second session. Packs that populate caches (`cmd.run`, `node.run`, `python.run`) point their language's cache env (`GOMODCACHE`, `npm_config_cache`, `PIP_CACHE_DIR`, …) at `.hdcache/` when a persistent clone is in use.

**Why shared-volume-with-subdirs over the alternatives** (control-plane-managed copy cache; one volume per subject): it adds the least new surface — it mirrors the existing `helmdeck-data` pattern, needs no per-session copy step, and no volume-sprawl lifecycle. Path-prefixing by subject gives tenant separation appropriate to helmdeck's **single-tenant-today** reality (one admin; ADR 039's namespace is the same soft boundary). The harder-isolation options remain open: the `<subject>/` prefix is the seam to later swap a shared volume for per-subject volumes or a control-plane-mediated mount without changing the `repo.*` contract.

### Threading the mount

- Extend `session.Spec` with `PersistentReposPath string` (host/volume → container `/repos`).
- `buildHostConfig()` adds the `helmdeck-repos` volume to `HostConfig.Mounts` when the spec sets it.
- Add `ec.PersistentReposPath` to `ExecutionContext`, derived from the session spec, so handlers know the mount exists and where it is. When unset (no volume configured), behavior is exactly as today — ephemeral `/tmp` clone.
- Compose: declare the `helmdeck-repos` named volume; the control-plane gains an env (`HELMDECK_REPOS_VOLUME` / `HELMDECK_PERSISTENT_REPOS=1`) that, when set, makes the runtime mount it into spawned sidecars.

### `repo.fetch` reuse path

When a persistent repos path is available and the pack opts in:

1. Compute `dir = /repos/<subject>/<repo-hash>`.
2. **Recall** `ec.Memory.Recall("repo/" + repo-hash)` for prior clone metadata (path, last SHA, fetched-at).
3. If `dir` exists on the volume and metadata is fresh → `git -C dir fetch && git checkout <ref> && git pull` (fast path) instead of a full clone.
4. Else → clone into `dir`.
5. **Store** updated metadata via `ec.Memory.Store("repo/"+repo-hash, …, WithCategory("repo"))` (the source of truth for *bytes* is the volume; memory is the index).
6. Return `clone_path = dir` plus a `reused: bool` / `pulled_sha` field so callers and the smoke test can assert the fast path.

The clone path is now stable and persistent, so cross-session follow-ups (`fs.*`, `cmd.run`, `repo.map`) work against it without needing the original session alive — complementing #232's same-session fix.

### Concurrency and integrity

A shared writable clone invites two hazards, handled normatively:

- **Git-lock contention**: two sessions operating on the same `<repo-hash>` simultaneously. The reuse path MUST take a per-`repo-hash` advisory lock (e.g. `flock` on `dir/.hdlock`) around fetch/checkout/pull; a second concurrent caller either waits or falls back to a private ephemeral `/tmp` clone rather than corrupting the shared tree.
- **Dirty trees**: a prior session may leave uncommitted changes. The reuse path resets to a known-clean ref (`git reset --hard` + `git clean -fdx` scoped to exclude `.hdcache/`) before handing the tree to the next consumer, unless the caller explicitly requests preservation.

### Garbage collection

Persistent clones, unlike `/tmp`, do not die with the session, so they need their own janitor — mirroring the artifact janitor (`artifact janitor running, default_ttl 7d`). A repos janitor evicts `<subject>/<repo-hash>` subtrees whose memory metadata is older than a configurable TTL (default 14d), bounded also by a total-size cap with LRU eviction. The memory entry and the on-disk subtree are evicted together.

### Explicitly deferred

- **Hard per-tenant isolation** (per-subject volumes or control-plane-mediated mounts) — the `<subject>/` path prefix is the forward seam; real multi-tenant isolation lands when helmdeck does.
- **Shared base + per-session overlay** (copy-on-write working trees) — an optimization over advisory locking, not needed for the first implementation.
- **Cross-host volume** (NFS/CSI for a multi-node control plane) — single-host Docker volume only, consistent with the current Compose topology.

## Consequences

**Positive:**
- `repo.fetch` + the repo-loop packs (`swe.solve`, `repo.map`, `cmd.run`) skip redundant clones and, more importantly, redundant dependency installs across sessions — the headline speedup for autonomous code-fix loops.
- Clones move out of the sidecar's `/tmp` onto a named volume, *strengthening* ADR 004's "no persistent state in the session container" invariant rather than weakening it.
- Reuses the memory layer as the clone index and the existing named-volume pattern — minimal new operational surface (one volume, one janitor).
- Default-off: no repos volume configured ⇒ `ec.PersistentReposPath` unset ⇒ ephemeral `/tmp` clone, exactly as today.

**Negative:**
- Soft, path-based tenant isolation — acceptable single-tenant-today, but a shared writable volume is a weaker boundary than ADR 004's per-session container. Documented as deferred hard isolation.
- Git-lock and dirty-tree handling are genuine correctness obligations; an advisory lock plus reset-to-clean is required, not optional.
- A second janitor and a size cap are new lifecycle code; an unbounded repos volume is a disk-exhaustion risk if GC is mis-configured.
- The `.hdcache/` dependency-cache layout couples a few packs (`node.run`, `python.run`, `cmd.run`) to the persistent-repos convention to realize the install-skip win; the clone-reuse half works without it.

## Related PRD Sections

§6.1 Browser Session Management, §6.6 Capability Packs, §19.7 Agent Memory and Session Persistence.

Related ADRs: [ADR 004](004-ephemeral-stateless-browser-sessions.md) (the ephemeral-session constraint this works within), [ADR 039](039-universal-memory-delivery-layer.md) (the memory layer used as the clone index, whose `repo.*` non-goal this ADR picks up), [ADR 031](031-object-store-garage-default-and-pluggable-s3.md) (the named-volume / object-store storage precedent). Implements [#259](https://github.com/tosin2013/helmdeck/issues/259); unblocked by [#232](https://github.com/tosin2013/helmdeck/issues/232) (#236).
