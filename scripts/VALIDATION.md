# Helmdeck Client Validation Scripts

Each MCP client integration gets its own validation script that exercises helmdeck's capability packs end-to-end through that client's MCP transport. The audit log is the truth source — LLM responses are unreliable (the model hallucinates failures and successes), so every test verifies the pack call landed in helmdeck's audit table.

## How it works

```
1. Mint a helmdeck JWT via POST /api/v1/auth/login
2. Register helmdeck as an MCP server in the client's config
3. For each test:
   a. Record the current timestamp
   b. Send a prompt via the client's agent CLI
   c. Query helmdeck's audit log for pack_call or mcp_call events since the timestamp
   d. Pass if ≥1 matching event found; fail if zero
4. Print summary: passed / failed / skipped
```

## Current scripts

| Script | Client | Transport | Tests |
| :--- | :--- | :--- | :--- |
| `validate-openclaw.sh` | OpenClaw | SSE (`/api/v1/mcp/sse`) | 9 |

## What `validate-openclaw.sh` tests

| # | Test name | Pack | What it proves |
| :--- | :--- | :--- | :--- |
| 1 | http.fetch GET example.com | `http.fetch` | Basic HTTP with custom headers |
| 2 | browser.screenshot_url example.com | `browser.screenshot_url` | CDP session spawn + Chrome screenshot |
| 3 | web.scrape_spa HN headlines | `web.scrape_spa` | CDP + CSS extraction from a JS-rendered page |
| 4 | slides.render deck | `slides.render` | Marp + Chromium → PDF artifact |
| 5 | browser.interact example.com | `browser.interact` | Multi-step: extract h1 + assert text + screenshot |
| 6 | github.list_prs openclaw | `github.list_prs` | GitHub REST API read (no PAT needed for public repos) |
| 7 | github.list_issues helmdeck | `github.list_issues` | GitHub REST API read |
| 8 | github.search SSE | `github.search` | GitHub code search API |
| 9 | repo.fetch + fs.list chain | `repo.fetch` + `fs.list` | HTTPS clone + session pinning via `_session_id` |

## What it intentionally skips

| Pack | Reason |
| :--- | :--- |
| `python.run` | Sidecar image may not be built (`make sidecar-python-build`) |
| `node.run` | Sidecar image may not be built (`make sidecar-node-build`) |
| `repo.push` | Write operation — needs a writable remote + vault credential |
| `github.create_issue` | Write operation — creates real issues on GitHub |
| `github.post_comment` | Write operation — posts real comments on GitHub |
| `github.create_release` | Write operation — creates real releases on GitHub |
| `doc.ocr` | Requires an image artifact in the session workspace |
| `desktop.run_app_and_screenshot` | Requires desktop-mode sidecar (`HELMDECK_MODE=desktop`) |
| `vision.click_anywhere` | Requires desktop session + vision model |
| `vision.extract_visible_text` | Requires desktop session + vision model |
| `vision.fill_form_by_label` | Requires desktop session + vision model |

## Usage

```bash
# Full run (mints JWT, rewrites MCP config, tests all 9 packs)
./scripts/validate-openclaw.sh

# Test one specific pack
./scripts/validate-openclaw.sh --pack browser.interact

# Skip JWT/MCP rewrite (faster for re-runs)
./scripts/validate-openclaw.sh --skip-mcp-rewrite

# Override helmdeck URL
HELMDECK_URL=http://my-host:3000 ./scripts/validate-openclaw.sh
```

## How to port to another client

The test prompts, audit log queries, and pass/fail logic are identical across all clients. Only two things change:

### 1. Agent invocation

Replace the `docker exec` call in `run_test()` with the client's equivalent:

| Client | Invocation |
| :--- | :--- |
| **OpenClaw** | `docker exec openclaw-gateway node /app/dist/index.js agent --message "$prompt" --to "+10000000001"` |
| **Claude Code** | `claude --headless --message "$prompt"` |
| **Hermes Agent** | `hermes "$prompt"` |
| **Gemini CLI** | `gemini --message "$prompt"` (if headless mode exists; otherwise manual) |

### 2. MCP server registration

Replace `write_helmdeck_mcp_server()` with the client's config-write method:

| Client | How to register helmdeck |
| :--- | :--- |
| **OpenClaw** | `openclaw mcp set helmdeck '{"url":"...","headers":{"authorization":"Bearer ..."}}'` |
| **Claude Code** | `claude mcp add helmdeck --transport http --url http://localhost:3000/api/v1/mcp/sse --header "Authorization: Bearer ..."` |
| **Hermes Agent** | Write `mcp_servers.helmdeck` section to `~/.hermes/config.yaml` |
| **Gemini CLI** | Write `mcpServers.helmdeck` to `~/.gemini/settings.json` with `httpUrl` field |

### 3. Everything else stays the same

- `mint_jwt()` — same (calls helmdeck's login endpoint)
- `audit_pack_calls_since()` — same (queries helmdeck's audit log)
- Test prompts — same (the packs are helmdeck's, not the client's)
- Pass/fail logic — same (audit log is the truth source)

### Template

```bash
cp scripts/validate-openclaw.sh scripts/validate-<client>.sh
# Edit: replace OPENCLAW_GATEWAY_CONTAINER, docker exec invocation,
# and write_helmdeck_mcp_server function. Keep everything else.
```

## Known issue: OpenClaw header case collision

When registering helmdeck with OpenClaw, use **lowercase** `authorization` as the header key:

```json
{"headers": {"authorization": "Bearer <jwt>"}}
```

Capital-A `Authorization` causes a case-collision bug in OpenClaw's `buildSseEventSourceFetch` that produces a malformed bearer header and a 401 from helmdeck. See `docs/integrations/openclaw.md` for details.
