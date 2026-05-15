# Helmdeck marketplace sidecar — see ADR 038, ADR 034, docs/SIDECAR-LANGUAGES.md.
#
# Runtime image for community marketplace command-handler packs.
# Bundles bash + jq + curl + python3 + node + the common Unix utility
# set so the typical "wrap a SaaS API and translate JSON" pack runs
# without operator-side toolchain installs.
#
# Why a dedicated image rather than baking these into the control
# plane: the control plane stays on distroless-static (per ADR 002).
# Marketplace packs run inside this sidecar via ec.Exec, reusing
# the same session/audit/egress infrastructure as built-in sidecared
# packs (python.run, slides.narrate, hyperframes.render).
#
# Tag: ghcr.io/tosin2013/helmdeck-sidecar-marketplace:<release>
#
# Pack manifests can declare a per-pack sidecar override
# (handler.sidecar.image) when they need a heavier toolchain
# (image processing, video, ML inference). Default for everything
# else is this image.
#
# Built by `make sidecar-marketplace-build`. CI publishes via
# .github/workflows/sidecar-marketplace.yml on tag pushes.

ARG BASE_IMAGE=ghcr.io/tosin2013/helmdeck-sidecar:latest
FROM ${BASE_IMAGE}

USER root

# Layer 1 — language toolchains. The base sidecar already ships
# Node 20 LTS + Chromium + marp-cli + xvfb. We add Python 3 here
# because community packs in Python are a major target audience.
# python3-pip lets packs install their own deps at handler-spawn
# time IF they declare them — but the default flow does not run
# `pip install`; packs that need additional deps should declare
# their own sidecar via handler.sidecar.image.
RUN apt-get update && apt-get install -y --no-install-recommends \
      python3 \
      python3-pip \
      python3-venv \
      bash \
      jq \
      curl \
      coreutils \
      sed \
      gawk \
      grep \
      ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Layer 2 — sanity checks that the toolchain is reachable. Per
# ADR 037 §"CLI-surface sentinel": grep for the flags helmdeck
# command-handler packs use by name. A future apt repo flip that
# strips `--version` from any of these tools fails this image
# build, not the first pack invocation in prod.
RUN bash --version >/dev/null \
 && jq --version >/dev/null \
 && curl --version >/dev/null \
 && python3 --version >/dev/null \
 && node --version >/dev/null \
 && sha256sum --version >/dev/null

USER helmdeck
WORKDIR /home/helmdeck

# Inherits ENTRYPOINT, CHROMIUM_PORT, HELMDECK_MODE, EXPOSE from
# the base image.
