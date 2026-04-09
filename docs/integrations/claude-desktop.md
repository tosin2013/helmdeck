# Claude Desktop

> **Status:** 🟡 Documented, not yet verified end-to-end
> Promote to ✅ once a maintainer has walked the Phase 5.5 loop with this client.

## Topology

Claude Desktop is **Topology B** — runs on the user's macOS or Windows machine alongside a local helmdeck stack. Linux is **not supported** by Claude Desktop. Connection is **stdio bridge only** — Claude Desktop's documented `mcpServers` config takes a `command` + `args` + `env` triple. URL-based MCP transports are not part of the local-server schema in the official quickstart.

## Prerequisites

- macOS or Windows
- A running helmdeck stack on the same machine (or reachable via a forwarded port)
- Claude Desktop installed
- Helmdeck JWT from the **API Tokens** panel
- For the Phase 5.5 walkthrough: a private GitHub repo + an `ssh-git` credential in the helmdeck Vault

## 1. Install Claude Desktop

Download from <https://claude.ai/download>. macOS `.dmg` and Windows installer.

Source: <https://modelcontextprotocol.io/quickstart/user>

## 2. Install the helmdeck-mcp bridge

```bash
# macOS / Linux
brew install tosin2013/helmdeck/helmdeck-mcp

# Windows (Scoop)
scoop bucket add helmdeck https://github.com/tosin2013/scoop-helmdeck
scoop install helmdeck-mcp
```

Verify: `helmdeck-mcp --version`.

## 3. Configure Claude Desktop

Edit the Claude Desktop config file:

- **macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows:** `%APPDATA%\Claude\claude_desktop_config.json`

Add a `mcpServers` entry:

```json
{
  "mcpServers": {
    "helmdeck": {
      "command": "helmdeck-mcp",
      "env": {
        "HELMDECK_URL": "http://localhost:3000",
        "HELMDECK_TOKEN": "<your-jwt>"
      }
    }
  }
}
```

Restart Claude Desktop (fully quit + relaunch — the file is read at startup). The hammer icon in the message composer should show the helmdeck packs.

## 4. (LLM gateway) — not supported

Claude Desktop's docs do not document any way to point it at a custom Anthropic-compatible (or OpenAI-compatible) base URL. Helmdeck-as-LLM-gateway is **not applicable** to Claude Desktop. Claude Desktop will always call api.anthropic.com directly, and helmdeck will only see the MCP tool calls (not the LLM dispatches). This is a Claude Desktop limitation, not a helmdeck one.

## 5. Walk the Phase 5.5 code-edit loop

Prompt Claude Desktop:

> Use the helmdeck packs to:
> 1. `repo.fetch` `git@github.com:<me>/<fixture-repo>.git` using vault credential `gh-deploy-key`.
> 2. `fs.list` the clone for `*.md` files.
> 3. `fs.read` the README and propose a one-line edit.
> 4. `fs.patch` to apply the edit.
> 5. `cmd.run` `go test ./...` in the clone.
> 6. `git.commit` with message `chore: helmdeck integration smoke`.
> 7. `repo.push` back to `origin`.

**Pass criteria:**

- The new commit lands on the remote branch.
- The Audit Logs panel in the helmdeck UI shows one row per pack call.
- The SSH private key never appears in the Claude Desktop chat — only the placeholder.

## Troubleshooting

- **Helmdeck doesn't appear in the hammer icon menu** — file was malformed (Claude Desktop silently ignores invalid JSON). Run it through `python -m json.tool < claude_desktop_config.json` and look for the parse error.
- **`helmdeck-mcp: command not found`** — bridge is not on the `PATH` of the Claude Desktop process. macOS GUI apps don't inherit shell `PATH`; pin the absolute path: `"command": "/opt/homebrew/bin/helmdeck-mcp"`.
- **Pack calls hang** — helmdeck control plane unreachable. Confirm `curl http://localhost:3000/healthz` returns `{"status":"ok"}`.
