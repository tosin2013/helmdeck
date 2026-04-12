# Helmdeck Client Integrations

**Helmdeck is a capability sidecar.** You install it next to your MCP-capable client — same machine for desktop CLIs, same docker network for containerized agents — and the client gets a battery of safe, audited tools (browser automation, filesystem, git, vault, OCR, repo fetch, slides, …) without you wiring each one up by hand. Drop helmdeck in, point your client at it, ship.

This page is the index. Each per-client guide below walks through install, sidecar wiring, and an end-to-end Phase 5.5 code-edit loop.

## Agent context files

- **[SKILLS.md](SKILLS.md)** — Load this into your MCP client's system prompt or agent config. It teaches the LLM how to use all 35 packs, retry transient errors, chain sessions, and file bug reports when it finds real issues.
- **[pack-demo-playbook.md](pack-demo-playbook.md)** — 20 copy-pasteable prompts that exercise every pack. Use it to validate a new integration or demo helmdeck to your team.
- **`CLAUDE.md`** (repo root) — Auto-loaded by Claude Code; points at the files above.

## Sidecar topology

Two deployment shapes cover every supported client:

```
┌─────────────────────────── Topology A: containerized client ────────────────┐
│                                                                             │
│   docker-compose network (helmdeck_default)                                 │
│   ┌─────────────────────┐         ┌──────────────────────────────┐          │
│   │ openclaw-gateway    │  HTTP   │ helmdeck-control-plane       │          │
│   │ (or any agent in a  ├────────►│ /api/v1/mcp/sse              │          │
│   │  container)         │         │ /v1/chat/completions         │          │
│   └─────────────────────┘         └──────────────────────────────┘          │
│                                                                             │
│   The client points at helmdeck via service-name DNS. No bridge binary.     │
│   Wiring: `deploy/compose/compose.openclaw-sidecar.yml` (or equivalent)     │
│   merges helmdeck's bridge network into the client's compose stack.         │
└─────────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────── Topology B: host CLI ────────────────────────────┐
│                                                                             │
│   user's laptop                                                             │
│   ┌─────────────────────┐  stdio  ┌──────────────────────────────┐          │
│   │ Claude Code / CLI   ├────────►│ helmdeck-mcp (stdio bridge)  │          │
│   │ Claude Desktop      │         │            │                 │          │
│   │ Hermes Agent        │         │            ▼ HTTP            │          │
│   │ Gemini CLI          │         │   docker compose stack       │          │
│   └─────────────────────┘         │   helmdeck @ localhost:3000  │          │
│                                   └──────────────────────────────┘          │
│                                                                             │
│   helmdeck runs as a local docker compose stack on the same host.           │
│   The client spawns the helmdeck-mcp bridge, which forwards to localhost.   │
└─────────────────────────────────────────────────────────────────────────────┘
```

The shape you use depends on whether your client supports a URL-based MCP transport. The matrix below tells you which:

## Status legend

| Badge | Meaning |
| :--- | :--- |
| ✅ **Tested & integrated** | A maintainer has walked the full Phase 5.5 code-edit loop end-to-end against a real private GitHub repo with this client. Date + Helmdeck version recorded in the banner. |
| 🟡 **Documented, not yet verified** | Setup instructions are written and believed correct, but the Phase 5.5 loop has not been walked end-to-end with this client yet. |
| ⚪ **Planned** | Stub page exists; setup not yet documented. |

## Client matrix

| Client | Topology | MCP transport | LLM gateway | Status |
| :--- | :--- | :--- | :--- | :--- |
| [OpenClaw](openclaw.md) | A (container) | URL/SSE via `/api/v1/mcp/sse` (T302a) | OpenRouter (own key) — helmdeck route TBD | ✅ Verified (2026-04-10, 9 packs via MCP + 20 via REST) |
| [NemoClaw](nemoclaw.md) | A (container, NVIDIA sandbox) | inherits OpenClaw | inherits OpenClaw | 🟡 Documented |
| [Hermes Agent](hermes-agent.md) | B (host) | stdio bridge | ✅ via `base_url` field — helmdeck sees every chat completion | 🟡 Documented |
| [Claude Code](claude-code.md) | B (host) | stdio bridge **or** URL/SSE (T302a) | ⚠️ via `ANTHROPIC_BASE_URL` (needs `/v1/messages`, blocked on T201b) | 🟡 Documented |
| [Claude Desktop](claude-desktop.md) | B (host, mac/win only) | stdio bridge | ❌ not documented | 🟡 Documented |
| [Gemini CLI](gemini-cli.md) | B (host) | stdio bridge **or** URL/HTTP (T302a) | ❌ hard-wired to Gemini/Vertex | 🟡 Documented |

> When a client is promoted to ✅, update both its page banner **and** the row in this matrix. Keep them in sync.

## What's wired today vs deferred

- **MCP-as-server is fully wired today.** The control plane mounts both `/api/v1/mcp/ws` (T302) and `/api/v1/mcp/sse` (T302a) — every client speaks one or both. The legacy `helmdeck-mcp` stdio bridge still ships for desktop clients that don't speak HTTP MCP natively.
- **Helmdeck-as-LLM-gateway** (clients route their own chat completions through helmdeck so the success-rate panel lights up) currently works cleanly for **Hermes Agent only**. Claude Code support is blocked on **T201b** (add `/v1/messages` Anthropic-shape facade to the gateway). Claude Desktop and Gemini CLI are hard-wired to their vendor's API and cannot be redirected.

## Manual validation helper

`scripts/validate-clients.sh` (T564) boots the compose stack and prints the connect snippets + a copy-pasteable JSON-RPC scenario for the code-edit loop. Use it as scaffolding while walking a client through the loop by hand — there is no automated pass/fail.
