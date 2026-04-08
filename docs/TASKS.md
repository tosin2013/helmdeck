# Helmdeck — Implementation Task Breakdown

Generated from `docs/adrs/001`–`030` and PRD §16 roadmap.
Each task lists its source ADR(s) and prerequisite tasks. IDs are stable for cross-reference.

**Legend:** `P0` blocker / critical path · `P1` required for phase exit · `P2` important but parallelizable · `P3` nice-to-have

---

## Phase 1 — Core Infrastructure (Weeks 1–4)

**Goal:** ephemeral browser sessions callable via REST, single-node Compose deploy.

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T101 | Bootstrap Go module `github.com/tosin2013/helmdeck`, set up `cmd/control-plane`, `cmd/helmdeck-mcp`, `internal/` layout | P0 | 002 | — |
| T102 | Wire goreleaser + GitHub Actions: build matrix (linux/darwin/windows × amd64/arm64), cosign signing, distroless image to ghcr.io | P0 | 002, 030 | T101 |
| T103 | Define `SessionRuntime` interface; implement Docker SDK backend (spawn, exec, logs, terminate) | P0 | 001, 004, 009 | T101 |
| T104 | Browser sidecar Dockerfile: Ubuntu base, headless Chromium, Marp, Tesseract (eng), ffmpeg, xdotool, scrot, Xvfb, XFCE4, noVNC, font packs | P0 | 001, 014, 015, 018, 019 | T101 |
| T105 | Session lifecycle: create/list/get/terminate REST endpoints with `shm_size`, `timeout`, `maxTasks`, memory/cpu limits; watchdog goroutine for leak/timeout recycle | P0 | 004 | T103 |
| T106 | CDP integration via `chromedp`: navigate, extract, screenshot, execute, interact endpoints | P0 | 002 | T105 |
| T107 | JWT auth middleware (Gin); token issuance scaffolding (full Access Control panel deferred to Phase 6) | P0 | 010 (security baseline) | T101 |
| T108 | SQLite migration runner; schema for sessions, audit log entries (Postgres parity behind interface) | P0 | 009 | T101 |
| T109 | Audit log writer: every API call records actor, session id, event type, payload (keys redacted) | P1 | 010 (baseline) | T108 |
| T110 | Compose stack `deploy/compose/compose.yaml`: control-plane + database + browser-pool template + internal `baas-net` bridge | P0 | 001, 009 | T102, T103 |
| T111 | Smoke test harness: `make smoke` spins compose stack, runs end-to-end navigate→screenshot→terminate flow | P1 | 009 | T106, T110 |

**Phase 1 exit criteria:** `make smoke` green; control-plane image <30 MB; browser sidecar image built and pushed; session create→navigate→screenshot→delete works end-to-end with JWT auth.

---

## Phase 2 — AI Gateway + Capability Pack Substrate (Weeks 5–8)

**Goal:** OpenAI-compatible gateway live; Capability Pack execution engine usable; first three reference packs shipped.

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T201 | OpenAI-compatible `/v1/chat/completions` + `/v1/models` facade routing on `provider/model` syntax | P0 | 005 | T107 |
| T202 | Provider adapters: Anthropic, Google Gemini, OpenAI, Ollama, Deepseek (HTTP clients with retry + connection pooling) | P0 | 005 | T201 |
| T203 | AES-256-GCM encrypted key store; key never returned in full; rotation API; provider test endpoint | P0 | 005, 007 | T108, T201 |
| T204 | Fallback chain rules engine: `{primary, fallback, trigger}` with rate-limit / error / timeout triggers | P1 | 005 | T201 |
| T205 | **Pack Execution Engine**: input schema validation → session acquire → handler invoke → output schema validation → artifact upload → typed result | P0 | 003, 008 | T106 |
| T206 | Closed-set typed error codes enforcement: middleware that maps any uncategorized handler error to nearest defined code | P0 | 008 | T205 |
| T207 | Pack registry: in-memory registration + REST `POST /api/v1/packs/{name}` dispatch + version routing `/v{n}` | P0 | 003, 024 | T205 |
| T208 | Built-in pack: `browser.screenshot_url` (reference pack — validates the whole substrate) | P0 | 021 | T207 |
| T209 | Built-in pack: `web.scrape_spa` with JSON Schema-driven extraction and partial-result handling | P0 | 017 | T207 |
| T210 | Built-in pack: `slides.render` (Marp + Chromium → PDF/PPTX/HTML) | P1 | 014 | T104, T207 |
| T211 | Object store integration (S3-compatible) for pack artifacts; signed URL generation | P0 | 014, 015, 018, 021 | T205 |
| T211a | Bundle Garage (`dxflrs/garage`) as the default object store in `deploy/compose/compose.yaml`; init container runs `garage layout assign` + `garage layout apply` on first boot; control plane env wired so `make smoke` exercises the persistent path end-to-end | P0 | 031 | T211, T110 |
| T211b | Artifact TTL janitor: control-plane goroutine scans audit-table pack output references older than `HELMDECK_ARTIFACT_TTL` (default 7d) and deletes the corresponding objects; per-pack overrides via pack manifest | P1 | 031 | T211, T109 |
| T211c | Cross-reference ADR 031 from ADRs 014 and 021 (one-line "see ADR 031 for backend choice" addition); update README install path to mention bundled Garage | P3 | 031 | T211a |
| T212 | A2A Agent Card endpoint `/.well-known/agent.json` auto-generated from pack registry | P2 | 026 | T207 |
| T213 | A2A task endpoint `POST /a2a/v1/tasks` with SSE streaming for long-running packs | P2 | 026 | T212 |

**Phase 2 exit criteria:** weak-model success rate ≥90% on `browser.screenshot_url` + `web.scrape_spa` against MiniMax-M2.7; AI gateway proxies all five providers; pack registry hot-loads new packs without restart.

---

## Phase 3 — MCP Registry + Bridge + Client Integrations (Weeks 9–10)

**Goal:** all installed packs callable from Claude Code, Claude Desktop, OpenClaw, Gemini CLI via the bridge.

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T301 | MCP server registry CRUD API; stdio/SSE/WebSocket transport adapters; manifest fetch + cache | P0 | 006 | T108 |
| T302 | Built-in MCP server exposing every installed pack as a typed MCP tool (auto-generated from pack registry) | P0 | 003, 006 | T207 |
| T303 | `helmdeck-mcp` bridge binary: stdio MCP server proxying to platform's WebSocket MCP endpoint via `HELMDECK_URL` + `HELMDECK_TOKEN` | P0 | 025, 030 | T302 |
| T304 | Bridge version-skew warning: emit deprecation notification on session start when older than platform's min recommended | P1 | 025, 030 | T303 |
| T305 | Distribution channels via goreleaser: Homebrew tap (`tosin2013/helmdeck`), Scoop bucket, GitHub Releases (cosigned) | P0 | 030 | T102, T303 |
| T306 | npm package `@helmdeck/mcp-bridge` with postinstall binary downloader from GH Releases | P1 | 030 | T305 |
| T307 | OCI image `ghcr.io/tosin2013/helmdeck-mcp` (multi-arch) for containerized agents | P1 | 030 | T305 |
| T308 | CI smoke matrix: spawn `helmdeck-mcp` from each of Claude Code, Claude Desktop, OpenClaw, Gemini CLI configs and assert `browser.screenshot_url` returns a PNG | P0 | 025 | T303, T208 |
| T309 | "Connect" UI snippets per client (deferred to Phase 6 when UI lands; stub the JSON generators now) | P2 | 025 | T303 |

**Phase 3 exit criteria:** all four target clients invoke `browser.screenshot_url` end-to-end via the bridge in CI; bridge installable via `brew install`, `npx`, `scoop install`, `docker run`.

---

## Phase 4 — Desktop Actions + Vision Mode (Weeks 11–13)

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T401 | Desktop Actions REST API: screenshot, click, type, key, launch, windows, focus (xdotool/scrot wrappers) | P0 | 027 | T106 |
| T402 | Built-in pack: `desktop.run_app_and_screenshot` | P1 | 018 | T401 |
| T403 | Built-in pack: `doc.ocr` (Tesseract with language pack support) | P1 | 019 | T207 |
| T404 | ~~Built-in pack: `web.fill_form`~~ — **superseded** by T503 (CDP cookie injection) + T408 (`vision.fill_form_by_label`); the "fill a form with a vault credential" capability ships through both | — | 020 | — |
| T405 | ~~Built-in pack: `web.login_and_fetch`~~ — **superseded** by T504 (`http.fetch` with `${vault:NAME}` substitution) + T503; the substantive auth pattern is the placeholder-token flow, not a dedicated browser-driven login pack | — | 016 | — |
| T406 | ~~Built-in pack: `slides.video`~~ — **deferred**; not on the GA path. Worth revisiting alongside T804 (WebRTC streaming) since the same audio/video pipeline serves both | — | 015 | — |
| T407 | Vision-mode endpoint `POST /api/v1/sessions/{id}/vision/act`: screenshot → AI gateway → action loop | P1 | 027 | T201, T401 |
| T408 | Reference vision packs: `vision.click_anywhere`, `vision.extract_visible_text`, `vision.fill_form_by_label` | P2 | 027 | T407 |
| T409 | noVNC live viewer endpoint `/api/v1/desktop/vnc-url` (baseline; WebRTC in Phase 6+) | P2 | 028 | T401 |
| T410 | Steel Browser optional integration as alternate browser layer behind `SessionRuntime` interface | P3 | 001 | T103 |

**Phase 4 exit criteria:** desktop session screenshots work; `web.login_and_fetch` succeeds against a test SaaS using a vault credential; vision mode demo on a Canvas-only page.

---

## Phase 5 — Credential Vault + Repo Packs + Hardening (Weeks 14–16)

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T501 | **Credential Vault**: AES-256-GCM store with separate encryption key, host/path pattern matcher, agent-scope ACL, usage log | P0 | 007 | T108, T203 |
| T502 | Vault credential types: website login, session cookies, API key, OAuth (with refresh), SSH/git | P0 | 007 | T501 |
| T503 | CDP cookie injection at session start (`Network.setCookies`) and form-autofill fallback | P0 | 007, 016 | T501, T106 |
| T504 | HTTP gateway placeholder-token interception: intercept agent egress, swap placeholder for real credential, forward | P0 | 007 | T501 |
| T505 | Built-in pack: `repo.fetch` (URL normalization, vault SSH key, `GIT_SSH_COMMAND` with `accept-new`, retries) | P0 | 022 | T501 |
| T506 | Built-in pack: `repo.push` (paired with `repo.fetch`; non-fast-forward → `schema_mismatch` with detail) | P1 | 023 | T505 |
| T507 | OneCLI delegation mode: optional config to forward credential resolution to external OneCLI | P3 | 007 | T501 |
| T508 | Application-layer egress guard: refuses any pack-handler call to a host that resolves to 169.254.169.254/32, RFC 1918 ranges, loopback, IPv6 link-local, or carrier-grade NAT — with DNS rebinding defense (every returned IP must pass). `HELMDECK_EGRESS_ALLOWLIST` for internal hosts. K8s `NetworkPolicy` lands separately as T706. | P0 | 011 | T103 |
| T509 | Sandbox baseline: non-root UID 1000 (helmdeck user in sidecar Dockerfile), `cap-drop ALL` + `cap-add SYS_ADMIN` (Chromium namespace sandbox), `no-new-privileges`, `pids-limit 1024` (defaults; override via `HELMDECK_PIDS_LIMIT`), `seccomp` defaults to docker's curated profile (override via `HELMDECK_SECCOMP_PROFILE`) | P0 | 011 | T103 |
| T510 | OpenTelemetry instrumentation: traces with `gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.*` on every LLM/MCP/pack span; OTLP exporter | P0 | 013 | T201, T205 |
| T511 | Trivy CI scan; fail pipeline on CRITICAL findings | P0 | 030 | T102 |

**Phase 5 exit criteria:** `repo.fetch` against a private GitHub repo with vault SSH key works end-to-end without agent ever seeing the key; OTel traces visible in a Langfuse instance; egress allowlist blocks metadata IP.

---

## Phase 5.5 — Code Edit Loop (interleaved with Phase 5)

**Goal:** turn `repo.fetch` into a working autonomous code-edit workflow by adding the file/git/cmd primitives the LLM needs to actually modify and test code inside a clone.

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T550 | Built-in pack: `fs.read` (read file from clone with size cap + sha256, path safety via `safeJoin`) | P0 | 022 | T505 |
| T551 | Built-in pack: `fs.write` (write file with `mkdir -p` for parents, content via stdin) | P0 | 022 | T505 |
| T552 | Built-in pack: `fs.patch` (literal search-and-replace, NOT regex; optional occurrence cap; sha256 of result) | P0 | 022 | T550, T551 |
| T553 | Built-in pack: `fs.list` (find files under clone path with optional glob, recursive flag, 5000-entry cap) | P1 | 022 | T550 |
| T554 | Built-in pack: `cmd.run` (run an arbitrary shell command in a clone path with stdin support; non-zero exit codes are normal pack outcomes) | P0 | 022 | T505 |
| T555 | Built-in pack: `git.commit` (stage + commit with `helmdeck-agent` author env injection; "nothing to commit" maps to invalid_input) | P0 | 023 | T505 |
| T556 | Built-in pack: `http.fetch` (placeholder-token demo: `${vault:NAME}` substitution in URL/headers/body via the wrapped http.Client; egress-guarded) | P0 | 007 | T504 |

**Phase 5.5 exit criteria:** an LLM can drive `repo.fetch` → `fs.list` → `fs.read` → `fs.patch` → `python.run` (or `cmd.run`) → `git.commit` → `repo.push` end-to-end against a real private GitHub repo, with the SSH key never in the LLM's context window and every step audit-logged.

---

## Phase 6 — Management UI (Weeks 17–20)

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T601 | React/Tailwind/shadcn UI shell embedded in Go binary; JWT login flow | P0 | 002 | T107 |
| T602 | Dashboard panel: metric cards + activity feed + Recharts memory chart | P1 | — | T601, T109 |
| T603 | Browser Sessions panel: data table, New Session modal, View Logs drawer, Terminate confirm | P0 | 004 | T601, T105 |
| T604 | AI Providers panel: provider cards, Configure modal, Test Connection, Routing Rules table | P0 | 005 | T601, T203 |
| T605 | MCP Registry panel: server table, Add Server multi-step modal, Tool Inspector | P0 | 006 | T601, T301 |
| T606 | **Capability Packs panel** (the killer feature): list grouped by namespace, Overview/Schema/Test Runner tabs | P0 | 003, 024 | T601, T207 |
| T607 | **Model Success Rates tab** with per-model breakdown, 80% threshold highlight, "Tighten Schema" diff view | P0 | 003, 008, 024 | T606, T510 |
| T608 | Pack Authoring UI: schema editor with live validation, inline Go handler editor, `.wasm` upload, Test Runner, Publish | P0 | 024 | T606 |
| T609 | Security Policies panel: Network/Sandbox/Access Control tabs | P1 | 011 | T601, T508 |
| T610 | Credential Vault panel: credentials table, Add Credential modal, Session Cookie import tool, Usage Log tab | P1 | 007 | T601, T501 |
| T611 | Audit Logs panel: filter bar, infinite-scroll table, Details drawer with redacted JSON payload | P1 | 013 | T601, T109 |
| T612 | "Connect" UI buttons for Claude Code / Claude Desktop / OpenClaw / Gemini CLI emitting OS-detected one-liners | P1 | 025, 030 | T601, T309 |

**Phase 6 exit criteria:** an operator can author, test, publish, and observe success rates for a custom pack entirely in the UI without restarting the control plane.

---

## Phase 7 — Kubernetes / Helm / Production Hardening (Weeks 21–22)

| ID | Task | Pri | ADRs | Depends on |
| :--- | :--- | :--- | :--- | :--- |
| T701 | `client-go` `SessionRuntime` backend: spawn pods in `baas-sessions` namespace via K8s API | P0 | 009 | T103 |
| T702 | Helm chart `charts/baas-platform/`: control-plane Deployment x2, PDB, Service, Ingress, ServiceAccount + Role + RoleBinding scoped to `baas-sessions` | P0 | 009 | T701 |
| T703 | PostgreSQL StatefulSet sub-chart (Bitnami); `database.external.enabled` toggle | P0 | 009 | T108, T702 |
| T704 | Session pod template: `restartPolicy: Never`, `automountServiceAccountToken: false`, seccomp Localhost profile, `/dev/shm` `emptyDir` `medium: Memory sizeLimit: 2Gi` | P0 | 004, 011 | T701 |
| T705 | NetworkPolicy 1: allow `baas-system` → `baas-sessions` on port 9222 | P0 | 011 | T702 |
| T706 | NetworkPolicy 2: restrict session pod egress, block 169.254.169.254/32 + 10.0.0.0/8, render allowlist from Security Policies | P0 | 011 | T508, T702 |
| T707 | KEDA ScaledObject reading `baas_queued_session_requests` and `baas_active_sessions / baas_pool_capacity` from Prometheus; thresholds 1 and 0.8 | P0 | 010 | T510, T702 |
| T708 | `browser-pool-warmup` Deployment maintaining N pre-initialized session pods; control plane claim/release protocol | P0 | 010 | T707 |
| T709 | `isolation.level` Helm value: `standard` (Docker default), `enhanced` (gVisor `runsc` RuntimeClass), `maximum` (firecracker-containerd RuntimeClass) | P1 | 011 | T704 |
| T710 | cert-manager `Certificate` resource + Ingress-NGINX TLS termination; `tls.enabled` toggle | P1 | 009 | T702 |
| T711 | OTel Collector DaemonSet (K8s tier) / sidecar (Compose tier); OTLP forwarder | P1 | 013 | T510 |
| T712 | External Secrets Operator integration; `vault.externalSecrets.enabled` toggle | P2 | 007 | T501, T702 |
| T713 | Argo CD reference application manifest in `deploy/gitops/` | P2 | 009 | T702 |
| T714 | Load test: 100 concurrent sessions, 24 h soak, validate ≤150 MB control plane footprint and ≤5 s recovery | P0 | 010 | T708 |
| T715 | External security audit; remediate findings before GA | P0 | 011 | T714 |

**Phase 7 exit criteria:** Helm install on a fresh GKE/EKS cluster passes the same smoke matrix as Compose; KEDA scales pool under synthetic load; gVisor tier passes the smoke matrix; security audit clean.

---

## Phase 8 — Innovation Backlog (Post-GA, Weeks 23+)

These are tracked but not on the GA critical path.

| ID | Task | Pri | ADRs |
| :--- | :--- | :--- | :--- |
| T801 | WASM Executor subsystem (`wasmtime-go`); WASI capability inspection; `.wasm` pack handler runtime | P1 | 012, 024 |
| T802 | Four-tier Memory API: Working (in-process) + Episodic (Redis) + Semantic (pgvector) + Procedural (read-only) | P1 | 029 |
| T803 | Procedural-memory → Pack promotion UI flow ("Pack Candidates") | P2 | 024, 029 |
| T804 | WebRTC live session streaming via `pion/webrtc`; LiveKit SFU optional path; bidirectional control data channel | P2 | 028 |
| T805 | Audio capture for desktop sessions (PulseAudio → WebRTC second track) | P3 | 028 |
| T806 | WebMCP detection on visited pages; preferential routing through `navigator.modelContext` when available | P2 | 027 |
| T807 | Pre-packaged Chrome DevTools MCP and Playwright MCP registry entries pointing at managed sessions | P2 | 006 |
| T808 | Firecracker isolation tier productionization (bare-metal node pool docs, networking model) | P2 | 011 |
| T809 | Lightpanda alternate browser engine evaluation behind `SessionRuntime` interface | P3 | 001 |

---

## Critical Path

```
T101 → T102 → T103 → T105 → T106 → T205 → T207 → T208 → T302 → T303 → T308
                              ↓                                        ↓
                            T201 → T202 → T203 → T501 → T504 → T505    │
                                                  ↓                    │
                                                T508 → T701 → T702 → T714 → T715 → GA
```

The hard sequence is: Go scaffolding → session runtime → CDP → pack engine → reference pack → MCP server → bridge → client smoke matrix; in parallel: AI gateway → vault → repo packs; converging on K8s + load test + audit before GA.

## Dependency-Free Parallel Tracks

These can be staffed independently from week 1:
- ~~**UI track** (T601 onward)~~ — Phases 1–5 are now shipped; the REST surface the UI consumes is stable. UI track is the next active workstream rather than a parallel one.
- **Helm chart track** (T702, T703, T705, T706) — once `client-go` `SessionRuntime` lands.
- **Distribution track** (T305, T306, T307) — once goreleaser config exists. ✅ shipped in v0.3.0.
- **Documentation track** — recipes for each integrated client (ADR 025) can be drafted as soon as the bridge contract is frozen.

## Open Questions to Resolve Before Phase 1 Kickoff

1. ~~Object store choice for pack artifacts: bundled MinIO vs. require external S3?~~ **Resolved by ADR 031 (2026-04-08): bundle Garage as the Compose default; treat the storage layer as a pluggable S3 client so any external backend is a first-class option; never bundle MinIO (upstream archived 2026-02). Tracked by T211a/T211b/T211c below.**
2. Which weak open-weight models (and at which quantizations) form the reference benchmark cohort for the Model Success Rates SLO?
3. Tenant boundary semantics for ADR 029 semantic memory — single-tenant only at GA, multi-tenant later?
4. License choice for the platform repo (affects distribution wording in ADR 030).
