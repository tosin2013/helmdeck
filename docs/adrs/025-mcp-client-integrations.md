---
description: "ADR-025: First-Class MCP Client Integrations: Claude Code, Claude Desktop, OpenClaw, Gemini CLI — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 25. First-Class MCP Client Integrations: Claude Code, Claude Desktop, OpenClaw, Gemini CLI

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design, distributed-systems

## Context
Helmdeck's product surface — Capability Packs (ADR 003) — is only valuable if popular agent runtimes can call it with zero or near-zero integration code. The four most relevant consumer surfaces today are Anthropic's **Claude Code** (CLI agent), **Claude Desktop** (GUI), Tosin Akinosho's **OpenClaw** (self-hosted multi-channel agent gateway), and Google's **Gemini CLI** (terminal agent). All four support MCP servers as their primary tool-extension mechanism, but each has its own configuration format, transport quirks, and packaging conventions.

## Decision
Treat each of these four as a first-class integration target with: (a) a tested configuration recipe in the docs, (b) a one-command install path emitted from the Management UI, and (c) ongoing CI smoke tests that verify a representative pack call works end-to-end on each client.

### Integration recipes

| Client | Transport | Configuration | Install path |
| :--- | :--- | :--- | :--- |
| **Claude Code** | stdio (preferred) or HTTP | `~/.claude/mcp.json` or `claude mcp add helmdeck --transport stdio --command "helmdeck-mcp" --env HELMDECK_URL=... --env HELMDECK_TOKEN=..." | UI → "Connect Claude Code" copies command to clipboard |
| **Claude Desktop** | stdio | `claude_desktop_config.json` `mcpServers.helmdeck` block pointing at the `helmdeck-mcp` bridge binary | UI → "Connect Claude Desktop" downloads pre-filled config snippet |
| **OpenClaw** | stdio or SSE | `openclaw mcp set helmdeck --command "helmdeck-mcp" --env ...` registry entry; Pi agent gains all packs as native tools | UI → "Connect OpenClaw" emits the exact `openclaw mcp set` command |
| **Gemini CLI** | stdio | `~/.gemini/settings.json` `mcpServers.helmdeck` block; Gemini CLI auto-loads MCP tools at session start | UI → "Connect Gemini CLI" emits the JSON snippet |

### Shared bridge binary
Ship a small Go binary, `helmdeck-mcp`, that all four clients spawn over stdio. It accepts `HELMDECK_URL` and `HELMDECK_TOKEN` from the environment, opens a single MCP session against the platform's WebSocket MCP endpoint, and proxies every JSON-RPC frame. This means:
- One bridge implementation, four supported clients.
- The platform's MCP server only needs to speak its native transport (WebSocket); the stdio wrapper is local to the agent host.
- Clients see all installed Capability Packs as typed MCP tools immediately on registration, with zero per-client tool wrapping.

### Versioning and compatibility
The bridge advertises a semver-pinned MCP protocol version. Pack tool schemas are versioned (ADR 024); the bridge always exposes the latest stable version unless `HELMDECK_PACK_PIN` is set. When the platform deprecates a pack version, the bridge logs a deprecation warning to the client at session start so users see it in their normal agent UI.

### Authentication
Each client gets its own JWT issued from the Management UI's "API Tokens" panel, scoped to a specific user and (optionally) a subset of packs. Tokens carry the client name as a label so the audit log shows which client invoked which pack. Token rotation is a UI action that does not require restarting the client — the bridge re-reads `HELMDECK_TOKEN` on the next request.

### Beyond MCP — degraded modes
- **Claude Code** also supports custom slash commands; for users who prefer it, helmdeck ships a `/helmdeck` slash command package that wraps the most common packs as natural-language shortcuts.
- **OpenClaw skills** (Python tool scripts) can call the REST API directly when MCP is not preferred, using the same JWT.
- **Gemini CLI** users on environments without stdio can fall back to the platform's HTTP REST surface via Gemini CLI's `@tool` extension format.

## Consequences
**Positive:** day-one usability for the four most likely consumer surfaces; "register MCP server once → get every helmdeck pack as a typed tool" is the integration story; one bridge binary keeps maintenance bounded; per-client JWTs give clean audit attribution.
**Negative:** four CI smoke matrices to keep green; each client's MCP support is evolving and occasionally breaks compatibility; the bridge becomes a load-bearing component that must be released alongside the platform.

## Related PRD Sections
§6.5 MCP Server Management, §13 Agent Consumer Ecosystem, §13.2 OpenClaw Integration, §8.5 MCP Registry Panel

---

## §2026-04-18 revision — OpenClaw `2026.4.18` consumer-side regression (CLI-only)

### Context

Upgrade of the local OpenClaw checkout (`/root/openclaw`) from `2026.4.10` → `2026.4.18` surfaced a new-in-release regression that affects this ADR's "CI smoke tests" acceptance criterion for the OpenClaw client. The upstream header-case-collision bug tracked in issue #1 still requires the lowercase `authorization` workaround; **this revision documents a separate, additional regression** that appeared only after `#68195` landed upstream.

### What changed

The `openclaw agent` CLI invocation (the path we used for scripted validation) no longer loads bundled MCP tools. `systemPromptReport.tools.entries` in the agent's response contains only the 24 built-in OpenClaw tools (`read`, `edit`, `write`, `exec`, `process`, `browser`, `web_fetch`, …) with **zero** `helmdeck__*` entries. No `bundle-mcp:` warn is emitted even with `--log-level debug`. The `mcp.servers.helmdeck` config is intact, the SSE connection succeeds from inside the OpenClaw container, and `/api/v1/mcp/sse` on helmdeck logs the handshake — but no `tools/call` frame ever arrives because the filter drops the tools before the agent's decision step.

The **chat UI path** (`http://localhost:18789`) still sees the full catalog. Direct proof: Gateway agent listed all 36 `helmdeck__*` tools on 2026-04-18 when asked. So this is a CLI-runtime issue, not a transport-layer or config-layer issue.

Suspect commit (identified via `git log v2026.4.10..v2026.4.18 -- 'src/agents/**'`):

**`0e7a992d`** — `fix(agents): filter bundled tools through final policy (#68195)` (2026-04-17). The commit body describes progressive hardening passes: "harden authorization signals on final tool policy," "cross-checks caller-provided groupId against the session-derived group context … when they disagree, the caller groupId … are dropped," and crucially "fail-closed when the session key and spawnedBy encode no group context." CLI invocations don't carry a group context → filter fails closed → helmdeck's entire bundled catalog is silently dropped.

### Decision

Preserve the ADR's core policy (the four-client integration table is still correct) but amend two acceptance criteria:

1. **CI smoke tests for OpenClaw now target the chat-UI path**, not the CLI. The chat UI is the supported end-user surface anyway; the CLI was a convenience for automation. Until upstream unfilters bundled tools on no-group-context sessions, scripted end-to-end tests for OpenClaw will exercise the Gateway WebSocket path via `scripts/validate-openclaw.sh` (to be updated accordingly — a TODO on the validation script owner).

2. **Version compatibility is now pinned to a range**, not "latest." Known-good: OpenClaw `2026.4.10`–`2026.4.16`. Known-broken for CLI: `≥ 2026.4.17` (the release containing `#68195`). Chat UI remains functional on `2026.4.18` and presumably later.

### Additional downstream findings

During the same investigation we confirmed:

- **SKILLS.md `systemPromptOverride` survives `docker compose up -d --force-recreate openclaw-gateway`** as long as the config file is a bind-mount (our setup) — volume isolation doesn't wipe it. No action needed in helmdeck docs beyond the upgrade runbook.
- **Model selection matters more in `2026.4.18`**. `openrouter/auto` occasionally routes to a model that doesn't natively speak MCP's `tools/call` wire format and instead emits text like `print(helmdeck__repo_fetch(url=…))`. The model free-associates a Python-syntax wrapper instead of issuing the JSON message OpenClaw's harness expects. Result: no tool call reaches helmdeck, the agent hallucinates a plausible answer from its prior. **Mitigation**: pin `agents.defaults.model.primary` to a known-tool-capable model (any `us.anthropic.claude-sonnet-4-5-*` or `us.anthropic.claude-opus-4-5-*` from the Bedrock catalog visible in `openclaw capability model list`).
- **BOOTSTRAP.md-driven bootstrap loops can derail test prompts.** OpenClaw's default workspace seeds a `BOOTSTRAP.md` file that instructs the agent to complete an identity flow before answering normally. On a freshly-cloned workspace the agent can get stuck in a "BOOTSTRAP pending" preamble and never reach the actual user prompt. Workaround: either clear `workspace/BOOTSTRAP.md` or complete the IDENTITY/USER/SOUL seed before running scripted tests. Not strictly a helmdeck concern but documented in the upgrade runbook for operators running our smoke scripts.

### Consequences

**Positive:** the ADR's core premise — "register MCP server once, get every pack as a typed tool" — remains true for the chat UI path, which is the production surface. Users hitting the Control UI see the full 36-pack catalog on day one.

**Negative:** `scripts/validate-openclaw.sh` needs rework to drive the chat UI path instead of the CLI. Until that lands, end-to-end validation on OpenClaw is manual-only on `≥ 2026.4.17`. If upstream doesn't revert or relax the no-group-context filter, we may need to ship a configuration recipe that synthesizes a group context for the CLI session key.

### Related artifacts

- Upgrade runbook: [`docs/integrations/openclaw-upgrade-runbook.md`](../integrations/openclaw-upgrade-runbook.md)
- Integration doc status banner: [`docs/integrations/openclaw.md`](../integrations/openclaw.md)
- Earlier OpenClaw bug (header case-collision, still valid): issue #1 + [`openclaw-upstream-issue.md`](../integrations/openclaw-upstream-issue.md)
- Suspect upstream commit: `0e7a992d` in the OpenClaw repo (PR #68195)
