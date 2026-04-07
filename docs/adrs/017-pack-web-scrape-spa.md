# 17. Pack: `web.scrape_spa`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
SPA scraping requires waiting for JavaScript hydration, network idle, DOM stability, and schema-conformant extraction — a sequence that frequently produces empty or partial output when driven by weak models (PRD §6.6, §19.10).

## Decision
Ship `web.scrape_spa` as a built-in pack.

**Input:** `{ url: string, schema: object (JSON Schema), wait_ms?: integer, max_wait_ms?: integer }`
**Output:** `{ data: object (matching schema), partial?: boolean, fields_extracted: integer }`
**Errors:** `not_found`, `timeout`, `schema_mismatch`, `network_error`

The handler navigates, waits for network idle (default 500 ms quiet period, max 30 s), runs an extraction routine that maps each schema property to a CSS/XPath selector or LLM-assisted field extraction, validates against the schema, and returns the result. On partial success it returns `partial: true` instead of failing.

## Consequences
**Positive:** structured extraction in one call; schema enforcement guarantees agent-consumable output.
**Negative:** schema design quality determines reliability; LLM-assisted extraction calls back through the AI gateway.

## Related PRD Sections
§6.6 Capability Packs, §10 Typed Error Codes
