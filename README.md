# helmdeck

> Today's helmdeck install ran a full 6-step code-edit loop (clone, read, patch,
> test, commit, push) on `gpt-oss-120b` for **$0.07**. The same loop on Cursor
> or Claude Code direct via Sonnet would have cost **$0.30+**. Same outcome,
> ~5× cheaper — and the "expensive" stack isn't even the most expensive option.

| Workflow | Frontier-model approach | Helmdeck (gpt-oss-120b) |
|---|---|---|
| Browser scrape + GitHub comment | $0.25 (Anthropic Computer Use) | **$0.005** |
| Code edit loop (6 steps) | $0.35 (Cursor / Aider) | **$0.07** |
| Multi-step browser test | $0.20 (Browser-use NL) | **$0.03** |
| PDF → structured Markdown | $1.00 (naive Sonnet vision) | **$0.003** |

> Most browser agents require GPT-4o or Claude Sonnet to work reliably.
> Helmdeck is built for the other 99% of deployments — **local 7B models,
> air-gapped environments, and teams that can't send credentials to a
> cloud API.** It wraps every browser, desktop, git, and code action
> into a single typed JSON call that even a small model can fill in correctly.
> The numbers above are the consequence: when packs absorb the work the
> LLM would otherwise burn tokens rediscovering, cheap or local models do
> agentic work that frontier-model APIs charge 10× more for.

A self-hosted, containerized platform for AI agents, exposed as **Capability Packs** — schema-validated, one-shot JSON tools — and native MCP. The defining metric is **≥90% pack success on 7B–30B-class open-weight models**, something no frontier-targeting competitor is optimizing for.

> 📊 **Full per-task comparison** with reproduction recipe at <https://helmdeck.dev/explanation/why-helmdeck>. These are one maintainer's findings; we welcome [community reproductions](https://helmdeck.dev/blog).

## Why this exists

Smart models thrive on bash and a README. Weak models stall on open-ended interfaces. Helmdeck closes that gap by hiding browser sessions, desktop actions, credentials, and multi-step workflows behind single typed REST / MCP calls.

Three audiences specifically:

- **Self-hosted AI teams** who can't leave their VPC and need MCP-native infra that doesn't phone home.
- **The LocalLLaMA / Ollama crowd** running 7B–30B models — pack contracts keep small models reliable where open-ended tool surfaces fail.
- **Security-sensitive orgs** who need agents to log into SaaS apps without the model ever seeing a credential (vault-backed placeholder tokens + MCP-level audit).

## Status

**v0.10.0 shipped** — 38 capability packs (incl. blog.publish + podcast.generate), credential vault, repo packs,
code-edit loop, OpenTelemetry GenAI instrumentation, and the full
Management UI are all live. Phase 6.5 (MCP Server Hosting & Pack
Evolution) is complete; next milestone is **v1.0 — Kubernetes & GA**
(Phase 7), with backlog materialised as GitHub issues tagged
[`good first issue`](https://github.com/tosin2013/helmdeck/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)
and [`help wanted`](https://github.com/tosin2013/helmdeck/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22).

- **36 ADRs** in [`docs/adrs/`](docs/adrs/) — every architectural decision with PRD back-references
- **Task breakdown** in [`docs/TASKS.md`](docs/TASKS.md) — ~85 tasks across 8 phases with critical path
- **GitHub milestones** in [`docs/MILESTONES.md`](docs/MILESTONES.md) — drop-in issue checklists with current ship state
- **Pack reference** in [`docs/PACKS.md`](docs/PACKS.md) — every shipped pack's input/output contract

## Quick start

```sh
git clone https://github.com/tosin2013/helmdeck
cd helmdeck
./scripts/install.sh
```

That's it. The script runs preflight checks (`docker`, `node` ≥20, `go` ≥1.26, `make`, `openssl`, `curl`) with platform-aware install hints, generates fresh secrets into `deploy/compose/.env.local` (chmod 600), builds the Management UI bundle, the Go binaries, and the browser sidecar image, brings the Compose stack up, and prints the URL plus a freshly generated admin password.

```text
✓ helmdeck is up

  URL:       http://localhost:3000
  Username:  admin
  Password:  <generated; printed once — save it now>
```

Useful flags:

- `./scripts/install.sh --reset` — tear down, regenerate secrets, reinstall (new admin password)
- `./scripts/install.sh --no-build` — skip build steps, just bring the stack up
- `./scripts/install.sh --help` — full flag reference

Or via `make`: `make install`.

### Connect a client

A running stack is just the platform — the value is **packs called by an
agent**. Wire one of the supported MCP clients to your fresh install:

| Client | Status | Setup guide |
|---|---|---|
| **OpenClaw** | ✅ validated end-to-end | [`docs/integrations/openclaw.md`](docs/integrations/openclaw.md) |
| Claude Code | 🟡 documented | [`docs/integrations/claude-code.md`](docs/integrations/claude-code.md) |
| Claude Desktop | 🟡 documented | [`docs/integrations/claude-desktop.md`](docs/integrations/claude-desktop.md) |
| Gemini CLI | 🟡 documented | [`docs/integrations/gemini-cli.md`](docs/integrations/gemini-cli.md) |
| Hermes Agent | 🟡 documented | [`docs/integrations/hermes-agent.md`](docs/integrations/hermes-agent.md) |

Once a client is connected, work through the
[`pack-demo-playbook.md`](docs/integrations/pack-demo-playbook.md) — 20+
copy-pasteable prompts that exercise every pack. The
[per-pack reference](https://helmdeck.dev/reference/packs/) covers each
pack's contract, error codes, and chained workflows.

### Advanced: manual setup

If you'd rather drive each step yourself instead of running the install script:

```sh
# 1. Build the Management UI bundle (needs Node 20+)
make web-deps && make web-build

# 2. Build the control-plane binary with the UI embedded
make build

# 3. Run the control plane with admin credentials
HELMDECK_JWT_SECRET=$(openssl rand -hex 32) \
HELMDECK_VAULT_KEY=$(openssl rand -hex 32) \
HELMDECK_ADMIN_PASSWORD=changeme \
./bin/control-plane
```

Or use the Compose stack directly (control plane + Garage object store + bundled init):

```sh
cp deploy/compose/.env.example deploy/compose/.env.local
# …edit deploy/compose/.env.local and fill in real secrets…
docker compose -f deploy/compose/compose.yaml --env-file deploy/compose/.env.local up -d
```

## Logging in to the Management UI

The login endpoint accepts a static admin password set via the
`HELMDECK_ADMIN_PASSWORD` env var on the control plane process.
Suitable for the dev / single-node Compose tier; OIDC SSO for
production deployments lands in a later phase.

| Setting | Default | Override |
| --- | --- | --- |
| Username | `admin` | `HELMDECK_ADMIN_USERNAME` env var |
| Password | *(none — UI login disabled)* | `HELMDECK_ADMIN_PASSWORD` env var (required) |
| Session length | 12 hours | Hardcoded in `internal/api/auth_login.go` |

**To change the password:** stop the control plane, set
`HELMDECK_ADMIN_PASSWORD` to the new value, and restart. There is
no in-UI "change password" flow today — the password is managed
out-of-band by whichever orchestrator runs the control plane
(Compose, systemd, Kubernetes Secret, etc.).

**If `HELMDECK_ADMIN_PASSWORD` is unset**, the login endpoint
returns `503 login_disabled`. The control plane still runs and the
API still works — operators can mint a JWT directly via the CLI:

```sh
./bin/control-plane -mint-token=alice -mint-token-scopes=admin -mint-token-ttl=12h
```

The minted token can be pasted into any tool that speaks
`Authorization: Bearer <token>`.

**Production note:** the static-password path uses constant-time
comparison so it's safe against timing attacks, but it's still a
shared secret that has to be rotated by hand. For production
deployments with multiple operators, OIDC SSO via your existing
identity provider is the right answer — see the Phase 6 follow-up
roadmap.

## Architecture at a glance

- **Sidecar pattern** — browser runs in its own container, never embedded in the agent (ADR 001)
- **Golang control plane** — single static binary, distroless image, embeds the React UI (ADR 002)
- **Capability Packs** — the primary product surface; user-authorable via Go or WASM (ADRs 003, 012, 024)
- **OpenAI-compatible AI gateway** — Anthropic, Gemini, OpenAI, Ollama, Deepseek with encrypted keys + fallback routing (ADR 005)
- **MCP server registry** — stdio/SSE/WebSocket transports; built-in MCP server auto-derived from the pack catalog (ADR 006)
- **Credential vault** — AES-256-GCM with placeholder-token injection; agents never see secrets (ADR 007)
- **Dual-tier deployment** — Docker Compose for dev/single-node, Helm chart for Kubernetes production (ADRs 009, 010, 011)
- **First-class MCP clients** — Claude Code, Claude Desktop, OpenClaw, Gemini CLI via a single shared `helmdeck-mcp` bridge binary (ADRs 025, 030)
- **Bundled object store** — [Garage](https://garagehq.deuxfleurs.fr/) ships in the Compose stack as the default artifact backend; pluggable to any S3-compatible endpoint (AWS S3, R2, B2, SeaweedFS) for production (ADR 031)

## Built-in Capability Packs

38 packs ship in the box. Each one hides a multi-step workflow
behind a single typed JSON-Schema call so weak open-weight models
can drive it as reliably as frontier models. The full input/output
contract for every pack lives in [`docs/PACKS.md`](docs/PACKS.md).
The highlights:

| Pack | What it hides |
| :--- | :--- |
| **Browser & web** | |
| `browser.screenshot_url` | Session lifecycle, navigation, render wait, cleanup |
| `browser.interact` | Deterministic multi-step CDP (navigate, click, type, scroll, screenshot, assert_text) — no LLM needed |
| `web.scrape` / `web.scrape_spa` | Firecrawl-backed markdown scrape OR schema-driven SPA extraction |
| `web.test` | Natural-language browser tests via Playwright MCP + LLM loop |
| `research.deep` | Multi-source Firecrawl search + per-source scrape + LLM synthesis with inline citations |
| `content.ground` | Parses a markdown file for claims, finds authoritative sources, inserts real `[link](url)` citations in place |
| **Document & vision** | |
| `slides.render` | Marp + Chromium + format flags |
| `slides.narrate` | Narrated MP4 video (ElevenLabs TTS per slide) + auto-generated YouTube metadata |
| `doc.parse` | Docling layout-aware parse — PDF tables, multi-format, OCR fallback |
| `doc.ocr` | Tesseract fallback for simple images |
| `desktop.run_app_and_screenshot` | Xvfb + xdotool + scrot + window focus |
| `vision.click_anywhere` | Native computer-use routing (Anthropic/OpenAI/Gemini schemas) with JSON-prompt fallback for Ollama/Deepseek |
| `vision.extract_visible_text` / `vision.fill_form_by_label` | Screenshot → vision model → action loop |
| **Code edit loop** | |
| `repo.fetch` / `repo.push` | SSH key selection from vault, `known_hosts`, key shred-on-exit; envelope returns `tree`/`readme`/`entrypoints`/`signals` so agents orient on the first turn |
| `repo.map` | Aider-style structural symbol map under a token budget |
| `fs.read` / `fs.write` / `fs.patch` / `fs.list` / `fs.delete` | Path-safe file ops inside a clone |
| `cmd.run` | Run an arbitrary command in a clone path |
| `git.commit` / `git.diff` / `git.log` | Stage + commit + review changes attributed to `helmdeck-agent` |
| **GitHub** | |
| `github.create_issue` / `github.list_issues` / `github.list_prs` / `github.post_comment` / `github.create_release` / `github.search` | Vault-stored PAT, never visible to the agent |
| **Language sidecars** | |
| `python.run` | CPython 3 + pytest + ruff + mypy in a Python sidecar image |
| `node.run` | Node 20 LTS + npm + pnpm + yarn + tsc in a Node sidecar image |
| **HTTP & credentials** | |
| `http.fetch` | Placeholder-token egress: `${vault:NAME}` substitution in URL/headers/body |

See ADRs 014–036 for per-pack contracts and
[`docs/SIDECAR-LANGUAGES.md`](docs/SIDECAR-LANGUAGES.md) for the
runbook on adding new language sidecars (Rust, Go, Ruby, etc.).
The contribution guide in [`CONTRIBUTING.md`](CONTRIBUTING.md)
walks through writing your own pack — the most useful contributions
right now are SaaS API wrappers (Slack, Linear, Stripe, Notion, etc.).

## License

Licensed under the [Apache License, Version 2.0](LICENSE). See
[`NOTICE`](NOTICE) for attribution to bundled and depended-upon
projects, and [`CONTRIBUTING.md`](CONTRIBUTING.md) for the
contribution guide and the SPDX header convention.

By submitting a pull request you agree to license your contribution
under the same terms (Apache 2.0 Section 5 covers the contribution
grant — there's no separate CLA).

## Author

[Tosin Akinosho](mailto:tosin.akinosho@gmail.com) ([@tosin2013](https://github.com/tosin2013))
