---
title: Route a request and read gap warnings
description: Use helmdeck.route and helmdeck://routing-guide to pick the right pack/pipeline, and act on the gap_warning when nothing fits.
keywords: [helmdeck, routing, helmdeck.route, routing-guide, gap analysis, ADR 047]
---

# Route a request and read gap warnings

When an agent gets a request, it has to pick *which* of the 52 packs (or 21 pipelines) to run. Hard-coding that logic in a system prompt doesn't scale. `helmdeck.route` (ADR 047) does it dynamically: it reads the structured catalog, blends in the caller's learned defaults, asks an LLM to reason, and returns a single recommendation — or a structured `gap_warning` when nothing in the catalog can serve the request.

This guide assumes helmdeck is installed with an AI gateway configured (routing is LLM-backed). For multi-action prompts use [`helmdeck.plan`](./intent-decomposition.md) instead; the two share the same catalog and memory.

## Step 1 — (optional) read the catalog the router sees

`helmdeck://routing-guide` is the structured catalog `helmdeck.route` reasons over. You rarely need to read it directly, but it's useful for debugging "why did it pick that?":

```json
resources/read  { "uri": "helmdeck://routing-guide" }
```

Each entry carries `accepts` / `produces` / `intent_keywords` / `typical_use` / `limitations`, and pipelines also carry `supersedes`. The routing rule: **prefer a pipeline over chaining its constituent packs** when the pipeline's `supersedes` lists those packs.

## Step 2 — route a single intent

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/helmdeck.route \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{ "user_intent": "turn this PDF into a narrated video", "model": "openrouter/auto" }'
```

The response shape:

```json
{
  "recommendation": {
    "kind": "pipeline",
    "id": "builtin.doc-rewrite-blog",
    "suggested_inputs": { "audience": "platform engineers" }
  },
  "alternatives": [ { "kind": "pack", "id": "doc.parse" } ],
  "reasoning": "...",
  "model": "openrouter/auto"
}
```

`suggested_inputs` is pre-filled from `helmdeck://my-defaults` — the caller's learned values for fields like `persona`, `audience`, and `model`. **Confirm with the user, then run the recommendation.** `helmdeck.route` never executes it for you.

## Step 3 — act on a `gap_warning`

When nothing in the catalog fits, the response omits a real `recommendation` and includes a `gap_warning` — a structured proposal for a *new* pack:

```json
{
  "gap_warning": {
    "name": "calendar.create_event",
    "input_schema":  { "title": "string", "start": "string", "attendees": "array" },
    "output_schema": { "event_id": "string", "html_link": "string" },
    "integration_pattern": "Google Calendar REST + vault google-oauth",
    "why_useful": "Several scheduling requests have no catalog match."
  }
}
```

Don't fabricate a tool call to fill the gap. Surface the proposal to the user, and if they confirm, file it with `github.create_issue` (repo `tosin2013/helmdeck`, labels `enhancement`, `area/packs`) so a maintainer can build it.

## How the router learns

Every `helmdeck.route` call is audited (per-caller, 30-day TTL). The audit feeds `helmdeck://my-defaults`, so the more a caller routes, the better the `suggested_inputs` pre-fills get. To reset that learning, run [`helmdeck.memory_forget`](../reference/packs/helmdeck/memory-forget.md) with `scope: all` (or scope it to one pack/pipeline). Operators can browse and clear the same data in the Routing Memory UI — see [the UI walkthrough](../tutorials/install-ui-walkthrough.md).

## Related

- [`helmdeck.route` reference](../reference/packs/helmdeck/route.md) and [`helmdeck.plan`](../reference/packs/helmdeck/plan.md).
- [MCP resources](../reference/mcp-resources.md) — `routing-guide`, `my-defaults`.
- [Intent decomposition](./intent-decomposition.md) — the multi-action companion.
- ADR 047 — Catalog Metadata, Memory-Driven Routing, and Gap Analysis.
