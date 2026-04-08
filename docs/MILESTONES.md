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
- [ ] **T404** Built-in pack: `web.fill_form` *(blocked on T501)*
- [ ] **T405** Built-in pack: `web.login_and_fetch` *(blocked on T501)*
- [ ] **T406** Built-in pack: `slides.video` *(blocked on T501)*
- [x] **T407** Vision-mode endpoint
- [x] **T408** Reference vision packs
- [x] **T409** noVNC live viewer baseline
- [ ] **T410** Steel Browser integration (optional)

---

## Milestone: `v0.5 — Vault, Repo Packs & Hardening` (Phase 5)
**Target:** Week 16 · **Exit:** `repo.fetch` against private GitHub via vault SSH key; OTel traces in Langfuse

- [x] **T501** Credential Vault (AES-256-GCM + ACL + usage log)
- [x] **T502** Credential types: login, cookies, API key, OAuth, SSH/git
- [x] **T503** CDP cookie injection + form-autofill fallback
- [ ] **T504** HTTP gateway placeholder-token interception
- [ ] **T505** Built-in pack: `repo.fetch`
- [ ] **T506** Built-in pack: `repo.push`
- [ ] **T507** OneCLI delegation mode
- [ ] **T508** NetworkPolicy egress allowlist + metadata IP block
- [ ] **T509** Sandbox baseline (non-root, drop caps, seccomp)
- [ ] **T510** OpenTelemetry GenAI instrumentation
- [ ] **T511** Trivy CI scan gate

---

## Milestone: `v0.6 — Management UI` (Phase 6)
**Target:** Week 20 · **Exit:** operator authors+publishes a custom pack entirely in the UI

- [ ] **T601** React/Tailwind/shadcn shell + JWT login
- [ ] **T602** Dashboard panel
- [ ] **T603** Browser Sessions panel
- [ ] **T604** AI Providers panel
- [ ] **T605** MCP Registry panel
- [ ] **T606** Capability Packs panel (list + Overview/Schema/Test Runner tabs)
- [ ] **T607** Model Success Rates tab (the killer feature)
- [ ] **T608** Pack Authoring UI (schema editor + Go/WASM handler + publish)
- [ ] **T609** Security Policies panel
- [ ] **T610** Credential Vault panel
- [ ] **T611** Audit Logs panel
- [ ] **T612** Connect-client OS-detected one-liner buttons

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
