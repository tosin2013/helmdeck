---
title: Troubleshoot the install
description: Symptom-first fixes for the known sharp edges in the helmdeck install path — 502 on first session, GHCR pull failures, lost admin password, sidecar build hangs, blank UI panels, accidental --reset.
keywords: [helmdeck, troubleshoot, install issues, 502, sidecar, GHCR, Docker Hub, admin password]
priority: 0.7
changefreq: monthly
---

# Troubleshoot the install

Symptom-first table. Find your error, follow the fix.

## `502 Bad Gateway` on the first session create

**Symptom**: `make install` finishes green, you call `POST /api/v1/sessions` (or your MCP client invokes `browser.screenshot_url`), and you get a 502 with no useful error.

**Cause**: the browser sidecar image isn't present locally yet. The `compose pull` step in `install.sh` should have caught this, but there are edge cases (the GHCR tag was pushed after install, Docker Hub rate-limited you, etc.).

**Fix**:
```bash
docker pull ghcr.io/tosin2013/helmdeck-sidecar:latest
# wait for it to finish, then retry the session create
```

If this hangs, you have a network reachability problem to GHCR. See [GHCR / Docker Hub unreachable](#ghcr--docker-hub-unreachable) below.

## `compose pull` fails during install

**Symptom**: `install.sh` exits at the "Pre-pulling published images" step with a network error.

**Cause**: your machine can't reach `docker.io` (Garage image) or `ghcr.io` (helmdeck-sidecar). Common reasons:

- Corporate proxy not configured.
- VPN intercepting TLS.
- Docker Hub anonymous rate limit hit (100 pulls / 6 h per IP).
- GHCR rate limit (rare, but possible during launch surges).

**Fix**:

```bash
# If you're behind a proxy, export it before re-running:
export HTTPS_PROXY=http://proxy.company.tld:8080
export NO_PROXY=localhost,127.0.0.1,.svc,.cluster.local

# If Docker Hub rate-limited you, log in:
docker login docker.io

# If GHCR is the problem, log in with a GitHub PAT (read:packages scope is enough):
echo $GITHUB_TOKEN | docker login ghcr.io -u <your-username> --password-stdin

# Then retry:
make install
```

## Admin password lost or never captured

**Symptom**: you closed the terminal before saving the password.

**Fix**: it's still in `.env.local`:

```bash
grep HELMDECK_ADMIN_PASSWORD deploy/compose/.env.local
# format: HELMDECK_ADMIN_PASSWORD=<value>
```

If `.env.local` was deleted (or chmod was changed and the file is unreadable), the only path is `./scripts/install.sh --reset`, which generates a fresh password and prints it again. **Caveat**: `--reset` brings the stack down with `-v`, so any session data, audit log entries, or unsaved artifacts in Garage are discarded.

## Sidecar build is taking forever

**Symptom**: `make sidecar-build` has been running for 10+ minutes.

**Cause**: the sidecar Dockerfile has Chromium + Tesseract + ffmpeg + Xvfb + XFCE4 + noVNC + Marp + a font pack. First-time builds on a cold cache are 3–5 minutes on a typical machine, longer on slow disks or networks.

**Verify it's actually progressing**:
```bash
# In a second terminal:
docker images | grep helmdeck-sidecar
# Should show :dev once the build completes
docker ps -a | grep buildkit
# Should show an active build container
```

If neither shows progress for 5+ minutes, kill the build (`Ctrl-C`) and check disk space (`df -h /var/lib/docker`).

## `make smoke` fails

**Symptom**: `make install` succeeded but `make smoke` fails partway through.

**Diagnostics in order**:

```bash
# Is the control plane responsive?
curl -fsS http://localhost:3000/healthz

# Are all containers up?
docker compose -f deploy/compose/compose.yaml ps

# Did sidecar-warm exit cleanly?
docker logs helmdeck-sidecar-warm
# Exit code 0 = pulled successfully. Exit code 1 = pull failed (see GHCR section above).

# Tail control-plane logs:
docker compose -f deploy/compose/compose.yaml logs -f control-plane
```

The most common smoke failure is the sidecar image absent or broken. The control plane logs will say `failed to start session: image not found` or similar.

## The Management UI shows blank panels

**Symptom**: you can log in, but every panel says "loading..." forever or "no data".

**Cause**: the React UI bundles a Vite build at install time. If you ran `make install` against an older repo and then `git pull`-ed without rebuilding, the UI you're loading is stale.

**Fix**: rebuild the UI bundle:

```bash
make web-build
docker compose -f deploy/compose/compose.yaml restart control-plane
```

(The control plane embeds the UI bundle into its binary, so the restart picks up the new bundle.)

## Trivy noise during `make check`

**Symptom**: running `make check` locally surfaces Trivy findings even on a clean checkout.

**Cause**: Trivy's vulnerability database refreshes daily. New CVEs against pinned dependencies show up regularly even when nothing in the repo changed.

**Fix**: cross-reference against the latest CI run on `main`:

```bash
gh run list --workflow ci.yml --branch main --limit 1
gh run view <run-id> --log-failed
```

If `main`'s CI is green, your local Trivy is hitting findings that haven't yet been fixed in the pinned versions — file an issue with `priority/P1` if any are HIGH or CRITICAL.

## I edited `.env.local` and now things are broken

**Symptom**: changes to `.env.local` aren't being picked up, or the control plane refuses to start.

**Fix**: most env vars are read once at control-plane boot. Restart:

```bash
docker compose -f deploy/compose/compose.yaml restart control-plane
docker compose -f deploy/compose/compose.yaml logs -f control-plane
```

If you changed `HELMDECK_VAULT_KEY` or `HELMDECK_KEYSTORE_KEY`, **stored credentials and provider keys are now undecryptable**. Restore the previous key value and the data will work again. There is no recovery without the original key.

## GHCR / Docker Hub unreachable

**Symptom**: `docker pull` hangs or times out against `ghcr.io` or `docker.io`.

**Diagnostics**:

```bash
# DNS resolution working?
host ghcr.io && host docker.io

# TCP reachability?
nc -zvw3 ghcr.io 443
nc -zvw3 docker.io 443

# Direct HTTPS reachability?
curl -fsSI https://ghcr.io/v2/ | head -3
curl -fsSI https://registry-1.docker.io/v2/ | head -3
```

If any of these fail, you have a network or firewall problem outside helmdeck's control. Speak to whoever runs the network — most often the fix is allowing egress to `ghcr.io:443`, `*.docker.io:443`, and `production.cloudflare.docker.com:443`.

## I ran `--reset` by accident

**Symptom**: you wanted to restart but typed `scripts/install.sh --reset` and now your data is gone.

**Reality**: `--reset` brings the stack down with `-v`, which deletes Compose volumes (Garage data, the SQLite database, the keystore). There is no built-in recovery. The admin password, vault credentials, and audit log are all freshly empty after `--reset`.

**Avoidance**: `docker compose -f deploy/compose/compose.yaml restart control-plane` is the surgical option for "I want the control plane to pick up new env vars". `scripts/install.sh` (no flags) is idempotent and won't reset anything.

## Still stuck

Open an issue at <https://github.com/tosin2013/helmdeck/issues> with:

1. Your OS + Docker version (`docker version`).
2. The output of `docker compose -f deploy/compose/compose.yaml ps`.
3. The last 100 lines of `docker compose -f deploy/compose/compose.yaml logs control-plane`.
4. The exact command that failed and its full output.

Tag the issue `priority/P0` if your install is fully broken with no workaround, otherwise `priority/P1`.

<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "FAQPage",
  "mainEntity": [
    {
      "@type": "Question",
      "name": "Why do I get 502 Bad Gateway on the first session create?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "The browser sidecar image isn't present locally yet. The compose pull step in install.sh should have caught this, but edge cases (a freshly pushed GHCR tag, Docker Hub rate limit, etc.) can let it through. Run: docker pull ghcr.io/tosin2013/helmdeck-sidecar:latest, then retry the session create."
      }
    },
    {
      "@type": "Question",
      "name": "Why does compose pull fail during install?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Your machine can't reach docker.io (Garage image) or ghcr.io (helmdeck-sidecar). Common causes: a corporate proxy that isn't configured, a VPN intercepting TLS, the Docker Hub anonymous pull rate limit (100 pulls / 6 h per IP), or a GHCR rate limit during launch surges. Set HTTPS_PROXY if behind a proxy, run docker login docker.io if rate-limited, then re-run make install."
      }
    },
    {
      "@type": "Question",
      "name": "I lost the admin password — how do I recover it?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "The password is still in deploy/compose/.env.local — grep HELMDECK_ADMIN_PASSWORD deploy/compose/.env.local. If that file was deleted, the only recovery path is scripts/install.sh --reset, which generates a fresh password but discards session data, audit log entries, and unsaved Garage artifacts."
      }
    },
    {
      "@type": "Question",
      "name": "Why is the sidecar build taking 10+ minutes?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "The sidecar Dockerfile bundles Chromium, Tesseract, ffmpeg, Xvfb, XFCE4, noVNC, Marp, and a font pack. Cold-cache first builds are 3–5 minutes on a typical machine, longer on slow disks or networks. If neither docker images nor docker ps shows progress for 5+ minutes, kill the build and check disk space (df -h /var/lib/docker)."
      }
    },
    {
      "@type": "Question",
      "name": "Why does make smoke fail after a successful install?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Most often the sidecar image is absent or broken. Check the control plane logs (docker compose logs control-plane) for an image not found error. Also confirm sidecar-warm exited cleanly (docker logs helmdeck-sidecar-warm). Health-check the API directly: curl -fsS http://localhost:3000/healthz."
      }
    },
    {
      "@type": "Question",
      "name": "Why are Management UI panels blank after install?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "The React UI is a Vite bundle compiled at install time and embedded into the Go binary. If you ran make install against an older repo and then git pulled without rebuilding, the UI is stale. Fix: make web-build && docker compose restart control-plane."
      }
    },
    {
      "@type": "Question",
      "name": "Why is Trivy reporting CVEs that aren't in the main branch CI?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Trivy's vulnerability database refreshes daily, so new CVEs appear against pinned dependencies even when nothing in the repo changed. Cross-reference against the latest CI run on main: gh run list --workflow ci.yml --branch main --limit 1. If main is green, the local findings haven't yet been triaged."
      }
    },
    {
      "@type": "Question",
      "name": "I edited .env.local and changes aren't being picked up.",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Most env vars are read once at control-plane boot. Restart with: docker compose restart control-plane. Caveat: rotating HELMDECK_VAULT_KEY or HELMDECK_KEYSTORE_KEY makes stored credentials and provider keys undecryptable. Restore the previous key value to recover."
      }
    },
    {
      "@type": "Question",
      "name": "GHCR or Docker Hub is unreachable from my host.",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Diagnose with host ghcr.io && host docker.io for DNS, then nc -zvw3 ghcr.io 443 for TCP. If those fail, you have a network or firewall issue outside helmdeck's control. The fix is to allow egress to ghcr.io:443, *.docker.io:443, and production.cloudflare.docker.com:443."
      }
    },
    {
      "@type": "Question",
      "name": "I ran scripts/install.sh --reset by accident — can I recover?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "No automatic recovery. --reset brings the stack down with -v, which deletes Compose volumes (Garage data, SQLite database, keystore). The admin password, vault credentials, and audit log are all freshly empty after --reset. Avoid this by using docker compose restart control-plane for env-var changes; install.sh without flags is idempotent and won't reset anything."
      }
    }
  ]
}
</script>
