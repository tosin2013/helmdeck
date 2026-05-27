# Helmdeck — Release Plan ("What Ships When")

Forward-looking changelog. Each release maps 1:1 to a phase milestone (`MILESTONES.md`) and has hard exit criteria pulled from `TASKS.md`.

---

## Agent sync checklist — every release

Helmdeck ships its agent instructions as a native **OpenClaw Skill** at `skills/helmdeck/SKILL.md`, stamped with the helmdeck commit hash in its frontmatter (`metadata.openclaw.helmdeckVersion`). The stamp is how operators detect drift between their deployed agent and the latest release.

**Every release — required:**

1. **Update the pack count and decision tables** in `skills/helmdeck/SKILL.md` if this release adds/removes packs, changes an error code, or revises a pattern (e.g. the `repo.fetch` signals table). When a release adds a pack or pipeline, also add its **prompt template** to `docs/reference/prompt-templates/` (`packs.md` / `pipelines.md`; copy the shape from `_template.md`).
2. **Bump the `helmdeckVersion` stamp** — `scripts/configure-openclaw.sh` regenerates this automatically from `git rev-parse --short HEAD` at install time, so you don't edit it by hand. Ensure the release commit lands on `main` before operators run the configure script, otherwise the stamp reflects a stale pointer.
3. **Call out new packs** in the release notes under "Ships" with their full `helmdeck__<name>` MCP prefix, so operators (and agents reading the release notes post-fact) know what's new.
4. **Tell deployed operators to refresh**:
   ```bash
   cd /path/to/helmdeck && git pull
   ./scripts/configure-openclaw.sh            # reinstalls the versioned SKILL.md
   ```
   The script is idempotent; re-running it without other flags will only touch the skill, the JWT (if expiring), and the model pin.
5. **Document upstream regressions** — if OpenClaw itself ships a breaking change between our tested versions and the current one, add a row to the table in `docs/integrations/openclaw-upgrade-runbook.md` pointing at the affected version range and the workaround.
6. **Refresh the README + cost-positioning numbers** — `README.md` opens with time-stamped prose ("Today's helmdeck install ran a full 6-step code-edit loop … for $0.07") and a four-row cost-comparison table. The same numbers live in the long-form `docs/explanation/why-helmdeck.md` and the cost-positioning blog post at `website/blog/2026-05-08-cheap-models-do-frontier-work.md`. On a release that meaningfully changes pack performance, the chat model recommendation, or the OpenRouter pricing landscape:
   - Re-run the 5 reproduction workflows from `docs/explanation/why-helmdeck.md` §"Run the comparison yourself" against the new pack set.
   - Update the comparison table in **all three places** (README, long-form explanation, blog post) so the numbers don't drift between them.
   - Either revise the time-stamped prose at the top of the README to reflect the new release, or — if numbers haven't moved meaningfully — leave it but add a "Last verified: vX.Y on YYYY-MM-DD" footer line so readers know the cited workflow is fresh enough.

   On a release that does NOT change agent-side performance, the cost numbers are stable enough to skip this step; only update if you'd otherwise be overstating the gap.
7. **Operator upgrade procedure** — every release MUST be cleanly upgradable from the prior tag without operator data loss or extended downtime. Verify before tagging:
   - The procedure in [`docs/howto/upgrade-helmdeck.md`](howto/upgrade-helmdeck.md) §"In-place Compose-stack upgrade" runs cleanly against a fresh checkout: `git checkout v<new>; make sidecars; make install` produces a healthy stack
   - `internal/store/migrations/` has any new migrations needed for new tables/columns. Auto-applied via `store.Open` on next startup (no manual `migrate up` required), but the migration file MUST be additive — no `DROP COLUMN` or `ALTER TABLE … RENAME` that would break a v<new-1> binary trying to read the same DB
   - If a release introduces a destructive schema change, flag it under `### Breaking` in `CHANGELOG.md` AND link from the upgrade howto's §7 "Version-specific notes" table
   - Pack-input-schema changes that drop a previously-required field, or change a closed-set value, are also `### Breaking` — agents written against the old schema will error
   - Post-tag, smoke-test the upgrade against a snapshot of v<new-1>'s `helmdeck.db` (a manual cross-version run; the automated CI smoke is tracked at the Phase 7 audit issue list under "upgrade smoke-test in CI")
8. **Re-publish to the official MCP Registry** — automated via [`.github/workflows/mcp-registry.yml`](../.github/workflows/mcp-registry.yml). The workflow fires on every `v*` tag push: it pulls the tag's version into `.mcp/server.json`, schema-validates the document, authenticates to `registry.modelcontextprotocol.io` via GitHub OIDC (no PAT needed — the workflow's `id-token: write` permission is enough), and publishes. Watch the run; the workflow summary prints the live listing URL. Downstream aggregators (mcp.so, Glama, PulseMCP) ingest within 24h.

   If the workflow fails or you need to re-publish without cutting a new tag, two fallback paths:
   - **`workflow_dispatch`** — go to the Actions tab → "Publish to MCP Registry" → "Run workflow" with an optional `version_override` input
   - **Local script** — [`scripts/publish-to-mcp-registry.sh`](../scripts/publish-to-mcp-registry.sh) builds the publisher locally, runs interactive GitHub OAuth, and publishes from a maintainer shell. Useful if the GitHub Actions OIDC path breaks for any reason.

**Related:**
- [OpenClaw upgrade runbook](integrations/openclaw-upgrade-runbook.md) — the operator-facing sync procedure
- [ADR 025 — MCP client integrations](adrs/025-mcp-client-integrations.md) — architecture decision record; the §2026-04-18 revision covers CLI vs chat-UI regression policy
- `skills/helmdeck/SKILL.md` — the canonical agent skill file (source of truth)

---

## v0.1.0 — Core Infrastructure (Week 4)
**Theme:** "A browser session is one REST call away."

> **Milestone:** [v0.1 — Core Infrastructure (Phase 1)](MILESTONES.md#milestone-v01--core-infrastructure-phase-1) · **Tasks:** [Phase 1](TASKS.md#phase-1--core-infrastructure-weeks-14)

### Ships
- Go control plane binary (Gin + chromedp + Docker SDK)
- Browser sidecar image with Chromium, Marp, Tesseract, ffmpeg, xdotool, Xvfb, XFCE4, noVNC
- Ephemeral session lifecycle: `POST /api/v1/sessions` … `DELETE /api/v1/sessions/{id}`
- CDP REST endpoints: navigate, extract, screenshot, execute, interact
- JWT bearer auth on every endpoint
- Audit log (write-only at this stage)
- Single-node Compose deployment (`deploy/compose/compose.yaml`)
- `make smoke` end-to-end harness in CI

### Does NOT ship
- AI gateway, packs, MCP, vault, UI, Kubernetes — all later

### Audience
Internal only. Tag a pre-release on GitHub.

---

## v0.2.0 — AI Gateway & First Packs (Week 8)
**Theme:** "Capability Packs are real, and weak models can drive them."

> **Milestone:** [v0.2 — AI Gateway & Pack Substrate (Phase 2)](MILESTONES.md#milestone-v02--ai-gateway--pack-substrate-phase-2) · **Tasks:** [Phase 2](TASKS.md#phase-2--ai-gateway--capability-pack-substrate-weeks-58)

### Ships
- OpenAI-compatible `/v1/chat/completions` and `/v1/models`
- Provider adapters: Anthropic, Gemini, OpenAI, Ollama, Deepseek
- Encrypted key store with rotation API
- Fallback routing rules (rate-limit / error / timeout triggers)
- **Pack Execution Engine** with input/output schema validation
- **Typed error code enforcement** (closed set per pack)
- Pack registry with versioned dispatch
- **Three reference packs:** `browser.screenshot_url`, `web.scrape_spa`, `slides.render`
- Object store integration + signed-URL artifacts
- A2A Agent Card at `/.well-known/agent.json`

### Hard exit gate
**≥90% success rate on `browser.screenshot_url` and `web.scrape_spa` against MiniMax-M2.7 and Llama 3.2 7B.** This is the *defining* metric of the platform — without it nothing else matters.

### Audience
Design partners. Public alpha tag.

---

## v0.3.0 — Bridge & Client Integrations (Week 10)
**Theme:** "Register one MCP server, get every helmdeck pack."

> **Milestone:** [v0.3 — MCP Bridge & Client Integrations (Phase 3)](MILESTONES.md#milestone-v03--mcp-bridge--client-integrations-phase-3) · **Tasks:** [Phase 3](TASKS.md#phase-3--mcp-registry--bridge--client-integrations-weeks-910)

### Ships
- MCP registry with stdio/SSE/WebSocket transports
- Built-in MCP server auto-derived from the pack catalog
- **`helmdeck-mcp` bridge binary** distributed via:
  - Homebrew tap `tosin2013/helmdeck`
  - Scoop bucket `tosin2013/helmdeck`
  - npm `@helmdeck/mcp-bridge` (with `npx` postinstall)
  - OCI image `ghcr.io/tosin2013/helmdeck-mcp`
  - GitHub Releases (cosigned)
- CI smoke matrix verifying `browser.screenshot_url` from **Claude Code, Claude Desktop, OpenClaw, Gemini CLI**

### Audience
Public beta. First "helmdeck works with my agent" demo video.

---

## v0.4.0 — Desktop & Vision (Week 13)
**Theme:** "Beyond the DOM."

> **Milestone:** [v0.4 — Desktop & Vision (Phase 4)](MILESTONES.md#milestone-v04--desktop--vision-phase-4) · **Tasks:** [Phase 4](TASKS.md#phase-4--desktop-actions--vision-mode-weeks-1113)

### Ships
- Desktop Actions REST API (xdotool/scrot)
- `desktop.run_app_and_screenshot`, `doc.ocr`
- Vision-mode endpoint `POST /api/v1/sessions/{id}/vision/act`
- Reference vision packs: `vision.click_anywhere`, `vision.extract_visible_text`, `vision.fill_form_by_label`
- noVNC live viewer endpoint

### Audience
Public beta continues.

---

## v0.5.0 — Vault & Repo Packs (Week 16)
**Theme:** "Agents stop holding secrets."

> **Milestone:** [v0.5 — Vault, Repo Packs & Hardening (Phase 5)](MILESTONES.md#milestone-v05--vault-repo-packs--hardening-phase-5) · also covers [v0.5.5 — Code Edit Loop](MILESTONES.md#milestone-v055--code-edit-loop-phase-55) · **Tasks:** [Phase 5](TASKS.md#phase-5--credential-vault--repo-packs--hardening-weeks-1416), [Phase 5.5](TASKS.md#phase-55--code-edit-loop-interleaved-with-phase-5)

### Ships
- AES-256-GCM Credential Vault with placeholder-token injection
- Vault types: login, session cookies, API keys, OAuth (with refresh), SSH/git
- CDP cookie injection at session start
- HTTP gateway intercept-and-substitute for outbound agent traffic
- **`repo.fetch` and `repo.push`** (closes the canonical 2026-04-06 git-SSH failure)
- **`web.login_and_fetch`, `web.fill_form`, `slides.video`** (vault-dependent packs)
- NetworkPolicy egress allowlist + metadata IP / RFC 1918 block
- Sandbox baseline: non-root, drop-all-caps, seccomp
- OpenTelemetry GenAI semantic conventions on every span
- Trivy CRITICAL gate in CI

### Audience
Production design partners. Hardening RC.

---

## v0.6.0 — Management UI (Week 20)
**Theme:** "Operators close the weak-model gap themselves."

> **Milestone:** [v0.6 — Management UI (Phase 6)](MILESTONES.md#milestone-v06--management-ui-phase-6) · **Tasks:** [Phase 6](TASKS.md#phase-6--management-ui-weeks-1720)

### Ships
- React/Tailwind/shadcn UI embedded in Go binary
- All read-only panels: Dashboard, Sessions, AI Providers, MCP Registry, **Capability Packs**, Security Policies, Credential Vault, Audit Logs, Connect Clients
- **Model Success Rates** section on the AI Providers panel (per-(provider, model) rollup over a configurable window, backed by the new `provider_calls` aggregation table written by every gateway dispatch)
- "Connect" panel emitting per-client MCP config snippets for Claude Code, Claude Desktop, OpenClaw, Gemini CLI, and Hermes Agent

### Deferred from v0.6.0
- **Pack Authoring** (T608) — moved to v1.x (Phase 8) and clustered with T801 (WASM Executor). The pack registry is in-process today and has no publish surface; building one requires either landing a sandboxed code runtime first (WASM) or a composite-pack JSON runtime. Neither is on the v0.6.0 critical path. Operators *observe and dispatch* packs in v0.6.0; they author them in v1.x.

### Audience
Public beta — full self-service for everything except authoring custom packs.

---

## v0.8.0 — MCP Server Hosting & Pack Evolution (Phase 6.5) — ✅ Shipped 2026-04-12 {#v080}

**Theme:** "Host third-party agent infrastructure instead of rebuilding it."

> **Milestone:** [v0.8 — MCP Server Hosting & Pack Evolution (Phase 6.5)](MILESTONES.md#milestone-v08) ✅
> **Tasks:** Phase 6.5 — see [`docs/TASKS.md`](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)

### Ships (36 packs total at the v0.8.0 cutover)

- **Playwright MCP bundled in the browser sidecar** ([T807a](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)) — auto-attached to the running Chromium via CDP; one browser, one cookie jar, shared state with chromedp packs.
- **Firecrawl as an optional compose overlay** ([T807b](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)) — `compose.firecrawl.yml`; new `web.scrape` pack returns clean markdown.
- **Docling as an optional compose overlay** ([T807c](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)) — `compose.docling.yml`; new `doc.parse` pack supersedes `doc.ocr` for layout/tables.
- **Native computer-use tool routing** ([T807f](TASKS.md#phase-65--mcp-server-hosting--pack-evolution), supersedes T807d) — Anthropic / OpenAI / Gemini schemas wired through the gateway; eight new desktop REST primitives; `vision.StepNative` cross-provider executor; `EventComputerUse` audit + replay.
- **`web.test`** ([T807e](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)) — natural-language browser testing via Playwright MCP accessibility tree; egress-guarded mid-test navigations.
- **`research.deep`** ([T622](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)) — Firecrawl-backed research composite (search + per-source scrape + LLM synthesis with inline citations).
- **`repo.fetch` context envelope + `repo.map`** ([T622a](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)) — agents orient on the first turn without chaining `fs.list`/`fs.read`; ctags-derived structural symbol map under a token budget.
- **`content.ground`** ([T623](TASKS.md#phase-65--mcp-server-hosting--pack-evolution)) — link grounding for blog posts; verbatim-substring patching skips hallucinated claims.
- **`slides.narrate`** ([T406](TASKS.md#phase-65--mcp-server-hosting--pack-evolution), moved from Phase 4) — narrated MP4 from Marp decks via ElevenLabs TTS + ffmpeg + LLM-generated YouTube metadata.
- **Provider-adapter community contributions** — Groq (PR #45) and Mistral (PR #47) adapters land alongside ([T202a](TASKS.md#phase-6--management-ui-weeks-1720)).

### Hard exit gate (met)

`scripts/validate-phase-6-5.sh` passes against a fresh stack including the Firecrawl + Docling overlays; native computer-use round-trip works against at least one frontier provider; 36 packs total.

### Audience

Public beta continues. Tag `v0.8.0` (shipped 2026-04-12). Sets up Phase 7 (Kubernetes & GA) as the next gate.

---

## v0.9.0 — Polish + plumbing (Phase 6.5+) — ✅ Shipped 2026-05-07 {#v090}

**Theme:** "Tighten what shipped before adding more."

> **Milestone:** Continuation of v0.8 / Phase 6.5 — no new milestone created. Aggregates 70 commits of post-v0.8.0 hardening.

### Ships

No new packs. No API changes. The 36-pack catalog from v0.8.0 stays the surface area. Operationally: a real install fix, public docs site at [helmdeck.dev](https://helmdeck.dev/), two community-contributed AI provider adapters (Groq, Mistral), gitleaks secret scanning, the planning-doc cross-references that were documented-but-not-implemented at v0.8.0, and the priority-label taxonomy (`priority/P0..P3`) on every issue.

See the full per-section breakdown in [`CHANGELOG.md` v0.9.0](https://github.com/tosin2013/helmdeck/blob/main/CHANGELOG.md#090---2026-05-07).

### Audience

Existing v0.8.0 operators. A direct upgrade — `git pull && make install` picks up everything (the install fix is the highest-value change for fresh deploys; existing deploys can ignore it).

---

## v0.10.0 — Content packs (Phase 6.5+) — ✅ Shipped 2026-05-09 {#v0100}

**Theme:** "Two new packs (blog + podcast), the cost story, and an upgrade procedure."

> **Repurposed slot.** The originally-planned v0.10.0 (Pack Authoring + Test Runner) didn't ship this cycle — `blog.publish` and `podcast.generate` were ready, plus the v0.9.0 → v0.10.0 doc work earned the version bump on its own. The Pack-Authoring + Test-Runner plan moves to v0.11.0 below.

### Ships

- **`blog.publish`** (#68) — Ghost Admin API + artifact-store destinations × body/prompt modes × markdown/html formats. Vault credential `ghost-admin-key`. Closes the personal-content marketplace seed.
- **`podcast.generate`** — multi-speaker (1..N) MP3 from script / prompt+model / source_url-or-source_text. Five themed system prompts (interview, debate, news-roundup, deep-dive, solo-essay) bake in podcast best practices. Day 1 ships **ElevenLabs** behind a `podcast.Engine` interface in `internal/podcast/` so future PRs add PlayHT / Hume.ai / Resemble.ai by adding one file. Vault credential `elevenlabs-key` (same as `slides.narrate`); silent-fallback when missing.
- **38 per-pack reference pages** at helmdeck.dev/reference/packs — every shipped pack on the agent-first / developer-second template, with live OpenClaw chat-UI transcripts.
- **OpenClaw transcript capture pipeline** at `scripts/oc-capture/` — `capture-oc.sh`, `capture-batch.sh`, `extract-oc-transcript.py`, `inject-transcripts.py`, plus prompt files for the three pack-doc clusters.
- **Cost-positioning blog** (`/blog/cheap-models-do-frontier-work`) + **long-form why-helmdeck reference** (`/explanation/why-helmdeck`) with five comparison tables and a reproduction recipe.
- **Operator upgrade documentation** at `/howto/upgrade-helmdeck` — pre-flight checklist, in-place Compose path, schema-migration handling, post-upgrade validation, rollback, Helm-path preview. **Closes the upgrade-docs gap** that was the maintainer's blocker for v1.0 prep.
- **SKILLS.md "Freshness contract"** + per-client "Load the agent skills" subsections for every integration doc.
- **Per-release-checklist additions** — step 6 (refresh README + cost numbers), step 7 (operator upgrade procedure smoke).

### Fixed (highlights — full list in `CHANGELOG.md`)

- `vision.click_anywhere` mechanical loop bug (#102) — per-step screenshots now reflect post-action state. **Caveat**: model-side completion-detection limitation remains; tracked at #112. Treat both vision packs as **experimental for production**.
- `repo.fetch` empty-remote infinite hang (#94)
- `fs.patch` Anthropic-edit-shape rejection (#90)
- `doc.parse` `formats: "markdown"` rejection (#91)
- OpenClaw capture pipeline cross-prompt context bleed — fresh `--session-id` per call ([#97](https://github.com/tosin2013/helmdeck/pull/97))

### Pre-Kubernetes audit issues filed (no v0.10.0 blockers)

- #108 — schema-migration cross-version test (P1, Phase 7)
- #109 — sidecar version pinning (P2, Phase 7)
- #110 — vault master-key rotation (P2, Phase 7)
- #111 — cross-version upgrade smoke in CI (P2, Phase 7)
- #112 — `vision.click_anywhere` model-side convergence research (P2)

### Audience

Production design partners + community.

---

## v0.10.2 — MCP Resources + registry description refinement — ✅ Shipped 2026-05-09 {#v0102}

**Theme:** "Browse helmdeck state as MCP resources, not just tools."

> **Closes [#44](https://github.com/tosin2013/helmdeck/issues/44).** Adds `resources/list` + `resources/read` so MCP clients can browse `helmdeck://packs` and `helmdeck://sessions` as read-only resources alongside the existing `tools/*` surface. Strictly additive — no breaking changes.

### Ships

- **MCP Resources spec implementation** — `resources/list` returns `helmdeck://packs` (always) and `helmdeck://sessions` (when a session runtime is wired); `resources/read` serves both as JSON. The `initialize` capabilities advert now includes `resources: {}`.
- **Refined registry description** — "Self-hosted MCP server: sandboxed browser, desktop, vision, code-edit packs for any agent." (was a 38-pack feature list). Leads with the value prop + self-hosted differentiator.
- **Registry submission script + workflow doc fixes** — point at the search API URL instead of the broken `/servers/<name>` web URL (registry is API-only in preview).

### Audience

Same as v0.10.1 — production design partners + community. MCP-client builders who want a browsable resource surface; everyone else can skip.

### Out of scope (deferred follow-ups)

- JWT scope filtering on resources (full #44 acceptance criteria item)
- Per-MCP-client integration tests for resource discovery

---

## v0.10.1 — MCP Registry namespace verification — ✅ Shipped 2026-05-09 {#v0101}

**Theme:** "Make the published artifacts pass the MCP Registry's namespace-verification checks."

> **Functionally identical to v0.10.0.** No pack/API/binary behavior changes. This release exists solely to add two pieces of metadata the official MCP Registry's validators need to confirm we own the `io.github.tosin2013/helmdeck` namespace. Existing v0.10.0 installs do not need to upgrade unless they specifically want the registry-listed install path.

### Ships

- **`mcpName` field on the npm package** — `@helmdeck/mcp-bridge@0.10.1`'s `package.json` now declares `"mcpName": "io.github.tosin2013/helmdeck"`. The npm validator reads this to confirm the package belongs to the registered namespace.
- **`io.modelcontextprotocol.server.name` label on the OCI image** — `ghcr.io/tosin2013/helmdeck-mcp:0.10.1` now carries the label. The OCI validator reads this to confirm namespace ownership.
- **`.github/workflows/mcp-registry.yml`** auto-publishes `.mcp/server.json` to `registry.modelcontextprotocol.io` on every `v*` tag push (also supports `workflow_dispatch` for ad-hoc runs). Authenticates via GitHub OIDC — no PAT required.

### Live registry entry

`io.github.tosin2013/helmdeck` published to the [official MCP Registry](https://registry.modelcontextprotocol.io/) as of `2026-05-09T17:13Z`, status `active`, both packages (npm + OCI) registered. Verify via the search API:

```
https://registry.modelcontextprotocol.io/v0/servers?search=io.github.tosin2013%2Fhelmdeck
```

Downstream aggregators (mcp.so, Glama, PulseMCP) ingest from the official registry on a 1–24h schedule and will appear automatically.

### Audience

Same as v0.10.0 — production design partners + community. Skip this release unless you need the registry-listed install path.

---

## v0.11.0 — podcast/slides UX hardening + image generation — ✅ Shipped 2026-05-10 {#v0110}

**Theme:** "The new content packs work — now their first-run UX matches."

> **Closes #136, #137, #138, #140, #141, #142, #143, #145, and ships the new `image.generate` pack (#71).** Adds the `helmdeck://voices` MCP resource. Closes #139 + #144 as duplicates. Defers #146 (chained image-gen integrations) to a follow-up release.

A coherent feature release driven by 9 issues filed during a v0.10.2 OpenClaw integration: silent MP3s when the credential name was wrong, hardcoded `/root/openclaw` paths, blocking Go preflight on the docker-only path, no voice discovery, no cost preview. The vault env-hydrate fix (#142) is the load-bearing piece — it root-causes the silent-fallback class of bug, not just the ElevenLabs instance.

### Ships

- **`image.generate` pack (#71)** — text → image via fal.ai's synchronous `fal.run` endpoint. Default model `fal-ai/flux/schnell` (~$0.003/image, 1-3s). 1-4 images per call. The `engine` input field is reserved for a follow-up community PR to add Replicate. Vault credential `fal-key` (with `HELMDECK_FAL_KEY` env-var fallback, auto-hydrated via #142).
- **Vault env-hydrate (#142)** — `WellKnownEnvCredentials` registry auto-imports `HELMDECK_*_API_KEY` env vars into the vault under their canonical names at startup. New `vault.Store.UpsertByName`. Wildcard ACL granted on first create; user-managed entries never clobbered. One INFO log per hydration (`vault env hydrate ok name=elevenlabs-key`).
- **`podcast.generate` + `slides.narrate` require narration by default (#138)** — pre-this-change, missing the ElevenLabs credential silently produced a silence-padded artifact. Now both packs hard-fail with `missing_credential` + an actionable message. Pass `allow_silent_output: true` to opt back into the silent path. Shared 4-step credential resolver: explicit input → vault `elevenlabs-key` → vault `elevenlabs-api-key` (back-compat alias) → `os.Getenv("HELMDECK_ELEVENLABS_API_KEY")`.
- **`helmdeck://voices` MCP resource (#143)** — exposes the operator's ElevenLabs voice catalog with 1h cache keyed on credential fingerprint. New `internal/voices/` package with `ListVoices(ctx, apiKey) → []Voice`.
- **`min_turn_duration_s` per-turn floor (#141)** — both packs gain the input (default `5s`); short TTS turns get padded with `anullsrc` so output respects the floor. `0` opts out.
- **`dry_run` + cost preview (#145)** — both packs gain `dry_run:bool`; short-circuits before TTS, returns `tts_chars` + `estimated_cost_usd`. Cost block also included in regular responses. Plan rate table covers Free/Starter/Creator/Pro/Scale; override via `HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD`.
- **`slides.narrate` ffmpeg failure surfaces full stderr (#140)** — inline cap raised 512 → 4096 bytes; full stderr persisted to artifact store as `ffmpeg-stderr-segment-NNN.txt`.
- **`scripts/install.sh` `--no-build` fix (#136)** — Go preflight skipped when `--no-build` is set; unblocks the docker-only path on hosts with apt-default Go 1.22.
- **`scripts/configure-openclaw.sh` paths + auth (#137)** — new `OPENCLAW_COMPOSE_FILE` env override; `OPENCLAW_LOAD_SHELL_ENV=true` recognized so the auth-list probe doesn't false-positive.

### Audience

Operators integrating helmdeck with OpenClaw or running the content packs (`podcast.generate`, `slides.narrate`); anyone wanting `image.generate` for podcast covers / blog hero images. The credential fail-loud change (#138) is a behavior break — silent-fallback callers must add `allow_silent_output: true` to keep working. Strictly additive otherwise.

### Out of scope (deferred follow-ups)

- **#146** — chain `image.generate` into `podcast.generate.cover_image` / `slides.narrate.shield_image` + `slide_images` / `blog.publish.hero_image`. The pack lands in this release; the integration layer on top of it lands later.
- Voice-id pre-validation in `podcast.generate` / `slides.narrate` — currently agents discover voices via `helmdeck://voices` and pass the IDs verbatim; future work could pre-validate at handler entry and return `invalid_voice` synchronously.
- `speakers: {"alice":"auto"}` auto-pick mode for `podcast.generate` — pick distinct voices automatically with seed for reproducibility.
- Replicate engine for `image.generate` — flagged as a community-friendly follow-up; the `engine` input field is in the schema from day 1 so adding it is a new switch arm rather than a schema break.

### MCP Registry

The auto-publish workflow (`.github/workflows/mcp-registry.yml`) republishes the listing on `v*` tag push. After tagging, verify at `https://registry.modelcontextprotocol.io/v0/servers?search=io.github.tosin2013%2Fhelmdeck` (expect `version: 0.11.0`, `isLatest: true`).

---

## v0.12.1 — Release-image hot-patch + v0.12.0 reliability bugs — ✅ Shipped 2026-05-13 {#v0121}

**Theme:** "Same-day hot-patch for what v0.12.0 missed."

Four bugs landed within hours of v0.12.0 shipping. The dominant one (#180) is a release-image regression — every fresh `docker pull` user saw a blank UI. The other three are smaller reliability fixes around the firecrawl overlay and the `content.ground` pack. All four landed as separate small PRs (#186–189) so each is independently revertible if a regression surfaces. Bundled into v0.12.1 the same day. Planning artefact: `/root/.claude/plans/i-would-like-to-elegant-kahan.md`.

### Shipped

- **#180 — release workflow now runs `npm run build` before docker image build.** The dominant fix. `web/dist/assets/` is gitignored; CI workflow was building the docker image without ever bundling the Vite output, so the image baked in whatever stale `web/dist/index.html` was last committed (referencing asset hashes not present in `web/dist/assets/`). Added Node setup + `cd web && npm ci && npm run build` step to `.github/workflows/release.yml`, plus a verify step that fails the release loud if rebuilt `index.html` references missing assets — defense in depth so this regression can't ship twice.
- **#181 — firecrawl-rabbitmq healthcheck `start_period: 15s` → `60s`.** RabbitMQ's Erlang VM + mnesia init takes 30-60s on alpine cold-boot. Shorter window exhausted retries before health was achievable → container reported unhealthy → `helmdeck-firecrawl` (correctly waiting via `depends_on: condition: service_healthy`) never started → operator had to `docker compose up` again. Aligned with `firecrawl-searxng`'s 60s precedent in the same file.
- **#179 — `content.ground` configurable completion-token cap.** Hard-coded 1024 was too tight for the structured claim-plan JSON (system prompt + topic + 5-8 claim entries ≈ 750 tokens, leaving ~270 of headroom; weak models or large posts blew through it → `CodeHandlerFailed: claim extractor returned unparseable JSON`). Default bumped 1024 → 2048; new optional `max_completion_tokens` input lets operators raise the cap up to 8192. Over-cap rejects with `CodeInvalidInput`.
- **#182 — `content.ground` fails loud when Firecrawl is unreachable.** Per-claim grounding loop was swallowing `callFirecrawlSearch` transport errors silently, producing empty-success "no sources found" output. Now tracks `firecrawlCalls` vs `firecrawlErrors` separately; when 100% of attempted calls hit transport errors → `CodeHandlerFailed` with a Firecrawl-reachability message. Partial-success runs preserved.

### Hard exit gates (all met)

- ✅ `go test ./internal/packs/builtin/... -run ContentGround` green (5 new tests)
- ✅ `make smoke` regression-protect v0.12.0
- ✅ `docker pull ghcr.io/tosin2013/helmdeck:0.12.1` shows assets matching index.html
- ✅ MCP-Registry chained publish (PR #177 workflow_run trigger) — validated this release
- ✅ npm `@helmdeck/mcp-bridge@0.12.1` published with provenance

### Not in v0.12.1 (deferred)

- **#183 audit-table columns** (`job_id`, `finish_reason`, `raw_content_len`) — migration + write-path changes; v0.13.0.
- **#173 / #174** — community good-first-issues, kept open for external contributors.

### Concurrent docs/SEO change

- **#184 — SKILL.md catalog refresh.** Pack count 36 → 39 (added `blog.publish`, `podcast.generate`, `image.generate` which had never been documented in SKILL.md). `helmdeckVersion` frontmatter from `24bd0c3` → `v0.12.0`. Shipped as PR #190, not part of the v0.12.1 patch bundle.
- **SEO sitemap trim.** Dropped `/blog/tags/*`, `/blog/archive`, `/blog/authors` from the Docusaurus sitemap (137 URLs → 122) after Google Search Console reported 61 URLs in "Discovered – currently not indexed" with crawl timestamp `1969-12-31`. Shipped as PR #185.

---

## v0.12.0 — Content-pack image chaining + v1.0 install-path unblocker + pack-authoring MVP — ✅ Shipped 2026-05-12 {#v0120}

**Theme:** "Covers come for free, the install path becomes Kubernetes-ready, and pack-authoring grows up."

Bundled release across four threads that lined up after v0.11.0. Originally framed as Pack Authoring + Test Runner alone; re-scoped during the v0.11.0 retrospective to absorb #146 (unblocked by v0.11.0's #71), #158 (sibling), and #134 step 1 (v1.0 prerequisite). Planning artefact: `/root/.claude/plans/i-would-like-to-elegant-kahan.md`.

### Shipped

- **#146 — chain `image.generate` into the three content packs.** `podcast.generate` gains `cover_image: bool` → emits `cover_image_artifact_key`. `slides.render` and `slides.narrate` gain `hero_image_prompt: string` → injects inline base64 PNG (before slide 1 for render; INTO slide 1 for narrate to preserve the per-slide TTS pipeline). `blog.publish` gains `feature_image_artifact_key` + `hero_image: bool` — for Ghost, uploads via `/ghost/api/admin/images/upload/` first then stamps the URL into `feature_image`; for artifact-mode, writes a sidecar `<slug>-cover.png`. All four packs share one `RunImageGen` entrypoint (extracted from `internal/packs/builtin/image_generate.go` in PR #165's first commit) so chains don't pay for a registry round-trip.
- **#158 — `helmdeck://image-models` MCP resource.** Mirrors `helmdeck://voices` (v0.11.0). 7-model curated catalog: flux/schnell, flux/dev, flux-pro/v1.1, fast-sdxl, flux-realism, recraft-v3, ideogram/v2. Each entry has cost, p50 latency, seed/image-size support, max resolution, capability tags. New `internal/imagemodels` package. Also lands the long-overdue `fal-key` entry in `WellKnownEnvCredentials` — closes the consistency gap `image_generate.go:74` advertised since v0.11.0.
- **#134 step 1 — unified install paths (P1 v1.0-rc1 unblocker).** `deploy/compose/compose.yaml` strips `build:` blocks, pins versioned tags. New `deploy/compose/compose.build.yaml` overlay re-adds them for source-build. `scripts/install.sh --image-mode` flag pulls pre-built images, skips Go/Node/make preflight. Hosts with only Docker + `openssl` + `curl` can install the full stack. The Helm chart (v1.0-rc1) will reuse the same versioned-tag convention.
- **T606a MVP — Pack Test Runner UI.** Click a pack row in `/packs` → modal with JSON textarea + Submit. POSTs to `/api/v1/packs/{name}`, renders response (duration, cost hint, full JSON). Closes the "no UI today" gap. Schema-derived form ships v0.13.0.
- **T811 MVP — subprocess pack type.** `packs.NewCommandPack(...)` constructor + `LoadCommandPacks` dir-scanner + `HELMDECK_COMMAND_PACKS_DIR` wire-up. Pack authors can ship in any language (Python, Node, Bash, Rust) without a Go toolchain. Protocol: stdin = JSON input; stdout = JSON output; exit ≠0 → `handler_failed` with truncated stderr.

### Hard exit gates (all met)

1. **Image-mode install works on a clean VM with no Go toolchain.** Verified locally; CI smoke leg (`compose-lint` job) validates both compose layouts on every PR.
2. **All four content-pack chains produce valid output end-to-end.** ~20 new unit tests cover each chain with stubbed fal.ai/ElevenLabs/Ghost.
3. **`helmdeck://image-models` lists 7 models.** Verified in `internal/mcp/resources_test.go`.
4. **T606a UI can run `image.generate` end-to-end.** Manual click-through plus full TypeScript strict-mode build green.
5. **T811 example pack round-trips through subprocess with audit-log parity to a Go pack.** 17 new tests via the self-exec pattern.

### Slipped to v0.13.0

- **T606a schema-derived form** — JSON Schema → React form (replaces the v0.12.0 MVP textarea)
- **T811 manifest format** — typed schemas via YAML sidecar (`#173`)
- **T811 egress sandbox** — confine subprocess pack network access (`#174`)
- Marketplace UI / install CLI — bundled with v0.13.0's T810

### Slipped to v1.0-rc1

- **#134 step 2** — the Helm chart itself
- arm64 sidecar image (still blocked on Marp upstream)

### Audience

Operators wanting Kubernetes prep; community contributors who want to write packs without Go; existing users who want covers/heroes for free.

### MCP Registry

The auto-publish workflow republishes the listing on `v*` tag push. Watch for the npm-publish race condition documented in `release.yml:118-157` — workflow_dispatch the `mcp-registry.yml` after npm publish completes if the first run fails with "package not found."

---

## v0.13.0 — Marketplace beta — ✅ Shipped 2026-05-15 {#v0130}

**Theme:** "Discover and install community packs."

### Shipped

**Marketplace track (the headline):**

- **T810 catalog endpoint ([#219](https://github.com/tosin2013/helmdeck/pull/219))** — `GET /api/v1/marketplace/catalog` + `POST /api/v1/marketplace/refresh`. Fetches `index.yaml` from `HELMDECK_MARKETPLACE_URL` (default `https://github.com/tosin2013/helmdeck-marketplace`) at boot; failed refresh preserves the previously-cached snapshot. Three URL shapes supported: `github.com/<owner>/<repo>`, direct raw URLs, `file:///` for air-gapped operators. `HELMDECK_MARKETPLACE_DISABLE=1` opts out.
- **T812 install/uninstall REST ([#220](https://github.com/tosin2013/helmdeck/pull/220))** — `POST /api/v1/marketplace/{install,uninstall}` + `GET /api/v1/marketplace/installed`. Hot-load: `git clone --depth=1 --filter=blob:none` the marketplace repo, copy `packs/<name>/` to `HELMDECK_PACKS_DIR`, register with the live `packs.Registry` — pack appears in `tools/list` immediately. `command`-handler packs only in beta; `builtin`/`composite`/`wasm` reject. Lands [ADR 038](adrs/038-marketplace-pack-execution-via-sidecar.md) — marketplace packs route through a dedicated `helmdeck-sidecar-marketplace` image (bash + jq + curl + python3 + Node 20) rather than the distroless control plane.
- **T813 `/marketplace` UI panel ([#221](https://github.com/tosin2013/helmdeck/pull/221))** — React panel with browse-by-category chips, free-text search, pack-detail dialog with schema preview + worked examples + trust badge, install/uninstall buttons with automatic `tools/list` cache invalidation, unsigned-pack confirmation per ADR 034. New `GET /api/v1/marketplace/packs/{name}` returns catalog entry + full `helmdeck-pack.yaml` manifest on demand (catalog endpoint deliberately doesn't pre-load every manifest).
- **Marketplace trust verification stage A ([#222](https://github.com/tosin2013/helmdeck/pull/222))** — replaces PR #220's structured stub with real deterministic SHA256 content-hash verification. Excludes `helmdeck-pack.yaml` from the hash (chicken-and-egg). Hard-rejects install on mismatch (removes materialized files). Stage B (full sigstore keyless cosign-verify) deferred to v1.0 hardening.
- **`helmdeck` CLI binary ([#223](https://github.com/tosin2013/helmdeck/pull/223))** — operator-facing CLI wrapping the marketplace endpoints: `pack list`, `pack marketplace [--refresh]`, `pack install <name>`, `pack uninstall <name>`, `pack installed`. Same env-var conventions as `helmdeck-mcp` (`HELMDECK_URL` + `HELMDECK_TOKEN`). `--json` for shell pipelines. Ships via goreleaser alongside `control-plane` + `helmdeck-mcp`. See [`docs/howto/use-the-helmdeck-cli.md`](howto/use-the-helmdeck-cli.md).
- **T814 community marketplace repo** — [`tosin2013/helmdeck-marketplace`](https://github.com/tosin2013/helmdeck-marketplace) seeded with three packs (`cmd.upper`, `ai.review`, `gif.make`) + maintainer-run `scripts/populate-trust-hashes.mjs` + CI `validate.yml` + `sign.yml`-with-`--check` gate.

**New built-in packs:**

- **`hyperframes.render` ([#200](https://github.com/tosin2013/helmdeck/issues/200))** — HTML/CSS/JS composition → deterministic MP4 via Chromium BeginFrame + ffmpeg using upstream [`hyperframes`](https://github.com/heygen-com/hyperframes) CLI in the new `helmdeck-sidecar-hyperframes` image. Composable sizing: `resolution` (`1080p`/`4k`) × `aspect_ratio` (`16:9`/`9:16`/`1:1`) resolves to one of six upstream presets. Mode-free audio: silent compositions produce silent MP4s; `<audio src>` produces narrated MP4s — chain `podcast.generate` → `hyperframes.render` by embedding the podcast's presigned URL. Short-form only (≤12 min, 512 MiB cap). Pack count 39 → 40.
- **`stock.search` ([#218](https://github.com/tosin2013/helmdeck/pull/218))** — Pexels-backed stock photo search; downloads top 1-4 results into the artifact store with per-photo attribution metadata. Same chained-input contract as `image.generate` — drops straight into `slides.render`/`slides.narrate`/`blog.publish`/`podcast.generate`/`hyperframes.render`. Engine-pluggable; `unsplash`/`pixabay` reserved for community PRs. Pack count 40 → 41.

**Quality + diagnostics:**

- **`slides.render` contrast guardrails ([#216](https://github.com/tosin2013/helmdeck/pull/216), closes [#202](https://github.com/tosin2013/helmdeck/issues/202))** — three-pronged fix: docs + agent skill teaching WCAG-AA 4.5:1; static contrast lint surfacing `section-background-without-nested-overrides` + `wcag-aa-text-contrast` warnings in the response; two curated embedded Marp themes (`helmdeck-dark`, `helmdeck-corporate`) declaring WCAG-AA colors for every nested element.
- **`provider_calls` diagnostic columns ([#183](https://github.com/tosin2013/helmdeck/issues/183))** — `job_id` (joins gateway audit to the pack-job that triggered the call), `finish_reason`, `raw_content_len`. Migration `0005_provider_calls_diagnostics.sql` via `ALTER TABLE ADD COLUMN` (O(1) metadata-only).
- **Subprocess pack manifest format ([#173](https://github.com/tosin2013/helmdeck/issues/173))** — operator-supplied command packs declare typed I/O schemas + execution overrides via a sibling `<basename>.helmdeck-pack.yaml`. Completes the v0.12.0 MVP. New how-to: [`docs/howto/build-subprocess-pack.md`](howto/build-subprocess-pack.md).
- **`blog.publish` artifact-first refactor ([#203](https://github.com/tosin2013/helmdeck/issues/203))** — `destination` is now optional, defaults to `"artifact"`. Ghost-targeted calls also save the body as an artifact by default (`also_save_artifact: false` to opt out). Ghost failures return a partial-success response (`status: "artifact_saved_ghost_failed"` + `ghost_error` + `artifact_key`) instead of losing the expensive prompt-expanded body.

### Architecture decisions captured

- **[ADR 034 — Pack marketplace](adrs/034-pack-marketplace.md)** — catalog + manifest + trust model + handler types. Written ahead of T810/T812/T813 implementation.
- **[ADR 037 — Upstream package version management](adrs/037-upstream-package-version-management.md)** — exact pins + CLI-surface sentinel + Dependabot. Surfaced by the hyperframes-npm-pin incident; now a project-wide discipline.
- **[ADR 038 — Marketplace pack execution via sidecar](adrs/038-marketplace-pack-execution-via-sidecar.md)** — control plane is distroless-static; marketplace packs need bash/jq/python/node; therefore packs route through `helmdeck-sidecar-marketplace` via `ec.Exec` rather than in-process `exec.CommandContext`.

### Slipped to v1.x

- **Stage B trust verification** — full sigstore keyless cosign-verify of the signer identity. Captures malicious-author-modifying-the-manifest, which stage A doesn't.
- **`hyperframes.render` long-form** ([#201](https://github.com/tosin2013/helmdeck/issues/201)) — multi-GB MP4 streaming via `ArtifactStore.PutStream`. Defers to the v1.x artifact-streaming track.
- **T606a schema-derived test-runner form** — JSON Schema → React form rendering. The v0.12.0 MVP textarea ships in v0.13.0 unchanged; schema-derived form lands later.
- **Multi-arch `helmdeck-sidecar-marketplace`** — amd64 only at v0.13.0; multi-arch follows the base sidecar's track.

### Audience

Operators looking for "an existing pack for X" before writing one. Designed to land before K8s so community surface area precedes enterprise surface area.

### MCP Registry

The auto-publish workflow republishes the listing on `v*` tag push. After tagging, verify at `https://registry.modelcontextprotocol.io/v0/servers?search=io.github.tosin2013%2Fhelmdeck` (expect `version: 0.13.0`, `isLatest: true`).

---

## v0.13.1 — Post-v0.13.0 cleanup — ✅ Shipped 2026-05-18 {#v0131}

**Theme:** Bug-cleanup release. No feature changes.

**Ships:**

- [#229](https://github.com/tosin2013/helmdeck/issues/229) — `deploy/compose/.env.example` missing `HELMDECK_FAL_KEY` and `HELMDECK_PEXELS_API_KEY`
- [#230](https://github.com/tosin2013/helmdeck/issues/230) — pexels-key vault auto-hydration missing (CHANGELOG advertises it, `internal/vault/hydrate.go` doesn't register it)
- [#231](https://github.com/tosin2013/helmdeck/issues/231) — `compose.firecrawl.yml` healthcheck uses `wget` against an image with neither `wget` nor `curl`
- [#232](https://github.com/tosin2013/helmdeck/issues/232) — `repo.fetch`'s `clone_path` invisible to subsequent `fs.*` / `cmd.run` / `repo.map` calls

**Out:**

- Anything feature-level. Patch-release discipline — same shape as [v0.12.1](#v0121).

**Discipline call:** [#231](https://github.com/tosin2013/helmdeck/issues/231) is the first to defer if v0.13.1 needs to ship faster than expected — it affects health UI only, not request serving.

---

## v0.13.2 — Hot-patch for v0.13.1 missing control-plane image — ✅ Shipped 2026-05-23 {#v0132}

**Theme:** v0.13.1 shipped without `ghcr.io/tosin2013/helmdeck:0.13.1` because the `Publish control-plane image` job in the Release workflow failed at `cd web && npm run build`. Dependabot [#247](https://github.com/tosin2013/helmdeck/pull/247) had landed three breaking majors (Vite 6 → 8, TypeScript 5 → 6, lucide-react 0 → 1) between the release branch cut and the tag push, and the `CI` workflow never builds `web/` — only `Release` does. Goreleaser binaries, the bridge image, and `@helmdeck/mcp-bridge@0.13.1` on npm shipped fine; this release closes the asymmetry.

**Ships:**

- [#250](https://github.com/tosin2013/helmdeck/pull/250) — Vite 8 / TS 6 / lucide-react 1 web build unblock. `manualChunks` (Rollup) → `codeSplitting.groups` (Rolldown); `baseUrl` dropped, new `web/src/vite-env.d.ts`; `Github` icon → `GitBranch`.

**Out:**

- Anything else. Strict hot-patch discipline — same shape as [v0.12.1's release-image regression patch](#v0121).

**Follow-ups discovered (not in v0.13.2, will file separately):**

- **CI gap**: the `CI` workflow doesn't build `web/`. Only the `Release` workflow does, so any breaking change to the web toolchain ships silently until the next tag. Fix: gate every PR to `main` on a `web build` step.
- **Dependabot ergonomics**: #247 grouped 14 deps including three majors and auto-merged. Three majors should not land in one group — re-bucket `web-npm` so majors land one-at-a-time.

---

## v0.14.0 — Autonomous code-fix + ADR 037 fully enforced — ✅ Shipped 2026-05-26 {#v0140}

**Theme:** `swe.solve` headline + close out [ADR 037](adrs/037-upstream-package-version-management.md) across every sidecar Dockerfile.

**Ships:**

- [#233](https://github.com/tosin2013/helmdeck/issues/233) — `swe.solve` epic: Phase 1 (`HelmdeckEnvironment` adapter, ✅ shipped #265) + Phase 3 (`swe.solve` Go pack handler, ✅ shipped #271) + Phase 4 (trajectory artifact in Garage S3, with Phase 3) + Phase 6 (GitHub-issue auto-trigger via ADR 033 — label an issue, get a PR; posts the result back as a comment)
- [#253](https://github.com/tosin2013/helmdeck/issues/253) — post-install/upgrade integration smoke check via OpenClaw round-trip (✅ shipped #263)
- [#212](https://github.com/tosin2013/helmdeck/issues/212)–[#215](https://github.com/tosin2013/helmdeck/issues/215) — ADR 037 fully enforced: dependabot, exact pins, CLI-surface sentinels, docs (✅ shipped #240–#243)
- [#248](https://github.com/tosin2013/helmdeck/issues/248) — ADR 037 follow-up cleanups: drop `marp --stdin`, fix `--html` format spec, pinned global `playwright-mcp` bin in the sidecar entrypoint (✅ shipped #264)
- [ADR 039](adrs/039-universal-memory-delivery-layer.md) — Universal Memory delivery layer (refines [ADR 029](adrs/029-four-tier-agent-memory-api.md)): **first implementation shipped** — pluggable `MemoryStore` (SQLite default, AES-256-GCM at rest), the `ec.Memory` engine seam + namespace model, `Context()` aggregation (#260), and the `github.list_issues` read-through cache exemplar (#258). Default-OFF and additive: packs without opt-in and deployments without `HELMDECK_MEMORY_KEY` behave exactly as before. Tracked in epic [#254](https://github.com/tosin2013/helmdeck/issues/254) (#255/#256/#257/#258/#260).
- [#259](https://github.com/tosin2013/helmdeck/issues/259) / [ADR 040](adrs/040-persistent-repos-volume.md) — Persistent repos volume + cross-session clone reuse: `repo.fetch` (and `swe.solve`) clone into a per-caller path on a shared `helmdeck-repos` volume and `git fetch` instead of re-cloning on a repeat, with a persistent per-language dependency cache (`.hdcache`) and a GC janitor (TTL + size cap). Unblocked by #232. Default-OFF (no volume ⇒ ephemeral `/tmp` clones); enabled by default in the bundled Compose via `HELMDECK_PERSISTENT_REPOS`.

**Out:**

- Universal memory **deferred tiers**: Redis-backed Episodic and the pgvector/Semantic tier remain out per ADR 039 (the pluggable `MemoryStore` interface keeps the door open). The community validation middleware (#268) is the next seam consumer.
- `swe.solve` remaining phases: **Phase 5** (OTel spans per agent step), **Phase 7** (A2A skill exposure via ADR 026), **Phase 8** (procedural-memory pack promotion via ADR 029). Phases 7–8 lean on ADRs currently `Status: Proposed` — premature to commit. (Phase 4 trajectory storage and Phase 6 GitHub-issue auto-trigger landed in this release — ADR 033 was already `Accepted`.)

**Status:** the ADR 037 quad (#212–#215) shipped together as planned, with the #248 cleanups completing the enforcement. `swe.solve` Phases 1, 3, 4, and 6 are in (adapter, pack, trajectory artifact, GitHub-issue auto-trigger), alongside the universal-memory layer (ADR 039) and persistent repos (ADR 040). #232 is resolved.

**Blocked by:** [#232](https://github.com/tosin2013/helmdeck/issues/232) — Phase 3 of `swe.solve` requires `repo.fetch → fs.*` working in a session.

---

## v0.15.0 — Pipelines as a first-class resource — ✅ Shipped 2026-05-26 {#v0150}

**Theme:** A pipeline — a stored, named, ordered sequence of pack steps — becomes a first-class resource any actor can create, run, and inspect. helmdeck stops being only a tool server and starts owning the workflow.

**Ships:**

- [ADR 041](adrs/041-pipelines-as-first-class-resource.md) — **Pipelines as a first-class resource** (runnable slice): a new `internal/pipelines` package (SQLite-persisted definitions + run history, a sequential runner reusing `Engine.Execute`, `${{ steps.X.output.field }}` / `${{ inputs.* }}` dot-notation templating, automatic `_session_id` threading), REST CRUD + async run + run-history at `/api/v1/pipelines`, and `helmdeck__pipeline-{list,get,create,run,run-status}` MCP tools so any connected agent (OpenClaw, Gemini CLI, Claude Code) can build and run pipelines conversationally.
- **~13 built-in starter pipelines** auto-seeded at startup and runnable out of the box — including `content.ground → slides.render` (grounded deck), `content.ground → blog.publish` (grounded blog), `research.deep → {slides,podcast,blog}`, `web.scrape → content.ground → blog.publish`, and `repo.fetch → {slides.narrate, podcast.generate}` (clone a repo → media about it). Provider-dependent starters degrade gracefully (stable premade voice + `allow_silent_output`); a starter whose packs aren't registered is skip-and-logged.
- `podcast.generate` now surfaces a presigned `audio_url` in its output (from the artifact store), unlocking a clean `podcast.generate → hyperframes.render` narrated-video chain (embed the URL in the composition's `<audio src>`).
- Migration `0007_pipelines.sql` (additive: `pipelines` + `pipeline_runs` tables, auto-applied).
- **Management UI `/pipelines` panel** — pulled forward from v1.2: list built-in + agent-created pipelines, trigger a run with JSON inputs, and watch run status/history poll live (pending → running → succeeded/failed, with per-step status) — operators see what agents build via the MCP tools.

**Out (deferred follow-ups, seams in place):**

- Cron + webhook pipeline triggers (the runner is HTTP-decoupled — ADR 033's receiver and a future scheduler call the same `StartRun`). A2A pipeline-management skill and "promote a successful run from the audit log into a pipeline" follow per ADR 041's sequencing (v1.0→v1.3).

**Status:** the v0.15.0 slice is the REST + MCP + runner + starters foundation; triggers/UI/audit-promote are explicitly later so the data model lands correct first.

---

## v0.16.0 — Correctness + housekeeping — ✅ Shipped 2026-05-27 {#v0160}

**Theme:** Sharp edges off the pipeline work — grounding stops silently truncating long slide decks, artifacts become deletable on demand, and a new `email.send` pack lands.

**Ships:**

- **`content.ground` rewrite no longer truncates long documents** — the optional full-document rewrite was hard-capped at 2048 output tokens, silently cutting off any input larger than the test fixtures (a 20–25 slide deck lost its back half when run through `builtin.grounded-deck`). The rewrite budget now scales with the input (cap 8192), a ceiling-hit rewrite (`finish_reason: length`) is discarded in favor of the structure-preserving citation-only version, and the prompt is told to keep every `---` separator. `grounded_text` is now always emitted so pipeline steps referencing it never fail. The deck pipelines (`builtin.grounded-deck`, `builtin.research-ground-deck`) now ground with `rewrite: false`. (#290)
- **Manual artifact deletion** — `DELETE /api/v1/artifacts/{key}` + a delete button in the Management UI Artifact Explorer; previously only the TTL janitor could delete. (#290)
- **`email.send` pack** (`helmdeck__email-send`) — send a transactional email via Resend (vault `resend-api-key`); 44 packs in-tree. (#289)
- **Prompt-template reference pages** at `/reference/prompt-templates/` — a copy-and-fill `{{VARIABLE}}` prompt for every pack and pipeline. (#288)

**Upgrade:** no migrations, no breaking changes; `grounded_text` is an additive output field. Clean in-place Compose upgrade from v0.15.0.

---

## v1.0.0-rc1 — Kubernetes preview (planned) {#v100rc1}

**Theme:** "Helm install works; production hardening pending."

### Hard prerequisite (must land before any rc1 work)

- **[#134](https://github.com/tosin2013/helmdeck/issues/134)** — unified install paths so `compose.yaml` and the Helm chart reference the same versioned GHCR tags (`ghcr.io/tosin2013/helmdeck:0.X.Y`) instead of the build-time-only `:dev` tag. The Helm chart cannot ship referencing `:dev` (operators have no source tree), so this gates rc1.

### Ships (planned)

- **T701** `client-go` `SessionRuntime` backend.
- **T702** Helm chart `charts/baas-platform/`.
- **T703** PostgreSQL StatefulSet sub-chart (Bitnami) with `database.external.enabled` toggle.
- **T704** Session pod template (seccomp, restartPolicy: Never, memory-backed `/dev/shm`).

Operators can install on GKE/EKS but production-hardening items (NetworkPolicy, isolation tiers, TLS, audit) are not gates.

---

## v1.0.0 — Kubernetes & GA (Week 22)
**Theme:** "Production."

> **Milestone:** [v1.0 — Kubernetes & GA (Phase 7)](MILESTONES.md#milestone-v10--kubernetes--ga-phase-7) · **Tasks:** [Phase 7](TASKS.md#phase-7--kubernetes--helm--production-hardening-weeks-2122)

### Ships
- `client-go` `SessionRuntime` backend
- **Helm chart** `charts/baas-platform/` with all toggles
- Two-namespace layout (`baas-system` / `baas-sessions`) + scoped RBAC
- Session pod template (seccomp, restartPolicy: Never, memory-backed `/dev/shm`)
- NetworkPolicies (ingress + egress)
- KEDA ScaledObject on `baas_queued_session_requests` + utilization
- `browser-pool-warmup` Deployment for cold-start elimination
- **`isolation.level`**: standard (Docker) / enhanced (gVisor) / maximum (Firecracker via RuntimeClass)
- cert-manager + Ingress-NGINX TLS termination
- OTel Collector DaemonSet
- External Secrets Operator integration
- Argo CD reference manifest in `deploy/gitops/`

### Hard exit gates
- Helm install on a fresh GKE or EKS cluster passes the same smoke matrix as Compose
- Load test: 100 concurrent sessions, 24h soak, ≤150 MB control plane footprint, ≤5 s recovery
- gVisor tier passes the smoke matrix
- External security audit clean

### Audience
**General availability.** Tag `v1.0.0`. Announce.

---

## v1.x — Post-GA Innovation Tracks

Released as feature-gated minors as they stabilize. No hard sequence.

| Version | Headline feature | ADR |
| :--- | :--- | :--- |
| v1.1 | WASM Executor for sandboxed third-party packs | 012, 024 |
| v1.2 | Four-tier Memory API (Working/Episodic/Semantic/Procedural) | 029 |
| v1.3 | Procedural→Pack promotion UI | 024, 029 |
| v1.4 | WebRTC live session streaming | 028 |
| v1.5 | WebMCP detection and preferential routing | 027 |
| v1.6 | Pre-packaged Chrome DevTools MCP / Playwright MCP entries | 006 |
| v1.7 | Firecracker production hardening (bare-metal node guidance) | 011 |
| v1.x | Lightpanda alternate browser engine | 001 |
| v1.x | **NVIDIA OpenShell integration** — sidecars in MicroVMs + L7 policy | 011, 036 (planned) |
| v1.x | **Long-form artifact streaming** ([#201](https://github.com/tosin2013/helmdeck/issues/201)) — `ArtifactStore.PutStream` for multi-GB MP4/audio outputs (unblocks `hyperframes.render` long-form, podcast videos 30–60 min) | 037 (planned) |

## v1.x — Enterprise integration tracks {#enterprise-integration-tracks}

Post-GA themes that compose with the innovation tracks above but are scoped as community-led integration work rather than core platform features. Each is broken into independently-mergeable phases tracked as separate GitHub issues so contributors can pick up one phase without blocking on the others.

### NVIDIA OpenShell integration

**Theme:** "Helmdeck sidecars inside hardware-isolated, policy-governed sandboxes."

[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) is a Rust-based safe runtime for autonomous AI agents — declarative YAML policies, OPA-enforced L7 network rules, libkrun MicroVM compute driver, Landlock filesystem isolation. Helmdeck's pack engine operates at the tool layer; OpenShell operates at the sandbox layer. The integration is non-duplicative — each project covers a layer the other doesn't.

Canonical design doc: [`docs/integrations/openshell.md`](integrations/openshell.md).

**Four phases (all post-v1.0):**

1. **Shallow integration** — run the helmdeck control plane inside an OpenShell sandbox. Docs + example policy only. No helmdeck code changes. Good first issue.
2. **Agent sandbox integration** — run the agent (OpenClaw / Claude Code / Hermes) inside an OpenShell sandbox with egress restricted to helmdeck MCP + `inference.local`. Docs + example policy. Extends `openclaw.md`'s topology section. Good first issue.
3. **`OpenShellSessionRuntime` backend** — third `SessionRuntime` implementation (alongside `DockerSessionRuntime` and v1.0's `KubernetesSessionRuntime`) that routes sidecar lifecycle through the OpenShell Gateway API. Hardware-isolated browser / Python / Node sidecars. Help wanted; multi-week Go work. Lands a new ADR (036).
4. **Correlated observability** — join helmdeck's OTel GenAI traces with OpenShell's OCSF security events on the sandbox ID. End-to-end traces from MCP tool call → policy decision → outbound HTTP. Help wanted; OTel collector + OPA experience.

**Why post-v1.0:** Phase 3 modifies `SessionRuntime`, the seam between helmdeck's pack engine and execution backends. Touching it pre-GA forks the v1.0 test matrix; post-GA it's purely additive. Plus OpenShell is alpha — production deployments need a stable OpenShell Gateway API first.

**Gating:** v1.0 ships first. Phases 1 and 2 can land as docs-only PRs once both projects are GA. Phases 3 and 4 wait on a stable OpenShell Gateway API (no calendar commitment).

---

## Versioning policy

- **Pre-1.0:** every minor may break compatibility; document in release notes.
- **1.0 onward:** SemVer. Breaking pack-schema changes require a new pack version under `/api/v1/packs/{name}/v{n}` (ADR 024); the previous version stays callable for at least one full minor cycle.
- **Bridge ↔ control plane:** version-pinned. The bridge logs a deprecation warning when older than the platform's minimum recommended (ADR 030).

## Distribution channels at GA

| Artifact | Channel |
| :--- | :--- |
| Control plane image | `ghcr.io/tosin2013/helmdeck:vX.Y.Z` |
| Browser sidecar image | `ghcr.io/tosin2013/helmdeck-sidecar:vX.Y.Z` |
| Helm chart | `oci://ghcr.io/tosin2013/charts/baas-platform` |
| `helmdeck-mcp` bridge | Homebrew, Scoop, npm, OCI, GH Releases |
| Compose stack | `deploy/compose/compose.yaml` in repo |
