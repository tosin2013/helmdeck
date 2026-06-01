---
title: helmdeck.memory_forget
description: Erase the caller's learned routing/audit history, scoped or wholesale.
keywords: [helmdeck, orchestration, memory, helmdeck.memory_forget, MCP, ADR 047]
---

# `helmdeck.memory_forget`

`helmdeck.memory_forget` clears the caller's pack/pipeline audit history — the rows that back learned defaults and memory-driven routing (ADR 047). Use it when the user asks to "forget" their defaults, start a new project context, or clear history before a tenant handoff. It targets only audit rows (categories `pack_history` / `pipeline_history`) and user-fact categories; it never touches pack output caches (`content.ground` Firecrawl cache, `github.*` REST cache) or vault credentials. It is scoped to the calling subject's namespace — you cannot forget another caller's history.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `scope` | `string` | no | `all` | One of `all` / `packs` / `pipelines` / `pack:<id>` / `pipeline:<id>` / `key:<exact-key>`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `scope` | `string` | The scope that was applied. |
| `deleted` | `number` | Count of rows removed. |

## Vault credentials needed

- **None.**

## Use it from your agent (OpenClaw chat-UI worked example)

> *OpenClaw chat capture pending.* See [`howto/agent-facts.md`](/howto/agent-facts).

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/helmdeck.memory_forget \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{ "scope": "all" }'
```

(REST equivalent: `POST /api/v1/memory/forget`.) When no memory store is configured the pack returns a no-op success with `deleted: 0`.

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | Malformed `scope`. |

## Session chaining

- **No session.** Bare-caller namespace.

## Async behavior

Synchronous only.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/memory_forget.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/memory_forget.go).
- Companion packs: [`helmdeck.memory_store`](memory-store.md), [`helmdeck.route`](route.md).
- MCP resource: `helmdeck://my-defaults` — see [`mcp-resources.md`](/reference/mcp-resources).
- ADR 047 — Catalog Metadata, Memory-Driven Routing, and Gap Analysis.
