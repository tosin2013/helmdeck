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
- [x] ~~**T406** Built-in pack: `slides.video`~~ — **moved to Phase 6.5** as `slides.narrate` with expanded scope (ElevenLabs TTS + YouTube metadata). See T406 under Phase 6.5 below.
- [x] **T407** Vision-mode endpoint
- [x] **T408** Reference vision packs
- [x] **T409** noVNC live viewer baseline
- [x] ~~**T410** Steel Browser integration~~ — **deferred indefinitely**. Playwright MCP (T807a) and native computer-use tool routing (T807f) cover the browser automation surface. Steel Browser adds marginal value over the existing stack.

---

## Milestone: `v0.5 — Vault, Repo Packs & Hardening` (Phase 5)
**Target:** Week 16 · **Exit:** `repo.fetch` against private GitHub via vault SSH key; OTel traces in Langfuse

- [x] **T501** Credential Vault (AES-256-GCM + ACL + usage log)
- [x] **T502** Credential types: login, cookies, API key, OAuth, SSH/git
- [x] **T503** CDP cookie injection + form-autofill fallback
- [x] **T504** HTTP gateway placeholder-token interception
- [x] **T505** Built-in pack: `repo.fetch`
- [x] **T506** Built-in pack: `repo.push`
- [x] ~~**T507** OneCLI delegation mode~~ — **deferred indefinitely**. The MCP bridge (T303) + SSE transport (T302a) + client integrations (T557–T563) already serve every client helmdeck targets. OneCLI adds a proprietary CLI layer with no clear demand signal.
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
- [x] **T565** Walk the Phase 5.5 code-edit loop against OpenClaw end-to-end and flip `docs/integrations/claude-code.md` + `README.md` matrix to ✅ — the actual milestone exit gate
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
- [x] **T618** `github.list_issues` + `github.search` — complete the GitHub CRUD + search set so agents can read and search issues/code, not just create them
- [x] **T619** `git.diff` + `git.log` — agents review what changed before committing
- [x] **T620** `fs.delete` — remove files in a session-local clone path
- [x] **T621** `browser.interact` — deterministic multi-step browser automation (navigate, click, type, scroll, screenshot, assert_text). Uses existing chromedp. The building block for AI-powered `web.test` in Phase 7.
- [x] **T302b** MCP inline image content — pack artifacts under 1 MB returned as `type: "image"` base64 content blocks in `tools/call` responses so vision-capable LLMs can see screenshots in one round trip *(ADR 032)*
- [x] **T613** Artifact Explorer UI panel — standalone `/artifacts` route in the Management UI with image preview, download button, pack/date filters, backed by `GET /api/v1/artifacts` *(ADR 032)* *(per-client cards with snippet + copy button for claude-code, claude-desktop, openclaw, gemini-cli, hermes-agent; OS-detected one-liners in T612a)*

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

## Milestone: `v0.8 — MCP Server Hosting & Pack Evolution` (Phase 6.5) ✅
**Status:** Complete. v0.8.0 tagged. 35 packs ship. `scripts/validate-phase-6-5.sh` is the validation harness.

This phase validated the "host, don't rebuild" architecture from ADR 035 and added native computer-use tool routing (T807f), narrated video generation (T406), and two composite packs (research.deep, content.ground). The container topology: Playwright MCP in the sidecar (shares Chromium), Firecrawl + Docling as separate optional compose services, ElevenLabs as a cloud TTS API with vault-stored key.

- [x] **T807a** Bundle Playwright MCP (`@playwright/mcp`) in the browser sidecar Dockerfile; auto-register when a session starts *(ADR 035)* *(sidecar.Dockerfile layer 4b installs Node 20 + `@playwright/mcp@latest` globally with `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1` so the Playwright postinstall doesn't pull ~200 MB of bundled Chromium that would never be used — the system chromium from layer 2 is the only browser. Entrypoint launches `npx @playwright/mcp --cdp-endpoint http://127.0.0.1:9222 --host 0.0.0.0 --port 8931 --headless --no-sandbox` after Chromium is live, so Playwright MCP **attaches** to the same browser process the existing `browser.*` chromedp packs use instead of launching its own — one Chromium, one cookie jar, shared state. Auto-registration surfaces as a new `PlaywrightMCPEndpoint` field on `session.Session` populated by the Docker runtime from container inspect, exposed on the session REST API as `playwright_mcp_endpoint` (`http://<container-ip>:8931/mcp`, matching upstream's standalone `--port` mount point); there's no entry in the external `/api/v1/mcp/servers` registry because that's for operator-configured MCP servers, not auto-launched sidecar children. Opt-out via `HELMDECK_PLAYWRIGHT_MCP_ENABLED=false` — handled in both the entrypoint (skips the npx launch) and in `buildPlaywrightMCPEndpoint` (returns empty string so downstream packs see the disabled state cleanly instead of connecting to a closed port). 4 new unit tests cover happy path, opt-out, typo-tolerant true, and the no-IP edge case.)*
- [x] **T807b** Add Firecrawl as an optional compose service (`HELMDECK_FIRECRAWL_ENABLED=true`); new `web.scrape` pack — no selectors, returns clean markdown *(ADR 035)* *(overlay file `deploy/compose/compose.firecrawl.yml` brings up firecrawl + playwright-service + redis on baas-net; pack registered unconditionally but handler gates on the env var so operators who haven't enabled the overlay get an actionable error pointing at the exact toggle; target URL is run through the egress guard before the Firecrawl call so the sidecar can't be used as an SSRF pivot to reach cloud metadata; 9 table-driven tests cover happy path, disabled-by-default, format whitelist, egress block, upstream 500, success=false, and empty-markdown edge cases)*
- [x] **T807c** Add Docling as an optional compose service (`HELMDECK_DOCLING_ENABLED=true`); new `doc.parse` pack — full document understanding (PDF layout, tables, multi-format, OCR) replacing `doc.ocr` *(ADR 035)* *(overlay file `deploy/compose/compose.docling.yml` brings up `quay.io/docling-project/docling-serve:latest` on baas-net with a named model-cache volume so cold restarts drop from ~45s to ~5s; pack accepts either `source_url` (http_sources) or `source_b64`+`filename` (file_sources) and hits `POST /v1/convert/source`; target URL is run through the egress guard so Docling can't be coerced into pulling cloud metadata, file sources skip the guard since bytes are inline; closed-set output formats (`md`/`text`/`html`) with `md` always force-included so the output schema's required `markdown` field stays populated; `partial_success` passes through unchanged while `failure`/`skipped` surface as `handler_failed` with Docling's own error list; `doc.ocr` stays in the catalog as the lightweight Tesseract-only fallback; 15 table-driven tests cover http/file happy paths, disabled-by-default, exactly-one-source rule, invalid-base64, format whitelist, `do_ocr=false` round-trip, egress guard both ways, upstream 500, status=failure, partial_success, and empty markdown)*
- [x] ~~**T807d** browser-use / Skyvern wrapper~~ — **superseded by T807f.** Mid-planning research showed that all three frontier providers (Anthropic, OpenAI, Google) now ship native computer-use tool schemas (2026), all client-runtime. Wrapping browser-use or Skyvern would embed a Python agent loop inside helmdeck's Go pack engine for functionality the models already provide natively. T807f upgrades helmdeck's existing vision.* + desktop sidecar to speak the provider-native schemas directly — same capability, no new runtime, vault-aware credential safety, cross-provider out of the box.
- [x] **T807f** Native computer-use tool routing + observability hooks *(ADR 035, supersedes T807d)* *(six work packages: **A** gateway.ChatRequest.Tools/ToolChoice + ContentPart tool_use/tool_result + Anthropic/OpenAI/Gemini adapter translation with provider-specific wire formats, **B** eight new desktop REST primitives (double_click, triple_click, drag, scroll, modifier_click, mouse_move, wait, zoom) covering the full Claude computer_20251124 / Gemini computer-use-preview action vocabulary, **C** vision.StepNative — one iteration of screenshot→ChatRequest with Tools=[computer]+ToolChoice=any→parse tool_use→dispatch via xdotool, routing via SupportsNativeComputerUse for Anthropic/OpenAI/Gemini with JSON-prompt fallback for Ollama/Deepseek, ComputerUseAction expanded internal type with provider-aware parsers including Gemini 0-1000 normalized coordinate scaling, **D** EventComputerUse audit constant + per-step screenshot artifact upload to Garage S3 for replay via the /artifacts panel, **E** AgentStatus field on VNCInfo + POST /api/v1/desktop/agent_status endpoint for noVNC witness-mode banner overlay, **F** docs + ADR 035 revision. Innovation angles: cross-provider schema abstraction (swap model field, same desktop), vault-aware typing (model never sees credentials), audit-backed replay, live human observability via noVNC. ~3200 lines across gateway/api/vision/packs/audit + 80 new tests.)*
- [x] **T807e** `web.test` — natural language browser testing via Playwright MCP accessibility tree *(ADR 035)* *(new `internal/pwmcp/` package is a narrow streamable-HTTP client for @playwright/mcp — `Initialize` captures the `Mcp-Session-Id` header and replays it on every follow-up, `ToolsCall` posts JSON-RPC and decodes either application/json or text/event-stream responses so it works against both Playwright MCP's single-shot and streamed tool-call paths; 5 client unit tests cover init+session-id, RPC errors, tool-level `isError`, SSE framing, and upstream 5xx. The `web.test` pack itself is in `internal/packs/builtin/webtest.go` (renamed from `web_test.go` — the `_test.go` suffix is reserved by Go for test files, which silently excludes the production code from the build). Input takes `{url, instruction, model, max_steps?, assertions?}`; handler flow is (1) validate + egress-check target URL, (2) read `PlaywrightMCPEndpoint` from the session populated by T807a (refuses with a clear CodeSessionUnavailable pointing operators at T807a if empty), (3) `initialize` the MCP session, (4) seed `browser_navigate` + `browser_snapshot` so the model's first turn sees the page without wasting a step on deterministic work, (5) plan-step loop: ask the gateway LLM for one tool call as JSON given (goal, current snapshot, compact history), parse it via a balanced-brace scanner tolerant of prose and markdown code fences, guard any mid-test `browser_navigate` through the egress check so the model can't pivot to metadata, execute via pwmcp, re-snapshot, repeat until `done`, `fail`, or max_steps. Optional assertions run a substring match against the final snapshot; completed=false if either the model didn't say done or any assertion failed. Registered conditionally in `cmd/control-plane/main.go` alongside the vision packs — only when a gateway dispatcher is configured. 13 table-driven tests cover happy path w/ assertions, missing session, empty PWMCP endpoint, missing fields (url/instruction/model), egress-blocks-target, egress-blocks-mid-test (verifies the blocked call is NOT forwarded to MCP), initialize failure, model emits fail with reason, max_steps exhausted, assertion-failed final report, unparseable JSON, prose/markdown-wrapped JSON tolerance, and unknown tool name. `browser.interact` (T621) stays in the catalog as the deterministic LLM-free option when the caller already knows the refs.)*
- [x] **T622** `research.deep` — Firecrawl-backed deep research: search a topic across multiple sources, scrape each to clean markdown, return a synthesis. Composite pack chaining Firecrawl search + scrape APIs. Depends on T807b. *(handler takes `{query, model, limit?, max_tokens?}`, fans out a single POST to `/v1/search` with `scrapeOptions.formats=["markdown"]` so search + per-source scrape happen in ONE upstream round trip — self-hosted Firecrawl's default Google backend handles the search with no extra config, SearXNG is supported via `SEARXNG_ENDPOINT` on the Firecrawl container. Limit defaults to 5 and hard-caps at 10 because larger values blow up synthesis token usage linearly. The frozen synthesis prompt instructs the model to cite URLs inline in parentheses and stay factual; user message is `QUERY:` + `SOURCES:` blocks (one `--- Source N: URL ---` header per item). Empty-markdown items are dropped before synthesis and the whole call fails with `handler_failed` if nothing usable survives. Shared Firecrawl HTTP helper `callFirecrawlSearch` landed in the same file so T623 `content.ground` can reuse it — single place to tune timeouts, response caps (16 MiB), and error shaping. Registered conditionally in main.go alongside the vision packs and web.test (needs a gateway dispatcher); env-gated on `HELMDECK_FIRECRAWL_ENABLED`. 10 table-driven tests cover happy path (synthesis receives both source bodies + URL headers), disabled-by-default, missing query, missing model, limit cap, Firecrawl 500, `success=false`, all-empty markdown → handler_failed, synthesis dispatch failure (quota error propagation), and whitespace-only synthesis response.)*
- [x] **T623** `content.ground` — link grounding for blog posts: parse a markdown file for claims, search GitHub + web for authoritative sources, insert real `[source](url)` links directly in the file. *(ADR 035)* *(session-scoped pack that takes `{clone_path, path, model, max_claims?, topic?}` and runs a two-phase pipeline against a markdown file inside a session-local clone. Phase 1: read the file via the same session-executor wc+cat pattern fs.patch uses, then ask the gateway LLM for a strict-JSON claim plan — the prompt requires `text` to be a VERBATIM substring of the post so the literal substring match in phase 2 works deterministically. Phase 2: for each claim, call Firecrawl `/v1/search` (via the shared `callFirecrawlSearch` helper T622 established) without scrapeOptions — grounding only needs the URL, not the body — pick the first result with a non-empty URL, and rewrite the markdown with `strings.Replace(..., count=1)` so only the first occurrence is annotated. Claims whose text doesn't literally appear in the file (hallucination) are skipped in the skipped[] report rather than corrupting the file. Claims with no source found are also skipped. Write-back only fires when the patched text actually differs — otherwise the file's mtime stays clean. Milestone originally described the pipeline as `github.search` + `http.fetch` + `web.scrape` + `fs.patch` but collapsing to one Firecrawl /v1/search covers "GitHub + web" in a single call (Google indexes GitHub repos/docs/issues); the `topic` input hint lets callers bias the extractor's generated queries toward `site:github.com` etc. without a second integration. Env-gated on `HELMDECK_FIRECRAWL_ENABLED`, registered conditionally on gateway dispatcher availability in main.go. 10 top-level tests (12 subtests) cover happy path with two claims both grounded and write-back asserted, empty claim plan → no file touch, hallucinated substring skipped while a good claim in the same batch still grounds, no source found → no file touch, disabled env var, missing executor → session_unavailable, missing required fields (clone_path/path/model subcases), empty file rejection, unparseable claim JSON, and max_claims input cap (5) overriding a 10-claim model response.)*
- [x] **T406** `slides.narrate` — **moved from Phase 4** (originally deferred as `slides.video`). Narrated MP4 video from Marp slide decks. Pipeline: parse speaker notes from `<!-- -->` comments → export per-slide PNGs via `marp --images` → ElevenLabs TTS per slide (API key from vault `elevenlabs-key`, voice randomly picked from top 5 when not specified) → ffmpeg per-slide segments with timed audio alignment → concatenate with optional fade transitions → LLM-generated YouTube metadata (title, description with `M:SS` timestamps, tags, category). Degrades gracefully: no vault key → silent video, no metadata_model → skip metadata. 23 tests (parser: 13, handler: 10).

---

## Milestone: `v1.0 — Kubernetes & GA` (Phase 7)

**Status:** Phase 6.5 complete (v0.8.0 tagged). 35 packs ship. All pre-GA feature work is done. Phase 7 is the production-readiness push: Kubernetes deployment, Helm chart, scaling, TLS, external secrets, load testing, security audit.

---

## Milestone: `v1.x — Innovation Backlog` (Phase 8)
**Target:** Post-GA · no fixed week

- [ ] **T801** WASM Executor + WASI capability inspection
- [ ] **T802** Four-tier Memory API (Working/Episodic/Semantic/Procedural)
- [ ] **T803** Procedural→Pack promotion UI
- [ ] **T804** WebRTC live session streaming
- [ ] **T805** Audio capture for desktop sessions
- [ ] **T806** WebMCP detection + preferential routing
- [x] ~~**T807** Pre-packaged Chrome DevTools MCP / Playwright MCP entries~~ — **completed by T807a** (Phase 6.5). Playwright MCP bundled in the sidecar Dockerfile, auto-registered on session start. Chrome DevTools MCP is redundant — chromedp-based packs already drive CDP directly.
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
