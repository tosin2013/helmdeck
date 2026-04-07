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
