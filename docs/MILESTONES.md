# Helmdeck — GitHub Milestones & Issue Checklists

Drop-in source for `gh issue create` and GitHub Projects. Each phase = one milestone. Each task ID from `TASKS.md` = one issue. Copy a section into `gh milestone create` + `gh issue create --milestone ...`.

---

## Milestone: `v0.1 — Core Infrastructure` (Phase 1)
**Target:** Week 4 · **Exit:** `make smoke` green end-to-end on Compose

- [x] **T101** Bootstrap Go module + repo layout (`cmd/control-plane`, `cmd/helmdeck-mcp`, `internal/`)
- [x] **T102** goreleaser + GitHub Actions: build matrix, cosign, distroless image to ghcr.io
- [x] **T103** `SessionRuntime` interface + Docker SDK backend
- [x] **T104** Browser sidecar Dockerfile (Chromium, Marp, Tesseract, ffmpeg, xdotool, scrot, Xvfb, XFCE4, noVNC)
- [x] **T105** Session lifecycle REST + watchdog
- [x] **T106** CDP integration via `chromedp`
- [x] **T107** JWT auth middleware
- [x] **T108** SQLite migrations + Postgres-compatible schema
- [x] **T109** Audit log writer
- [x] **T110** Compose stack `deploy/compose/compose.yaml`
- [x] **T111** `make smoke` end-to-end harness

---

## Milestone: `v0.2 — AI Gateway & Pack Substrate` (Phase 2)
**Target:** Week 8 · **Exit:** ≥90% weak-model success on `browser.screenshot_url` + `web.scrape_spa`

- [x] **T201** OpenAI-compatible `/v1/chat/completions` + `/v1/models`
- [x] **T202** Provider adapters: Anthropic, Gemini, OpenAI, Ollama, Deepseek
- [x] **T203** AES-256-GCM encrypted key store + rotation
- [x] **T204** Fallback chain rules engine
- [x] **T205** Pack Execution Engine
- [x] **T206** Closed-set typed error code enforcement
- [x] **T207** Pack registry + REST dispatch + version routing
- [x] **T208** Built-in pack: `browser.screenshot_url`
- [x] **T209** Built-in pack: `web.scrape_spa`
- [x] **T210** Built-in pack: `slides.render`
- [x] **T211** Object store integration + signed URLs
- [x] **T211a** Bundle Garage as default object store in Compose stack *(ADR 031)*
- [x] **T211b** Artifact TTL janitor *(ADR 031)*
- [x] **T211c** Cross-reference ADR 031 from ADRs 014/021 + README *(ADR 031)*
- [x] **T212** A2A Agent Card endpoint
- [x] **T213** A2A task endpoint with SSE

---

## Milestone: `v0.3 — MCP Bridge & Client Integrations` (Phase 3)
**Target:** Week 10 · **Exit:** four-client smoke matrix green in CI

- [x] **T301** MCP server registry CRUD + transport adapters
- [x] **T302** Built-in MCP server exposing all packs
- [x] **T302a** SSE MCP transport at `/api/v1/mcp/sse` *(unblocks the sidecar pattern: containerized clients like OpenClaw point at the URL transport instead of having to bake the helmdeck-mcp stdio bridge into their image. PackServer is transport-agnostic so the SSE handler is a thin adapter; WS transport untouched. JWT-protected via the same router middleware as every other /api/v1/* route.)*
- [x] **T303** `helmdeck-mcp` bridge binary
- [x] **T304** Bridge version-skew warning
- [x] **T305** Distribution: Homebrew tap + Scoop bucket + GH Releases (cosigned)
- [x] **T306** npm package `@helmdeck/mcp-bridge`
- [x] **T307** OCI image `ghcr.io/tosin2013/helmdeck-mcp`
- [x] **T308** CI smoke matrix: Claude Code · Claude Desktop · OpenClaw · Gemini CLI
- [x] **T309** "Connect" UI snippet generators (stubs)

---

## Milestone: `v0.4 — Desktop & Vision` (Phase 4)
**Target:** Week 13 · **Exit:** vault-backed `web.login_and_fetch` + vision demo on Canvas page

- [x] **T401** Desktop Actions REST API (xdotool/scrot wrappers)
- [x] **T402** Built-in pack: `desktop.run_app_and_screenshot`
- [x] **T403** Built-in pack: `doc.ocr`
- [x] ~~**T404** Built-in pack: `web.fill_form`~~ — **superseded** by T503 (CDP cookie injection) + T408 (`vision.fill_form_by_label`)
- [x] ~~**T405** Built-in pack: `web.login_and_fetch`~~ — **superseded** by T504 (`http.fetch` with `${vault:NAME}`) + T503
- [x] ~~**T406** Built-in pack: `slides.video`~~ — **deferred**, not on the GA path; revisit alongside T804 WebRTC streaming
- [x] **T407** Vision-mode endpoint
- [x] **T408** Reference vision packs
- [x] **T409** noVNC live viewer baseline
- [ ] **T410** Steel Browser integration (optional, P3)

---

## Milestone: `v0.5 — Vault, Repo Packs & Hardening` (Phase 5)
**Target:** Week 16 · **Exit:** `repo.fetch` against private GitHub via vault SSH key; OTel traces in Langfuse

- [x] **T501** Credential Vault (AES-256-GCM + ACL + usage log)
- [x] **T502** Credential types: login, cookies, API key, OAuth, SSH/git
- [x] **T503** CDP cookie injection + form-autofill fallback
- [x] **T504** HTTP gateway placeholder-token interception
- [x] **T505** Built-in pack: `repo.fetch`
- [x] **T506** Built-in pack: `repo.push`
- [ ] **T507** OneCLI delegation mode
- [x] **T508** NetworkPolicy egress allowlist + metadata IP block
- [x] **T509** Sandbox baseline (non-root, drop caps, seccomp)
- [x] **T510** OpenTelemetry GenAI instrumentation
- [x] **T511** Trivy CI scan gate

---

## Milestone: `v0.5.5 — Code Edit Loop` (Phase 5.5)
**Target:** alongside Phase 5 · **Exit:** every client in `docs/integrations/` has a setup guide, and at least Claude Code is marked ✅ tested against the Phase 5.5 code-edit loop (`repo.fetch` → `fs.*` → `cmd.run` → `git.commit` → `repo.push`)

- [x] **T550** Built-in pack: `fs.read` (read file from clone)
- [x] **T551** Built-in pack: `fs.write` (write file to clone)
- [x] **T552** Built-in pack: `fs.patch` (literal search-and-replace)
- [x] **T553** Built-in pack: `fs.list` (find files under clone path)
- [x] **T554** Built-in pack: `cmd.run` (run an arbitrary command in clone)
- [x] **T555** Built-in pack: `git.commit` (stage + commit changes)
- [x] **T556** `http.fetch` placeholder-token demo pack *(landed with T504)*
- [x] **T557** `docs/integrations/README.md` — index + per-client status matrix (✅ tested / 🟡 documented / ⚪ planned)
- [x] **T558** `docs/integrations/claude-code.md` — setup + Phase 5.5 loop walkthrough *(🟡 — awaiting end-to-end walk to flip to ✅)*
- [x] **T559** `docs/integrations/claude-desktop.md` — setup + Phase 5.5 loop walkthrough *(🟡)*
- [x] **T560** `docs/integrations/openclaw.md` — setup + Phase 5.5 loop walkthrough *(🟡; also corrected `connect.go` openclaw shape to real `~/.openclaw/openclaw.json`)*
- [x] **T561** `docs/integrations/nemoclaw.md` — wrapper over openclaw.md with sandbox-specific notes; NemoClaw reuses OpenClaw's MCP schema inside the sandbox so it is intentionally not a separate `connect.go` target *(🟡)*
- [x] **T562** `docs/integrations/gemini-cli.md` — setup + Phase 5.5 loop walkthrough *(🟡)*
- [x] **T563** `docs/integrations/hermes-agent.md` — setup + Phase 5.5 loop walkthrough; added `hermes-agent` case to `connect.go` (YAML config, `format: "yaml"` field) *(🟡)*
- [x] **T564** `scripts/validate-clients.sh` — manual helper that boots the stack and prints connect snippets + a copy-pasteable JSON-RPC code-edit-loop scenario (no pass/fail automation)
- [ ] **T565** Walk the Phase 5.5 code-edit loop against Claude Code end-to-end and flip `docs/integrations/claude-code.md` + `README.md` matrix to ✅ — the actual milestone exit gate
- [x] **T570** `scripts/install.sh` — one-command bootstrap on a fresh box. Verified end-to-end on the dev box (all four scenarios: happy path, login round-trip, idempotent re-run, `--reset` rotates password). Surfaced and fixed nine pre-existing wiring bugs in compose.yaml / garage.toml / garage-init / Dockerfiles along the way. Multipass VM verification still recommended before tagging v0.6.0.

---

## Milestone: `v0.6 — Management UI` (Phase 6)
**Target:** Week 20 · **Exit:** every read-only Phase 6 panel ships against a real backend; pack *authoring* (schema editor + handler runtime + publish) is deferred to Phase 8 alongside T801 (WASM Executor) — see T608 below

- [x] **T601** React/Tailwind/shadcn shell + JWT login
- [x] **T602** Dashboard panel *(stat cards + status table; Recharts memory chart in T602a)*
- [x] **T603** Browser Sessions panel *(read-only list; New Session modal in T603a)*
- [x] **T604** AI Providers panel *(read-only key list backed by GET /api/v1/providers/keys; Add/Rotate modal in T604a)*
- [x] **T605** MCP Registry panel *(read-only list; Add Server modal in T605a)*
- [x] **T606** Capability Packs panel *(read-only list grouped by namespace; Test Runner in T606a)*
- [x] **T202a** Wire provider adapters into the gateway registry at startup *(gap discovered while preparing OpenClaw validation: T202 shipped the adapter code but the integration step — instantiating each adapter from a stored key and registering it with `gateway.Registry` — was never wired in `cmd/control-plane/main.go`. Without this fix `/v1/models` returned empty, `/v1/chat/completions` always 404'd, and the T607 success-rate panel could never show data. Fix: new `internal/gateway/hydrate.go` reads the keystore at boot and on every key add/rotate/delete (hot reload), plus an env-var fast path for OpenAI-compatible aggregators like OpenRouter via `HELMDECK_OPENROUTER_API_KEY`.)*
- [x] **T607** Model Success Rates tab *(provider_calls table written by gateway dispatch on every success/error path; GET /api/v1/providers/stats aggregates by (provider, model) over a configurable window; rendered as a second section on the AI Providers panel)*
- [x] ~~**T608** Pack Authoring UI (schema editor + Go/WASM handler + publish)~~ — **deferred to Phase 8**, clustered with T801 (WASM Executor) and T803 (Procedural→Pack promotion). Today the pack registry is in-process and has no publish surface; building one means landing either a sandboxed code runtime (WASM, T801) or a composite-pack JSON runtime first. Neither is on the v0.6.0 critical path. Read-only Capability Packs panel (T606) ships in v0.6.0; authoring lands in v1.x.
- [x] **T609** Security Policies panel *(read-only snapshot of egress allowlist + sandbox baseline + auth + telemetry; backed by new GET /api/v1/security; edit + reload-config in T609a)*
- [x] **T610** Credential Vault panel *(read-only list; Add Credential modal + Usage Log in T610a)*
- [x] **T611** Audit Logs panel *(GET /api/v1/audit + filters: event_type / severity / actor / from / to / limit; React panel replaces stub)*
- [x] **T612** Connect Clients panel
- [x] **T504** repo.fetch + repo.push HTTPS clone/push support with vault-stored PAT via GIT_ASKPASS *(public repos need no credential; private repos pass `"credential":"vault-name"`)*
- [x] **T504a** Session pinning via `_session_id` input field — repo.fetch preserves session for follow-on packs (fs.*, cmd.run, git.commit, repo.push) to reuse
- [x] **T615** GitHub PAT setup in `scripts/install.sh` — optional interactive prompt stores token in vault as `github-token`
- [x] **T616** GitHub webhook listener at `POST /api/v1/webhooks/github` — HMAC-SHA256 validated, async pack dispatch per event rules *(ADR 033; Phase 1: push + pull_request, env-var rules)*
- [x] **T617** Core `github.*` pack set — `github.create_issue`, `github.list_prs`, `github.post_comment`, `github.create_release` using vault-stored PATs via `api.github.com` REST *(ADR 034)*
- [ ] **T618** `github.list_issues` + `github.search` — complete the GitHub CRUD + search set so agents can read and search issues/code, not just create them
- [ ] **T619** `git.diff` + `git.log` — agents review what changed before committing
- [ ] **T620** `fs.delete` — remove files in a session-local clone path
- [ ] **T621** `browser.interact` — deterministic multi-step browser automation (navigate, click, type, scroll, screenshot, assert_text). Uses existing chromedp. The building block for AI-powered `web.test` in Phase 7.
- [ ] **T302b** MCP inline image content — pack artifacts under 1 MB returned as `type: "image"` base64 content blocks in `tools/call` responses so vision-capable LLMs can see screenshots in one round trip *(ADR 032)*
- [ ] **T613** Artifact Explorer UI panel — standalone `/artifacts` route in the Management UI with image preview, download button, pack/date filters, backed by `GET /api/v1/artifacts` *(ADR 032)* *(per-client cards with snippet + copy button for claude-code, claude-desktop, openclaw, gemini-cli, hermes-agent; OS-detected one-liners in T612a)*

---

## Milestone: `v1.0 — Kubernetes & GA` (Phase 7)
**Target:** Week 22 · **Exit:** Helm install on fresh GKE/EKS passes smoke; security audit clean

- [ ] **T701** `client-go` SessionRuntime backend
- [ ] **T702** Helm chart `charts/baas-platform/`
- [ ] **T703** PostgreSQL StatefulSet sub-chart
- [ ] **T704** Session pod template (seccomp, shm, restartPolicy: Never)
- [ ] **T705** NetworkPolicy: control-plane → sessions on 9222
- [ ] **T706** NetworkPolicy: session egress restriction
- [ ] **T707** KEDA ScaledObject on custom metrics
- [ ] **T708** `browser-pool-warmup` Deployment + claim protocol
- [ ] **T709** `isolation.level` Helm value (standard/enhanced/maximum)
- [ ] **T710** cert-manager + Ingress-NGINX TLS
- [ ] **T711** OTel Collector DaemonSet/sidecar
- [ ] **T712** External Secrets Operator integration
- [ ] **T713** Argo CD reference manifest
- [ ] **T714** Load test (100 concurrent, 24h soak)
- [ ] **T715** External security audit
- [ ] **T807a** Bundle Playwright MCP (`@playwright/mcp`) in the browser sidecar Dockerfile; auto-register when a session starts *(ADR 035)*
- [ ] **T807b** Add Firecrawl as an optional compose service (`HELMDECK_FIRECRAWL_ENABLED=true`); new `web.scrape` pack — no selectors, returns clean markdown *(ADR 035)*
- [ ] **T807c** Add Docling as an optional compose service; new `doc.parse` pack — full document understanding (PDF layout, tables, multi-format, OCR) replacing `doc.ocr` *(ADR 035)*
- [ ] **T807e** `web.test` — natural language browser testing via Playwright MCP accessibility tree *(ADR 035)*

---

## Milestone: `v1.x — Innovation Backlog` (Phase 8)
**Target:** Post-GA · no fixed week

- [ ] **T801** WASM Executor + WASI capability inspection
- [ ] **T802** Four-tier Memory API (Working/Episodic/Semantic/Procedural)
- [ ] **T803** Procedural→Pack promotion UI
- [ ] **T804** WebRTC live session streaming
- [ ] **T805** Audio capture for desktop sessions
- [ ] **T806** WebMCP detection + preferential routing
- [ ] **T807** Pre-packaged Chrome DevTools MCP / Playwright MCP entries
- [ ] **T808** Firecracker isolation tier productionization
- [ ] **T809** Lightpanda alternate browser engine evaluation
- [ ] **T810** Pack marketplace registry model — `index.yaml` catalog, `helmdeck-pack.yaml` manifest schema, cosign trust verification, `HELMDECK_MARKETPLACE_URL` config *(ADR 034)*
- [ ] **T811** `command` handler type — subprocess packs in any language (stdin JSON / stdout JSON protocol) with egress guard + audit
- [ ] **T812** `helmdeck pack install/uninstall` CLI commands + `POST /api/v1/marketplace/install` endpoint
- [ ] **T813** Marketplace UI panel — `/marketplace` route with browse-by-category, search, pack detail, install/uninstall, trust badges
- [ ] **T814** Community marketplace repo (`tosin2013/helmdeck-marketplace`) with initial pack catalog + contribution guide
- [ ] **T815** Pack ratings + install counts (requires marketplace-web frontend)
- [ ] **T816** MCP Server Hosting framework — generic `helmdeck mcp install <server>` for community MCP servers with sandboxed execution; converges with ADR 034 marketplace *(ADR 035)*

---

## Bulk-create script

```bash
#!/bin/bash
# scripts/bootstrap-issues.sh — run once after gh auth login
set -euo pipefail
REPO="tosin2013/helmdeck"

declare -a MILESTONES=(
  "v0.1 — Core Infrastructure"
  "v0.2 — AI Gateway & Pack Substrate"
  "v0.3 — MCP Bridge & Client Integrations"
  "v0.4 — Desktop & Vision"
  "v0.5 — Vault, Repo Packs & Hardening"
  "v0.6 — Management UI"
  "v1.0 — Kubernetes & GA"
  "v1.x — Innovation Backlog"
)

for m in "${MILESTONES[@]}"; do
  gh api "repos/$REPO/milestones" -f title="$m" || true
done

# Then parse this MILESTONES.md and gh issue create per checkbox.
# Each issue body should link to docs/TASKS.md#<task-id> and docs/adrs/<n>-*.md
```

## Labels
Apply consistently: `phase/1`..`phase/8`, `priority/P0`..`priority/P3`, `area/control-plane`, `area/sidecar`, `area/ui`, `area/helm`, `area/bridge`, `area/packs`, `area/vault`, `area/security`, `area/observability`, `kind/feature`, `kind/test`, `kind/docs`.
