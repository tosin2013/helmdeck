---
slug: trust-stage-a-hash-of-hash
title: "Trust stage A: when the file containing the hash is in the hash"
authors: [tosin]
tags: [security, agent-architecture]
description: Helmdeck's marketplace verifies that a pack's content hasn't been tampered with by comparing a SHA256 of every file in the pack against the hash stored in the pack's manifest. But the manifest IS one of the files in the pack — including it in the hash creates a circular dependency. The one-line fix and the threat-model boundary it draws.
image: /img/social-card.png
date: 2026-05-20
draft: false
---

## Hook

Helmdeck v0.13.0's marketplace beta verifies installed packs by comparing a SHA256 over every file in the pack against the hash stored in the pack's manifest. The fix to the obvious circular dependency — the manifest contains the hash, so including the manifest in the hash creates a chicken-and-egg — is one line of Go:

```go
if rel == "helmdeck-pack.yaml" { return nil }  // exclude the manifest
```

What that one line buys, what it deliberately gives up, and why "stage A" is enough for v0.13.0 even though "stage B" is the real answer.

## Context

PR [#222](https://github.com/tosin2013/helmdeck/pull/222) replaced the structured stub from PR #220 with real trust verification: when an operator installs a pack from the marketplace, the control plane recomputes a SHA256 over the pack's content and rejects the install if it doesn't match what the pack's manifest declares.

The shape of a marketplace pack on disk:

```
packs/cmd.upper/
├── helmdeck-pack.yaml   ← manifest (name, version, handler, trust.sha256, signed_by)
├── handler.sh           ← the actual pack code
└── README.md            ← optional, for the marketplace UI's detail dialog
```

The maintainer-run script in the marketplace repo ([`populate-trust-hashes.mjs`](https://github.com/tosin2013/helmdeck-marketplace/blob/main/scripts/populate-trust-hashes.mjs)) walks each pack directory, computes the hash, and writes it into `helmdeck-pack.yaml`'s `trust.sha256` field. The control plane recomputes on install and verifies.

This sounds simple. The first cut wasn't.

## Finding

The naive walk:

```go
err := filepath.Walk(packDir, func(path string, info os.FileInfo, _ error) error {
    if info.IsDir() { return nil }
    rel, _ := filepath.Rel(packDir, path)
    body, _ := os.ReadFile(path)
    inner := sha256.Sum256(body)
    fmt.Fprintf(outer, "%s\x00%x\n", filepath.ToSlash(rel), inner)
    return nil
})
return fmt.Sprintf("%x", outer.Sum(nil)), nil
```

It walks every file (sorted by `filepath.Walk` for determinism), hashes each, folds the per-file hashes into an outer hash with the relative path as a separator. On the maintainer's machine, this computes `bf2219701e87ce52d5e4d7867e5b5f01e54f70b29031c4e1a7e8fe4402da3276` for `cmd.upper`. The maintainer writes that hash into the manifest. The maintainer commits.

The control plane recomputes on the operator's machine — and gets a *different* hash. Because the manifest now contains the hash. Which is a byte the maintainer's hash didn't see (the hash was computed before the manifest was updated), but which the operator's hash does see.

The fix:

```go
if rel == "helmdeck-pack.yaml" { return nil }
```

Exclude the manifest from the hash. Maintainer and operator both compute the same digest. The marketplace's `sign.yml` workflow does a `--check` pass on every PR to validate the in-tree hash matches what the script would compute fresh — defense in depth that no one accidentally lands a hash that wouldn't verify.

### What stage A catches

With the manifest excluded:

- **Handler code modified** between author-sign and operator-install — caught. The handler's bytes change, the file's inner hash changes, the outer hash changes.
- **Data files modified** (README, assets, prompt templates) — caught. Same reason.
- **File added** to the pack — caught. The walk visits the new path; the outer hash includes a new line.
- **File removed** — caught. One fewer line in the outer fold.
- **File renamed** — caught. The path is part of the fold key.
- **Corrupt download** (mid-transfer error, disk bitrot before install) — caught. Bytes differ from the manifest's declared hash.

The implementation hard-rejects on mismatch: removes the materialized files, deletes the install state, returns `trust verification failed`. The operator sees a clean error; the pack doesn't appear in `tools/list`. There's no "warn and proceed" path because the threat model doesn't have one.

### What stage A doesn't catch

The deliberate gap:

- **Manifest modified by a malicious author.** Anyone who controls the manifest can change `trust.signed_by`, `version`, `description`, or `handler.command` — the recomputed hash won't change, because the manifest isn't in the hash. So an attacker who can get a PR landed on `helmdeck-marketplace` could ship a manifest that says `signed_by: anthropic-security@anthropic.com` for a handler the author actually wrote.

This is what **stage B** solves: full sigstore keyless cosign-verify of the signer identity, attested through the marketplace repo's `sign.yml` workflow using OIDC. The signature commits to the manifest's bytes, so manifest-modification breaks the signature.

We deferred stage B to v1.0 hardening because v0.13.0's risk picture is bounded: the marketplace catalog defaults to `tosin2013/helmdeck-marketplace`, which we maintain. PRs are reviewed before merge. Operators can switch to a self-hosted marketplace by overriding `HELMDECK_MARKETPLACE_URL`. So "malicious author lands a PR with a forged `signed_by`" requires either a successful social-engineering campaign past PR review or a compromised maintainer account — risks that stage A doesn't address, but which also don't realistically materialize in v0.13.0's beta-scope audience.

The honest framing in the release: stage A says "this pack's content is what its manifest says it is." Stage B will say "and the signer is who the manifest says they are." For v0.13.0, the first half is enough.

## Why this matters to you

If you're designing any content-addressed packaging — extensions, plugins, packs, modules, anything you ship as a directory of files plus a metadata manifest — you will hit the same chicken-and-egg the first time you put a content hash in the manifest. There are three ways out:

1. **Exclude the manifest from the hash** (what we did). One line of code; preserves a clean fold. Gives up manifest-integrity.
2. **Two-pass hashing.** Compute the content hash with the manifest's hash field blanked out, write it in, then compute a signed-document hash over the now-populated manifest separately. Two hashes in the manifest; more bookkeeping; closes the manifest-integrity gap without needing signatures.
3. **Skip the in-manifest hash entirely** — compute the digest at distribution time, surface it externally (registry metadata, OCI manifest digest). What container images already do. Adds infrastructure but punts the bookkeeping to systems already solving it.

We picked (1) because the marketplace ships as a git repo, not an OCI registry, and the maintainer-run script is the simpler authoring story. The trade was documented in the release announcement and is exactly the right kind of gap for a beta — small, named, and the path to closing it (stage B) is clear.

The teach: **content-addressed packaging always has a hash-of-hash problem somewhere**. Find it explicitly. Decide where to put it. Document what the decision gives up. The worst version of this is silently picking (1) without writing down what it gives up, and then discovering at a later release that you've been telling users the system catches something it never did.

## See also

- [PR #222 — Marketplace trust verification stage A](https://github.com/tosin2013/helmdeck/pull/222)
- [Marketplace catalog reference §Trust model](/docs/reference/marketplace/catalog#trust-model)
- [v0.13.0 release announcement](/blog/v0-13-0-marketplace-beta)
- [Sigstore keyless signing](https://docs.sigstore.dev/cosign/signing/overview/) — the basis for v1.0's stage B
