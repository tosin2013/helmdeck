---
slug: helmdeck-image-mode-install
title: "Image-mode install: helmdeck without a Go toolchain"
authors: [tosin]
tags: [field-report, deployment, kubernetes]
description: v0.12.0 splits the Compose stack into image-mode (pull versioned ghcr.io tags) vs source-build (overlay adds build:). A clean Linux VM with Docker + curl + openssl now installs helmdeck in under 90 seconds — and the same tag convention unblocks the v1.0 Helm chart.
date: 2026-05-12
draft: false
---

## The friction

Through v0.11.0, installing helmdeck required:

- Docker Engine + Compose v2
- **`go ≥ 1.26`** (the control plane's Go binary)
- **`node ≥ 20`** (the Management UI Vite bundle)
- **`make`** (build orchestration)
- `openssl`, `curl`, ~6 GB disk

The `go ≥ 1.26` requirement is the killer. Distro packages lag (Debian ships 1.22; even Trixie is still on 1.23). Operators evaluating helmdeck on a fresh VM had to install Go from upstream before they could try anything — and many didn't.

The fix isn't subtle: ship pre-built images and let operators pull them.

<!-- truncate -->

## The fix

v0.12.0 closes #134 step 1 by splitting `deploy/compose/compose.yaml` into two layers:

**Base (`compose.yaml`) — image-mode:**

```yaml
services:
  control-plane:
    image: ghcr.io/tosin2013/helmdeck:${HELMDECK_VERSION:-latest}
  sidecar-warm:
    command: ["docker pull ghcr.io/tosin2013/helmdeck-sidecar:${HELMDECK_VERSION:-latest}"]
```

**Overlay (`compose.build.yaml`) — source-build:**

```yaml
services:
  control-plane:
    build:
      context: ../..
      dockerfile: deploy/docker/control-plane.Dockerfile
```

Operators choose at `compose up` time:

```bash
# Image-mode (no Go toolchain needed):
docker compose -f deploy/compose/compose.yaml up -d

# Source-build (contributors):
docker compose -f deploy/compose/compose.yaml \
               -f deploy/compose/compose.build.yaml up -d
```

`scripts/install.sh` exposes both as a single flag:

```bash
./scripts/install.sh --image-mode   # pull pre-built images
./scripts/install.sh                # default = source build
```

`--image-mode` implies `--no-build` AND skips the host Go / Node / `make` preflight checks. The image-mode install only needs Docker + `openssl` + `curl`.

## How it composes

Compose's deep-merge has a non-obvious behaviour that makes this work: when a service has BOTH `build:` and `image:` set, Compose picks `build:` (and tags the resulting image with the `image:` reference). So the overlay doesn't have to repeat the image tag — it inherits from the base.

The same `${HELMDECK_VERSION:-latest}` substitution applies in both modes:

- **Image-mode + `HELMDECK_VERSION=0.12.0`** → pulls `ghcr.io/tosin2013/helmdeck:0.12.0` from GHCR
- **Image-mode (no var)** → pulls `:latest` (handy for evaluation; pin in production)
- **Source-build + `HELMDECK_VERSION=dev`** → builds locally, tags as `ghcr.io/tosin2013/helmdeck:dev`

Operators pin a specific release in `.env.local` for reproducible deploys:

```bash
echo "HELMDECK_VERSION=0.12.0" >> deploy/compose/.env.local
```

## The v1.0 implication

This isn't just a Compose-quality-of-life win. The v1.0-rc1 Helm chart will reuse the same versioned-tag convention:

```yaml
# Helm values.yaml — coming with v1.0-rc1
image:
  controlPlane: ghcr.io/tosin2013/helmdeck
  tag: "0.12.0"  # or 1.0.0, 1.0.1, ...
```

So the Compose-to-Helm mental model becomes "swap the orchestrator, keep the tag pin." Operators upgrading a Compose install to a Helm install in v1.0 don't have to relearn the deployment story; they're already pinned to a versioned tag.

#134 step 1 is a hard prerequisite for v1.0.0-rc1 specifically because the Helm chart can't reuse `ghcr.io/tosin2013/helmdeck:dev` (Compose's prior default tag) — the chart needs versioned tags that match what's published at every release.

## Upgrade path

The upgrade howto (`docs/howto/upgrade-helmdeck.md`) now has two paths for §2 "In-place Compose-stack upgrade":

- **Path A (image-mode):** `git checkout v0.12.0`, bump `HELMDECK_VERSION` in `.env.local`, run `./scripts/install.sh --image-mode`. ~1 minute on warm Docker cache.
- **Path B (source-build):** `git checkout v0.12.0`, `make sidecars`, `make install`. ~3 minutes on warm cache.

Operators on production should already be on path A (or about to be after v0.12.0 — the path A upgrade is one-shot from any prior tag).

## What's NOT in this PR

A few things the plan deliberately left for a future ship:

- **The Helm chart itself.** That's #134 step 2 and ships with v1.0-rc1 ([planning](https://github.com/tosin2013/helmdeck/blob/main/docs/RELEASES.md#v100rc1)).
- **arm64 sidecar image.** Still blocked on Marp's amd64-only upstream tarball.
- **Production hardening** — NetworkPolicies, RBAC, KEDA scaling, gVisor toggles all show up in v1.0-rc1 or v1.0 GA.
- **A separate `helmdeck-garage-init` image.** The garage-init helper still builds locally on every install (~5 seconds, no Go toolchain dependency). Lazy on purpose: pulling it back into image-mode would mean publishing yet another image at every release.

## Numbers

A clean Debian 12 VM with stock packages:

```text
$ git clone https://github.com/tosin2013/helmdeck
$ cd helmdeck
$ ./scripts/install.sh --image-mode
... (90 seconds: pulls control-plane, sidecar, garage, garage-init helper builds)
✓ helmdeck is up
  URL:       http://localhost:3000
  Username:  admin
  Password:  s3cr3t-passw0rd-not-actually-this
```

No `apt install golang-go`. No `nodesource.com` curl-bash. No `make` build cache. The fastest path from "clean VM" to "logged-in admin UI" yet.

Source build is still the right path for contributors — local changes need to compile from the working copy, and you want the layer cache. Image-mode is the right path for evaluation, demos, and any production install that wants reproducibility over freshness.
