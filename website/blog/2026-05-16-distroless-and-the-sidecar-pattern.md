---
slug: distroless-control-plane-needs-to-run-bash
title: "Your distroless control plane just got a request that needs bash. What now?"
authors: [tosin]
tags: [agent-architecture, security]
description: Helmdeck's control plane ships on gcr.io/distroless/static:nonroot — no shell, no jq, no Python. The v0.13.0 marketplace beta needed all three. The decision we walked, and why "another sidecar image" was the answer that didn't compromise the orchestrator.
image: /img/social-card.png
date: 2026-05-16
draft: false
---

## Hook

Helmdeck's control plane ships on `gcr.io/distroless/static:nonroot`. No shell, no `jq`, no Python, no `node`. That's deliberate: smaller attack surface, faster boot, no untrusted user code reaching the orchestrator. v0.13.0's marketplace beta introduced a new kind of pack — operator-installed scripts from a community catalog — and the very first one (`cmd.upper`, the canonical worked example) needed all three. The two facts cannot coexist. Here's the decision tree we walked.

## Context

The v0.13.0 release tagged on 2026-05-15 carries the **Marketplace beta**: operators discover community-published packs from a signed catalog ([`tosin2013/helmdeck-marketplace`](https://github.com/tosin2013/helmdeck-marketplace) by default), install them with one REST call or one CLI invocation, and call them immediately via `tools/list`. The first three seed packs are intentionally polyglot — `cmd.upper` (bash + `jq`), `ai.review` (Python over `httpx` against the helmdeck gateway), `gif.make` (bash + ImageMagick). The point of the seeds isn't the work they do; it's proving the catalog supports any language.

Built-in packs are Go code linked into the control-plane binary, so they run wherever the binary runs. Subprocess packs (introduced as a v0.12.0 MVP) `os/exec.CommandContext` an executable in the same filesystem. Marketplace packs are subprocess packs, except the executables come from an untrusted upstream and call shell utilities the control plane doesn't ship.

## Finding

The decision space had three real options. Two had teeth.

### Option 1: drop distroless

"Just use `debian-slim` for the control plane and put bash + jq + python + node in it. Operators don't care about the base image."

Cost: every CVE in `bash`, `jq`, `python3`, `node`, and the long tail of `libc`, `libssl`, and standard utilities is now a control-plane CVE. The control plane runs as the orchestrator for browser sessions, vault unwrapping, the AI gateway, and audit logging. A `helmdeck:0.13.0` Trivy scan that goes from "no findings" (today) to "12 high-severity findings in the userland Python stdlib" is a non-trivial regression in the security narrative we've been telling design partners. Reject.

### Option 2: run packs in the browser sidecar

The browser sidecar (`helmdeck-sidecar-browser`) already has bash + Python + node + ffmpeg + Chromium + Marp + Xvfb + xdotool. It's the kitchen-sink image — about 1.2 GB compressed.

If marketplace packs run there, every install spins up a Chromium just to uppercase a string. Worse, the sidecar's session-per-pack model means a 2 GB memory budget per call where a `cmd.upper` invocation literally needs 4 MB.

The compounding issue: the browser sidecar exists to host *one* responsibility (browser automation) and is already overloaded. Quietly adding "and also runs untrusted marketplace scripts" makes its threat surface harder to reason about. Reject.

### Option 3: dedicated lean sidecar

A new image — `helmdeck-sidecar-marketplace` — based on `debian-slim`, with only what marketplace packs are documented to depend on:

```dockerfile
FROM ghcr.io/tosin2013/helmdeck-sidecar:0.13.0 AS base
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash jq curl python3 ca-certificates \
 && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/*
```

The pack handler closure in the control plane uploads the pack's `handler.sh` (or `.py`, or `.js`) to the sidecar via `ec.Exec`, `chmod +x`'s it, and pipes the pack input on stdin — the same execution model `slides.narrate` and `hyperframes.render` already used for their respective sidecars. Each call is a fresh session with the marketplace sidecar image; the script writes to stdout, the control plane reads it, the response shape matches the pack's declared output schema.

Cost: another image to maintain, another build job in CI, another tag to publish per release, another binary on the operator's pull list. Real cost, but bounded — the build is two lines of CI, the image is ~180 MB compressed, and we already have the muscle for sidecar images from `helmdeck-sidecar-browser`, `helmdeck-sidecar-hyperframes`, etc.

Returns: distroless control plane stays distroless. Marketplace packs run in an image where their dependencies are documented (not "whatever happens to be in the kitchen sink"). The threat model is clean — a malicious marketplace pack can do whatever bash/Python/node can do *inside the sidecar container*, with seccomp and the egress guard already wrapping that.

This is the answer captured in [ADR 038](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/038-marketplace-pack-execution-via-sidecar.md).

### Per-pack override

One detail that mattered for usability: pack authors with heavier toolchains (image processing, video, ML) can declare a custom sidecar image in their manifest:

```yaml
# helmdeck-pack.yaml
name: bg.remove
version: 0.1.0
handler:
  type: command
  command: handler.py
  sidecar:
    image: ghcr.io/example/rembg-sidecar:v2
```

Without that override, every heavy pack would either get jammed into the default sidecar (image bloat) or refused (capability bug). With it, the per-pack image is the pack author's decision, and operators can audit it before installing — the manifest is part of the trust-verified content hash.

## Why this matters to you

If you're shipping a hardened control plane that needs to host untrusted code (agent platforms, CI runners, plugin systems, anything that says "install this"), the temptation is to make the control plane Just A Bit Wider so the code has room to run. Resist that. The dedicated-sidecar pattern is more boring — one more image, one more pull, one more registry entry — but it preserves the property you set out to have: the orchestrator is small and the things you grant code-execution to are explicitly bounded.

The pattern generalizes. Helmdeck has `helmdeck-sidecar-browser` (Chromium), `helmdeck-sidecar-hyperframes` (Node 22 + ffmpeg), and now `helmdeck-sidecar-marketplace` (bash + jq + python + node). Each one was a "the control plane can't do this" decision, and each one ended up being the right call even when it felt like deferred work at the time.

The teach: when the obvious move is to give the orchestrator another capability, draw the option tree first. There's almost always a "delegate to a smaller bounded thing" option, and it's almost always the answer.

## See also

- [ADR 038 — Marketplace pack execution via sidecar](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/038-marketplace-pack-execution-via-sidecar.md)
- [Marketplace catalog reference](/docs/reference/marketplace/catalog)
- [v0.13.0 release announcement](/blog/v0-13-0-marketplace-beta)
- [`helmdeck-marketplace` repo](https://github.com/tosin2013/helmdeck-marketplace)
