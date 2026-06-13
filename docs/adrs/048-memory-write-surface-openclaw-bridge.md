---
description: "ADR-048: Memory Write Surface and OpenClaw Memory-Corpus Bridge — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 48. Memory Write Surface and OpenClaw Memory-Corpus Bridge

**Status**: Accepted (slice 1 shipped: embedding sidecar + install integration; write surface and OpenClaw corpus bridge are PR #2 and #3 of this roadmap)
**Date**: 2026-05-31
**Domain**: memory, agent-integrations, deploy, openclaw

## Context

[ADR 039](039-universal-memory-delivery-layer.md) shipped the per-caller memory layer; [ADR 047](047-pipeline-routing-and-memory.md) turned it into a *queryable* surface — `helmdeck://my-defaults`, the Routing Memory UI, programmatic forget, and the `helmdeck.route` meta-pack that fuses catalog + defaults. But every audit row written today comes from `Engine.Execute` or `Runner.RunSync` — **the surface is read-only from outside the engine**. No MCP client can persist a fact, and the existing four documented integrations (OpenClaw, Claude Code, Gemini CLI, Claude Desktop) each carry their own ad-hoc memory model with no shared place to put "user prefers React" or "deploy via Konflux only."

Three findings from investigating OpenClaw's memory architecture shape the right fix:

1. **OpenClaw's `memory_search` works without embeddings.** Its `query-expansion.ts` has an FTS fallback — keyword/BM25 only, no semantic recall. The "no API key for openai" error operators hit is a *quality* degradation, not a feature break. So the embedding requirement is a polish concern, not a blocker.
2. **OpenClaw exposes a real plugin API for external memory contribution** — `registerMemoryCapability`, `registerMemoryCorpusSupplement`, and an MCPorter QMD bridge in `packages/memory-host-sdk/src/host/`. Helmdeck can register as a corpus supplement so its audit history + agent-written facts surface through OpenClaw's own `memory_search` tool, indexed alongside the user's conversational memory.
3. **The other three clients have no external-memory-write API.** Claude Code, Gemini CLI, and Claude Desktop don't document a plugin model for sidecars to contribute memory entries. Their only practical sharing path is MCP resources/tools that helmdeck already speaks.

The combined gap: helmdeck has the right data model (per-caller, TTL'd, category-tagged) but the wrong contract (engine-write only). Closing that gap turns helmdeck into a **shared writable memory backend** — deep integration for OpenClaw (the only client that supports it), MCP-resource-and-pack for the rest.

## Decision

Three-PR roadmap, each independently shippable, that together makes helmdeck's memory layer a first-class shared backend:

### PR #1 — Embedding sidecar (Ollama + nomic-embed-text) (this PR)

Add an opt-in `compose.embeddings.yml` overlay running `ollama/ollama:latest` as `helmdeck-embeddings`, joined to `baas-net` so OpenClaw resolves it as `http://helmdeck-embeddings:11434/v1`. A one-shot init service runs `ollama pull nomic-embed-text` on first start; the model cache lives in a named volume so re-creates don't re-download.

`scripts/install.sh` gains a `--no-embeddings` opt-out (default is ON — overlay layered automatically). Operators who prefer OpenAI cloud opt out and configure OpenClaw against their own endpoint via `openclaw agents add main`. `docs/howto/openclaw-memory.md` documents both paths.

OpenClaw still requires one manual `openclaw agents add main` to register the `openai-compatible` provider pointing at `http://helmdeck-embeddings:11434/v1` — there's no env-var auto-discovery in OpenClaw today, and helmdeck shouldn't reach into OpenClaw's workspace volume from outside. A future upstream change to OpenClaw (or a dedicated init-time pairing script) can collapse that to zero-config; not blocking for PR #1.

Resource cost: ~1 GB image, ~600 MB RAM. Acceptable for the default helmdeck deployment shape; opt-out covers light-touch installs.

### PR #2 — Helmdeck memory write surface

Adds `POST /api/v1/memory/store` + `helmdeck.memory_store` pack so any MCP client (and the chat agent) can persist user-supplied facts under the caller's namespace. Free-form key/value with required category tagging. Mirrors the existing `helmdeck.memory_forget` shape so the lifecycle stays symmetric — write surface + forget surface ship from the same contract.

Reserved categories: `pack_history` and `pipeline_history` are **rejected with 400** to prevent agent-pollution of the audit log. Agents store under `user_facts` (default), `project_conventions`, `preferences`, or any caller-supplied non-reserved category. TTL is mandatory — every write expires (default 90 days, minimum 1 hour, maximum 365 days).

Adds a new always-listed MCP resource `helmdeck://my-memory` that returns the caller's user-written categories projection (`{categories: [{name, count, recent_keys[]}], note}`) so the agent can discover what facts already exist before re-storing.

### PR #3 — OpenClaw memory-corpus bridge

A new `helmdeck.memory_corpus` MCP tool that returns the caller's memory as QMD-shaped chunks (path + content + metadata). OpenClaw's MCPorter daemon consumes it via `registerMemoryCorpusSupplement`; the supplement gets indexed alongside the user's own memory and surfaces through `memory_search` with the same FTS + embedding pipeline.

The corpus projection includes both engine-written audit rows (formatted as "## Pack call: <pack-name>\n\nPersona: technical\nAt: 2026-05-30T...") and user_facts rows from PR #2's write surface. So once all three PRs land, the user can ask OpenClaw "what was that thing about my Konflux deployment preference" and `memory_search` returns hits from helmdeck's user_facts category alongside any conversational chunks where it came up.

## Consequences

**Positive.**
- OpenClaw's `memory_search` quality jumps to semantic-recall level on a fresh install without operator config (PR #1).
- Every MCP client gains a place to persist user facts that survive sessions — no per-client memory plumbing (PR #2).
- OpenClaw users get cross-source recall: their conversational memory + helmdeck's audit + helmdeck's user_facts in one search (PR #3).
- The other clients (Claude Code, Gemini CLI, Claude Desktop) get the same write capability through the REST/pack surface, even without their own memory plugin model.

**Negative.**
- Adds a second optional sidecar to the default install (PR #1) — ~1 GB image, ~600 MB RAM. Mitigated by opt-out flag.
- The write surface is a new attack/abuse surface. Mitigated by: caller-scoped namespacing, mandatory TTL, reserved-category guard against `pack_history` / `pipeline_history` writes.
- MCPorter wire shape is documented but not necessarily stable across OpenClaw versions. PR #3 should track the exact tool/resource shape OpenClaw expects at impl time; the design here is the contract direction, not the wire format.

**Out of scope.**
- **Embeddings inside helmdeck's memory layer.** The store stays prefix-keyed SQLite. Semantic search happens *in OpenClaw* via the corpus bridge in PR #3. Adding a vector tier inside helmdeck is a separate ADR; doing it via OpenClaw gets the benefit without doubling the dependency surface.
- **Per-pack memory-write ACLs.** PR #2's write surface is generic; we don't add a `pack.AcceptsExternalWrites` flag or finer-grained per-pack permissions. Category + caller namespace + TTL is the full contract.
- **Cross-device fact sharing.** This roadmap stops at single-deployment scope. Multi-device sync is OpenClaw's territory.
- **Claude Code / Gemini CLI / Claude Desktop memory plugins.** When/if those vendors document an external-memory-contribution API, that's a follow-up PR per client. Until then, those clients get the REST/pack/resource path PR #2 ships.

## See also

- ADR 039 — Universal Memory Delivery Layer. This roadmap layers a write surface and an external bridge on top.
- ADR 047 — Catalog metadata + memory-driven routing. PR #3 reuses `packs.BuildDefaults` projection logic, the `helmdeck.memory_forget` cleanup vocabulary, and the same caller-namespacing pattern.
- OpenClaw's `packages/memory-host-sdk/src/host/` — the plugin-API surface PR #3 bridges into.
