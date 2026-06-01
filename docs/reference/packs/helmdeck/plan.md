---
title: helmdeck.plan
description: Decompose a multi-intent prompt into an ordered, pipeline-aware sequence of helmdeck tool calls.
keywords: [helmdeck, orchestration, intent decomposition, helmdeck.plan, MCP, ADR 049]
---

# `helmdeck.plan`

`helmdeck.plan` is the multi-intent companion to [`helmdeck.route`](route.md). Given a prompt that spans several actions (*"remember this, draft a blog, and generate a cover image"*), it returns an ordered `steps[]` array (each `{order, tool, args, rationale}`), a derived `rewritten_prompt` the agent can execute line-by-line, and a `complexity` classifier (`single-action` / `pipeline-direct` / `pack-chain`). It is pipeline-aware: when a curated pipeline covers the intent end-to-end it emits ONE `helmdeck__pipeline-run` step rather than re-decomposing it.

Two execution paths: iterate `steps[]` structurally, or feed `rewritten_prompt` back into a small model as a clearer instruction. The planner cannot call itself (recursive `helmdeck.plan` steps are demoted to `"tool": "unknown"`), and unknown tool ids are similarly demoted — never dispatch those.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `user_intent` | `string` | yes | — | The multi-action user request. |
| `model` | `string` | yes | — | A routable `provider/model` id. |
| `context` | `object` | no | — | Optional extra hints. |
| `max_tokens` | `number` | no | — | Cap on the LLM step. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `steps` | `array` | Ordered `{order, tool, args, rationale}` calls. |
| `rewritten_prompt` | `string` | The plan as a single executable instruction. |
| `complexity` | `string` | `single-action` / `pipeline-direct` / `pack-chain`. |
| `reasoning` | `string` | Why the plan is shaped this way. |
| `compaction` | `object` | Present when the LLM context manager (ADR 050) compacted the catalog to fit the model's budget. |
| `model` | `string` | The model that produced the plan. |

## Vault credentials needed

- **None** directly — requires a configured AI gateway (gateway-gated pack).

## Use it from your agent (OpenClaw chat-UI worked example)

> *OpenClaw chat capture pending.* See [`howto/intent-decomposition.md`](/howto/intent-decomposition) for the wire shape and execution patterns, and [`howto/free-models-and-context.md`](/howto/free-models-and-context) for the `compaction` field on small models.

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/helmdeck.plan \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "user_intent": "remember I prefer React, then research SSR and draft a blog post",
    "model": "openrouter/auto"
  }'
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | `user_intent` or `model` missing. |
| `internal` | Registered without a gateway dispatcher or pack registry. |
| `handler_failed` | The model returned no parseable plan. |

## Session chaining

- **No session.** Stateless meta-pack.

## Async behavior

Synchronous only.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/plan.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/plan.go).
- Companion packs: [`helmdeck.route`](route.md), [`helmdeck.memory_store`](memory-store.md).
- MCP resources: `helmdeck://my-plans`, `helmdeck://context-budgets` — see [`mcp-resources.md`](/reference/mcp-resources).
- ADR 049 — Intent Decomposition and the Self-Learning Plan Pack; ADR 050 — LLM Context Manager.
