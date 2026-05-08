---
title: Gemini CLI MCP integration
description: Wire Google's Gemini CLI to a self-hosted helmdeck stack via MCP. Install steps, JWT mint, and the Phase 5.5 code-edit loop walkthrough.
keywords: [helmdeck, Gemini CLI, Google, MCP integration, capability packs]
---

# Gemini CLI

> **Status:** 🟡 Documented, not yet verified end-to-end
> Promote to ✅ once a maintainer has walked the Phase 5.5 loop with this client.

## Topology

Gemini CLI is **Topology B** — runs on the user's host alongside a local helmdeck stack. Connection options:

- **streamable-http** via `httpUrl` (recommended, since T302a) — Gemini CLI connects directly to `http://localhost:3000/api/v1/mcp/sse` with no bridge subprocess.
- **stdio bridge** via `command` (universal fallback) — Gemini CLI spawns `helmdeck-mcp`.

Source: <https://github.com/google-gemini/gemini-cli/blob/main/docs/tools/mcp-server.md>

## Prerequisites

- Node.js ≥ 18
- A running helmdeck stack
- Gemini CLI installed
- A Gemini API key from <https://aistudio.google.com/apikey>
- Helmdeck JWT from the **API Tokens** panel

## 1. Install Gemini CLI

```bash
npm install -g @google/gemini-cli

# Or via Homebrew
brew install gemini-cli

# Or run without installing
npx @google/gemini-cli
```

Set the API key:

```bash
export GEMINI_API_KEY="<your-key>"
```

## 2. Configure helmdeck as an MCP server

Edit `~/.gemini/settings.json` and add an entry under `mcpServers`. Pick one of the two transports.

### 2a. Streamable HTTP (recommended)

```json
{
  "mcpServers": {
    "helmdeck": {
      "httpUrl": "http://localhost:3000/api/v1/mcp/sse",
      "headers": {
        "Authorization": "Bearer <your-jwt>"
      }
    }
  }
}
```

### 2b. Stdio bridge (fallback)

```bash
brew install tosin2013/helmdeck/helmdeck-mcp
```

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

Run `gemini` and the helmdeck packs appear in the tool catalog.

## 3. Load the agent skills

Gemini CLI auto-loads a `GEMINI.md` file from the working directory (project-scoped) or `~/.gemini/GEMINI.md` (global). Drop helmdeck's [`SKILLS.md`](./SKILLS) into one:

```bash
# Project-scoped — applies only when running gemini from this directory:
curl -fsSL https://raw.githubusercontent.com/tosin2013/helmdeck/main/docs/integrations/SKILLS.md \
  > GEMINI.md

# Or global — applies to every gemini invocation:
mkdir -p ~/.gemini
curl -fsSL https://raw.githubusercontent.com/tosin2013/helmdeck/main/docs/integrations/SKILLS.md \
  > ~/.gemini/GEMINI.md
```

Source: [Gemini CLI memory/context docs](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/memory.md).

Verify: run `gemini` and ask *"what helmdeck packs do you know about?"* — the model should list the full catalog (browser, web, repo, github, fs, cmd, git, http, doc, slides, vision, language). If it doesn't, check that the file is at the path Gemini CLI reads (`gemini --help` shows the precedence).

Refresh after pulling a new helmdeck release — the catalog grows over time.

## 4. (LLM gateway) — not supported

Gemini CLI is hard-wired to Google's Gemini API and Vertex AI. There is no documented escape hatch for an OpenAI-compatible base URL. Helmdeck-as-LLM-gateway is **not applicable** — Gemini CLI will always call Google directly, and helmdeck only sees the MCP tool calls.

## 5. Walk the Phase 5.5 code-edit loop

Prompt Gemini CLI:

> Use the helmdeck packs to:
> 1. `repo.fetch` `git@github.com:<me>/<fixture-repo>.git` using vault credential `gh-deploy-key`.
> 2. `fs.list` the clone for `*.md` files.
> 3. `fs.read` the README and propose a one-line edit.
> 4. `fs.patch` to apply the edit.
> 5. `cmd.run` `go test ./...` in the clone.
> 6. `git.commit` with message `chore: helmdeck integration smoke`.
> 7. `repo.push` back to `origin`.

**Pass criteria:**

- New commit lands on the remote branch.
- Helmdeck Audit Logs panel shows one entry per pack call.
- SSH private key never appears in the Gemini CLI session output.

## Troubleshooting

- **`tools/list` empty** — verify connectivity: `curl -H "Authorization: Bearer $JWT" http://localhost:3000/api/v1/mcp/sse` should return the SSE handshake frame within 1s.
- **`401 unauthorized`** — JWT expired or scoped wrong. Mint a new one.
- **Stdio path: `helmdeck-mcp: command not found`** — bridge not on `PATH`. Use the absolute path in the `command` field.
