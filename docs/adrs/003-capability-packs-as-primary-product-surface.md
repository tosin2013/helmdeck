# 3. Capability Packs as the Primary Product Surface

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
Live testing on 2026-04-06 produced three distinct weak-model failure modes: missing binary (Dockerfile-fixable), open-ended reasoning over long input (stalled regardless), and raw-shell external access (brittle SSH/git workflows). The common factor in failures 2 and 3 is an interface too open-ended for a model with limited reasoning. Frontier models can drive bash and a README; weak 7B models cannot. The platform's value depends on closing this gap (PRD §3.1, §6.6, §19.10).

## Decision
The primary product surface is **Capability Packs** — opinionated, schema-validated, one-shot tool bundles with typed JSON input and deterministic JSON or artifact output. Each pack is exposed simultaneously as `POST /api/v1/packs/{name}` and as an MCP tool via the platform's built-in MCP server. Multi-step orchestration (session creation, navigation, network-idle wait, extraction, validation, cleanup) lives inside packs, never in the agent's reasoning loop. The defining success metric is **≥90% pack success rate on 7B–30B-class open-weight models across 5 reference packs.**

## Consequences
**Positive:** weak models become viable production agents; agent integration is one MCP-server registration away; brittle environmental knowledge (SSH known_hosts, git auth, ffmpeg flags) moves from "every agent figures it out" to "encoded once in the pack."
**Negative:** every new capability requires authoring a pack rather than exposing raw substrate; pack catalog is the long-term maintenance surface.

## Related PRD Sections
§3.1 Primary Goals, §6.6 Capability Packs, §6.7 Pack Authoring, §8.6 Capability Packs Panel, §18 Success Metrics, §19.10 Progressive Disclosure
