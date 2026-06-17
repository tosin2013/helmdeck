# Helmdeck HyperFrames sidecar — see ADR 001, docs/SIDECAR-LANGUAGES.md.
#
# Extends the base sidecar with Node 22 + FFmpeg + the `hyperframes`
# CLI so the `hyperframes.render` pack (#200) can turn an HTML/CSS/JS
# composition into a deterministic MP4 via Chromium BeginFrame.
#
# Tag: ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:<release>
#
# Base inheritance:
#   - Chromium + xvfb (from helmdeck-sidecar layer 3a)
#   - ffmpeg already on the base image (slides.narrate uses it)
# So the only extra weight here is Node 22 (the base ships Node 20, but
# `hyperframes` requires >=22) plus the hyperframes CLI itself, which
# pulls in its own bundled puppeteer + Chrome via the producer pipeline.
#
# Built by `make sidecar-hyperframes-build`. CI publishes via
# .github/workflows/sidecar-hyperframes.yml on tag pushes.

ARG BASE_IMAGE=ghcr.io/tosin2013/helmdeck-sidecar:latest
FROM ${BASE_IMAGE}

USER root

# HyperFrames pinned exactly per ADR 037 #213 (no `^` / `~` / `@latest`).
# Caret-pinning floats patch-within-minor, which is exactly what bit
# us in the upstream-rename incident this ADR was written to prevent.
# Dependabot proposes patch bumps as PRs that exercise the full build
# (the sentinel below is the second line of defense).
ARG HYPERFRAMES_VERSION=0.6.110

# Layer 1 — Node 22.
#
# The base sidecar ships Node 20 LTS for @playwright/mcp and marp-cli;
# hyperframes requires Node >= 22 (it uses worker_threads features that
# graduated to stable in v22). nodesource's setup_22.x replaces the
# existing nodejs install in place — apt-get install -y nodejs picks
# up the new repo's package version.
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get update && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/* \
 && node --version  # sanity check: must report >= v22

# Layer 2 — hyperframes CLI, installed globally.
#
# The CLI bundles its own puppeteer + Chrome via @puppeteer/browsers,
# so we don't try to redirect it at the base image's /usr/bin/chromium.
# Setting PUPPETEER_SKIP_DOWNLOAD here would defeat the bundle and
# leave the CLI unable to launch a browser. The download happens once
# at install time; subsequent renders reuse the cached browser.
RUN npm install -g --no-fund --no-audit "hyperframes@${HYPERFRAMES_VERSION}" \
 && npm cache clean --force

# scripts/hyperframes-init.sh — invoked by hyperframes.compose via
# ec.Exec (issue #503). Wraps `hyperframes init --example=<x>` and
# emits the stitched composition HTML. Lives in /usr/local/bin so the
# pack handler can call it by a stable path without negotiating a
# working directory — same pattern as av-validate.sh.
COPY scripts/hyperframes-init.sh /usr/local/bin/hyperframes-init.sh
RUN chmod +x /usr/local/bin/hyperframes-init.sh

# Install smoke (ADR 037 #214). Cheap `--version` checks that catch
# a yanked release, a typo-squat, or a flat-out missing install at
# `docker build` time. Also verifies the `init` subcommand exists
# (the surface hyperframes-init.sh depends on) and that our script
# is present and executable. The richer flag-by-flag CLI-surface
# assertion — does `hyperframes render --help` document every flag
# hyperframes_render.go passes by name (--resolution, --fps,
# --quality, --output)? — runs in the Go test at
# internal/packs/builtin/cli_surface_invariant_test.go against the
# built image. See the equivalent comment in sidecar.Dockerfile for
# the rationale.
RUN hyperframes --version \
 && hyperframes init --help >/dev/null \
 && test -x /usr/local/bin/hyperframes-init.sh

USER helmdeck
WORKDIR /home/helmdeck

# Inherits ENTRYPOINT, CHROMIUM_PORT, HELMDECK_MODE, EXPOSE from the
# base image.
