---
title: Install helmdeck (CLI walkthrough)
description: A 10-minute walkthrough from `git clone` to a running stack with the admin password printed in your terminal. Covers prereqs, what each step does, smoke testing, and the .env.local secrets layout.
keywords: [helmdeck, install, getting started, docker compose, make install, self-hosted, AI agent, setup]
sidebar_position: 2
priority: 0.8
changefreq: monthly
---

# Install helmdeck (CLI walkthrough)

This tutorial takes you from a clean machine to a running helmdeck stack in about 10 minutes. You'll log in to the Management UI, see the catalog of 36 capability packs, and run a smoke test that creates a session and takes a screenshot end-to-end.

If you'd rather skim the README and figure it out yourself, [scripts/install.sh](https://github.com/tosin2013/helmdeck/blob/main/scripts/install.sh) is the canonical bootstrap. This page walks through what it does and why.

## Prerequisites

| What | Version | Why |
|---|---|---|
| Docker Engine + Compose v2 | 24.x or newer | Runs the control plane, the Garage object store, and per-session browser containers. |
| `make` | any | Drives the build pipeline; aliases the install script. |
| `node` | 20+ | Builds the embedded React Management UI. |
| `go` | 1.26 | Builds the Go control plane and the `helmdeck-mcp` bridge. |
| `openssl`, `curl` | any | Generate secrets, poll healthchecks. |
| Disk | ~6 GB | Sidecar image (~2 GB), Garage data, control-plane image. |

The install script's preflight stage checks all of these and prints platform-aware install hints if any are missing.

## Step 1 — Clone and run install

```bash
git clone https://github.com/tosin2013/helmdeck
cd helmdeck
make install
```

`make install` invokes `scripts/install.sh`, which is **idempotent** — re-running it won't regenerate secrets, won't double-build images, and won't bring up the stack twice. Use `scripts/install.sh --reset` if you need to start over from scratch (rotates the admin password, clears Garage data, brings the stack down first).

## Step 2 — What the script does

The script runs in clear stages:

1. **Preflight** — verifies Docker, make, node, go, openssl, curl. Aborts with platform-specific install hints if anything is missing.
2. **Secret generation** — writes `deploy/compose/.env.local` (chmod 600) with freshly minted `HELMDECK_JWT_SECRET`, `HELMDECK_VAULT_KEY`, `HELMDECK_KEYSTORE_KEY`, `HELMDECK_ADMIN_PASSWORD`. The file is in your repo's `.gitignore` already; never commit it.
3. **`make web-deps && make web-build`** — installs npm deps and produces the Management UI's Vite bundle into `web/dist/`.
4. **`make build`** — compiles the two Go binaries: `bin/control-plane` and `bin/helmdeck-mcp`.
5. **`make sidecar-build`** — builds the browser sidecar image as `helmdeck-sidecar:dev`. **This is the slow step (~3–5 minutes the first time)** because the image bundles Chromium, Marp, Tesseract, ffmpeg, xdotool, Xvfb, XFCE4, and noVNC. Subsequent rebuilds are fast thanks to Docker's layer cache.
6. **`docker compose pull --ignore-buildable`** — pre-pulls the published images (Garage, the GHCR-published sidecar tag) so `compose up` doesn't block on a slow background pull. If your network can't reach GHCR or Docker Hub, this step fails fast with a clear error rather than letting the first session hang on a 30-second timeout.
7. **`docker compose up -d --build`** — starts the control plane, Garage, the garage-init bootstrap, and the optional `sidecar-warm` belt-and-suspenders pull container.
8. **Health check** — polls `http://localhost:3000/healthz` for up to 30 seconds.
9. **Optional GitHub PAT prompt** — interactive only. If you paste a PAT, the script mints a JWT, calls the vault REST API, and stores it as `github-token` so `repo.fetch`/`repo.push` against private repos works without SSH keys.

## Step 3 — The post-install summary

When the script finishes, you'll see something like:

```
✓ helmdeck is up

  URL:       http://localhost:3000
  Username:  admin
  Password:  <freshly-generated-password>

  (save the password now — it's only printed here once.
   the same value lives in deploy/compose/.env.local, mode 0600.)

  Next steps in the UI:
    Add an AI provider key:  http://localhost:3000/admin/ai-providers
    Connect an MCP client:   http://localhost:3000/admin/connect
    Add a vault credential:  http://localhost:3000/admin/vault

  First session note:
    The browser sidecar image was just built. Your first session create call
    will be quick. If you ever see a 502 on first session, the sidecar image
    is still warming — wait ~30s and retry. See:
      docs/howto/troubleshoot-install.md
```

**Save the password.** It's printed once. The canonical copy is in `deploy/compose/.env.local` (mode 0600); if you lose the terminal output, `grep ADMIN .env.local` works.

## Step 4 — Smoke test

Verify everything end-to-end:

```bash
make smoke
```

This brings the stack up (idempotent if already running), creates a session, navigates to a known URL, takes a screenshot, downloads the artifact, and tears the session down. If `make smoke` is green, your install is healthy.

## Step 5 — Log into the Management UI

Open `http://localhost:3000` and log in with `admin` + the password from the post-install summary.

You'll land on the Dashboard. From here, the [UI walkthrough tutorial](./install-ui-walkthrough.md) covers what to do next: add an AI provider key, mint a JWT for your MCP client, add vault credentials, and create your first session.

## Anatomy of `.env.local`

The secrets file at `deploy/compose/.env.local` carries everything the control plane needs at startup:

| Variable | Purpose | Rotate? |
|---|---|---|
| `HELMDECK_JWT_SECRET` | Signs every JWT issued by the control plane. | Restart required after rotation. |
| `HELMDECK_VAULT_KEY` | AES-256-GCM key for the credential vault. **Lose this and every stored credential becomes unrecoverable.** | Re-encrypt the vault before rotating. |
| `HELMDECK_KEYSTORE_KEY` | AES-256-GCM key for the AI provider key store. | Same caution as vault key. |
| `HELMDECK_ADMIN_PASSWORD` | Initial admin password. The control plane bcrypt-hashes this on first boot; subsequent restarts ignore it unless `--reset`. | Use the UI password change flow once available; until then, `--reset` is the path. |

Never commit this file. The repo `.gitignore` already excludes `deploy/compose/.env.local`, but always double-check `git status` before committing if you've been editing in that directory.

## Troubleshooting

If anything went wrong above, head to **[How-to → Troubleshoot the install](../howto/troubleshoot-install.md)** for the specific signature → fix table.
