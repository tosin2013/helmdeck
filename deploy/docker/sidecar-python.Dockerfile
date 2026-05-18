# Helmdeck Python sidecar — see ADR 001, docs/SIDECAR-LANGUAGES.md.
#
# Extends the base sidecar with a CPython 3 toolchain so the
# python.run pack can execute snippets, run pytest, lint with ruff,
# type-check with mypy, and install dependencies via pip.
#
# Tag: ghcr.io/tosin2013/helmdeck-sidecar-python:<release>
#
# Built by `make sidecar-python-build`. Operators who need a different
# Python version or extra packages should fork this Dockerfile per
# docs/SIDECAR-EXTENDING.md and tag their own image — pack handlers
# pick the image via SessionSpec.Image so multiple language sidecars
# coexist in the same helmdeck deployment.

ARG BASE_IMAGE=ghcr.io/tosin2013/helmdeck-sidecar:latest
FROM ${BASE_IMAGE}

USER root

# CPython + pip + venv + the dev quality-of-life kit. Installed via
# apt for stability (system Python is what `python3 -c` resolves to);
# users who need a specific minor version can layer pyenv on top.
RUN apt-get update && apt-get install -y --no-install-recommends \
      python3 \
      python3-pip \
      python3-venv \
      python3-dev \
      build-essential \
 && rm -rf /var/lib/apt/lists/*

# Common dev tools installed system-wide so the LLM doesn't have to
# `pip install` them on every session start. The --break-system-packages
# flag is required on Debian 12+ to install outside a venv; we accept
# that tradeoff because the sidecar is a throwaway container with no
# persistent system Python state to protect.
RUN pip3 install --break-system-packages --no-cache-dir \
      pytest \
      ruff \
      mypy \
      requests \
      httpx \
      pyyaml \
      rich

# CLI-surface sentinels (ADR 037 #214). The python-sidecar tools are
# invoked by users via the python.run pack, not by helmdeck Go pack
# argv, so the sentinel just verifies each pinned tool resolves and
# reports its version. A bad install or a yanked upstream fails the
# image build, not the first python.run call.
RUN python3 --version \
 && pip3 --version \
 && pytest --version \
 && ruff --version \
 && mypy --version

USER helmdeck
WORKDIR /home/helmdeck

# Inherits ENTRYPOINT, CHROMIUM_PORT, HELMDECK_MODE, EXPOSE from the
# base image — the python sidecar is a strict superset of the base
# sidecar, not a replacement.
