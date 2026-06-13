---
description: "How to configure semantic recall in OpenClaw's `memory_search` tool. Verify the default Ollama embeddings sidecar, override it, or fall back to BM25 keyword search."
---

# How to configure OpenClaw memory (semantic recall)

OpenClaw's `memory_search` tool ranks results semantically when an embedding provider is configured, and falls back to keyword/BM25 search (FTS) when one isn't. Helmdeck ships a default embedding sidecar so semantic recall works out of the box; this page covers how to verify it, how to override it, and how to opt out.

## Default: local Ollama (zero config)

When you run `scripts/install.sh` without `--no-embeddings`, the install layers `deploy/compose/compose.embeddings.yml`, which brings up two services:

- `helmdeck-embeddings` — `ollama/ollama:latest`, exposing an OpenAI-compatible `/v1/embeddings` endpoint at `http://helmdeck-embeddings:11434/v1` on the `baas-net` bridge.
- `helmdeck-embeddings-init` — one-shot container that pulls `nomic-embed-text` on first start (~270 MB) into a named volume so re-creates skip the download.

OpenClaw resolves the sidecar by service-name DNS. The first time it runs `memory_search`, it auto-discovers the `openai-compatible` embedding provider config (see "Verify" below) and hits the local Ollama for embeddings.

**Resource footprint.** ~600 MB RAM idle, ~270 MB disk for the model, CPU-only. If your host runs tight on memory, the `--no-embeddings` opt-out below is the right call.

## Verify it's working

From the host:

```bash
# 1. Sidecar healthy?
docker ps --format '{{.Names}}\t{{.Status}}' | grep helmdeck-embeddings

# 2. Model present?
docker exec helmdeck-embeddings ollama list

# 3. Embeddings endpoint reachable from OpenClaw?
docker exec openclaw-openclaw-gateway-1 \
  sh -c 'curl -s http://helmdeck-embeddings:11434/v1/models | head -c 200'
```

From inside OpenClaw, ask the agent a semantic-only query against memory — something the FTS keyword index would miss but a semantic embedder would catch:

> "what was that thing we discussed about deploying"

With embeddings on, you should get hits on deploy/Konflux/CI conversations even when "deploying" isn't a literal keyword in the indexed chunks. Without embeddings, the same query returns no hits (FTS can't bridge "deploying" → "deployment" without an explicit synonym).

## Opt out: use OpenAI cloud or a remote Ollama

Run `scripts/install.sh --no-embeddings` (or omit `-f compose.embeddings.yml` from your compose command). Then configure OpenClaw's embedding provider yourself:

```bash
# Inside the openclaw-gateway container, register an openai-compatible
# embeddings provider against your preferred endpoint.
docker exec -it openclaw-openclaw-gateway-1 openclaw agents add main
# Follow the prompts; set the embeddings baseUrl to e.g.
#   https://api.openai.com/v1
# and paste the OpenAI API key when asked.
```

OpenClaw's `openai-compatible` adapter (`src/plugins/openai-compatible-embedding-provider.ts`) accepts any endpoint that implements the OpenAI `POST /v1/embeddings` shape — OpenAI cloud, Azure OpenAI, a remote Ollama, an LM Studio server, etc. The model name comes from whatever your endpoint serves (`text-embedding-3-small` for OpenAI cloud, `nomic-embed-text` for Ollama).

## Override the local model

By default the sidecar pulls `nomic-embed-text`. To use a different Ollama-compatible embedding model — e.g. `mxbai-embed-large` for higher-dimension vectors, or a domain-tuned model — set:

```bash
# in deploy/compose/.env.local
EMBEDDING_MODEL=mxbai-embed-large
```

Then re-run `docker compose ... up -d helmdeck-embeddings-init`. The init container is idempotent and the pull is a no-op if the model is already cached.

## When semantic recall isn't worth it

Skip the sidecar (`--no-embeddings`, no OpenAI key) when:

- You're running helmdeck in a memory-constrained environment (<2 GB RAM total).
- All your memory queries are exact-match keyword lookups (file names, error codes, model IDs).
- You're doing short-session chat where cross-session memory rarely fires.

OpenClaw's FTS fallback (`packages/memory-host-sdk/src/host/query-expansion.ts`) still gives you keyword/BM25 search across the same memory index — you just lose the "that thing about X" semantic-fuzzy recall.

## See also

- [ADR 048 — Memory write surface + OpenClaw memory-corpus bridge](../adrs/048-memory-write-surface-openclaw-bridge.md) — why this sidecar exists, what comes next.
- `deploy/compose/compose.embeddings.yml` — the overlay this page describes.
