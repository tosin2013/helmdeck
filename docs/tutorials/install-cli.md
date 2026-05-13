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

## Pick your install mode

Helmdeck supports two install paths:

| Mode | Prerequisites | When to use |
|---|---|---|
| **Image-mode** (`--image-mode`) | Docker + `openssl` + `curl` only | Production, evaluation, anyone without a Go/Node toolchain. Pulls pre-built `ghcr.io/tosin2013/helmdeck:<version>` images. |
| **Source-build** (default) | Docker + `make` + `node 20+` + `go 1.26` + `openssl` + `curl` | Contributors, dirty trees, debugging local changes. Builds the control-plane and web bundle from your working copy. |

Image-mode is the path the v1.0 Kubernetes Helm chart will use; the Compose stack runs on the same versioned tag convention so operators can switch from Compose to Helm without re-learning the deploy mental model.

## Prerequisites — image-mode

| What | Version | Why |
|---|---|---|
| Docker Engine + Compose v2 | 24.x or newer | Runs the control plane, the Garage object store, and per-session browser containers. |
| `openssl`, `curl` | any | Generate secrets, poll healthchecks. |
| Disk | ~4 GB | Sidecar image (~2 GB), Garage data, control-plane image. No build cache. |

## Prerequisites — source-build (default)

| What | Version | Why |
|---|---|---|
| Docker Engine + Compose v2 | 24.x or newer | Runs the control plane, the Garage object store, and per-session browser containers. |
| `make` | any | Drives the build pipeline; aliases the install script. |
| `node` | 20+ | Builds the embedded React Management UI. |
| `go` | 1.26 | Builds the Go control plane and the `helmdeck-mcp` bridge. |
| `openssl`, `curl` | any | Generate secrets, poll healthchecks. |
| Disk | ~6 GB | Sidecar image (~2 GB), Garage data, control-plane image, build cache. |

The install script's preflight stage checks all of these and prints platform-aware install hints if any are missing.

## Step 1 — Clone and run install

**Image-mode (production / no-toolchain):**

```bash
git clone https://github.com/tosin2013/helmdeck
cd helmdeck
# Optional: pin the version (omit to track latest)
echo "HELMDECK_VERSION=0.12.0" >> deploy/compose/.env.local 2>/dev/null || true
./scripts/install.sh --image-mode
```

**Source-build (contributors):**

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

The secrets file at `deploy/compose/.env.local` carries everything the control plane reads from environment variables at startup. There are three layers — what `install.sh` writes for you, what you may want to add for optional integrations, and what you should **not** put here (it goes through the UI instead).

### Quick API-key reference (where each key lives)

A frequent first-install question: *which third-party API keys do I actually need, and where do they go?* Single-glance answer:

| Service | API key required? | Where it goes | Required by |
|---|---|---|---|
| **helmdeck control plane** | the four secrets from `install.sh` (auto-generated) | `.env.local` | the platform itself |
| **Firecrawl** (self-hosted overlay) | **No** — runs in `USE_DB_AUTHENTICATION=false` on the private `baas-net` bridge | nowhere | `web.scrape`, `research.deep`, `content.ground` |
| **Docling** (self-hosted overlay) | **No** — same private-bridge story | nowhere | `doc.parse` |
| **Anthropic / OpenAI / Gemini / Ollama / Deepseek** (AI gateway providers) | yes | UI → *AI Providers* panel (`/admin/ai-providers`) | vision packs, `web.test`, `research.deep`, `content.ground` |
| **OpenRouter** (OpenAI-compatible aggregator, env-var fast path) | yes | `.env.local` as `HELMDECK_OPENROUTER_API_KEY` | gateway dispatcher when used through OpenRouter |
| **GitHub PAT** (`github-token`) | yes | UI → *Vault* panel (`/admin/vault`), or install.sh interactive prompt | private-repo `repo.fetch`/`repo.push`, `github.*` family |
| **ElevenLabs** (`elevenlabs-key`) | yes | UI → *Vault* panel | `slides.narrate` full TTS (without it, ships silent video) |
| **OpenClaw model auth** (the LLM driving the agent) | yes | OpenClaw's own auth flow → `~/.openclaw/` | OpenClaw chat-UI itself, NOT a helmdeck concern |

**Bottom line for a fresh install**: the only API keys you *must* obtain to use overlay packs are an AI provider key (one of Anthropic/OpenAI/Gemini/OpenRouter — for the gateway). Everything else (Firecrawl, Docling) ships fully self-contained.

### What `install.sh` writes for you (always present)

| Variable | Purpose | Rotate? |
|---|---|---|
| `HELMDECK_JWT_SECRET` | Signs every JWT issued by the control plane. | Restart required after rotation. |
| `HELMDECK_VAULT_KEY` | AES-256-GCM key for the credential vault. **Lose this and every stored credential becomes unrecoverable.** | Re-encrypt the vault before rotating. |
| `HELMDECK_KEYSTORE_KEY` | AES-256-GCM key for the AI provider key store. | Same caution as vault key. |
| `HELMDECK_ADMIN_PASSWORD` | Initial admin password. The control plane bcrypt-hashes this on first boot; subsequent restarts ignore it unless `--reset`. | Use the UI password change flow once available; until then, `--reset` is the path. |
| `HELMDECK_ADMIN_USERNAME` | Defaults to `admin`. | Edit and restart control plane. |
| `HELMDECK_DOCKER_GID` | Host docker group GID — auto-detected so the non-root control-plane container can read `/var/run/docker.sock`. | Override only if your host remaps the docker group. |
| `HELMDECK_SIDECAR_IMAGE` | Pinned to `helmdeck-sidecar:dev` (the local `make sidecar-build` output). Falls back to the published `ghcr.io` tag if unset. | Edit only if you publish your own sidecar fork. |

### What you may want to add (optional, depends on which packs you'll use)

These are commented-out in `deploy/compose/.env.example`. Uncomment + edit `.env.local` when you want the corresponding pack to work. After any change here, **recreate the control-plane container** (`docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env.local up -d --force-recreate control-plane`) — `restart` does not re-read env files.

| Variable | Set to | Required by | Notes |
|---|---|---|---|
| `HELMDECK_FIRECRAWL_ENABLED` | `true` | `web.scrape`, `research.deep`, `content.ground` | Also bring up the Firecrawl overlay: `docker compose -f deploy/compose/compose.yaml -f deploy/compose/compose.firecrawl.yml --env-file deploy/compose/.env.local up -d`. **No API key needed**: the overlay runs in `USE_DB_AUTHENTICATION=false` mode and only accepts callers on the private `baas-net` bridge, so the control plane reaches it without auth. The hosted Firecrawl SaaS (`api.firecrawl.dev`) is **not** what helmdeck calls — only the self-hosted overlay. First boot of the overlay takes ~60-90s — RabbitMQ's Erlang VM needs to initialize before `firecrawl` reports healthy and starts; subsequent restarts are faster. |
| `HELMDECK_DOCLING_ENABLED` | `true` | `doc.parse` | Also bring up the Docling overlay: same pattern with `compose.docling.yml`. **No API key needed** — same private-bridge story as Firecrawl. `doc.ocr` (Tesseract fallback) works without this overlay. |
| `HELMDECK_PLAYWRIGHT_MCP_ENABLED` | `false` (override default `true`) | `web.test` (when disabled, returns clear error) | Default is on. Set `false` only on tiny VMs to skip ~80 MB of Node + Playwright in the sidecar. |
| `HELMDECK_SIDECAR_PYTHON` | `helmdeck-sidecar-python:dev` | `python.run` | Run `make sidecars` first to build the image. Without this, the pack returns `session_unavailable: No such image: ghcr.io/tosin2013/helmdeck-sidecar-python:latest`. |
| `HELMDECK_SIDECAR_NODE` | `helmdeck-sidecar-node:dev` | `node.run` | Same shape as above. |
| `HELMDECK_OPENROUTER_API_KEY` | `sk-or-v1-…` | The OpenRouter env-var fast path on the helmdeck **gateway** (NOT OpenClaw — see below) | Provider keys normally go in the UI's *AI Providers* panel; this is the escape hatch for OpenAI-compatible aggregators not yet modeled in the keystore schema. Same pattern: `HELMDECK_GROQ_API_KEY`, `HELMDECK_MISTRAL_API_KEY`. |
| `HELMDECK_EGRESS_ALLOWLIST` | comma-separated CIDRs | Pack handlers reaching internal hosts | Default blocks RFC 1918, metadata IP, loopback. Add your internal CI server / git server here if a pack needs to reach it. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` + `HELMDECK_OTEL_ENABLED=true` | OTLP receiver URL | Anyone wanting traces | Compatible with Tempo, Jaeger, Honeycomb, Datadog, Langfuse. |

### What does **not** go in `.env.local`

These credentials live in encrypted stores accessed through the Management UI — `.env.local` is the wrong place to put them:

| Credential | Goes where | Path |
|---|---|---|
| Anthropic / OpenAI / Gemini / Ollama / Deepseek / Groq / Mistral provider API keys | **AI Providers** panel | `http://localhost:3000/admin/ai-providers` — encrypted with `HELMDECK_KEYSTORE_KEY` |
| `github-token` (for `repo.fetch`/`repo.push` against private repos and the `github.*` family) | **Vault** panel | `http://localhost:3000/admin/vault` — encrypted with `HELMDECK_VAULT_KEY` |
| `elevenlabs-key` (for `slides.narrate` full TTS) | **Vault** panel | same as above |
| Per-pack `${vault:NAME}` placeholders for `http.fetch` | **Vault** panel | same as above |
| OpenClaw's **own** OpenRouter / OpenAI / Bedrock model auth (the LLM that drives the agent — separate from helmdeck's gateway) | OpenClaw's auth flow | `docker compose -f /root/openclaw/docker-compose.yml run --rm openclaw-gateway node dist/index.js models auth login <provider>` — stored under `~/.openclaw/`. **Not** a helmdeck concern. |

### Why the split matters

The two encryption keys (`HELMDECK_VAULT_KEY`, `HELMDECK_KEYSTORE_KEY`) are intentionally separate per [ADR 007](/adrs/credential-vault-with-placeholder-tokens) so a leak of one domain doesn't compromise the other. The UI flow encrypts at write, and the Vault panel records every read in a usage log for audit. Putting raw API keys in `.env.local` defeats both.

### Three-store cheat-sheet

When a pack page says "needs an API key", check **which store**:

- For **invoking models** → AI Providers UI panel.
- For **the pack to call upstream services** (GitHub, ElevenLabs, third-party REST) → Vault UI panel.
- For **the agent client (e.g. OpenClaw) talking to its model** → that client's auth flow, not helmdeck.

### `.gitignore` reminder

Never commit `deploy/compose/.env.local`. The repo `.gitignore` already excludes it, but always double-check `git status` before committing if you've been editing in that directory.

## Troubleshooting

If anything went wrong above, head to **[How-to → Troubleshoot the install](../howto/troubleshoot-install.md)** for the specific signature → fix table.
