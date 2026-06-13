---
description: "ADR-047: Catalog Metadata, Memory-Driven Routing, and Gap Analysis — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 47. Catalog Metadata, Memory-Driven Routing, and Gap Analysis

**Status**: Accepted — fully shipped in v0.22.0 (all four PRs: catalog metadata + `helmdeck://routing-guide`, memory audit hooks + `helmdeck://my-defaults` + `helmdeck.memory_forget`, the `helmdeck.route` meta-pack with gap analysis, and the Routing Memory management UI)
**Date**: 2026-05-31
**Domain**: pack-engine, pipelines, mcp, memory, agent-integrations

## Context

The chat agent's routing logic — *"if the user pastes a brief use `builtin.brief-rewrite-blog`, if a PDF use `builtin.doc-rewrite-blog`, if a code change against a repo use `builtin.issue-to-pr` …"* — lives in `skills/helmdeck/SKILL.md`. That file is now ~500 lines, has grown a row for every PR that adds a pipeline, and is loaded into every conversation as system-prompt text. Two problems compound:

1. **Routing-as-static-prose doesn't scale.** Every catalog addition requires hand-editing SKILL.md, agents waste tokens reading the whole file on every turn, the picker logic is informal English that the agent must re-derive each conversation, and there's no programmatic way to ask "which pipeline accepts this input shape?" SKILL.md is the *only* surface that knows, and it isn't queryable.
2. **Memory is read-through cache only.** [ADR 039](039-universal-memory-delivery-layer.md) shipped a per-caller memory layer. A handful of packs opt into it (e.g. `content.ground` caches Firecrawl results, `github.list_issues` caches REST responses), but the system never writes audit entries for pack/pipeline runs, never reads them for personalization, and has no surface to expose them to the chat agent. There's no way for the agent to know "this caller's typical persona for blog posts about AI agents is `technical`" — every conversation starts from zero.

These compose into the real product gap: the agent can't get smarter over time, can't notice when a request doesn't match anything in the catalog, and can't query the catalog programmatically. As helmdeck grows past ~50 packs and ~25 pipelines (today: 52 packs, 21 pipelines), the routing surface has to become first-class — declarative, queryable, memory-backed — rather than a static blob.

## Decision

Replace SKILL.md-as-routing-manual with a **three-PR sequence**, each independently mergeable, that together delivers a self-describing catalog the agent queries dynamically, audit memory that personalizes over time, and a routing pack that combines both with gap analysis. SKILL.md becomes a thin fallback ("when in doubt, query `helmdeck://routing-guide`").

### PR #1 — Catalog metadata + `helmdeck://routing-guide` resource (this PR)

Define structured metadata that packs and pipelines declare alongside their existing schemas, expose the structured catalog via a new MCP resource, and populate metadata on a representative subset to validate the schema.

- **`packs.PackMetadata`** — embedded as a nested `Metadata` field on `*packs.Pack`. Fields:
  - `Accepts []string` — input kinds the pack consumes (`markdown`, `url`, `pdf`, `repo_url`, `query`, `brief`, `source_content`, `session_id`, …)
  - `Produces []string` — output kinds the pack emits (`blog_markdown`, `pdf`, `mp4`, `mp3`, `code_diff`, `pr_url`, `slide_deck`, `outline_markdown`, …)
  - `IntentKeywords []string` — short phrases an agent matches against user intent (`"make blog post"`, `"from pdf"`, `"explain to executives"`)
  - `TypicalUse string` — one-sentence "use this when…" line
  - `Limitations []string` — what this pack CAN'T do (the seed for PR #3's gap analysis: `"does not support audio sources"`, `"requires source markdown — does not fetch URLs"`)
- **`pipelines.PipelineMetadata`** — same shape plus:
  - `Supersedes []string` — pack IDs this pipeline replaces (so an agent reading "found a pipeline that accepts your input AND supersedes pack X" knows not to chain X by hand)
- **`helmdeck://routing-guide` MCP resource** — emits a JSON document keyed by pack/pipeline ID containing the populated metadata plus a top-level `policy` block teaching how to use the catalog: prefer pipelines over packs when both match, treat `Limitations` as gap-analysis seeds, query memory (PR #2) for caller defaults.
- **SKILL.md** — collapsed to point at the resource as the source of truth; existing picker rows kept short as a fallback for offline reads.
- **Initial metadata population** — 10 packs and 5 pipelines covering the diverse cases. Rest of the catalog populated incrementally in follow-up PRs as packs are touched. Empty metadata is the zero value, so missing-metadata packs are valid (additive contract).

### PR #2 — Memory audit hooks + `helmdeck://my-defaults` + `helmdeck.memory_forget`

Hook `*packs.Engine.Execute` and `*pipelines.Runner.RunSync` to write per-caller audit entries to the existing memory layer on every terminal outcome (success and caller-fixable failure). The projection only learns from successes; outcomes are recorded for both so observability isn't selective. Expose two new surfaces:

- **`helmdeck://my-defaults`** — always-listed MCP resource. Aggregates the caller's recent runs into top-N packs + top-N pipelines, each with a `common_inputs` map carrying the most-used value per learnable field (`persona`, `audience`, `angle`, `model`, `theme`, `voice`, `persona_used`, `kind`, `format`, `title`, `author`). Empty when the caller has no history; the agent's contract is to peek here before asking the user for inputs that have learned defaults.
- **`helmdeck.memory_forget`** — pack the chat agent calls when the user says "forget my defaults" or "start fresh." Inputs: `scope` (`all` | `packs` | `pipelines` | `pack:<id>` | `pipeline:<id>`). Targets only the audit categories (`pack_history` / `pipeline_history`) so pack caches and vault credentials are never touched.

Bounded retention: every audit row writes with `memory.WithTTL(packs.AuditTTL)` where `AuditTTL` is **30 days** today. Long enough to learn monthly usage patterns; short enough that SQLite stays bounded on heavy callers. Manual forget is the escape hatch before the TTL expires.

Audit rows write to the caller's **bare namespace** (just `callerFromContext(ctx)`), not the session-scoped namespace `ec.Memory` uses for per-session caches. This is the design point that lets learning span sessions: every chat agent, every CLI invocation, every cron job under the same JWT subject contributes to and benefits from the same defaults pool.

Learnable input fields are a closed set (see `internal/packs/audit.go`). Markdown bodies, URLs, raw queries, and other large/opaque values are dropped at audit-write time — audit memory is for routing hints, not data retention.

Pack-level audit (per the user's PR #1 spec) captures every `Engine.Execute` outcome, not just pipeline runs — so single-pack usage patterns are visible. Acceptable volume given memory's existing cache TTLs.

### PR #3 — `helmdeck.route` meta-pack with gap analysis

A meta-pack the chat agent calls before routing. Takes `{user_intent: string, context?: any}`, queries `helmdeck://routing-guide` + memory defaults internally, returns:

```json
{
  "recommendation": {"kind": "pipeline" | "pack", "id": "builtin.brief-rewrite-blog",
                     "suggested_inputs": {"persona": "technical", "audience": "platform engineers"}},
  "alternatives": [{"kind": "pack", "id": "blog.publish", "why": "…"}],
  "gap_warning": null,
  "reasoning": "Brief-shaped input + blog target. Caller's typical persona for AI-agent topics is technical."
}
```

When nothing matches, `gap_warning` is populated with a structured proposal:

```json
{
  "missing_capability": "extract transcript from YouTube video",
  "proposed_pack": {
    "name": "youtube.transcript",
    "input_schema": {"url": "string"},
    "output_schema": {"transcript_text": "string", "video_title": "string", "channel_name": "string"},
    "integration_pattern": "http-fetch + auth via vault credential 'youtube-api-key'",
    "why_useful": "Would chain with podcast.generate or blog.rewrite_for_audience for video-source content workflows."
  }
}
```

Agent confirms the gap with the user and optionally files a GitHub issue via `github.create_issue`.

SKILL.md collapses to: *"For any multi-step request, call `helmdeck__route` first. It returns the recommended pipeline/pack plus suggested inputs. Confirm with the user, then run."*

### PR #4 — Memory-management UI in `web/`

A page in the existing web app that surfaces what PR #2 wrote: lists recent audit rows (pack name, when, outcome), shows the `helmdeck://my-defaults` projection visually, and exposes the forget surface as buttons (per-row "forget this run", per-pack "forget pack history", global "clear all"). No new backend — this PR wires UI to existing PR #2 endpoints (REST shim around `helmdeck.memory_forget` and an MCP-over-WS read of `helmdeck://my-defaults`).

Rationale for splitting it out: the *capability* to inspect and clear ships in PR #2 (resource + pack). The *visual surface* is a UX concern with its own design loop. Coupling them would slow PR #2's review (Go reviewers vs. React reviewers) and bloat the diff. Users who want CLI/MCP-only management never need this PR.

## Consequences

**Positive.**
- The catalog becomes a queryable surface rather than a static blob. Future maintainers add metadata next to the pack definition, not in a 500-line skill file.
- Agent routing decisions improve over time (PR #2) and get cheaper (the agent loads `helmdeck://routing-guide` instead of all of SKILL.md).
- Gap analysis (PR #3) turns "we don't have this" from a dead end into a structured pack-proposal the maintainer can act on.
- Each PR is independently shippable and improves the system in isolation — PR #1's catalog is useful even without PR #2/3.

**Negative.**
- More fields to keep accurate. A pack whose metadata says `Accepts: ["markdown"]` but really only accepts a specific markdown shape (Marp) is a lie that misroutes agents. Mitigation: populate metadata on the same PR that touches a pack's behavior; reviewer enforcement.
- The agent has two routing surfaces during the transition: SKILL.md text *and* the resource. Mitigation: SKILL.md explicitly says "the resource is canonical; this skill is fallback."

**Out of scope of this ADR.**
- A learning loop that updates `IntentKeywords` from real chat traffic — that's a research project, not a PR.
- A pure-metadata catalog without LLM-based routing — PR #3 commits to the LLM-routing-pack model because the gap-analysis case needs LLM reasoning, not just metadata matching.

## See also

- ADR 039 — Universal Memory Delivery Layer. PR #2 of this roadmap layers on its existing per-caller namespace.
- ADR 041 — Pipelines as a first-class resource. PR #1 extends the Pipeline struct.
- ADR 046 — Coding pipelines and agent-integration roadmap. The gap-analysis surface in PR #3 is the same shape as the coding-agent recommendations in ADR 046's "Future integrations" table — applied dynamically to user intent rather than just curated by maintainers.
