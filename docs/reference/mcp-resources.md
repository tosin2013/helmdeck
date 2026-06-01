---
title: MCP resources
description: Reference for every read-only MCP resource helmdeck exposes — catalog discovery, routing, learned defaults, memory, and context budgets.
keywords: [helmdeck, MCP, resources, routing-guide, my-defaults, my-memory, context-budgets, my-plans]
---

# MCP resources

Beyond the capability packs (exposed as MCP *tools*), helmdeck exposes read-only **resources** for discovery and personalization. Enumerate them with `resources/list` and fetch one with `resources/read` (URI in `params.uri`). Resources are also browsable in the Management UI's **MCP** panel.

All resources are scoped to the calling JWT subject where personalization applies (`my-*`), and global where they describe engine state (`models`, `context-budgets`).

## Catalog & discovery

| Resource | Since | Returns |
|---|---|---|
| `helmdeck://packs` | — | Live pack catalog. Equivalent to `tools/list`, as a browsable resource. |
| `helmdeck://sessions` | — | Live session list (`id`, `status`, `image`, `created_at`). |
| `helmdeck://voices` | — | ElevenLabs voice catalog (`id`, `name`, `labels`, preview URL) for `podcast.generate` and `slides.narrate`. Requires `elevenlabs-key`. |
| `helmdeck://image-models` | v0.12.0 | Curated fal.ai model catalog for `image.generate` and chained image inputs. Each entry has cost, p50 latency, max resolution, capabilities. |
| `helmdeck://models` | ADR 043 | Chat-completion models the gateway can route to right now, as full `provider/model` ids. Use one **verbatim** for any pack/pipeline `model` input. |

## Routing, memory & context (v0.22.0, ADRs 047-050)

These five are **always listed**. When there's no data yet (memory disabled, no caller history), they return empty arrays plus a `note` string explaining the empty state.

### `helmdeck://routing-guide` (ADR 047)

The structured catalog the chat agent queries to pick the right pipeline or pack for a request. Each entry carries `accepts` / `produces` / `intent_keywords` / `typical_use` / `limitations` (and `supersedes` for pipelines), plus a top-level `policy` block intended as system-prompt context.

- **When to read:** first, for any multi-step request. This is the source of truth that `helmdeck.route` and `helmdeck.plan` consult internally; `SKILLS.md` is the offline fallback.
- **Routing rule:** prefer a pipeline over chaining its constituent packs when the pipeline's `supersedes` lists those packs.

### `helmdeck://my-defaults` (ADR 047 PR #2)

Per-caller projection over recent pack/pipeline runs. Returns `packs[]` and `pipelines[]` ranked by frequency, each with `common_inputs` — the most-used value for each learnable input field (`persona`, `audience`, `angle`, `model`, `theme`, …).

- **When to read:** before asking the user for inputs that already have a learned default. Pre-fill from `common_inputs` and confirm rather than re-asking from scratch.
- **REST equivalent:** `GET /api/v1/memory/defaults`.
- **Clear it:** [`helmdeck.memory_forget`](/reference/packs/helmdeck/memory-forget) (`POST /api/v1/memory/forget`).

### `helmdeck://my-memory` (ADR 048 PR #2)

Per-caller index of user-supplied facts stored via [`helmdeck.memory_store`](/reference/packs/helmdeck/memory-store). Returns `categories[]` with `name` + `count` + `recent_keys[]`. Audit categories (`pack_history` / `pipeline_history`) are excluded — those surface via `my-defaults`.

- **When to read:** before storing a new fact, to avoid duplicates or re-asking the user something already known.

### `helmdeck://context-budgets` (ADR 050 PR #2)

Per-model prompt budgets that `internal/llmcontext` applies when compacting the catalog projection for LLM-backed packs (`helmdeck.plan`, `helmdeck.route`). Returns `budgets[]` (each `{model, input_tokens, output_tokens, max_catalog_bytes, tier}`), a `fallback` entry for unmapped models, and a `policy` string explaining the lookup.

- **Tiers:** A = no compaction; B/C = progressively aggressive metadata trim.
- **When to read:** when investigating why a free-model plan saw a slim catalog, or when adding a new model id to your deployment. See [Free models & context management](/howto/free-models-and-context).

### `helmdeck://my-plans` (ADR 049 + ADR 050 PR #3)

Per-caller projection over `plan_history` audit rows (written by `helmdeck.plan`). Returns `groups[]` of intent-sha cohorts with `count` + most-frequent `complexity` + top tools picked + last-seen timestamp + models used.

- **When to read:** to audit the planner's behavior over time and detect stable learned plans (an intent-sha with `count > 1` and a stable top-tools list).

## See also

- [`SKILLS.md`](/integrations/SKILLS) §"MCP resources" — the agent-facing summary.
- Orchestration packs: [`helmdeck.route`](/reference/packs/helmdeck/route), [`helmdeck.plan`](/reference/packs/helmdeck/plan), [`helmdeck.memory_store`](/reference/packs/helmdeck/memory-store), [`helmdeck.memory_forget`](/reference/packs/helmdeck/memory-forget).
- ADRs 047 (routing + memory), 048 (memory write surface), 049 (intent decomposition), 050 (LLM context manager).
