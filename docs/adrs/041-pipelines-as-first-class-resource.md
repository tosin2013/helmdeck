# 41. Pipelines as a First-Class Resource

**Status**: Proposed
**Date**: 2026-05-26
**Domain**: pack-engine, api-design, distributed-systems

## Context

helmdeck is a tool server: an agent calls packs one at a time and orchestrates any multi-step workflow itself, re-threading each pack's output and session id by hand on every run. Nothing in helmdeck remembers that "research → ground → slide deck" is a workflow the operator runs every week. The orchestration lives in the agent's prompt, not in the platform — so it can't be scheduled, triggered by a webhook, shared between agents, or replayed.

A **pipeline** — a stored, named, ordered sequence of pack steps — closes that gap. The strategic shift: helmdeck stops being only a place agents call out to, and becomes the persistent record of what agents do *and* the engine for what they should do next. Packs produce artifacts; pipelines sequence packs; agents create pipelines; the loop closes inside helmdeck with every step audited, every credential vaulted, every run reproducible.

The reality-check of the current engine (all confirmed):
- Packs run via `packs.Engine.Execute(ctx, *Pack, json.RawMessage) (*Result, error)`; `Result` carries `Output`, `SessionID`, `Artifacts`. Reusable verbatim in a loop.
- Cross-pack data flow today is manual: `repo.fetch` surfaces a `session_id` on `Result.SessionID`, and the *agent* must pass it back as `_session_id`. There is **no output-templating** mechanism.
- The MCP server already exposes non-pack tools (`pack.start/status/result`) by intercepting them in `tools/call` before the registry lookup — the seam for `helmdeck__pipeline-*` tools.
- The async job registry (`internal/mcp/jobs.go`), the SQLite migration mechanism (`internal/store`), the audit log, the GitHub webhook receiver (ADR 033), and the A2A agent card (ADR 026, live at `/.well-known/agent.json`) all exist and are reusable.

## Decision

Introduce **pipelines as a first-class, persisted resource**, addressable by every actor that can reach helmdeck's REST or MCP surface — user, OpenClaw/Gemini via MCP, GitHub webhook, or A2A agent.

A pipeline is pure data (unlike packs, which carry Go closures), so it lives in SQLite. Each step is `{id, pack, input}`; a step's input may reference an earlier step's output via `${{ steps.<id>.output.<path> }}` or a run input via `${{ inputs.<name> }}`. A sequential **runner** executes steps by reusing `Engine.Execute`, resolving templates, threading `Result.SessionID` forward as `_session_id`, and recording a run history. Runs are async (chains are long-running): start returns a `run_id`, status is polled.

### Resource model (the contract)

| Method + path | Actor | Purpose |
| :--- | :--- | :--- |
| `GET /api/v1/pipelines` | user, agent | list |
| `POST /api/v1/pipelines` | user, agent, integration | create |
| `GET/PUT/DELETE /api/v1/pipelines/{id}` | user, agent | read / update / delete |
| `POST /api/v1/pipelines/{id}/run` | user, agent, webhook, cron | trigger (async) → `run_id` |
| `GET /api/v1/pipelines/{id}/runs[/{runId}]` | user, agent | run history / poll status |

MCP tools auto-derived from this surface — `helmdeck__pipeline-{list,get,create,run,run-status}` — appear in `tools/list` for every connected agent, intercepted in `tools/call` exactly like the async wrapper tools.

### Templating discipline (normative)

Resolution operates on the **decoded** input tree, not raw text: a string that is exactly one reference takes the referent's native JSON type; an embedded reference is string-coerced and spliced; the result is re-marshaled via `encoding/json`. Resolution is **single-pass** — a resolved value is never re-scanned — so a resolved value can neither break out of its JSON position (escaping) nor trigger second-order template injection. An unresolved reference is a **loud failure** (`RefError` → the step fails), never a silent empty.

### Built-in starter pipelines

Ship a curated set auto-seeded at startup (idempotent `builtin.*` upsert), runnable out of the box — e.g. `content.ground → slides.render` (grounded deck), `content.ground → blog.publish` (grounded blog), `research.deep → {slides,podcast,blog}`, `web.scrape → content.ground → blog.publish`, and `repo.fetch → {slides.narrate, podcast.generate}` (clone a repo → media about it). A starter whose packs aren't registered (e.g. a vision pack with no gateway) is skip-and-logged, so startup never fails. Provider-dependent starters degrade gracefully (stable premade ElevenLabs voice + `allow_silent_output`); discovery of valid voice/model ids for *authoring* pipelines rides the existing `helmdeck://voices` (#143) and `helmdeck://image-models` (#158) resources.

### Sequencing

| Release | Ships |
| :--- | :--- |
| **v0.15.0 (this ADR's slice)** | REST CRUD + run + history; the runner + dot-notation templating + session threading; `helmdeck__pipeline-*` MCP tools; ~13 built-in starters; SQLite persistence; **the Management UI `/pipelines` panel** (list / run / live status — pulled forward from v1.2 so operators can watch agent-built pipelines). |
| v1.0 | cron + webhook triggers (the runner is HTTP-decoupled so they reuse it). |
| v1.1 | A2A skill exposure of pipeline management. |
| v1.3 | "Promote a successful run from the audit log into a pipeline." |

## Consequences

**Positive:**
- One consistent resource that user, agent, webhook, and A2A orchestrator all create/run/inspect — the platform, not the prompt, owns the workflow.
- Reuses `Engine.Execute`, the SQLite/migration mechanism, the async-tool interception, the audit log — minimal new surface, no new Go dependency.
- Out-of-the-box starters make the feature immediately useful (the grounded-deck/blog chains the operator already wanted).
- The runner is HTTP-decoupled, so cron/webhook/A2A triggers slot in later without touching execution.

**Negative:**
- Templating is a new evaluator; bounded (single-pass, depth-capped, escaped) but a new correctness surface — heavily unit-tested.
- A run failing mid-pipeline leaves earlier artifacts (acceptable; TTL-bounded) — no compensation/rollback in the first slice.
- Session-sharing chains depend on the upstream pack preserving its session within the watchdog window; the first starter set is mostly session-independent content chains.
- Built-ins are read-only (`409` on PUT/DELETE) — operators clone-then-edit; a deliberate v0.15.0 simplification.

## Related PRD Sections

§6.6 Capability Packs, §19.7 Agent Memory and Session Persistence.

Related ADRs: [ADR 033](033-github-webhook-listener.md) (the v1.0 webhook trigger reuses the same runner), [ADR 026](026-a2a-agent-card-endpoint.md) (A2A pipeline management, v1.1), [ADR 032](032-artifact-explorer-and-inline-images.md) (run outputs are artifacts), [ADR 039](039-universal-memory-delivery-layer.md) (the engine seam pipelines execute through).
