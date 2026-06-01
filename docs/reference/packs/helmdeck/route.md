---
title: helmdeck.route
description: Recommend the best pipeline/pack for a natural-language intent, with gap analysis when nothing fits.
keywords: [helmdeck, orchestration, routing, helmdeck.route, MCP, ADR 047]
---

# `helmdeck.route`

`helmdeck.route` is an orchestration meta-pack: it does no work itself, but tells the agent *which* pack or pipeline to use for a single natural-language intent. It combines three signals — the structured catalog (surfaced as `helmdeck://routing-guide`), the caller's learned defaults (`helmdeck://my-defaults`), and an LLM reasoning step — and returns one recommendation, up to three alternatives, and a structured `gap_warning` when nothing in the catalog can serve the request. Call it FIRST for a single-intent request; for multi-action prompts use [`helmdeck.plan`](plan.md).

The agent confirms the recommendation with the user, then runs the recommended pack/pipeline. `helmdeck.route` never executes the recommendation itself.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `user_intent` | `string` | yes | — | The user's natural-language request. |
| `model` | `string` | yes | — | A routable `provider/model` id (see `helmdeck://models`). |
| `context` | `object` | no | — | Optional extra hints (current repo, recent artifacts, etc.). |
| `max_tokens` | `number` | no | — | Cap on the LLM reasoning step. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `recommendation` | `object` | `{kind: "pack"\|"pipeline", id, suggested_inputs}` — inputs pre-filled from learned defaults. |
| `alternatives` | `array` | Up to 3 other candidates. |
| `gap_warning` | `object` | Present only when nothing fits: a proposed new pack `{name, input_schema, output_schema, integration_pattern, why_useful}`. |
| `reasoning` | `string` | Why the router chose this recommendation. |
| `model` | `string` | The model that produced the recommendation. |

## Vault credentials needed

- **None** directly — but the pack requires a configured AI gateway (it is one of the 10 gateway-gated packs). Without a gateway it is not registered.

## Use it from your agent (OpenClaw chat-UI worked example)

> *OpenClaw chat capture pending.* See [`pack-demo-playbook.md`](/integrations/pack-demo-playbook) §"Orchestration meta-packs" for example prompts and expected outputs, and [`howto/routing-and-gap-analysis.md`](/howto/routing-and-gap-analysis) for the end-to-end workflow.

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/helmdeck.route \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "user_intent": "turn this PDF into a narrated video",
    "model": "openrouter/auto"
  }'
```

## Error codes

Closed set in [`internal/packs/errors.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/errors.go).

| Code | Triggers |
|---|---|
| `invalid_input` | `user_intent` or `model` missing. |
| `internal` | Registered without a gateway dispatcher or pack registry. |
| `handler_failed` | The model returned no parseable recommendation. |

## Session chaining

- **No session.** Stateless meta-pack; chains freely.

## Async behavior

Synchronous only.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/route.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/route.go).
- Companion packs: [`helmdeck.plan`](plan.md), [`helmdeck.memory_store`](memory-store.md), [`helmdeck.memory_forget`](memory-forget.md).
- MCP resources: `helmdeck://routing-guide`, `helmdeck://my-defaults` — see [`mcp-resources.md`](/reference/mcp-resources).
- ADR 047 — Catalog Metadata, Memory-Driven Routing, and Gap Analysis.
