---
title: Register helmdeck with your MCP client
description: Wire helmdeck into any MCP-capable client (Claude Code, Claude Desktop, OpenClaw, Gemini CLI, Hermes Agent, Cursor) via the official MCP Registry-published metadata.
keywords: [helmdeck, MCP registry, server.json, mcp-publisher, Claude Code, Claude Desktop, OpenClaw, Gemini CLI, Cursor]
---

# Register helmdeck with your MCP client

Helmdeck is published to the [official MCP Registry](https://registry.modelcontextprotocol.io/) under `io.github.tosin2013/helmdeck`. Registry-aware clients can install + configure it from a single command; clients that don't speak the registry yet can still copy the stdio config snippet below verbatim.

This page covers the **install side**. For the operator-side stack (running the helmdeck control plane on your host), see [Install helmdeck via the CLI](../tutorials/install-cli.md).

## Prerequisites

You need a running helmdeck control plane and a JWT before any client can talk to it. If you don't have that yet, finish [`install-cli`](../tutorials/install-cli.md) first — the rest of this page assumes:

- Control-plane URL — typically `http://localhost:3000` for a local install
- A JWT bearer token — mint it from the **API Tokens** panel in the UI, or:
  ```bash
  curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
    -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"YOUR_PASSWORD"}' | jq -r .token
  ```

Keep both handy — every client wires them in as `HELMDECK_URL` + `HELMDECK_TOKEN` env vars.

## Stdio config snippet (universal)

Every client below wraps the same underlying command. The bridge binary `helmdeck-mcp` proxies stdio JSON-RPC to your control plane's HTTP API.

```jsonc
{
  "command": "npx",
  "args": ["-y", "@helmdeck/mcp-bridge"],
  "env": {
    "HELMDECK_URL": "http://localhost:3000",
    "HELMDECK_TOKEN": "<your JWT>"
  }
}
```

**Alternative install methods** (use whichever fits your environment):

| Method | Command / image |
|---|---|
| npm via npx (zero-install) | `npx -y @helmdeck/mcp-bridge` |
| Homebrew | `brew install tosin2013/helmdeck/helmdeck-mcp` |
| Scoop (Windows) | `scoop install helmdeck-mcp` |
| OCI image (containerized) | `docker run --rm -i --network host ghcr.io/tosin2013/helmdeck-mcp:0.10.0` |
| Go install | `go install github.com/tosin2013/helmdeck/cmd/helmdeck-mcp@latest` |
| GitHub Releases | Download a signed binary from [the releases page](https://github.com/tosin2013/helmdeck/releases) |

All install paths produce the same `helmdeck-mcp` binary — the registry metadata at `.mcp/server.json` declares both npm and OCI as canonical sources, and most registry-aware clients auto-pick npm.

## Per-client setup

| Client | Detailed guide | One-liner (if registry-aware) |
|---|---|---|
| **Claude Code** | [claude-code.md](../integrations/claude-code.md) | `claude mcp add helmdeck --command npx --args "-y @helmdeck/mcp-bridge" --env HELMDECK_URL=… --env HELMDECK_TOKEN=…` |
| **Claude Desktop** | [claude-desktop.md](../integrations/claude-desktop.md) | Edit `~/Library/Application Support/Claude/claude_desktop_config.json` — paste the stdio block above under `mcpServers.helmdeck` |
| **OpenClaw** | [openclaw.md](../integrations/openclaw.md) | `./scripts/configure-openclaw.sh` re-stamps the SKILL.md and registers the bridge |
| **Gemini CLI** | [gemini-cli.md](../integrations/gemini-cli.md) | Edit `~/.gemini/settings.json` — paste the stdio block under `mcpServers.helmdeck` |
| **Hermes Agent** | [hermes-agent.md](../integrations/hermes-agent.md) | Edit your Hermes config; same env-var contract |
| **Cursor** | (community-supported) | Edit `~/.cursor/mcp.json` — same shape as Claude Desktop |

The detailed per-client guides cover platform-specific gotchas (config file locations, restart requirements, OS-permission prompts). Start there if anything is unclear.

## First-run smoke test

After registering, verify end-to-end. Pick the simplest pack — a screenshot of `example.com` — and ask your agent to run it:

> Use helmdeck to take a screenshot of https://example.com — tell me the artifact key and the size in bytes.

Expected: a JSON response with `artifact_key: "browser.screenshot_url/<hash>-screenshot.png"` and `size_bytes` in the 5–20 KB range. If your client surfaces tool calls, you should see exactly one call to `browser.screenshot_url` with `url=https://example.com`.

If that works, helmdeck is wired correctly and all 38 packs are available.

## Troubleshooting

### "MCP server failed to connect"

Most common cause: bridge can't reach the control plane.

1. Confirm the control plane is up: `curl http://localhost:3000/healthz` should return `ok`.
2. If `HELMDECK_URL` points at `localhost` but the client runs in a sandbox/container (Claude Desktop on macOS sometimes does), try `http://host.docker.internal:3000` instead.
3. Restart the client after editing its config — most clients only re-read MCP server lists at launch.

### "401 unauthorized"

The JWT has expired or was minted against a different control plane.

```bash
# Re-mint and update your client config:
NEW_JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"YOUR_PASSWORD"}' | jq -r .token)
echo "$NEW_JWT"
```

Then paste it into the `HELMDECK_TOKEN` env var slot for whichever client you're using.

### "Tool not found: helmdeck__some-pack"

Different clients namespace MCP tools differently — Claude Code calls them `helmdeck__browser-screenshot_url`, OpenClaw via SKILL.md uses the same prefix, raw MCP exposes them as bare `browser.screenshot_url`. Use whatever your client is already used to; the underlying pack is the same.

If a tool you expect is genuinely missing, run the [pack-demo-playbook](../integrations/pack-demo-playbook.md) prompt for that pack — it'll surface whether the pack is registered server-side or not.

### Registry cache stale

If you upgraded helmdeck (say v0.10.0 → v0.10.1) and a registry-aware client is still showing the old version's pack list:

```bash
# Force-refresh the bridge to the latest npm version
npx -y @helmdeck/mcp-bridge@latest --version
```

Or pin the version explicitly in your client config: `"args": ["-y", "@helmdeck/mcp-bridge@0.10.1"]`.

## How registry publication works (for the curious)

The metadata at [`.mcp/server.json`](https://github.com/tosin2013/helmdeck/blob/main/.mcp/server.json) is the single source of truth. On every helmdeck release a maintainer runs:

```bash
./scripts/publish-to-mcp-registry.sh
```

That script: validates the JSON against the upstream schema, builds the `mcp-publisher` CLI from source, runs an interactive GitHub OAuth login, and publishes to `registry.modelcontextprotocol.io`. Downstream aggregators (mcp.so, Glama, PulseMCP) pull from there on a schedule. Namespace ownership is verified via GitHub — only the `tosin2013` GitHub owner can publish under `io.github.tosin2013/...`.

If you fork or repackage helmdeck, you'll need your own namespace (`io.github.<your-username>/...` for a GitHub fork, or `com.<yourdomain>/...` if you own a domain). See the [official publishing guide](https://modelcontextprotocol.io/registry/about) for the full flow.

## Related

- [Install helmdeck via the CLI](../tutorials/install-cli.md) — operator-side install (run this first)
- [Upgrade helmdeck](./upgrade-helmdeck.md) — version-bump procedure for an existing operator
- [Troubleshoot the install](./troubleshoot-install.md) — symptom-first table for known sharp edges
- [Pack reference](../reference/packs/) — all 38 packs with input/output schemas
