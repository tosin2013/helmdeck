---
slug: v0-13-0-marketplace-beta
title: "v0.13.0 ships Marketplace beta — install community packs from a signed catalog"
authors: [tosin]
tags: [release-notes, agent-architecture]
description: helmdeck v0.13.0 turns the in-tree pack catalog into one of two paths. Built-ins still ship in the binary, but anything else — from a one-script helper to a video-rendering toolchain — can live in a community marketplace, install with a single REST call or the new CLI, and run inside a dedicated sidecar. Plus hyperframes.render, stock.search, contrast guardrails, and three new ADRs.
image: /img/social-card.png
date: 2026-05-15
draft: false
---

## Hook

Helmdeck v0.13.0 lands the marketplace beta. Operators can now install a community-published pack from a signed catalog — one REST call or one `helmdeck pack install` command — and the pack appears in `tools/list` immediately, no restart, no rebuild. The headline change is structural: helmdeck is no longer "the 41 packs that ship in the binary." It's the 41 built-ins **plus everything else operators want to install from the community surface**.

## Context

The v0.13.0 cycle was framed in [`docs/RELEASES.md`](https://github.com/tosin2013/helmdeck/blob/main/docs/RELEASES.md) as "Marketplace beta," and four task-IDs (T810 catalog, T812 install/uninstall, T813 UI, T814 community repo) carried that thread. Alongside the marketplace track, the cycle absorbed the bigger pack lift queued from the v0.12.0 retro (`hyperframes.render`, #200), one community feature request (`stock.search` over Pexels, #218), an accessibility bug class found in production (`slides.render` contrast, #202), a diagnostics improvement that came out of debugging real failing pack calls (`provider_calls` columns, #183), the completion of the v0.12.0 subprocess-pack MVP (manifest format, #173), and a content-pack reliability refactor (`blog.publish` artifact-first, #203). Eight headline threads in one release tag.

Two things made the cycle feel coherent rather than a grab-bag. First: every thread either *was* marketplace work or *unblocked future* marketplace work. `hyperframes.render` validated the sidecar-per-pack pattern that marketplace packs needed; the contrast-guardrails work taught the lint pattern we'll reuse for marketplace-pack validation; the subprocess manifest format is what marketplace `command`-handler packs use. Second: three new ADRs land with the cycle. **ADR 034** captured marketplace design ahead of implementation. **ADR 037** turned the hyperframes-npm-pin incident (an unpinned upstream that broke build on Dependabot's next bump) into a project-wide rule. **ADR 038** explains why marketplace packs can't run in the control plane.

## Finding

The decision that mattered most was **ADR 038 — marketplace packs route through a dedicated sidecar**.

The helmdeck control plane is `gcr.io/distroless/static:nonroot` — no shell, no `jq`, no Python, no `node`. That's by design: smaller attack surface, faster boot, no untrusted user code reaching the orchestrator. But marketplace `command`-handler packs need `bash` to dispatch, `jq` to parse input, `python3` / `node` for actual work. Running them in the control plane would mean dropping distroless. Running them in the existing `helmdeck-sidecar-browser` image would mean tying every marketplace pack to Chromium's 1.2 GB footprint.

The answer was a new `helmdeck-sidecar-marketplace` image — Debian-slim base, `bash` + `jq` + `curl` + `python3` + Node 20 + the standard Unix utilities. Installed marketplace packs get their handler script uploaded to the sidecar via `ec.Exec` on each call, `chmod +x`'d, and piped the pack input on stdin. The same execution model that `slides.narrate` and `hyperframes.render` use. Pack authors who need a heavier toolchain — image processing, video, ML — can override the sidecar per-pack via `handler.sidecar.image` in their `helmdeck-pack.yaml` manifest.

The trust model ships as **stage A**: a deterministic SHA256 over the pack's non-manifest files (the manifest is excluded to avoid the chicken-and-egg of "the file containing the hash is in the hash"). The maintainer-run `scripts/populate-trust-hashes.mjs` in the marketplace repo writes the hash + `signed_by` block into each pack's manifest. The control plane recomputes the hash on install and hard-rejects on mismatch, removing the materialized files and returning `trust verification failed`. Stage A catches: handler/data modified between author-sign and install, file rename/add/remove, corrupt downloads. Stage A does **not** catch: a malicious author modifying the manifest itself — that's **stage B** (full sigstore keyless cosign-verify of the signer identity), tracked as a v1.0 hardening item.

```text
$ helmdeck pack marketplace
NAME            DESCRIPTION                           TRUST
cmd.upper       Uppercase a string. Demo pack.        Signed (bf22197...)
ai.review       Code review on a unified diff.        Signed (a1c44de...)
gif.make        Build an animated GIF from frames.    Signed (e3811a0...)

$ helmdeck pack install cmd.upper
Installing cmd.upper from tosin2013/helmdeck-marketplace ...
✓ trust verified (sha256 = bf2219701e87ce52d5e4d7867e5b5f01e54f70b29031c4e1a7e8fe4402da3276)
✓ pack registered (cmd.upper@0.1.0)
```

## Why this matters to you

If you've been wanting to ship a pack but didn't want to fork the helmdeck repo, your path just opened up. Send a PR to `helmdeck-marketplace` with `packs/<your-name>/helmdeck-pack.yaml` + a `handler.sh` (or a Python script, or a Node binary), and operators can install it without waiting for a helmdeck release. The pack-install loop is hot — no restart, no rebuild, the pack appears in `tools/list` immediately and the UI's pack list re-renders.

If you're an operator, the new `helmdeck` CLI is the cleanest path to driving the marketplace from CI. `helmdeck pack install <name> --json | jq '.trust_verified'` gives you a one-liner gate. The CLI ships via goreleaser alongside `control-plane` and `helmdeck-mcp`, so the same release artifacts that operators are already pulling now include the CLI binary.

If you're considering helmdeck for the first time: the value prop hasn't changed (≥90% pack success on 7B–30B-class open-weight models, schema-validated tools, vault-injected credentials, audited gateway). What did change is that the *catalog* is no longer a static thing baked into the binary at release time.

## See also

- [Marketplace catalog reference](/docs/reference/marketplace/catalog)
- [Use the helmdeck CLI](/docs/howto/use-the-helmdeck-cli)
- [ADR 038 — Marketplace pack execution via sidecar](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/038-marketplace-pack-execution-via-sidecar.md)
- [ADR 037 — Upstream package version management](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/037-upstream-package-version-management.md)
- [ADR 034 — Pack marketplace](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/034-pack-marketplace.md)
- [v0.13.0 changelog](/changelog#0130---2026-05-15)
- [`helmdeck-marketplace` repo](https://github.com/tosin2013/helmdeck-marketplace)
