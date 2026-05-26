---
slug: clones-arent-browser-state
title: "Clones aren't browser state: persisting git across ephemeral sessions"
authors: [tosin]
tags: [agent-architecture, cost]
description: Helmdeck sessions are deliberately ephemeral — Chromium leaks, so every session is a fresh container that's torn down after use. That made repo.fetch re-clone and re-`npm install` on every run. The fix wasn't to weaken the ephemerality; it was to notice that a git working tree was never the thing ADR 004 wanted thrown away.
image: /img/social-card.png
date: 2026-05-26
draft: true
---

## Hook

Helmdeck's sessions are ephemeral on purpose: [ADR 004](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/004-ephemeral-stateless-browser-sessions.md) makes every browser session a fresh container with a watchdog that recycles it, because Chromium leaks memory under sustained autonomous load and OOM-kills after ~20h. Good rule. But it had a side effect nobody designed: `repo.fetch` cloned into the session's `/tmp`, so the clone died with the session. Every autonomous code-fix run re-cloned the repo and re-ran `npm install` / `go mod download` from cold. The fix for v0.14.0 ([#259](https://github.com/tosin2013/helmdeck/issues/259), [ADR 040](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/040-persistent-repos-volume.md)) is one sentence of architecture: a git working tree is not browser state, so ADR 004 was never talking about it.

## Context

The flagship example in our memory-layer proposal was "`repo.fetch` remembers the clone location across sessions and just `git pull`s." It reads like a memory-layer win. It isn't — and conflating the two would have been a mistake. Memory (the [`ec.Memory` seam we shipped alongside](/blog/memory-as-a-default-off-seam)) is an encrypted key-value tier; it records *facts*. A 200 MB working tree plus a `node_modules` is not a fact, it's a filesystem. Persisting it needed real infrastructure, and it sat on top of a since-fixed session-reuse bug ([#232](https://github.com/tosin2013/helmdeck/issues/232)). So we filed it separately and built it separately.

The tension to resolve was the interesting part. ADR 004 says, in normative terms, *persistent state lives outside the session container.* Cookies, the DOM, the Chromium cache — all discarded on terminate, by design. If we let a clone survive a session, are we violating that?

## Finding

No — and seeing *why not* is the whole design. ADR 004 is about **browser** state: the things that make a long-lived Chromium dangerous (memory growth, cookie accumulation, cross-tenant DOM bleed). A checked-out git tree has none of those properties. It's a build artifact sitting on disk. The mistake wasn't persisting it; the mistake was ever letting it land *inside* the session container's `/tmp` in the first place.

So persistent repos move the clone *out* of the container onto a named volume (`helmdeck-repos`), mounted into each fresh session at `/repos`:

```
/repos/<caller>/<repo-hash>/          # the git working tree (clone)
/repos/<caller>/<repo-hash>/.hdcache/ # the per-language dependency cache
```

The session, Chromium, and `/dev/shm` stay every bit as ephemeral as before — still `RemoveVolumes: true` on terminate. We didn't weaken ADR 004; we *strengthened* its invariant, because the clone no longer leaks into the sidecar at all. A second `repo.fetch` for the same repo — even from a brand-new session — finds the existing tree under an `flock` and runs `git fetch` + reset-to-clean instead of a cold clone.

The headline number isn't the clone, though. Cloning is cheap. The expensive thing an autonomous code-fix loop does over and over is **install dependencies**. So the clone gets a sibling `.hdcache/`, and the language packs point their cache environment at it:

```
GOMODCACHE      → /repos/<caller>/<hash>/.hdcache/go-mod
npm_config_cache→ /repos/<caller>/<hash>/.hdcache/npm
PIP_CACHE_DIR   → /repos/<caller>/<hash>/.hdcache/pip
CARGO_HOME      → /repos/<caller>/<hash>/.hdcache/cargo
```

`git clean -fdx -e .hdcache` preserves it across reuse. The first `swe.solve` on a repo pays the full `npm install`; the second — minutes or hours later, in a different session — gets a warm cache. For a loop that iterates on the same repo dozens of times, that's the difference between paying the install tax once and paying it every step.

The honest negatives, made normative in the ADR rather than swept under it:

- **Concurrency.** Two sessions touching the same clone is a corruption risk. Every reuse takes a per-repo `flock`; a loser either waits or falls back to a private `/tmp` clone. The clone is never half-mutated.
- **Dirty trees.** A prior session may have left uncommitted work. Reuse resets to a clean ref (`git reset --hard` + `git clean -fdx -e .hdcache`) before handing the tree on.
- **Disk.** Persistent things grow. A repos janitor — the on-disk twin of our artifact janitor — evicts clones untouched past a TTL (14d default) and enforces a total-size cap with LRU eviction. It takes the same `flock` non-blocking, so it never yanks a clone out from under a live session.
- **Isolation.** Clones are namespaced per caller, but a shared writable volume is a softer boundary than a per-session container. That's fine for single-tenant-today; the `<caller>/` path prefix is the seam where harder isolation (per-subject volumes, or a control-plane-mediated mount) slots in later without changing the `repo.*` contract.

And the safety contract that made it landable: it's **default-off**. No volume configured ⇒ `ec.PersistentReposPath` is empty ⇒ `repo.fetch` mktemps a `/tmp` clone, byte-for-byte as before. The bundled Compose turns it on; a hand-rolled deployment opts in by naming the volume.

## Why this matters to you

When a system has a strong, correct invariant — "sessions are ephemeral" — the easy failure mode is to treat it as a wall and route *everything* around it, or to chip a hole in it for the one case that hurts. Both are wrong. The right move is to ask what the invariant was actually protecting. ADR 004 was protecting you from a leaky, stateful *browser*. It was never protecting you from a folder of source code. Once that's named out loud, the design writes itself: keep the dangerous thing ephemeral, move the cheap durable thing to durable storage, and put a janitor on it.

If you're building agent infrastructure with ephemeral execution environments, you'll hit this exact fork the moment your agents start doing real work that has setup cost — clones, dependency installs, model weights, build caches. Don't weaken the isolation, and don't make the agent own a side-channel. Find the seam where "the thing that must be ephemeral" and "the thing that's just expensive to recompute" come apart. They almost always do.

## See also

- [ADR 040 — Persistent repos volume + cross-session clone reuse](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/040-persistent-repos-volume.md)
- [ADR 004 — Ephemeral stateless browser sessions](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/004-ephemeral-stateless-browser-sessions.md) — the invariant this works within
- [`repo.fetch` reference](/reference/packs/repo/fetch) — the `reused` / `persistent` output fields and the env knobs
- [Universal memory that's invisible until you opt in](/blog/memory-as-a-default-off-seam) — the sibling v0.14.0 seam, and why repo caching is *not* a memory-layer benefit
- [Issue #259](https://github.com/tosin2013/helmdeck/issues/259) / [#232](https://github.com/tosin2013/helmdeck/issues/232) — the feature and the session-reuse bug that gated it
