---
title: Configure helmdeck via the Management UI
description: Panel-by-panel walkthrough of the post-install configuration steps every operator needs — AI providers, vault credentials, MCP client connect, sessions, audit log.
keywords: [helmdeck, management UI, configuration, AI providers, credential vault, MCP client, JWT]
sidebar_position: 3
priority: 0.8
changefreq: monthly
---

# Configure helmdeck via the Management UI

You ran `make install` and you're staring at the Dashboard. This page is the panel-by-panel tour of what to configure, in roughly the order you'll need each piece.

If you haven't installed yet, start with the [CLI install walkthrough](./install-cli.md). For the env-var side of configuration (the optional toggles operators need to enable for overlay-gated packs like `web.scrape` / `doc.parse` / `python.run` / `node.run`), see **[Anatomy of `.env.local`](./install-cli.md#anatomy-of-envlocal)** in the CLI tutorial.

## What ships UI-only vs CLI-only

| Configuration | UI | CLI / env var |
|---|---|---|
| Admin password | Read-only today (rotate via `--reset`) | `HELMDECK_ADMIN_PASSWORD` in `.env.local` |
| AI provider keys | **UI-only** — Add via *AI Providers* panel | not exposed |
| Vault credentials | UI (Vault panel) **or** install.sh interactive PAT prompt | `scripts/install.sh` for GitHub PAT only |
| MCP client config | UI emits the snippet (Connect Clients panel); your client's config file is the consumer | `gh helmdeck connect <client>` (if installed) |
| JWT signing key | not in UI | `HELMDECK_JWT_SECRET` in `.env.local` (restart to rotate) |
| Vault encryption key | not in UI | `HELMDECK_VAULT_KEY` in `.env.local` (re-encrypt before rotation) |

The takeaway: **keys and secrets stay in `.env.local`**, **operational config (provider keys, vault credentials, MCP servers) lives in the UI**.

## Panel-by-panel walkthrough

### 1. Dashboard

Lands you here after login. Top cards show: active sessions, recent pack invocations, control-plane memory, audit-log tail. If any of these read zero or "stale", come back after running `make smoke` to see live numbers.

### 2. AI Providers (`/admin/ai-providers`) — **start here**

Until you add at least one provider key, the AI gateway is offline and `/v1/chat/completions` returns 401. Click **Add Provider** and pick one of:

- **Anthropic** — paste your `sk-ant-...` key. Validate with the *Test Connection* button.
- **OpenAI** — paste your `sk-...` key.
- **Gemini** — Google AI Studio API key.
- **Ollama** — point at your `OLLAMA_HOST` (no key needed).
- **Deepseek**, **Groq**, **Mistral** — same pattern; per-provider notes shown in the modal.

Keys are encrypted with `HELMDECK_KEYSTORE_KEY` and stored in SQLite. You won't see a full key after save — only a `sk-***ed_suffix` for verification.

### 3. Browser Sessions (`/admin/sessions`)

Read-only list of active and recent sessions. Click **New Session** to spin up a sidecar — once it's ready, the noVNC tile lets you watch what the agent sees. Useful for debugging vision packs and confirming the sidecar image is healthy.

### 4. MCP Registry (`/admin/mcp`)

Lists every MCP server helmdeck knows about — both auto-discovered (the built-in pack server, Playwright MCP if `HELMDECK_PLAYWRIGHT_MCP_ENABLED` is true) and operator-added. Click **Add Server** to register a third-party MCP server with stdio/SSE/WebSocket transport.

### 5. Capability Packs (`/admin/packs`)

The **read-only catalog** of all 36 packs grouped by namespace (browser, web, repo, github, slides, doc, desktop, vision, fs, shell, http, research, language). Click any pack to see its schema. There is no in-UI execution today (Test Runner is [tracked as T606a](/TASKS#phase-6--management-ui-weeks-1720)) — for now, drive packs from your MCP client or a `curl`.

### 6. Credential Vault (`/admin/vault`)

The vault is where pack-facing credentials live, encrypted at rest with `HELMDECK_VAULT_KEY` and accessed via `${vault:NAME}` placeholders the model never sees in cleartext. The two most-needed credentials for fresh installs:

#### Add a GitHub PAT (`github-token`)

Required for: private-repo `repo.fetch` / `repo.push`, every pack in the `github.*` family.

> If you ran `scripts/install.sh` interactively and answered the GitHub PAT prompt, this is already done — skip this section. Verify by visiting *Vault → Credentials* and looking for the `github-token` row.

1. Create the token at <https://github.com/settings/tokens/new>:
   - **Note**: `helmdeck` (or your install hostname).
   - **Expiration**: pick what your security policy allows. 90 days is a reasonable default; the install.sh prompt also accepts no-expiration tokens but those are harder to rotate.
   - **Scopes** (Classic PAT) or **Repository access** (Fine-grained):
     - Classic: `repo` (full) — covers `repo.fetch`/`repo.push` + the `github.*` REST surface.
     - Fine-grained: pick the specific repos you want the agent to reach; under *Repository permissions*, set **Contents: Read and write**, **Issues: Read and write**, **Pull requests: Read and write**, **Metadata: Read-only** (auto-required), and **Webhooks: Read and write** if you'll use `github.create_release` to fire deploy hooks.
   - Generate, copy the token (it's shown once).

2. Open *Vault* → *Add Credential*:
   - **Name**: `github-token` (the exact name; the pack handlers look for this string).
   - **Type**: `api_key`.
   - **Host pattern**: `api.github.com` (and add `github.com` separately if you also use SSH-style remotes — but with HTTPS PAT auth, the hostname above is enough).
   - **Value**: paste the PAT.
   - **ACL**: leave default (`*` actor) for a single-operator install, or scope to a specific JWT subject if you mint per-client tokens.
   - Save.

3. Verify with a public-repo `repo.fetch` (no token needed) followed by a private one:
   ```bash
   JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
     -H 'Content-Type: application/json' \
     -d "{\"username\":\"admin\",\"password\":\"$(grep ADMIN_PASSWORD deploy/compose/.env.local | cut -d= -f2)\"}" \
     | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

   # Public repo — should always work
   curl -fsS -X POST http://localhost:3000/api/v1/packs/repo.fetch \
     -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
     -d '{"url":"https://github.com/tosin2013/helmdeck.git"}'

   # Private repo — needs the PAT
   curl -fsS -X POST http://localhost:3000/api/v1/packs/repo.fetch \
     -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
     -d '{"url":"https://github.com/your-org/private-repo.git","credential":"github-token"}'
   ```
   The second call returns `repo.fetch.output.clone_path` only if the PAT is in the vault and granted to your actor. If it returns `invalid_input: unknown placeholder` or `denied`, re-check the credential name and ACL.

#### Add an ElevenLabs key (`elevenlabs-key`)

Required for: `slides.narrate` full-TTS path. Without it, `slides.narrate` ships **silent video** — the deck still renders and ffmpeg still produces an MP4, but with no per-slide narration. Useful as a fallback but not what you want for a real demo.

1. Create the API key at <https://elevenlabs.io/app/settings/api-keys>:
   - Free tier gives 10,000 characters / month — enough for ~10 minutes of narration; plenty to validate `slides.narrate` end-to-end.
   - Copy the key (starts with `sk_…`).

2. Open *Vault* → *Add Credential*:
   - **Name**: `elevenlabs-key` (exact name).
   - **Type**: `api_key`.
   - **Host pattern**: `api.elevenlabs.io`.
   - **Value**: paste the key.
   - **ACL**: same default-or-scope choice as above.
   - Save.

3. Verify with a tiny narrated deck. This needs an AI provider key (any of Anthropic/OpenAI/Gemini) for the speaker-notes generation step, so add one in the *AI Providers* panel first if you haven't:
   ```bash
   curl -fsS -X POST http://localhost:3000/api/v1/packs/slides.narrate \
     -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
     -d '{
       "markdown": "---\nmarp: true\n---\n# Hello helmdeck\n<!-- speaker:notes Welcome to a quick demo. -->\n\n---\n\n# Thanks for listening\n<!-- speaker:notes See you next time. -->",
       "voice_id": "EXAVITQu4vr4xnSDxMaL",
       "model_id": "eleven_turbo_v2_5"
     }'
   ```
   The response includes a `video_artifact_key` you can fetch from the artifact store. Look for `voice_used` in the output: when the vault credential is missing, that field is empty and the video is silent.

#### Other vault entries

- **Per-host login cookies / session cookies** — for `web.scrape_spa` against authenticated pages. Type: `cookie`. Host pattern: the target site.
- **Custom API keys** — anything an agent will inject as `${vault:NAME}` in a `http.fetch` request. Type: `api_key`. Host pattern: the target API hostname.

For every credential you add, scope its ACL — by default, all packs invoked by the admin actor can use it. Tighten scopes if you give other actors JWTs.

### 7. Connect Clients (`/admin/connect`)

For each MCP-capable client (Claude Code, Claude Desktop, OpenClaw, Gemini CLI, Hermes Agent), a card shows a one-liner snippet with auto-detected OS conventions. Copy → paste into your client config → restart the client → 36 helmdeck packs become available as MCP tools.

The snippet mints a fresh JWT scoped to the client subject. It's the single piece of config you give your client; the client never sees the underlying credentials in the vault.

### 8. Security Policies (`/admin/security`)

Read-only snapshot of the currently-enforced security policy:

- Network egress allowlist (host patterns).
- Sandbox baseline (non-root UID, capability drops, seccomp profile).
- Auth model (JWT issuer, scopes).
- Telemetry sinks (OTLP endpoints).

Edit + reload-config is [tracked as T609a](/TASKS#phase-6--management-ui-weeks-1720). Until then, change `HELMDECK_*` env vars in `.env.local` and restart with `docker compose -f deploy/compose/compose.yaml restart control-plane`.

### 9. Audit Logs (`/admin/audit`)

Every API call. Filter by event type (`pack.invoke`, `vault.read`, `gateway.dispatch`, …), severity, actor, time window. Click a row for the redacted JSON payload.

This is your reach-for-it page when something goes wrong: the audit log records what the agent attempted, with which credentials, against which target.

### 10. Artifact Explorer (`/artifacts`)

Lists pack output artifacts (screenshots, PDFs, videos, scrape results). Inline image preview, download button, filter by pack and date range. Useful when an MCP client returns just an artifact key — paste the key here to see the contents.

## Mint a JWT for your MCP client (the most-asked operation)

The Connect Clients panel emits a one-liner per client, but if you want it without the UI:

```bash
ADMIN_PW=$(grep HELMDECK_ADMIN_PASSWORD deploy/compose/.env.local | cut -d= -f2)

JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

echo "$JWT"
```

Plug `$JWT` into your MCP client config's `Authorization: Bearer ...` header and you're connected.

## Now do something with it

The fastest way to confirm everything works is the **[Pack demo playbook](../integrations/pack-demo-playbook.md)** — 20 copy-pasteable prompts that exercise every pack against your fresh install. If the playbook is green, you have a working agent platform.
