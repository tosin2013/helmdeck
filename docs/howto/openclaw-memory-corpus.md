---
description: "How helmdeck's per-caller memory layer wires into OpenClaw's `memory_search` tool as a corpus supplement. Covers wiring, failure modes, and the operator opt-out path."
---

# How the OpenClaw memory-corpus bridge works

ADR 048 PR #3 wires helmdeck's per-caller memory layer into OpenClaw's `memory_search` tool as a corpus supplement. After this lands, an OpenClaw agent asking "what was that thing about Konflux deploys" sees hits from both the user's own conversational memory **and** helmdeck's audit history + agent-written user facts — ranked together by OpenClaw's search pipeline.

This page explains the wiring, the failure modes, and the opt-out.

## What surfaces in `memory_search`

Two categories of helmdeck-side data appear as corpus chunks alongside the user's own memory:

| Category | What it is | Snippet format |
| --- | --- | --- |
| `pack_history` | Audit row for one pack execution (e.g. `blog.rewrite_for_audience`) | `## Pack call: <name>\n\nOutcome: ok\nInputs used:\n- persona: technical\n- audience: platform engineers` |
| `pipeline_history` | Audit row for one pipeline run | `## Pipeline run: builtin.brief-rewrite-blog\n\nOutcome: succeeded\nRun ID: ...` |
| `user_facts` (default) | Agent-written facts via `helmdeck.memory_store` | `## preferences/frontend-framework\n\nReact over Vue (Konflux project constraint)\n\n(category: user_facts)` |
| Any other agent-written category (`project_conventions`, `preferences`, etc) | Same shape as `user_facts` | Same |

Each chunk has a stable `docid`, a `collection` named `helmdeck-<category>`, and a substring/keyword score against the search query. **No embeddings inside helmdeck** — semantic recall comes from OpenClaw's embedding pipeline ranking these chunks alongside the user's own.

## The wire path

```
OpenClaw agent
   └── memory_search "konflux"
        └── QMD manager (extensions/memory-core/src/memory/qmd-manager.ts)
             └── MCPorter daemon (`mcporter call helmdeck.query`)
                  └── SSE → helmdeck-control-plane:3000/api/v1/mcp/qmd/sse
                       └── QMDServer.queryHandler
                            └── store.List(caller_jwt_subject, "")
                                 └── projectCorpus → [{docid, score, snippet, collection}]
```

The bridge is a separate MCP endpoint (`/api/v1/mcp/qmd/sse`) — not multiplexed onto the main `/api/v1/mcp/sse` — because MCPorter expects the tool name to be exactly `query` and the main PackServer uses dotted pack names. Keeping the QMD endpoint narrow also keeps the security review tractable.

## First-time setup

`scripts/install.sh` runs `scripts/openclaw-register-qmd.sh` automatically at the end of every install (when the OpenClaw container is present). If you're applying the openclaw-sidecar overlay manually — or upgrading from before ADR 048 PR #3 landed — run it yourself once:

```bash
scripts/openclaw-register-qmd.sh
```

The script reads the helmdeck JWT OpenClaw already stores at `/home/node/.openclaw/openclaw.json:mcp.servers.helmdeck.headers.authorization` (the same one used for the main `/api/v1/mcp/sse` connection) and writes a matching `mcporter` config entry inside the OpenClaw container. Idempotent — safe to re-run; updates the JWT each time so a rotated token doesn't break the bridge.

Why this isn't baked into the compose overlay: the helmdeck JWT is materialized only after OpenClaw boots and reads its server config. A compose-level init container would have to race that boot. Running this script AFTER the stack is healthy is simpler and survives token rotation.

## Verifying the bridge is on

After `compose.openclaw-sidecar.yml` is applied (default in `scripts/install.sh`):

```bash
# 1. Endpoint reachable from OpenClaw
docker exec openclaw-openclaw-gateway-1 sh -c \
  'curl -sN -H "Authorization: Bearer $OPENCLAW_GATEWAY_TOKEN" \
   http://helmdeck-control-plane:3000/api/v1/mcp/qmd/sse | head -2'
# Expect: event: endpoint ... data: /api/v1/mcp/qmd/sse/message?sessionId=...

# 2. MCPorter daemon picked it up
docker logs openclaw-openclaw-gateway-1 2>&1 | grep -i "mcporter\|helmdeck.query" | head

# 3. Smoke test from inside OpenClaw — store a fact, search for it
# (do this in the OpenClaw chat UI):
#   "Remember that I prefer React over Vue for Konflux projects."
#   ...later, in a new conversation:
#   "What do you remember about my frontend preferences?"
# Expect: agent recalls the React/Vue fact via memory_search.
```

## What happens when the bridge can't connect

- **Helmdeck down**: MCPorter logs `Tool 'helmdeck.query' not found` and `memory_search` returns only the user's own chunks. No agent-side error; degraded silently.
- **Helmdeck up but memory store disabled**: `/api/v1/mcp/qmd/sse` returns 503 with `qmd_unavailable`. MCPorter logs the failure and treats the corpus as empty.
- **Network partition (e.g. helmdeck container restart mid-query)**: MCPorter's per-request timeout fires; the agent's `memory_search` still returns whatever the local chunks turned up.

## Opting out

The bridge defaults to ON via the openclaw-sidecar compose overlay. To disable for a deployment, set `OPENCLAW_QMD_ENABLED=false` in your shell before `docker compose ... up -d`. The OpenClaw container then starts with `OPENCLAW_MEMORY_QMD_MCPORTER_ENABLED=false` and the MCPorter daemon doesn't dial helmdeck. The `/api/v1/mcp/qmd/sse` endpoint stays available; it's just unused.

## Auth model

The MCPorter daemon runs inside the OpenClaw container and dials helmdeck with the OpenClaw agent's gateway token. Helmdeck's JWT-subject auth gate runs on the QMD route the same way it runs on every `/api/v1/*` route — so the corpus chunks the bridge returns are scoped to whichever subject the OpenClaw token resolved to. Multi-tenant deployments where different agents have different tokens each see their own slice of helmdeck's memory; same caller-isolation guarantee the rest of the memory surface enforces.

## What the bridge does NOT do

- **No write path through the QMD endpoint.** The bridge is query-only. Agents that want to persist facts use `helmdeck.memory_store` (over the main `/api/v1/mcp/sse` endpoint or via `POST /api/v1/memory/store`), not the QMD route.
- **No cross-caller corpus mixing.** Even when two agents share an OpenClaw deployment with different JWT subjects, the corpus they each see is scoped to their own subject. There is no admin-level "show me everything" query.
- **No vault / credential leaks.** The corpus pulls only from memory categories. Vault secrets, pack cache rows (e.g. `content.ground`'s Firecrawl cache), and gateway provider keys are out of scope.

## See also

- [ADR 048 — Memory write surface + OpenClaw memory-corpus bridge](../adrs/048-memory-write-surface-openclaw-bridge.md)
- [How to configure OpenClaw memory (semantic recall)](openclaw-memory.md) — the embedding sidecar (PR #1) that makes OpenClaw's pipeline rank the QMD chunks semantically.
- [How agents persist and recall user facts](agent-facts.md) — the write half (PR #2).
- `internal/mcp/qmd_server.go` — the helmdeck-side query handler.
- `internal/api/mcp_qmd_sse.go` — the SSE transport.
