---
title: OpenClaw MCP integration
description: Wire OpenClaw (open-source Claude-compatible agent) to a self-hosted helmdeck stack via the SSE MCP transport. Install, JWT mint, gateway config, and known regressions on the CLI path.
keywords: [helmdeck, OpenClaw, MCP integration, SSE, JWT, Phase 5.5 code edit loop]
---

# OpenClaw

> **Status (chat UI path):** ✅ Verified on OpenClaw `2026.4.18` with helmdeck `1b91f6c`. Gateway chat UI at `http://localhost:18789` sees the full 36-pack catalog (prefixed `helmdeck__`) and SSE handshake succeeds.
> **Status (CLI path):** ⚠️ Regressed on OpenClaw `≥ 2026.4.18`. `openclaw agent --agent main …` does not load bundled MCP tools — only 24 built-in tools appear, no `helmdeck__*`. Suspect upstream commit: `0e7a992d` (`fix(agents): filter bundled tools through final policy`). Use the chat UI for end-to-end agent runs until upstream fixes the CLI path.
> **Last full end-to-end green:** 2026-04-10 with OpenClaw `2026.4.10` + helmdeck `v0.6.0`.
>
> **Validation history:** Originally validated via `scripts/validate-openclaw.sh` — 9 packs tested through OpenClaw → SSE MCP → helmdeck round trip with `openrouter/auto` as the LLM. Packs validated: `http.fetch`, `browser.screenshot_url`, `web.scrape_spa`, `slides.render`, `browser.interact`, `github.list_prs`, `github.list_issues`, `github.search`, `repo.fetch` + `fs.list` chain. Additionally validated via direct REST: full code-edit loop (`repo.fetch` → `fs.write` → `fs.patch` → `fs.read` → `cmd.run` → `git.commit` → `repo.push`) + all GitHub write packs (`create_issue`, `post_comment`, `create_release`) + `python.run` + `node.run`.
>
> **Upgrading OpenClaw?** Follow [`openclaw-upgrade-runbook.md`](./openclaw-upgrade-runbook.md) — covers the `git pull` sequence, JWT / SKILLS.md preservation checks, and the regression triage steps for the CLI-path issue above.

## Topology

OpenClaw is **Topology A** — both OpenClaw and helmdeck run as docker compose stacks on the same host, joined onto a shared bridge network so OpenClaw resolves `helmdeck-control-plane` by service-name DNS.

```
┌──── helmdeck_default network ─────────┐
│  helmdeck-control-plane:3000          │
│  ┌────────────────────────────┐       │
│  │ /api/v1/mcp/sse  (MCP)     │       │
│  │ /v1/chat/completions (LLM) │       │
│  └────────────────────────────┘       │
│            ▲                          │
│            │ HTTP, JWT-protected      │
│  openclaw-gateway:18789               │
└───────────────────────────────────────┘
```

## Prerequisites

- Docker + docker compose v2
- Helmdeck cloned at `/root/helmdeck` (or wherever)
- ≥ 4 GB RAM, ≥ 2 CPUs (the install script preflight enforces this)

## Setup at a glance

The full first-time wiring is six steps. Sections 1–6 below walk through them; this is the "are we sure we got everything?" cheat-sheet.

| # | Step | Where it lives |
|---|---|---|
| 1 | `make install` (helmdeck stack up) | `scripts/install.sh` |
| 2 | `git clone https://github.com/openclaw/openclaw.git ~/openclaw` + `./scripts/docker/setup.sh` (OpenClaw stack up) | OpenClaw repo |
| 3 | Mint a helmdeck JWT for OpenClaw to send as MCP `Authorization` | curl against `/api/v1/auth/login` |
| 4 | **Authenticate OpenClaw with an LLM provider** — interactive, one-time | `docker compose -f /root/openclaw/docker-compose.yml run --rm -it openclaw-cli models auth login --provider openrouter` (paste API key when prompted; **single line, `-it` required** — see note below) |
| 5 | Pin a tool-capable model + wire MCP config + install SKILL.md + JWT refresh + network bridge | `scripts/configure-openclaw.sh --model openrouter/<provider>/<model> --seed-identity` (helmdeck repo) |
| 6 | Walk the Phase 5.5 code-edit loop in the chat UI to confirm | http://localhost:18789 |

> ⚠️ **Steps 4 and 5 are separate.** `configure-openclaw.sh` (step 5) sets the model OpenClaw will use and wires every piece of helmdeck integration — but it can't paste an API key for you. Step 4 stores the OpenRouter / Bedrock / etc. API key under `~/.openclaw/`; step 5 pins which model OpenClaw asks that provider for. Helmdeck's `HELMDECK_OPENROUTER_API_KEY` in `.env.local` is **not** the same thing — that wires helmdeck's *own* gateway, not OpenClaw's chat-UI loop. The `configure-openclaw.sh` script now probes for the missing provider auth and fails fast with the exact command to run if step 4 was skipped.

> 💡 **Copy-paste step 4 as a single line, with `-it`** to avoid the trailing-whitespace-after-`\` shell trap *and* the no-TTY error (`Error: models auth login requires an interactive TTY`):
>
> ```
> docker compose -f /root/openclaw/docker-compose.yml run --rm -it openclaw-cli models auth login --provider openrouter
> ```
>
> Verify auth landed with:
>
> ```
> docker compose -f /root/openclaw/docker-compose.yml run --rm -T openclaw-cli models auth list
> ```
>
> You should see your provider's profile in the *Profiles* block (e.g. `openrouter`). If it shows `Profiles: (none)`, the auth wizard didn't write — re-run the login command and watch for any TUI errors. The `-T` on the verify command suppresses the TUI (read-only listing doesn't need a TTY).
>
> ⚠️ **OpenClaw 2026.5.6 API change**: the `login` subcommand now takes `--provider <id>` as a flag rather than as a positional argument. Older docs (and the helmdeck `validate-openclaw.sh` script) still show the bare positional form — those are stale and need updates.

## 1. Install helmdeck

```bash
git clone https://github.com/tosin2013/helmdeck.git
cd helmdeck
./scripts/install.sh
```

The script generates a `.env.local` with strong random secrets, builds every binary + the React UI, brings the compose stack up, polls `/healthz`, and prints the admin password. Save it.

## 2. Install OpenClaw

```bash
git clone https://github.com/openclaw/openclaw.git
cd openclaw
OPENCLAW_GATEWAY_BIND=lan ./scripts/docker/setup.sh
```

This builds the OpenClaw image, runs the onboarding flow, and brings up `openclaw-gateway` on port `18789`. The setup script prints the gateway token at the end — save it.

OpenClaw's Control UI requires HTTPS or `localhost` (WebCrypto secure-context check). For remote access, the simplest path is an SSH tunnel from your workstation:

```bash
ssh -L 18789:localhost:18789 -L 3000:localhost:3000 root@<server>
```

Then open `http://localhost:18789` and `http://localhost:3000` in your browser — both are now treated as secure-context localhost.

## 3. Join the networks

Helmdeck ships an overlay file that merges OpenClaw's compose stack onto helmdeck's bridge network:

```bash
docker compose \
  -f /root/openclaw/docker-compose.yml \
  -f /root/helmdeck/deploy/compose/compose.openclaw-sidecar.yml \
  up -d openclaw-gateway
```

After this, `openclaw-gateway` can resolve `helmdeck-control-plane:3000` via DNS.

## 4. Configure helmdeck as an MCP server in OpenClaw

Two paths — pick whichever you prefer:

### 4a. Use the OpenClaw CLI (recommended — schema-validated)

```bash
docker compose -f /root/openclaw/docker-compose.yml run --rm openclaw-cli \
  mcp set helmdeck '{"url":"http://helmdeck-control-plane:3000/api/v1/mcp/sse","headers":{"authorization":"Bearer <your-helmdeck-jwt>"}}'
```

The CLI writes to `~/.openclaw/openclaw.json` and validates the shape against OpenClaw's config schema before saving — preferred over hand-editing because the schema occasionally shifts between OpenClaw releases.

### 4b. Edit `~/.openclaw/openclaw.json` directly (advanced)

OpenClaw stores MCP servers at the **top level** of the config under `mcp.servers`, keyed by server name (NOT under each agent):

```json
{
  "gateway": { "...": "..." },
  "agents":  { "...": "..." },
  "mcp": {
    "servers": {
      "helmdeck": {
        "url": "http://helmdeck-control-plane:3000/api/v1/mcp/sse",
        "headers": {
          "authorization": "Bearer <your-helmdeck-jwt>"
        }
      }
    }
  }
}
```

Then restart `openclaw-gateway` to pick up the change:

```bash
docker compose -f /root/openclaw/docker-compose.yml restart openclaw-gateway
```

### Mint the JWT

```bash
JWT=$(curl -s -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<from install.sh>"}' | jq -r .token)
echo "$JWT"
```

Paste the value into the `Authorization` header above.

## 5. Configure OpenClaw's LLM provider

OpenClaw needs its own LLM credentials. The easiest path is OpenRouter (which is also what helmdeck routes to in the validation walkthrough):

```bash
docker compose -f /root/openclaw/docker-compose.yml run --rm openclaw-cli \
  models auth login --provider openrouter
```

Follow the prompts to paste your OpenRouter API key. Then set the active model:

```bash
docker compose -f /root/openclaw/docker-compose.yml run --rm openclaw-cli \
  models use openrouter/minimax/minimax-m2.7
```

> **Helmdeck-as-LLM-gateway path:** OpenClaw's docs do not clearly document a custom OpenAI-compatible base URL escape hatch as of v0.6.0 of helmdeck. If we confirm via inspection of `models.json` that an arbitrary `base_url` works, this section will gain a "Route OpenClaw's LLM through helmdeck" subsection that points OpenClaw at `http://helmdeck-control-plane:3000/v1/chat/completions` so the T607 success-rate panel lights up from OpenClaw runs. Until then, OpenClaw uses its OpenRouter key directly and helmdeck only sees the MCP tool calls.

## 5b. Confirm the catalog is visible from OpenClaw

Before opening the chat UI, smoke-test the MCP handshake from the CLI side. This proves the JWT works, the `authorization` header case is right, the network bridge is wired, and the model can see the tool catalog:

```bash
docker compose -f /root/openclaw/docker-compose.yml run --rm -T openclaw-cli agent \
  --agent main \
  --json \
  --message "List every MCP tool whose name starts with helmdeck__. Just the names, one per line."
```

You should see the assistant reply listing **39 `helmdeck__*` tools** — the 36 capability packs plus 3 async wrappers (`helmdeck__pack-start`, `helmdeck__pack-status`, `helmdeck__pack-result`).

> 🧩 **Naming convention**: MCP tool names can't contain dots, so helmdeck's `browser.screenshot_url` becomes `helmdeck__browser-screenshot_url` over MCP. The mapping is mechanical — `<family>.<action>` → `helmdeck__<family>-<action>`. Pack reference pages list both forms.

If the response says "I don't have access to MCP tools" or returns 0 helmdeck tools:
- Check `docker logs helmdeck-control-plane | grep mcp/sse` — should show recent `GET /api/v1/mcp/sse` entries.
- Check the JWT in OpenClaw's MCP config didn't expire (default 7-day window from `configure-openclaw.sh`); rotate with `./scripts/configure-openclaw.sh --rotate-jwt`.
- Confirm the lowercase `authorization` header survived any manual edits to `~/.openclaw/openclaw.json` (issue #1 workaround — Pascal-cased `Authorization` 401's against OpenClaw 2026.4+).

## 5c. Load the agent skills

The helmdeck pack catalog, schemas, error-handling rules, session-chaining contract, and freshness contract live in [`SKILLS.md`](./SKILLS). The `configure-openclaw.sh` script you ran in §4 already stamped this file into `~/.openclaw/skills/helmdeck/SKILL.md` inside the OpenClaw gateway container, where the agent loads it automatically every turn.

**You don't have to do anything here on a first install.** This section is for the refresh case — after pulling a new helmdeck release that grew the catalog or updated the contracts, re-stamp the skill so OpenClaw sees the new content:

```bash
cd /path/to/helmdeck && ./scripts/configure-openclaw.sh
```

The script is idempotent — it only re-writes the stamped skill, leaving JWTs, network bridges, and MCP config untouched.

Verify by repeating the catalog smoke test from §5b — the response should include any newly-added pack names.

## 6. Walk the Phase 5.5 code-edit loop

Open `http://localhost:18789` in your browser, paste the OpenClaw gateway token into Settings, then send a chat prompt:

> Use the helmdeck packs to:
> 1. `repo.fetch` `git@github.com:<me>/<fixture-repo>.git` using vault credential `gh-deploy-key`.
> 2. `fs.list` the clone for `*.md` files.
> 3. `fs.read` the README and propose a one-line edit.
> 4. `fs.patch` to apply the edit (literal search-and-replace).
> 5. `cmd.run` `go test ./...` (or any project check) in the clone.
> 6. `git.commit` with message `chore: helmdeck integration smoke`.
> 7. `repo.push` back to `origin`.

**Pass criteria:**

- The new commit lands on the remote branch.
- The Audit Logs panel in the helmdeck UI (`http://localhost:3000`) shows one entry per pack call, in order.
- The SSH private key never appears in OpenClaw's chat transcript — only the `${vault:gh-deploy-key}` placeholder.

If all three hold, update the status banner at the top of this file to ✅ with today's date + the helmdeck version, and flip the matching row in [`README.md`](README.md).

## Known issue: header key MUST be lowercase `authorization`

> **Status:** Confirmed against OpenClaw 2026.4.10 + `@modelcontextprotocol/sdk@1.29.0` + `eventsource@3.0.7`. Filed upstream as a draft issue at [`docs/integrations/openclaw-upstream-issue.md`](openclaw-upstream-issue.md).

If you write the helmdeck MCP server config with capital-A `Authorization`:

```json
{ "url": "...", "headers": { "Authorization": "Bearer <jwt>" } }
```

…OpenClaw's `bundle-mcp` will fail to connect to helmdeck with:

```
[bundle-mcp] failed to start server "helmdeck" (.../api/v1/mcp/sse): Error: SSE error: Non-200 status code (401)
```

Helmdeck's audit log shows the request as `GET /api/v1/mcp/sse → 401`.

### Why

OpenClaw's `buildSseEventSourceFetch` (`/app/dist/content-blocks-k-DyCOGS.js`) merges the user's `headers` over the SDK's headers as a plain JS object via spread:

```js
return fetchWithUndici(url, {
    ...init,
    headers: { ...sdkHeaders, ...headers }   // sdkHeaders from Headers iteration → lowercase keys
});
```

The MCP SDK returns headers as a `Headers` instance, and iterating it yields **lowercase** keys per the spec — so `sdkHeaders` ends up with `authorization`. When the user config has `Authorization` (capital), the spread produces a plain object with **two distinct keys**:

```js
{ accept: "text/event-stream", authorization: "Bearer <jwt>", Authorization: "Bearer <jwt>" }
```

Undici then constructs a `Headers` list from that object using `append`, which **comma-joins** duplicates (per the Fetch spec) into:

```
Authorization: Bearer <jwt>, Bearer <jwt>
```

Helmdeck's bearer-token parser (and any standards-compliant parser) rejects this malformed header with 401.

### Workaround (until upstream fix)

Use **lowercase `authorization`** as the key in your OpenClaw helmdeck config:

```bash
docker compose -f /root/openclaw/docker-compose.yml run --rm openclaw-cli \
  mcp set helmdeck '{"url":"http://helmdeck-control-plane:3000/api/v1/mcp/sse","headers":{"authorization":"Bearer <jwt>"}}'
```

This makes OpenClaw's spread merge into a single `authorization` entry, which undici then sends as a single well-formed `Authorization` header.

### Upstream fix (proposed)

OpenClaw's `buildSseEventSourceFetch` should construct a `Headers` instance and use `.set()` (which is case-insensitive and replaces) instead of plain-object spread:

```js
function buildSseEventSourceFetch(headers) {
  return (url, init) => {
    const merged = new Headers(init?.headers ?? {});
    for (const [k, v] of Object.entries(headers)) merged.set(k, v);
    return fetchWithUndici(url, { ...init, headers: merged });
  };
}
```

This eliminates the case-collision regardless of how the user wrote the key.

## Webhook callback — push pack results to OpenClaw

Heavy packs (`slides.narrate`, `research.deep`, `content.ground`) automatically return a SEP-1686 task envelope so the JSON-RPC request never blocks. Most clients SHOULD just poll, but if you want **true push semantics** — the LLM's next turn fires when a pack completes, no polling at all — wire up the webhook receiver bundled in [`examples/webhook-openclaw/`](../../examples/webhook-openclaw/).

### Architecture

```
┌─────────────────┐    1. tools/call slides.narrate
│   OpenClaw LLM  ├─────────────────────────────────┐
└─────────────────┘                                 ▼
                                          ┌──────────────────┐
                                          │ helmdeck-control │
                                          │  -plane (MCP)    │
                                          └────────┬─────────┘
                  3. POST /helmdeck-callback       │
                     X-Helmdeck-Signature: sha256= │  (60-180s
                  ┌─────────────────────────────────┘   later)
                  ▼
         ┌─────────────────┐    4. POST /chat/inject
         │ helmdeck-callback ├──────────┐
         │  (Node, ~50 LOC)  │          ▼
         └─────────────────┘   ┌──────────────────┐
                               │ OpenClaw chat-   │
                               │ injection API    │
                               └──────────────────┘
                                        │
                       5. LLM sees: "[helmdeck] Pack
                          completed. Video: <url>"
```

Step 2 (helmdeck task envelope returns immediately) and step 3 (helmdeck POSTs the result when done) are independent — the LLM doesn't have to wait or poll.

### Setup

1. **Run the receiver** alongside helmdeck and OpenClaw:

   ```yaml
   # in your docker-compose override
   services:
     helmdeck-callback:
       build: ./examples/webhook-openclaw
       environment:
         OPENCLAW_INJECT_URL: http://openclaw-openclaw-gateway-1:3210/api/chat/inject
         WEBHOOK_SECRET: ${HELMDECK_WEBHOOK_SECRET}
       networks: [helmdeck_default, openclaw_default]
   ```

2. **Set the shared secret** in your `.env.local`:

   ```bash
   HELMDECK_WEBHOOK_SECRET=$(openssl rand -hex 32)
   ```

3. **Tell the LLM to use the webhook** in your prompt. SKILLS.md teaches the LLM the pattern; for an explicit prompt:

   ```
   Render this Marp deck as a narrated video. Use webhook_url=http://helmdeck-callback:8080/done
   and webhook_secret=<your-secret>. Don't poll — I'll get notified when it's ready.

   ---
   marp: true
   ---

   # My Deck
   <!-- Welcome to my deck. -->
   The future is now.
   ```

4. **What you'll see**: the LLM calls `slides.narrate` once, gets back "task started" (SEP-1686 envelope), and a few minutes later you see a fresh chat message:

   > **system:** [helmdeck] Pack `slides.narrate` completed. Video artifact: `slides.narrate/<key>/video.mp4` — open at `http://localhost:3000/artifacts/...`

   The LLM can then respond to that message in its normal turn cycle.

### Generic spec

For non-OpenClaw clients (custom A2A bridges, Slack bots, anything else) the same webhook fires with the same payload. See [`docs/integrations/webhooks.md`](./webhooks.md) for the wire contract.

## Troubleshooting

- **`origin not allowed (use HTTPS or localhost secure context)`** — OpenClaw's Control UI requires a secure context. Use the SSH tunnel from step 2, not the public IP.
- **OpenClaw can't reach `helmdeck-control-plane:3000`** — confirm the network overlay is applied: `docker network inspect helmdeck_default` should list `openclaw-gateway` as a member.
- **`401 unauthorized` on every tool call** — JWT expired or wrong scope. Mint a new one and update `~/.openclaw/openclaw.json`.
- **`tools/list` returns nothing** — check that the helmdeck Pack Registry is populated: `curl -H "Authorization: Bearer $JWT" http://localhost:3000/api/v1/packs` should list dozens of packs. If empty, the control plane hasn't registered the built-ins (check `docker compose logs control-plane`).

## References

- [OpenClaw MCP CLI docs](https://docs.openclaw.ai/cli/mcp)
- [OpenClaw Docker install](https://docs.openclaw.ai/install/docker)
- [Helmdeck MCP SSE transport (T302a)](../../internal/api/mcp_sse.go)
