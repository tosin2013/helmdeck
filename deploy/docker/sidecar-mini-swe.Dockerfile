# Helmdeck mini-swe-agent sidecar — see ADR 001, epic #233 (swe.solve).
#
# Extends the Python language sidecar with mini-swe-agent so the
# swe.solve pack can run a self-contained SWE agent loop INSIDE the
# session container. mini executes using the sidecar's own bash and
# routes its LLM calls to helmdeck's OpenAI-compatible AI gateway via
# litellm (OPENAI_API_BASE / OPENAI_API_KEY / model passed in the run
# environment by the swe.solve handler).
#
# Tag: ghcr.io/tosin2013/helmdeck-sidecar-mini-swe:<release>
#
# Built by `make sidecar-mini-swe-build`. The image pins mini-swe-agent
# to an EXACT version (ADR 037 — no @latest) and also installs the
# vendored helmdeck-environment adapter so the same image can run mini
# either local-in-session (swe.solve) or as a cmd.run-routing
# environment.

ARG BASE_IMAGE=ghcr.io/tosin2013/helmdeck-sidecar-python:latest
FROM ${BASE_IMAGE}

USER root

# mini-swe-agent (the SWE agent loop) + universal-ctags (so swe.solve's
# repo.map context-seed step works in-image). The agent's LLM transport
# is litellm, which mini-swe-agent pulls in as a dependency.
#
# Version pin: ADR 037 forbids @latest. 1.16.0 is a plausible recent
# release of mini-swe-agent — VERIFY the latest exact version on PyPI
# (https://pypi.org/project/mini-swe-agent/) before tagging a release
# image and bump this pin accordingly.
RUN apt-get update && apt-get install -y --no-install-recommends \
      universal-ctags \
 && rm -rf /var/lib/apt/lists/*

RUN pip3 install --break-system-packages --no-cache-dir \
      mini-swe-agent==1.16.0

# Install the vendored helmdeck-environment adapter (contrib, Phase 1).
# Copied in and installed from source so the image carries the exact
# adapter version that ships in this repo, not a PyPI release.
COPY contrib/helmdeck-environment /opt/helmdeck-environment
RUN pip3 install --break-system-packages --no-cache-dir /opt/helmdeck-environment

# CLI-surface sentinel (ADR 037 #214). swe.solve invokes `mini` by argv
# from the Go handler, so a missing/renamed CLI must fail the BUILD, not
# the first swe.solve call. `mini --help` exercises the entrypoint;
# ctags --version confirms the repo.map seed dependency resolves.
RUN mini --help >/dev/null \
 && ctags --version >/dev/null \
 && python3 -c "import helmdeck_environment"

USER helmdeck
WORKDIR /home/helmdeck

# Inherits ENTRYPOINT, CHROMIUM_PORT, HELMDECK_MODE, EXPOSE from the
# base chain — this image is a strict superset of the python sidecar.
