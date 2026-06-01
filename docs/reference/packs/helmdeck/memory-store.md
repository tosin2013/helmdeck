---
title: helmdeck.memory_store
description: Persist a durable user fact to the caller's memory namespace across sessions.
keywords: [helmdeck, orchestration, memory, helmdeck.memory_store, MCP, ADR 048]
---

# `helmdeck.memory_store`

`helmdeck.memory_store` writes a durable, user-supplied fact into the caller's memory namespace (ADR 048). Use it when the user shares a preference, project convention, or decision worth remembering across sessions (*"I always deploy via Konflux"*, *"prefer React over Vue"*). Read `helmdeck://my-memory` first to avoid duplicates. The default category is `user_facts` (90-day TTL); pass `category` for a richer taxonomy. The fact is scoped to the calling JWT subject — facts written under one caller are invisible to another.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `key` | `string` | yes | — | Short identifier for the fact. |
| `value` | `string` | yes | — | The fact text. |
| `category` | `string` | no | `user_facts` | Taxonomy bucket. `pack_history` and `pipeline_history` are reserved for engine audit writes and reject with `invalid_input`. |
| `tags` | `array` | no | — | Optional string tags. |
| `ttl_seconds` | `number` | no | 90 days | Mandatory and bounded: min 1h, max 365d. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `key` | `string` | Echoed key. |
| `category` | `string` | The assigned (validated) category. |
| `expires_at` | `string` | RFC 3339 expiry — surface this to the user so they know what's stored. |

## Vault credentials needed

- **None.**

## Use it from your agent (OpenClaw chat-UI worked example)

> *OpenClaw chat capture pending.* See [`howto/agent-facts.md`](/howto/agent-facts) for the write/read/forget contract.

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/helmdeck.memory_store \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "key": "deploy_pipeline",
    "value": "I always deploy via Konflux",
    "category": "preferences"
  }'
```

(REST equivalent: `POST /api/v1/memory/store`.)

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | `key`/`value` missing, reserved category, or out-of-range `ttl_seconds`. |
| `internal` | Memory store not wired. |

## Session chaining

- **No session.** `NeedsSession: false` — the namespace is the bare caller, so facts learned in one session appear in the next.

## Async behavior

Synchronous only.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/memory_store.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/memory_store.go).
- Companion packs: [`helmdeck.memory_forget`](memory-forget.md).
- MCP resource: `helmdeck://my-memory` — see [`mcp-resources.md`](/reference/mcp-resources).
- ADR 048 — Memory Write Surface and OpenClaw Bridge.
