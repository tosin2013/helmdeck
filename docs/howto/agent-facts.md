---
description: "How OpenClaw agents persist and recall user-supplied facts via helmdeck's per-caller memory layer. Covers the contract, surfaces, and lifecycle of stored facts."
---

# How agents persist and recall user facts

Helmdeck's memory layer doesn't just track *what the engine learned* (audit history, learned defaults) — as of ADR 048 PR #2, agents can also persist *what the user told them* under the same per-caller namespace. This page covers the contract, the surfaces, and the lifecycle.

## What gets stored

Durable user-supplied facts that should survive the conversation: preferences, conventions, decisions, project constraints. Examples:

- `"I always deploy via Konflux, never manual kubectl"`
- `"prefer React over Vue (Konflux project constraint)"`
- `"slides for the platform team should use helmdeck-dark theme"`
- `"omit Stripe-related links from blog drafts — legal hold"`

What does NOT belong here: ephemeral conversation context, secrets/credentials (those go to the vault), or anything that's already captured by helmdeck's audit history (persona used per pack call, etc — those live in `helmdeck://my-defaults`).

## Two surfaces, same store

| Surface | When to use |
| --- | --- |
| `POST /api/v1/memory/store` | REST clients (the management UI, scripts, external services with a JWT). |
| `helmdeck__memory_store` MCP tool | Chat agents (OpenClaw, Claude Code, Gemini CLI, Claude Desktop) calling helmdeck via MCP. |

Both validate via the same engine policy (`internal/packs/facts.go` → `packs.ValidateFact`), so the wire shape and error taxonomy never drift.

## Write — request shape

```json
{
  "key": "preferences/frontend-framework",
  "value": "React over Vue (Konflux project constraint)",
  "category": "preferences",
  "tags": ["frontend", "konflux"],
  "ttl_seconds": 7776000
}
```

- **`key`** (required) — a stable identifier. Slashes form an informal hierarchy you can prefix-list later (`preferences/`, `konflux/`). Trimmed of surrounding whitespace.
- **`value`** (required) — the fact text. No structure imposed; an agent can store JSON, prose, code snippets, whatever. Trimmed.
- **`category`** (optional, default `"user_facts"`) — a tag for grouping. Pick whatever taxonomy fits your project: `preferences`, `project_conventions`, `deploy_targets`, `dislikes`. **Reserved**: `pack_history` and `pipeline_history` (engine-owned; rejected with 400).
- **`tags`** (optional) — array of free-form strings for finer-grained filtering. The forget surface doesn't filter by tag today; this is a forward-compat slot.
- **`ttl_seconds`** (optional, default 7776000 = 90 days) — when the entry auto-expires. **Minimum** 3600 (1 hour). **Maximum** 31536000 (~1 year). A `0` is treated as "use default" (not "live forever").

## Read — discovering what's already stored

Agents should peek before storing to avoid duplicates and to discover existing facts. Read `helmdeck://my-memory` (MCP resource):

```json
{
  "scope": "caller=<jwt-subject>",
  "fetched_at": "2026-05-31T...",
  "categories": [
    {"name": "preferences", "count": 3, "recent_keys": ["preferences/frontend-framework", "preferences/lang/backend", "preferences/slide-theme"]},
    {"name": "project_conventions", "count": 1, "recent_keys": ["konflux/deploy"]}
  ]
}
```

Only counts + recent keys are surfaced — the actual fact *values* aren't echoed here to keep the resource compact. To recall an individual fact value today, an agent connected through the OpenClaw corpus bridge (ADR 048 PR #3) uses OpenClaw's `memory_search` against the QMD endpoint; there is no dedicated `helmdeck.memory_recall` pack (that surface was folded into the corpus bridge — see [`openclaw-memory-corpus.md`](./openclaw-memory-corpus.md)).

## Forget — the cleanup surface

Same vocabulary as ADR 047 PR #2's `helmdeck.memory_forget` pack:

| Scope | Deletes |
| --- | --- |
| `all` | All audit + facts under the caller's namespace |
| `pack:<id>` | Audit rows for one pack (engine-owned) |
| `pipeline:<id>` | Audit rows for one pipeline (engine-owned) |
| `key:<exact-key>` | One specific fact by key |

To forget a single fact: `helmdeck.memory_forget` with `scope: "key:preferences/frontend-framework"`. The audit-category-based scopes (`packs` / `pipelines`) don't touch `user_facts` entries — agent-written facts are deleted only by `all` or by exact-key.

## Lifecycle guarantees

- **Per-caller isolation.** Facts written under one JWT subject are invisible to another, even on the same deployment. The namespace key is `auth.FromContext(ctx).Subject` (falls back to `"unknown"` when auth is disabled).
- **Bounded TTL.** No permanent storage by design. Facts that matter long-term should be written into the codebase (CLAUDE.md, project docs) where they're versioned alongside the work; memory is for cross-session continuity, not authoritative knowledge.
- **No silent over-writes.** Same `key` twice with different values keeps the latest — but the engine logs the `UpdatedAt` so a future `helmdeck.memory_history` pack could show the trajectory if needed.
- **Audit-category guard.** The reserved-category list (`pack_history`, `pipeline_history`) is checked at write time. An agent attempting to write under those gets a 400 / `CodeInvalidInput`, never a silent success that would poison the `helmdeck://my-defaults` projection.

## When to skip the fact-store

Don't use the memory layer for:

- **Secrets / credentials.** Use the vault (`vault.write`, `vault.read`). Vault entries are encrypted at rest with a separate master key; memory entries are AES-256-GCM but the threat model is different.
- **Anything you'd want versioned with the code.** Project conventions that change with the codebase belong in CLAUDE.md or a `docs/CONVENTIONS.md`. The fact-store is for cross-session continuity, not source-of-truth knowledge.
- **Large blobs.** Use the artifact store. Memory entries should be lookup-keyed prose / config — not embedded files.

## See also

- [ADR 048 — Memory write surface + OpenClaw memory-corpus bridge](../adrs/048-memory-write-surface-openclaw-bridge.md)
- [How to inspect audit logs](inspect-audit-logs.md) — engine-side audit memory has its own surfaces (`helmdeck://my-defaults`, `/api/v1/memory/defaults`).
- `internal/packs/facts.go` — the engine policy (`ValidateFact`, reserved categories, TTL bounds).
