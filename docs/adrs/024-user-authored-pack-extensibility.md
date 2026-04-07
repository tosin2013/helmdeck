# 24. User-Authored Pack Extensibility

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
Built-in packs (ADRs 014–023) cannot anticipate every workflow. Operators and developers need to add custom packs without forking the platform or waiting for upstream releases. Pack-authoring velocity (≤10 minutes from idea to published pack — PRD §18) is a defining metric.

## Decision
Make pack authoring a first-class operator workflow with three components:

1. **Schema-first definition.** A pack is uniquely identified by its name and defined by an input JSON Schema and an output JSON Schema. Both are edited in the Management UI's Pack Authoring panel with live validation, autocomplete, and example values.
2. **Two handler runtimes.** Authors choose between (a) an inline Go handler compiled by the control plane on save (for built-in packs and trusted operators) or (b) a `.wasm` module (see ADR 012) for sandboxed third-party packs. Both runtimes have access to the substrate APIs (sessions, vault, AI gateway, object store) via a stable host interface.
3. **Test-runner-driven feedback loop.** Before publish, the author runs the pack against a real session in the Test Runner tab and inspects raw output, latency, and validation errors. After publish, the Model Success Rates tab (§8.6) feeds reliability data back into schema iteration: if a pack underperforms on weak models, the author tightens the schema, adds defaults, or constrains the decision surface until success rises above the 80% threshold.

Packs are versioned. Old versions remain callable at `/api/v1/packs/{name}/v{n}` until explicitly deprecated, and the MCP tool manifest exposes the latest stable version by default.

## Consequences
**Positive:** the platform's product surface grows without platform releases; operators close their own weak-model gaps via the Model Success Rates loop; pack catalogs become a shareable artifact.
**Negative:** governance burden — every published pack is part of the security boundary and the support surface; need a review/staging workflow for production environments.

## Related PRD Sections
§6.7 Pack Authoring, §8.6 Capability Packs Panel, §18 Success Metrics, §19.10 Progressive Disclosure
