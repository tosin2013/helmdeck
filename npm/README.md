# @helmdeck/mcp-bridge

Stdio MCP bridge for [helmdeck](https://github.com/tosin2013/helmdeck).
Connects Claude Code, Claude Desktop, OpenClaw, and Gemini CLI to a
helmdeck control plane via the [Model Context Protocol](https://modelcontextprotocol.io).

## Quick start

```sh
# One-shot, no install
HELMDECK_URL=https://helmdeck.example HELMDECK_TOKEN=... npx @helmdeck/mcp-bridge

# Or install globally
npm install -g @helmdeck/mcp-bridge
helmdeck-mcp
```

## Configuration

The bridge reads two environment variables:

| Variable | Description |
| --- | --- |
| `HELMDECK_URL` | Base URL of the helmdeck control plane (e.g. `https://helmdeck.example`) |
| `HELMDECK_TOKEN` | Bearer JWT issued from the Management UI's API Tokens panel |

## Client snippets

The control plane exposes ready-to-paste configuration snippets at
`GET /api/v1/connect/{client}` for each supported client. Example:

```sh
curl -H "Authorization: Bearer $HELMDECK_TOKEN" \
  "$HELMDECK_URL/api/v1/connect/claude-code"
```

Supported clients: `claude-code`, `claude-desktop`, `openclaw`, `gemini-cli`.

## How it works

`postinstall` downloads the platform-matching `helmdeck-mcp` binary
from the corresponding GitHub Release, verifies its SHA256 against
`checksums.txt`, and places it in `bin/`. Set
`HELMDECK_MCP_SKIP_DOWNLOAD=1` to skip the download (useful in CI
images that bake the binary in another way).

## License

Apache-2.0
