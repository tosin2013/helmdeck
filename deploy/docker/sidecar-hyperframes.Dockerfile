# Helmdeck HyperFrames sidecar — see ADR 001, docs/SIDECAR-LANGUAGES.md.
#
# Extends the base sidecar with FFmpeg and the @hyperframes/cli toolchain
# so the `hyperframes.render` pack (#200) can turn an HTML/CSS/JS
# composition into a deterministic MP4 video via Chromium BeginFrame.
#
# Tag: ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:<release>
#
# Base inheritance:
#   - Chromium + xvfb (from helmdeck-sidecar layer 3a)
#   - Node 20 LTS + npm + @playwright/mcp + @marp-team/marp-cli
#     (from helmdeck-sidecar layer 4b)
# So the only extra weight here is ffmpeg + @hyperframes/cli.
#
# Built by `make sidecar-hyperframes-build`. CI publishes via
# .github/workflows/sidecar-hyperframes.yml on tag pushes.

ARG BASE_IMAGE=ghcr.io/tosin2013/helmdeck-sidecar:latest
FROM ${BASE_IMAGE}

USER root

# HyperFrames pinned. Bump deliberately — the producer pipeline's env-var
# contract has changed between minor releases historically.
ARG HYPERFRAMES_VERSION=1.4.0

# Layer 1: FFmpeg + libavcodec for the encode stage of the producer
# pipeline. The base sidecar already has ffmpeg installed for
# slides.narrate, but pin libx264 + libfdk-aac explicitly to fail
# loud rather than relying on transitive availability.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ffmpeg \
      libavcodec-extra \
      libx264-164 \
 && rm -rf /var/lib/apt/lists/*

# Layer 2: @hyperframes/cli installed globally via npm. npm resolves
# the platform-native puppeteer binary automatically (no per-arch
# tarball footgun like the marp-cli amd64-only release that bit
# v0.12.x — see #205 for the fix that retired that pattern).
#
# PUPPETEER_SKIP_DOWNLOAD=1 because the base sidecar already has
# Chromium at /usr/bin/chromium — hyperframes-cli reads
# PUPPETEER_EXECUTABLE_PATH at runtime.
RUN PUPPETEER_SKIP_DOWNLOAD=1 \
    npm install -g --no-fund --no-audit \
      @hyperframes/cli@${HYPERFRAMES_VERSION} \
 && npm cache clean --force

# Producer pipeline env contract (#200).
#
# - PRODUCER_DISABLE_GPU=true — Chromium headless in containers has no
#   GPU; disabling it avoids a 30-second startup probe + falls back to
#   software rasterization cleanly.
# - PRODUCER_FORCE_SCREENSHOT=true — required on Linux Chromium for
#   alpha-channel preservation. Mac defaults differ; CI must verify.
# - PRODUCER_PUPPETEER_LAUNCH_TIMEOUT_MS=120000 — large compositions
#   trip the default 30s launch timeout on cold containers; 120s leaves
#   headroom without making real failures slow to surface.
# - PRODUCER_STREAMING_ENCODE_MAX_DURATION_SECONDS=3600 — internal
#   pipeline cap; the pack enforces its own 12-minute / 512 MiB cap at
#   the Go layer before this matters, but the env var prevents
#   accidentally-streaming pipelines from running forever.
# - PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium — point hyperframes-cli
#   at the base image's Chromium binary.
ENV PRODUCER_DISABLE_GPU=true \
    PRODUCER_FORCE_SCREENSHOT=true \
    PRODUCER_PUPPETEER_LAUNCH_TIMEOUT_MS=120000 \
    PRODUCER_STREAMING_ENCODE_MAX_DURATION_SECONDS=3600 \
    PUPPETEER_EXECUTABLE_PATH=/usr/bin/chromium

# Sanity check: hyperframes-cli resolves and reports a version. Fails
# the image build (not container startup) if the install didn't land.
RUN hyperframes --version

USER helmdeck
WORKDIR /home/helmdeck

# Inherits ENTRYPOINT, CHROMIUM_PORT, HELMDECK_MODE, EXPOSE from the
# base image.
