# 29. Four-Tier Agent Memory API

**Status**: Proposed
**Date**: 2026-04-07
**Domain**: distributed-systems, database-design

## Context
Agents repeatedly re-discover the same site-specific automation patterns on every run because helmdeck currently has no cross-session memory. The 2026 agent-memory landscape (Mem0 state-of-the-art survey) has converged on a four-tier architecture that maps cleanly onto the platform's existing storage primitives (PRD §19.7).

## Decision
Implement a **Session Memory API** with four tiers, each backed by an appropriate store and exposed through a single uniform endpoint:

| Tier | Storage | Scope | Lifetime | Use Case |
| :--- | :--- | :--- | :--- | :--- |
| **Working** | In-process Go map (control plane) | Single session | Until session terminates | Current page state, active task context |
| **Episodic** | Redis | Per-agent, cross-session | Configurable TTL (default 30 d) | "Last time I logged into this site, the SSO button was at #login-sso" |
| **Semantic** | PostgreSQL + pgvector | Shared across agents in a tenant | Indefinite, with operator pruning | Site-specific navigation patterns, form-field mappings, schema-extraction rules |
| **Procedural** | WASM modules / Go scripts | Platform-wide | Versioned, deprecated explicitly | Reusable automation workflows — promoted to Capability Packs (ADR 024) once stable |

**API surface:**
- `GET /api/v1/memory/{agent_id}?tier=...&query=...`
- `POST /api/v1/memory/{agent_id}` `{ tier, key, value, ttl?, embedding? }`
- `DELETE /api/v1/memory/{agent_id}/{key}`

Semantic-tier writes auto-generate embeddings via the configured AI gateway (ADR 005), so callers pass plain text. Episodic and working tiers are pure key/value. The procedural tier is read-only via this API — promotion happens through the Pack Authoring workflow.

**Memory writes are scoped by JWT claims** so a token issued for one agent cannot read another agent's episodic memory. The semantic tier is shared within a tenant boundary defined by the Access Control panel (§8.7).

**Promotion path:** procedural memory entries that show repeated successful use (tracked by the Model Success Rates instrumentation, §8.6) surface in the UI as "Pack Candidates" — the operator can one-click convert a stable procedural pattern into a versioned Capability Pack.

## Consequences
**Positive:** progressive learning becomes a first-class capability — agents stop rediscovering site quirks; the four-tier model gives operators clean retention/scope controls per data class; the procedural→pack promotion path closes the loop between observation and codification.
**Negative:** introduces Redis and pgvector as new required dependencies in the production tier; embedding generation consumes AI gateway budget and must be rate-limited; cross-tenant data leakage via shared semantic tier is a real risk requiring strict scope enforcement and audit.

## Related PRD Sections
§19.7 Agent Memory and Session Persistence, §6.6 Capability Packs, §6.7 Pack Authoring, §8.6 Capability Packs Panel, §8.7 Access Control
