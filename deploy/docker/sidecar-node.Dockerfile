# Helmdeck Node.js sidecar — see ADR 001, docs/SIDECAR-LANGUAGES.md.
#
# Extends the base sidecar with a Node.js LTS toolchain so the
# node.run pack can execute snippets, run npm/yarn/pnpm scripts,
# lint with eslint, format with prettier, and install dependencies.
#
# Tag: ghcr.io/tosin2013/helmdeck-sidecar-node:<release>
#
# Built by `make sidecar-node-build`. Operators who need a different
# Node version or a specific package manager pinned should fork this
# Dockerfile per docs/SIDECAR-EXTENDING.md.

ARG BASE_IMAGE=ghcr.io/tosin2013/helmdeck-sidecar:latest
FROM ${BASE_IMAGE}

# Pinned tool versions (ADR 037 #213). Every package manager and npm
# tool installed globally in this Dockerfile has its own ARG so
# Dependabot can target the pin and so the build fails loud if the
# upstream rename-squats or yanks. Do NOT replace any of these with
# `@latest` / `@stable` / `^x` / `~x`.
ARG PNPM_VERSION=11.1.2
ARG YARN_VERSION=4.14.1
ARG TYPESCRIPT_VERSION=6.0.3
ARG TS_NODE_VERSION=10.9.2
ARG ESLINT_VERSION=10.4.0
ARG PRETTIER_VERSION=3.8.3
ARG VITEST_VERSION=4.1.6

USER root

# Node 20 LTS via NodeSource. Debian's packaged node is several
# majors behind; NodeSource is the standard upstream for current
# LTS lines.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg \
 && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/*

# pnpm + yarn alongside the bundled npm so package.json scripts work
# regardless of which package manager the cloned repo uses. corepack
# ships with node and is the official path for managing pnpm/yarn
# without polluting the global npm namespace. Pin yarn to its 4.x
# Berry stream — Yarn 1.x (legacy) is npm-distributed and not what
# corepack prepares.
RUN corepack enable \
 && corepack prepare "pnpm@${PNPM_VERSION}" --activate \
 && corepack prepare "yarn@${YARN_VERSION}" --activate

# Common dev tools installed globally so the LLM doesn't have to
# install them per session.
RUN npm install -g --no-fund --no-audit \
      "typescript@${TYPESCRIPT_VERSION}" \
      "ts-node@${TS_NODE_VERSION}" \
      "eslint@${ESLINT_VERSION}" \
      "prettier@${PRETTIER_VERSION}" \
      "vitest@${VITEST_VERSION}"

# CLI-surface sentinels (ADR 037 #214). The node-sidecar tools are
# invoked by users via the node.run pack, not by helmdeck Go pack
# argv, so the sentinel just verifies each pinned tool resolves and
# reports its version. A bad install or a yanked upstream fails the
# image build, not the first node.run call.
RUN pnpm --version \
 && yarn --version \
 && tsc --version \
 && ts-node --version \
 && eslint --version \
 && prettier --version \
 && vitest --version

USER helmdeck
WORKDIR /home/helmdeck

# Inherits ENTRYPOINT, CHROMIUM_PORT, HELMDECK_MODE, EXPOSE from the
# base image.
