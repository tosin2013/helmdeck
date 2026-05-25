---
slug: memory-as-a-default-off-seam
title: "Universal memory that's invisible until you opt in: a default-off engine seam"
authors: [tosin]
tags: [agent-architecture]
description: We added an agent-memory layer to every pack in helmdeck without changing how a single existing pack behaves. The trick was making the seam default-off at two independent gates.
image: /img/social-card.png
date: 2026-05-25
draft: true
---

We shipped the first implementation of the Universal Memory layer (ADR 039)
this release — a namespace-scoped, TTL-aware key/value store that any pack
can use to remember things between runs. swe.solve recalls prior solves for
a repo; `github.list_issues` serves a read-through cache instead of burning
GitHub rate limit on every identical call.

The interesting part isn't the store. It's that we threaded a new capability
through the **center** of the pack engine — the pipeline every pack runs
through — without changing the observable behavior of a single existing pack.

<!-- truncate -->

## The two-gate default-off contract

The pack engine (`internal/packs/packs.go`) runs every pack through a fixed
pipeline: validate input → acquire session → invoke handler → validate output
→ collect artifacts. Memory needed a pre/post seam around the handler — exactly
the kind of change that usually ripples through every test in the suite.

We made it inert unless **two independent gates** both open:

1. **No store wired ⇒ `ec.Memory == nil`.** The engine only builds the
   memory handle when an operator configured `WithMemoryStore(...)`. A
   deployment with no `HELMDECK_MEMORY_KEY` never constructs a store, so
   handlers see a nil `ec.Memory` and the cache seam is skipped entirely.
2. **No `Pack.Memory` config ⇒ no hooks.** Even with a store wired, the
   read-through cache only runs for packs that opt in with
   `Memory: &MemoryConfig{Cache: true, TTL: ...}`. Every other pack flows
   through the pipeline byte-for-byte as before.

```go
cacheEnabled := ec.Memory != nil && pack.Memory != nil && pack.Memory.Cache
```

Both gates are off by default. The result: the cache exemplar is a one-line
opt-in on `github.list_issues`, and *nothing else moved*. The full suite
(1000+ tests) passed without touching a single existing pack test.

## Why two gates instead of one

A single gate (just `Pack.Memory`) would have been simpler, but it would
have coupled the *operator's* deployment choice to the *pack author's*
declaration. With two gates the concerns stay orthogonal:

- The pack author decides *whether this pack's output is cacheable* — a
  correctness call (never cache credential-bearing or per-call-volatile
  responses).
- The operator decides *whether memory exists at all* — an infrastructure
  call (do I want a memory key, encryption at rest, a `/data` volume).

A pack that opts into caching on a deployment with no memory key simply runs
its handler every time. No error, no warning, no behavior change. That's the
property that let us merge a center-of-engine change with confidence.

## Encryption inherited, not reinvented

The SQLite backend encrypts every value at rest with AES-256-GCM — the exact
construction the credential vault already uses (`aes.NewCipher` +
`cipher.NewGCM`, random nonce per write). Memory gets its own 32-byte master
key (`HELMDECK_MEMORY_KEY`), distinct from the keystore and vault keys, so a
leak of one domain's key doesn't expose another. The fingerprint
(`sha256(plaintext)[:16]`) is stored in the clear for cache coherence and is
safe to log.

A test asserts the property directly: write a known marker, read the raw
`value_ciphertext` column, and confirm the plaintext never appears in it —
then confirm it round-trips through decryption.

## The takeaway

When you add a cross-cutting capability to the hot path, make "off" the
zero-config default at *every* gate the feature touches. The cost is a couple
of extra nil-checks; the payoff is that the diff is provably additive and the
existing test suite is your regression net for free.
