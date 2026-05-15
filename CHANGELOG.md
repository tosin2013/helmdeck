# Changelog

All notable changes to helmdeck are documented here. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) starting
at v1.0.0; pre-1.0 minor versions may break compatibility (documented per release).

For the forward-looking *release plan* — what is targeted for upcoming versions
and the hard exit gates for each — see
[`docs/RELEASES.md`](https://github.com/tosin2013/helmdeck/blob/main/docs/RELEASES.md).

## [Unreleased]

### Added

- **`slides.render` contrast guardrails (#202)** — three-pronged fix for "LLM picks a custom palette that produces unreadable slides" (the dark-blue-section-with-default-light-tables reproducer). **(A) Docs + agent skill**: new "Color contrast best practices" section in [`docs/reference/packs/slides/render.md`](docs/reference/packs/slides/render.md) + an updated `slides.render` entry in `skills/helmdeck/SKILL.md` teach the WCAG-AA 4.5:1 rule and the "override every nested element when you change `section { background }`" checklist. **(B) Static contrast lint**: the pack now parses the markdown's frontmatter `style:` block and embedded `<style>` tags before render, flagging two anti-patterns — `section-background-without-nested-overrides` (the reproducer pattern) and `wcag-aa-text-contrast` (any single rule whose hex `color`/`background-color` pair contrasts below 4.5:1). Warnings surface in the response's new `warnings: [{rule, selector, recommendation}]` array — informational, not errors; the render still succeeds. **(C) Curated helmdeck themes**: two embedded Marp themes ship with the control-plane binary — `helmdeck-dark` (slate/sky palette, modern technical look) and `helmdeck-corporate` (white/blue palette, business deck). Both declare WCAG-AA colors for every nested element type explicitly. The agent picks one via `theme: helmdeck-dark` in the frontmatter; the pack uploads the embedded CSS to the sidecar and passes `--theme-set` to marp automatically. Response carries `curated_theme_used` so callers can confirm the theme applied. Source: [`internal/packs/builtin/themes/`](internal/packs/builtin/themes/).
- **`hyperframes.render` built-in pack (#200)** — HTML/CSS/JS composition → deterministic MP4 via Chromium BeginFrame + ffmpeg using the upstream [`hyperframes` CLI](https://github.com/heygen-com/hyperframes), running in the new `helmdeck-sidecar-hyperframes` image (env override `HELMDECK_SIDECAR_HYPERFRAMES`; Node 22 + ffmpeg on top of the base sidecar). Sizing surface is composable: `resolution` (`1080p` / `4k`) × `aspect_ratio` (`16:9` YouTube standard, `9:16` Shorts/TikTok/Reels, `1:1` Instagram feed) resolves to one of six upstream CLI presets (`landscape`/`portrait`/`square` ± `-4k`). Composition must be authored at the matching aspect ratio — upstream's `--resolution` flag is an integer-multiple upscale knob, not a dimension setter. Audio handling is mode-free: a composition with no `<audio>` tag produces a silent MP4; an inline `<audio src>` produces a narrated MP4 — chain `podcast.generate` → `hyperframes.render` by embedding the podcast's presigned audio URL in the composition's `<audio src>` and the audio track flows through automatically. Short-form only (≤12 min, 512 MiB cap; oversize rejects as `CodeHandlerFailed` pointing at #201 for the v1.x long-form streaming track). Pack is `Async: true`, 4 GiB session memory, 60-minute timeout. See [`docs/reference/packs/hyperframes/render.md`](docs/reference/packs/hyperframes/render.md), [`docs/SIDECAR-LANGUAGES.md`](docs/SIDECAR-LANGUAGES.md). Pack count: **39 → 40**.
- **`provider_calls` diagnostic columns (#183)** — three new columns on the gateway audit table for diagnosing failed LLM-backed pack calls in a single SQL query instead of timestamp-matching `ts` against the job's `ended_at`: `job_id` (joins back to the pack job that triggered the call; indexed), `finish_reason` (provider-reported `stop`/`length`/`tool_calls`/`content_filter`/…), `raw_content_len` (bytes in `choices[0].message.content` after trim — instantly distinguishes "model returned no visible text" from "model returned text the pack couldn't parse"). Migration `0005_provider_calls_diagnostics.sql` adds columns via SQLite `ALTER TABLE ADD COLUMN` (O(1) metadata-only, safe on multi-million-row tables). The async-job runner (`internal/mcp/jobs.go`) stamps the pack job ID on the dispatch context via the new `gateway.WithJobID` helper so existing per-pack call sites don't need touching. Existing rows keep NULL `job_id` / NULL `finish_reason` / `0 raw_content_len` — no backfill required.
- **Subprocess pack manifest format (#173)** — operator-supplied command packs (`$HELMDECK_COMMAND_PACKS_DIR`) can now declare typed input/output schemas + execution overrides via a sibling `<basename>.helmdeck-pack.yaml` file. The manifest carries `name`, `version`, `description`, `author`, `input_schema`/`output_schema` blocks (BasicSchema-compatible: `string`, `number`, `boolean`, `object`, `array`), `timeout_s`, `max_output_bytes`, and an `env` list. Missing manifest falls back to passthrough (the v0.12.x MVP behavior); malformed manifest skips the pack entirely with an error logged. New how-to: [`docs/howto/build-subprocess-pack.md`](docs/howto/build-subprocess-pack.md).

### Changed

- **`blog.publish` artifact-first refactor (#203)** — `destination` is now optional and defaults to `"artifact"`. When `destination="ghost"`, the pack ALSO saves the post body as an artifact (the safety net) by default; a new `also_save_artifact: false` input restores the pre-#203 ghost-only behaviour. Ghost failures with the safety net enabled return a partial-success response (`status: "artifact_saved_ghost_failed"` + `ghost_error` + `artifact_key`/`artifact_url`) instead of a hard error — agents can retry the Ghost step against the saved artifact without paying for prompt expansion again. Strictly additive schema change; existing callers that send `destination="ghost"` now also see `artifact_key`/`artifact_url`/`size` in the response. See [`docs/reference/packs/blog/publish.md`](docs/reference/packs/blog/publish.md) §Partial success.

## [0.12.1] - 2026-05-13

**Theme:** hot-patch for the v0.12.0 release-image regression + three reliability bugs found within hours of v0.12.0 shipping.

The release-blocker (#180) is the dominant fix: every fresh `docker pull ghcr.io/tosin2013/helmdeck:0.12.0` user saw a blank Management UI because the embedded `web/dist/index.html` referenced asset hashes not present in the image. Root cause was a workflow sequencing bug — the release workflow never ran `npm run build` before bundling the docker image, so the image baked in whatever stale `index.html` was last committed. The fix adds a Node + web-build step before `docker/build-push-action` plus a verify step that fails the release loud if the rebuilt `index.html` references assets that aren't on disk. Defense in depth: if v0.12.0's release had run this check, the broken image would never have shipped.

The other three are smaller but each pinches at a real operator-visible failure mode introduced (or surfaced) by v0.12.0's content-pack push.

### Fixed

- **Release image's blank Management UI on fresh pulls (#180)** — `.github/workflows/release.yml` now runs `cd web && npm ci && npm run build` before `docker/build-push-action`, then verifies that every asset hash referenced from the rebuilt `web/dist/index.html` exists in `web/dist/assets/`. Closes #180. Doesn't change `web/dist/`'s gitignore status — the workflow-step fix is the architecturally correct choice (committing the dist folder would create merge churn on every `web/src/` PR).
- **`firecrawl-rabbitmq` cold-boot race (#181)** — `deploy/compose/compose.firecrawl.yml` bumps the rabbitmq healthcheck's `start_period: 15s` → `60s`. RabbitMQ's Erlang VM + mnesia init takes 30-60s on alpine; the shorter window exhausted retries before the node was ready → container reported unhealthy → `helmdeck-firecrawl` (correctly waiting via `depends_on: condition: service_healthy`) never started → operator had to `docker compose up` again. 60s aligns with `firecrawl-searxng`'s precedent in the same file. Tutorial note added that firecrawl overlay cold-boot takes ~60-90s. Closes #181.
- **`content.ground` truncated-JSON failure mode (#179)** — the hard-coded 1024-token completion cap was too tight for the structured claim-plan JSON the extractor returns (~750 tokens for 5 claims left ~270 tokens of headroom; weak models or large posts blew through it). Default bumped to **2048** (~1200 tokens of output budget); new optional `max_completion_tokens` input on `contentGroundInput` lets operators raise the cap up to 8192. Over-cap requests now reject with `CodeInvalidInput` (runaway-cost guard) instead of silently truncating downstream. Closes #179.
- **`content.ground` silent degradation when Firecrawl unreachable (#182)** — the per-claim grounding loop swallowed `callFirecrawlSearch` transport errors silently, producing an empty-success "no sources found" output instead of surfacing the underlying reachability issue. Now tracks `firecrawlCalls` vs `firecrawlErrors` separately; when 100% of attempted calls hit transport errors, the handler returns `CodeHandlerFailed` with a message pointing at the firecrawl service URL. Partial-success runs preserved: claims with "search succeeded but no usable source" still land under `skipped` and the run completes. Mirrors the v0.11 narration contract's fail-loud-on-missing-dependency pattern. Closes #182.

### Tests

- 5 new tests in `content_ground_test.go` — `DefaultMaxTokens`, `MaxCompletionTokensOverride`, `MaxCompletionTokensOverCap`, `FirecrawlAllErrors`, `FirecrawlPartialErrorsSucceed`.

### Changed

- `skills/helmdeck/SKILL.md` — refreshed catalog (#184). Now correctly advertises 39 packs (was stamped at pre-v0.10.2 commit `24bd0c3` advertising 36 — missing `blog.publish`, `podcast.generate`, `image.generate`). Frontmatter `helmdeckVersion` bumped to `v0.12.0`. Brings SKILL.md in line with `docs/integrations/SKILLS.md`, which was already current.
- `website/docusaurus.config.ts` — sitemap ignores `/blog/tags`, `/blog/tags/**`, `/blog/archive`, `/blog/authors` to concentrate Google crawl budget on content pages (137 URLs → 122). Filed as SEO follow-up after Search Console reported 61 URLs in "Discovered – currently not indexed" with crawl timestamp `1969-12-31` (never crawled). Pages still render at their URLs — they're just no longer advertised in the sitemap.

## [0.12.0] - 2026-05-12

**Theme:** content-pack image chaining + v1.0 install-path unblocker + pack-authoring MVP.

A bundled release covering four threads that lined up after v0.11.0: chain `image.generate` into the three content packs (#146, unblocked by v0.11.0's #71); `helmdeck://image-models` MCP resource (#158, sibling to #146); unified install paths (#134 step 1, P1 blocker for v1.0.0-rc1); and the originally-planned Pack Authoring MVP (T606a UI + T811 subprocess pack type).

The narrative: covers come for free, the install path becomes Kubernetes-ready, and pack-authoring grows up — operators with no Go toolchain can install via pulled images, and pack authors with no Go can ship in any language via subprocess packs.

### Added

- **Content-pack image chaining (#146)** — additive convenience syntax across four packs, all backed by a shared `RunImageGen` entrypoint extracted from `internal/packs/builtin/image_generate.go`:
  - **`podcast.generate` `cover_image: bool`** — auto-generates podcast cover artwork via `image.generate`; output gains `cover_image_artifact_key` + `cover_image_model_used`. Optional `cover_image_model` override (default `fal-ai/flux/schnell`).
  - **`slides.render` `hero_image_prompt: string`** — auto-generates hero artwork; base64-inlined as `<img data:image/png;base64,…>` before slide 1 (after Marp frontmatter when present). Inline bytes avoid Marp needing network access inside the sidecar.
  - **`slides.narrate` `hero_image_prompt: string`** — same as `slides.render` but inlined INTO slide 1 (no `---` separator) so the per-slide TTS pipeline still sees a populated narrated slide.
  - **`blog.publish` `feature_image_artifact_key` + `hero_image: bool`** — operator-supplied artifact OR auto-generate from the post title. For Ghost destination, uploads via `/ghost/api/admin/images/upload/` (multipart, same JWT) then stamps the returned URL into the post's `feature_image` field. Artifact-mode writes a sidecar `<slug>-cover.png`.
- **`helmdeck://image-models` MCP resource (#158)** — mirrors `helmdeck://voices` (shipped v0.11.0). Curated in-tree catalog of 7 fal.ai models (flux/schnell, flux/dev, flux-pro/v1.1, fast-sdxl, flux-realism, recraft-v3, ideogram/v2) with cost, p50 latency, supports-seed, supports-image-size, max resolution, capability tags, and one-sentence trade-off notes. Backed by new `internal/imagemodels` package.
- **`fal-key` in vault env-hydrate (#158)** — closes the consistency gap `image_generate.go:74` has advertised since v0.11.0 ("auto-hydrated to vault as 'fal-key' once #142 lands"). `HELMDECK_FAL_KEY` now imports into the vault under `fal-key` on startup, same shape as `elevenlabs-key`.
- **`deploy/compose/compose.build.yaml` overlay (#134 step 1)** — operators choose between image-mode (just `compose.yaml`, pulls `ghcr.io/tosin2013/helmdeck:${HELMDECK_VERSION:-latest}`) and source-build (base + this overlay, builds locally). Compose's deep-merge picks `build:` when both are present, so the same `image:` tag becomes the local build's name.
- **`scripts/install.sh --image-mode` flag (#134 step 1)** — pulls pre-built images instead of building from source. Implies `--no-build`. Skips host Go / Node / `make` preflight checks — the path needs only Docker, `openssl`, `curl`. Pin reproducible deploys via `HELMDECK_VERSION=0.12.0` in `.env.local`.
- **Pack Test Runner UI MVP (T606a)** — click any pack row in `/packs` → modal opens with a JSON textarea + Submit. POSTs to `/api/v1/packs/{name}` and renders the response (duration, cost hint when present, full JSON). Schema-derived form rendering ships in v0.13.0; this MVP unblocks "no UI today."
- **Subprocess pack type (T811 MVP)** — `packs.NewCommandPack(name, version, description, inSchema, outSchema, spec)` constructor turns any executable into a pack via the stdin-JSON / stdout-JSON protocol. Operator-supplied packs auto-register from `$HELMDECK_COMMAND_PACKS_DIR` (one pack per executable, named `cmd.<basename>`). Pack authors can now ship in any language — Python, Node, Bash, Rust — without a Go toolchain dependency.

### Changed

- **`deploy/compose/compose.yaml` is now image-mode by default (#134 step 1)** — `build:` blocks stripped from the base file; `control-plane` and `sidecar-warm` pin `ghcr.io/tosin2013/helmdeck[-sidecar]:${HELMDECK_VERSION:-latest}`. Operators wanting source-build layer in `compose.build.yaml` via `docker compose -f compose.yaml -f compose.build.yaml`. The Helm chart (v1.0-rc1) will reuse the same versioned-tag convention.
- **`docs/tutorials/install-cli.md`** — adds "Pick your install mode" section with side-by-side prerequisites for image-mode (Docker only) vs source-build (Docker + Go + Node + `make`).
- **`docs/howto/upgrade-helmdeck.md` §2 splits into Path A (image-mode) + Path B (source-build)** — operators on a fresh box can `git clone && ./scripts/install.sh --image-mode` and skip the Go toolchain entirely.
- **`SlidesRender(v, eg)` signature** — was `SlidesRender()`; now takes vault + egress for `RunImageGen` access. `cmd/control-plane/main.go` updated to pass `vaultStore, egressGuard`.
- **`SlidesNarrate(d, vs, eg)` signature** — gained third `eg` parameter for the same reason.

### Tests

~50 new tests across the bundle. Highlights:

- `podcast.generate` cover-image happy path + dry-run-skips-cover + model override (3 tests)
- `slides.render` hero-image insertion (after frontmatter / no frontmatter / model override / no-fal-credential fails loud), empty-prompt skips, mermaid-coexistence (5 tests)
- `slides.narrate` hero inlined into slide 1 + dry-run skips (2 tests)
- `blog.publish` artifact + ghost feature-image paths, supplied-key + auto-gen, mutual-exclusion validation (4 tests)
- `helmdeck://image-models` resource list/read/unwired + catalog shape + defensive copy (6 tests)
- Subprocess pack via test-binary self-exec: happy path, transform, non-zero exit + stderr, non-JSON stdout, empty stdout, timeout, missing path/binary, raw-binary sniff, OutputSchema vs handler boundary, capped-writer truncation (11 tests)
- Subprocess pack dir-loader: empty/nonexistent dir, executable discovery, non-executable skip, basename sanitization (6 tests)

### Fixed

- **`image_generate.go:74` consistency gap** — the doc string promised `fal-key` auto-hydration "once #142 lands"; #142 shipped v0.11.0 but the `WellKnownEnvCredentials` entry was missing. Now added.

### Out of scope (slipped to v0.13.0 / v1.0-rc1)

- **#134 step 2** — the Helm chart itself ships with v1.0-rc1.
- **T606a schema-derived form** — JSON Schema → React form rendering; v0.13.0.
- **T811 manifest format** — typed schemas via YAML sidecar (`#173`); v0.13.0.
- **T811 egress sandbox** — confine subprocess pack network access (`#174`); v0.13.0.
- **arm64 sidecar image** — still blocked on Marp's amd64-only upstream tarball.

### MCP Registry

The auto-publish workflow (`.github/workflows/mcp-registry.yml`) republishes the listing on `v*` tag push. After tagging, verify at `https://registry.modelcontextprotocol.io/v0/servers/io.github.tosin2013/helmdeck` (expect `version: 0.12.0`, `isLatest: true`). Watch for the npm-publish race condition documented in `release.yml:118-157` — workflow_dispatch the `mcp-registry.yml` after npm publish completes if the first run fails with "package not found."

## [0.11.0] - 2026-05-10

**Theme:** podcast/slides UX hardening + onboarding fixes + image generation.

A coherent feature release that addresses 9 issues filed during a v0.10.2 OpenClaw integration: the new content packs work, but their first-run UX assumed you already knew the conventions. Silent MP3s when the credential name is wrong, hardcoded `/root/openclaw` paths, blocking Go preflight on the docker-only path, no voice discovery, no cost preview — all fixed.

The vault env-hydrate fix (#142) is the load-bearing piece: it root-causes the silent-fallback class of bug, not just the ElevenLabs instance. Pairing #138 (the per-pack contract change) with #142 (the platform fix) closes the bug class.

### Added

- **`image.generate` pack (#71)** — text → image via fal.ai's synchronous `fal.run` endpoint. Default model `fal-ai/flux/schnell` (~$0.003/image, 1-3s). 1-4 images per call. The `engine` input field is reserved so a follow-up community PR can add Replicate without a schema change. Vault credential `fal-key` (with `HELMDECK_FAL_KEY` env-var fallback, auto-hydrated). 9 unit tests cover happy path + multi-image + missing credential hard-fail + env fallback + bad engine + 401 surfacing.
- **Vault env-hydrate (#142)** — at control-plane startup, `WellKnownEnvCredentials` registry auto-imports `HELMDECK_*_API_KEY` env vars into the vault under their canonical names. Operators who set `HELMDECK_ELEVENLABS_API_KEY` in `.env.local` per the README now get a working `elevenlabs-key` vault entry without a manual `POST /vault/credentials` call. Wildcard ACL granted on first create. Subsequent restarts respect user-managed entries (`metadata.source != "env-hydrate"` skips re-upsert). One INFO log per hydration (`vault env hydrate ok name=elevenlabs-key host=api.elevenlabs.io`).
- **`vault.Store.UpsertByName`** — sibling to `Create`. Inserts if absent, rotates ciphertext + refreshes patterns/metadata in place if present. Returns `(record, created, error)`.
- **`helmdeck://voices` MCP resource (#143)** — exposes the operator's ElevenLabs voice catalog via the same `resources/list` + `resources/read` surface as `helmdeck://packs` and `helmdeck://sessions`. 1h in-memory cache keyed on the credential's plaintext fingerprint (rotating the key invalidates the cache automatically).
- **`internal/voices/`** — new package with `ListVoices(ctx, apiKey) → []Voice` extracted from `slides.narrate`'s inline `pickRandomVoice`. Voice exposes `voice_id`, `name`, `labels` (accent/gender/use_case), `preview_url`, `source`. Tests use overridable `ElevenLabsBaseURL` package var.
- **`podcast.generate` + `slides.narrate` per-turn duration floor (#141)** — new `min_turn_duration_s: number` input (default `5`). Short TTS turns get padded with trailing `anullsrc` silence so the output respects a per-segment minimum (matches the slides.narrate house style). Pass `min_turn_duration_s: 0` explicitly to opt out and preserve raw TTS pacing.
- **`podcast.generate` + `slides.narrate` dry_run / cost preview (#145)** — new `dry_run: bool` (default `false`) short-circuits before TTS synthesis and returns the script + per-speaker (or per-slide) `tts_chars` map + `estimated_cost_usd` + breakdown. Cost block is also included in regular (non-dry-run) responses. New `internal/podcast/cost.go` with plan rate table (Free/Starter/Creator/Pro/Scale) and `HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD` override.
- **`podcast.generate` + `slides.narrate` `allow_silent_output` opt-in** — paired with the #138 contract change below; `true` activates the (now opt-in) silence-padded fallback for CI smoke tests / demo placeholders.

### Changed

- **`podcast.generate` + `slides.narrate` require narration by default (#138)** — pre-this-change, missing the ElevenLabs credential silently produced a silence-padded artifact with `has_narration: false` buried in the response. Operators discovered the misconfiguration only by listening to the MP3. Now the packs hard-fail with a typed `missing_credential` error and an actionable message ("Set HELMDECK_ELEVENLABS_API_KEY in deploy/compose/.env.local..."). Pass `allow_silent_output: true` to opt back into the silent path. Shared 4-step credential resolver (`internal/packs/builtin/elevenlabs_creds.go`): explicit `credential` input → vault `elevenlabs-key` → vault `elevenlabs-api-key` (back-compat alias) → `os.Getenv("HELMDECK_ELEVENLABS_API_KEY")`. Both packs log one INFO line on successful resolve naming the ladder step that matched.
- **`slides.narrate` ffmpeg failure surfaces full stderr (#140)** — inline error message cap raised from 512 → 4096 bytes. Full stderr (plus the failing command line) persisted to the artifact store as `ffmpeg-stderr-segment-NNN.txt` / `ffmpeg-stderr-concat.txt`; the artifact key is referenced from the inline error so operators can fetch the unredacted output via the artifacts API.

### Fixed

- **`scripts/install.sh` blocked `--no-build` on hosts with old Go (#136)** — `check_go_version` ran unconditionally even with `--no-build`, failing on Debian/Ubuntu's apt-default Go 1.22. The control-plane Dockerfile builds inside `golang:1.26-alpine`, so the docker-only path needs no host Go. Wrapped in `if [[ "${DO_BUILD}" -eq 1 ]]`.
- **`scripts/configure-openclaw.sh` hardcoded `/root/openclaw` + over-strict shell-env auth check (#137)** — added `OPENCLAW_COMPOSE_FILE` env override (default unchanged); replaced 3 hardcoded path references. Auth-list `die` downgraded to `warn` when the OpenClaw container has `OPENCLAW_LOAD_SHELL_ENV=true` and `<PROVIDER>_API_KEY` is set on it (the auth-list probe is a guaranteed false positive in that documented setup path).

### Closed as duplicates

- #139 (duplicate of #141) and #144 (duplicate of #145) — closed without separate fixes.

### Deferred

- #146 (chain `image.generate` into podcast/slide/blog covers) — defers to a follow-up release. The `image.generate` pack lands in this release; the integration layer on top of it lands later.

### MCP Registry

The auto-publish workflow (`.github/workflows/mcp-registry.yml`) republishes the listing on `v*` tag push. After tagging, verify at `https://registry.modelcontextprotocol.io/v0/servers/io.github.tosin2013/helmdeck` (expect `version: 0.11.0`, `isLatest: true`).

## [0.10.2] - 2026-05-09

A small patch release that ships the **MCP Resources** surface (closes [#44](https://github.com/tosin2013/helmdeck/issues/44)) plus a refined registry-listing description. Functionally additive only; no breaking changes.

### Added

- **MCP Resources** (`#44`) — the MCP server now serves `resources/list` and `resources/read` per the 2024-11-05 spec, alongside the existing `tools/list` / `tools/call`. Two read-only resources surface today:
  - `helmdeck://packs` — the live pack catalog (every registered pack with its input schema). Equivalent to `tools/list` as a browsable resource.
  - `helmdeck://sessions` — live session list (id, status, image, created_at). Wired only when the control plane has an active session runtime; safely omitted otherwise.
  - The `initialize` response now declares the `resources` capability so MCP clients discover the new surface automatically.
  - 7 unit tests cover both happy paths, the missing-runtime fallback, the unknown-URI error, lister error propagation, and the capability declaration.

### Changed

- **Registry description** now reads *"Self-hosted MCP server: sandboxed browser, desktop, vision, code-edit packs for any agent."* (was "38 capability packs (browser, desktop, vision, repo, fs, slides, podcast) for MCP agents."). Leads with the value proposition + self-hosted differentiator instead of the feature list.
- **Registry submission script + workflow** corrected to point at the search API URL — the registry has no human-facing web UI today, only the metadata API. Was a pre-1.0 documentation bug from the v0.10.1 cycle.

### Operator notes

- **No action required for existing v0.10.1 installs** — MCP Resources is purely additive (new methods don't break existing tools/* clients). Upgrade if you want to expose `helmdeck://sessions` and `helmdeck://packs` to your agent for browsing.
- **Out of scope for #44** (deferred): JWT scope filtering on resources, per-MCP-client integration tests. Tracked as follow-ups; the spec implementation is complete and the 7 unit tests cover the surface.

---

## [0.10.1] - 2026-05-09

A patch release that completes helmdeck's listing on the [official MCP Registry](https://registry.modelcontextprotocol.io/). The v0.10.0 attempt failed namespace verification because two pieces of metadata weren't yet declared on the published artifacts — this release adds them. Functionally identical to v0.10.0; no pack/API/binary behavior changes.

### Fixed

- **`@helmdeck/mcp-bridge` npm package** now declares `mcpName: "io.github.tosin2013/helmdeck"` in its `package.json`. The MCP Registry's npm validator reads this field to confirm the package belongs to the registered namespace; without it, registry submission failed with `NPM package '@helmdeck/mcp-bridge' is missing required 'mcpName' field`.
- **`ghcr.io/tosin2013/helmdeck-mcp` OCI image** now carries the `io.modelcontextprotocol.server.name="io.github.tosin2013/helmdeck"` label. The OCI validator reads this label to confirm namespace ownership; the v0.10.0 image lacked it.

### Operator notes

- **No action required for existing v0.10.0 installs.** The bridge binary, control plane, and all 38 packs are unchanged. Skip this release unless you specifically need the registry-listed install path.
- **Registry entry goes live on tag push.** [`.github/workflows/mcp-registry.yml`](https://github.com/tosin2013/helmdeck/blob/main/.github/workflows/mcp-registry.yml) auto-fires; verify via the search API at `https://registry.modelcontextprotocol.io/v0/servers?search=io.github.tosin2013%2Fhelmdeck` (the registry is API-only in preview — there is no human-facing web UI; browse downstream aggregators like mcp.so, Glama, and PulseMCP instead).

---

## [0.10.0] - 2026-05-09

A "content packs" release. Two new packs land — **`blog.publish`** for posting to Ghost or stuffing markdown/HTML into the artifact store, and **`podcast.generate`** for multi-speaker podcast MP3s via a pluggable TTS engine. The capture pipeline ships in-repo, the upgrade procedure is documented for the first time, and the README now opens with the quantified cost-positioning argument the platform earned by shipping the per-pack reference work. Pack count: **36 → 38**.

The originally-planned v0.10.0 theme (Pack Authoring + Test Runner) slips to v0.11.0 — the work didn't happen this cycle, the slot got repurposed because the new packs were ready.

### Added

- **`blog.publish` pack** (#68 via [#103](https://github.com/tosin2013/helmdeck/pull/103)) — publish to a Ghost installation (live Admin API) OR render markdown/HTML to the helmdeck artifact store. Two body modes (agent-supplied OR prompt+model the pack expands). Goldmark added to `go.mod` for the markdown→HTML shim. Ghost JWT minted inline via `golang-jwt/jwt/v5` (5-min HS256, audience `/admin/`).
- **`podcast.generate` pack** ([#106](https://github.com/tosin2013/helmdeck/pull/106)) — produce a 1..N speaker podcast MP3 from a script, a prompt, or long-form content (URL/text → LLM converts). Three input modes (script / prompt+model / source_*+model). Five themed system prompts: `interview`, `debate`, `news-roundup`, `deep-dive`, `solo-essay`. Day 1: **ElevenLabs** behind a `podcast.Engine` interface so future PRs (PlayHT, Hume.ai, Resemble.ai) slot in by adding a new file under `internal/podcast/`. Vault credential `elevenlabs-key` (same as `slides.narrate`); silent-fallback when missing. Optional `cover_image_prompt` output for downstream image-gen packs.
- **38 per-pack reference pages** at [helmdeck.dev/reference/packs](https://helmdeck.dev/reference/packs) — every shipped pack on the agent-first / developer-second template, with live OpenClaw chat-UI transcripts embedded alongside `curl` developer references. (PR-A [#83](https://github.com/tosin2013/helmdeck/pull/83) + PR-B [#95](https://github.com/tosin2013/helmdeck/pull/95) + PR-C [#101](https://github.com/tosin2013/helmdeck/pull/101).) Closes #51, #53, #54, #55, #56, #58, #59, #60, #61, #62, #63, #64.
- **OpenClaw transcript capture pipeline** at `scripts/oc-capture/` ([#97](https://github.com/tosin2013/helmdeck/pull/97) + [#104](https://github.com/tosin2013/helmdeck/pull/104)) — three scripts (`capture-oc.sh`, `extract-oc-transcript.py`, `inject-transcripts.py`), a generic `capture-batch.sh` driver, and prompt files for the three pack-doc clusters.
- **Cost-positioning blog + long-form reference** ([#99](https://github.com/tosin2013/helmdeck/pull/99)) — `website/blog/2026-05-08-cheap-models-do-frontier-work.md` + `docs/explanation/why-helmdeck.md` with five per-task comparison tables vs. Anthropic Computer Use, OpenAI Operator, Browser-use, Cursor, Aider, Unstructured.io, LlamaParse, Pictory. Includes a "Run the comparison yourself" reproduction recipe + community-contribution invitation.
- **Operator upgrade documentation** at [`docs/howto/upgrade-helmdeck.md`](https://helmdeck.dev/howto/upgrade-helmdeck) ([#107](https://github.com/tosin2013/helmdeck/pull/107)) — pre-flight checklist, in-place Compose-stack upgrade, schema-migration handling, post-upgrade validation, rollback, Kubernetes/Helm path preview.
- **SKILLS.md gains a "Freshness contract" section** ([#98](https://github.com/tosin2013/helmdeck/pull/98)) — teaches agents to re-call stateful packs when state may have changed since the last call. Plus per-client "Load the agent skills" subsections for every integration doc (Claude Code via CLAUDE.md, Claude Desktop via Projects, Gemini CLI via GEMINI.md, Hermes via system_prompt_file).
- **Per-release-checklist additions** in `docs/RELEASES.md`: step 6 (refresh README + cost numbers per release, [#100](https://github.com/tosin2013/helmdeck/pull/100)), step 7 (operator upgrade procedure smoke, [#107](https://github.com/tosin2013/helmdeck/pull/107)).

### Fixed

- **`vision.click_anywhere` mechanical loop bug** (#102 via [#105](https://github.com/tosin2013/helmdeck/pull/105)) — per-step screenshots now genuinely reflect post-action desktop state. Two changes: `Step` and `StepNative` thread prior-turn actions into the next user message as textual history, and a 250 ms post-dispatch wait gives Xvfb time to repaint. Same fix applies to `vision.fill_form_by_label`. Verified live: per-step PNG artifacts now have **distinct file sizes** between iterations (vs. PR-B baseline where every step's bytes were identical because Xvfb hadn't repainted before scrot fired). **However**, the model-side completion-detection limitation remains — the model still rarely emits `done` on real tasks even when the click visibly landed. **Tracked separately at [#112](https://github.com/tosin2013/helmdeck/issues/112)** for follow-up research (try gpt-4o vs. haiku-4.5, native computer-use schema, two-shot verification). Treat `vision.click_anywhere` as **experimental for production workflows** until #112 lands an answer.
- **`repo.fetch` empty-remote infinite hang** (#94 via [#96](https://github.com/tosin2013/helmdeck/pull/96)) — `git ls-remote --heads` runs first; pack errors fast with `invalid_input: remote has no branches; push at least one commit before cloning`.
- **`fs.patch` Anthropic-edit-shape rejection** (#90 via [#93](https://github.com/tosin2013/helmdeck/pull/93)) — both `{search, replace}` and `{edits: [{oldText, newText}]}` shapes accepted.
- **`doc.parse` `formats: "markdown"` rejection** (#91 via [#93](https://github.com/tosin2013/helmdeck/pull/93)) — `markdown` aliases `md`; both work.
- **OpenClaw capture pipeline cross-prompt context bleed** ([#97](https://github.com/tosin2013/helmdeck/pull/97)) — every `capture-oc.sh` invocation now mints a fresh `--session-id`. Side-effect: per-call cost dropped ~140× (no 280-event session bloat shipped on every turn).
- **Vision pack loops now check `ctx.Err()`** (in [#105](https://github.com/tosin2013/helmdeck/pull/105)) — cancelled callers exit cleanly instead of spinning to `max_steps`.
- **`vision.fill_form_by_label` parity fix** ([#105](https://github.com/tosin2013/helmdeck/pull/105)) — now records per-step PNG artifacts (parity with `click_anywhere`).

### Changed

- **Pack count: 36 → 38** (`blog.publish` + `podcast.generate`)
- **`README.md`** opens with the quantified cost-positioning argument ($0.07 Phase 5.5 loop on `gpt-oss-120b` vs $0.30+ on Sonnet via Cursor) plus a 4-row comparison table; "other 99%" framing kept as the follow-on paragraph
- **Homepage tagline** rewritten from "Self-hosted AI agent platform for small open-weight models" to lead with the cost angle
- **`docs/integrations/SKILLS.md`** picks up the Freshness contract, expanded "How to load" subsection with per-client instructions, "Blog" and "Podcast" catalog entries, and the pack count bump

### Operator notes

- **Upgrade procedure**: `git fetch && git checkout v0.10.0 && make sidecars && make install`. See [`/howto/upgrade-helmdeck`](https://helmdeck.dev/howto/upgrade-helmdeck) for the full pre-/post-upgrade checklist.
- **Schema migrations**: auto-applied on `store.Open`. Cross-version smoke is tracked in [#108](https://github.com/tosin2013/helmdeck/issues/108) (P1).
- **OpenClaw skill refresh**: re-run `./scripts/configure-openclaw.sh` after pulling so the new SKILL.md (with podcast/blog entries + Freshness contract) lands in the OpenClaw container.
- **No breaking changes** to existing pack input/output schemas. All `### Added` work is additive; all `### Fixed` items improve observable behavior in agents' favor.
- **Pre-Kubernetes audit issues filed**: [#108](https://github.com/tosin2013/helmdeck/issues/108) (schema-migration cross-version test, P1), [#109](https://github.com/tosin2013/helmdeck/issues/109) (sidecar version pinning, P2), [#110](https://github.com/tosin2013/helmdeck/issues/110) (vault master-key rotation, P2), [#111](https://github.com/tosin2013/helmdeck/issues/111) (cross-version upgrade smoke in CI, P2). All tagged Phase 7; none block v0.10.0.
- **Known limitation**: `vision.click_anywhere` and `vision.fill_form_by_label` are **experimental** — the underlying loop fix in #105 works mechanically (screenshots progress per turn) but the vision model rarely emits `done` on real tasks. See [#112](https://github.com/tosin2013/helmdeck/issues/112) for the research track. Use at your own risk in production workflows; prefer `web.test` (Playwright MCP, deterministic) for browser-automation goals where possible.

## [0.9.0] - 2026-05-07

A "polish + plumbing" release. No new packs and no API changes — the 36
packs from v0.8.0 stay the surface area. What landed: a real install
fix that was breaking first-session sessions, a public docs site at
helmdeck.dev, two community-contributed AI provider adapters, secret
scanning in CI, and the planning-doc cross-references that were
documented-but-not-implemented at v0.8.0.

### Added
- **Documentation site** at [helmdeck.dev](https://helmdeck.dev/) —
  Docusaurus 3, Diataxis-organized (Tutorials / How-to / Reference /
  Explanation), deployed to Vercel with auto-preview on PRs. Search via
  `@easyops-cn/docusaurus-search-local`. SEO-tuned for Google Search
  Console submission: explicit titles, OG social card, robots.txt,
  sitemap with per-route priority bumps, schema.org/WebSite +
  FAQPage JSON-LD.
- **Install tutorials** — `docs/tutorials/install-cli.md` (10-minute
  walkthrough from `git clone` to running stack) and
  `docs/tutorials/install-ui-walkthrough.md` (panel-by-panel UI tour).
- **Troubleshooting how-to** — `docs/howto/troubleshoot-install.md`
  with FAQPage schema covering 10 known sharp edges (502 on first
  session, GHCR pull failures, lost admin password, etc.).
- **Per-pack documentation framework** — `docs/reference/packs/` with
  template + fully-written browser family (`browser.screenshot_url`,
  `browser.interact`). 12 family-tracking issues opened for community
  to pick up the remaining 34 packs.
- **OSS hygiene files** at repo root — `CHANGELOG.md`, `SECURITY.md`
  (90-day disclosure window), `CODE_OF_CONDUCT.md` (Contributor
  Covenant 2.1).
- **GitHub priority taxonomy** — `priority/P0..P3` labels applied to
  all 39 open issues. P1 cohort (14 items) is the next-release
  shortlist.
- **`docs/sitemap.xml`** — documcp-generated source-side sitemap for
  link audits and search-engine submission tracking, separate from
  Docusaurus's runtime sitemap.
- **Custom logo** — helm-wheel + H letterform mark, light/dark
  variants, SVG favicon. Replaced the scaffolded Docusaurus brand
  assets.
- **Provider adapters via community PRs** — Groq (PR #45 by @Dev-31)
  and Mistral (PR #47, resolved from @vijit-vishnoi's PR #46) both
  ride the `HELMDECK_{PROVIDER}_API_KEY[_FILE]` / `_BASE_URL` /
  `_MODELS` env-var contract introduced for OpenRouter in v0.8.0.

### Changed
- **Planning docs** (`RELEASES.md`, `MILESTONES.md`, `TASKS.md`) are
  now cross-linked. Every release has a Milestone + Tasks pointer;
  every milestone has a Ships-in pointer; the v0.8.0 RELEASES section
  was added (was missing). 19 task IDs that lived in MILESTONES
  without rows in TASKS got promoted into proper rows.
- README's install section links to the new tutorial pages.
- Trivy CI scan scope narrowed to `scanners: vuln,misconfig`. Action
  pin bumped 0.28.0 → 0.35.0.

### Fixed
- **Install bug** — `docker compose up -d --build` only builds
  services with a `build:` clause, so published images (Garage, the
  GHCR-published sidecar tag) weren't pulled before stack-up. Result:
  first session calls hung on a 30-second timeout. Fix: new
  `compose_pull` step in `scripts/install.sh` runs `docker compose
  pull --ignore-buildable` between sidecar build and `compose up`,
  fast-failing on network/proxy issues with an actionable error. The
  `sidecar-warm` service no longer swallows pull failures with
  `|| true`.
- **CI race** — `TestBridgeRoundTrip`'s shared `bytes.Buffer` between
  the test goroutine and the bridge writer. Wrapped in a
  `sync.Mutex`-guarded `safeBuffer`. Production code unchanged.
- **`vercel.json`** — `cleanUrls: true` added so `/PACKS` resolves to
  `/PACKS.html` (matched to Docusaurus's `trailingSlash: false`).

### Security
- **Gitleaks** secret-scanning CI workflow on every push + PR. Runs
  via `gitleaks/gitleaks-action@v2` with `fetch-depth: 0` so the
  scanner walks full history. Allowlist covers stable dev credentials
  in `deploy/compose/garage.toml` (file header already documents
  these as override-in-production).
- **`serialize-javascript`** bumped 6.0.2 → 7.0.5 via npm `overrides`
  to address GHSA-5c6j-r48x-rmvq (HIGH) and CVE-2026-34043 (MEDIUM).
  Both shipped as transitive deps in @docusaurus/bundler.

### Developer experience
- **`make check`** target wraps `vet + race test + build` — exactly
  what CI's `vet + test + build` job runs. Plus `make install-hooks`
  to wire an opt-in `pre-push` hook.

## [0.8.0] - 2026-04-12

### Added
- 36 capability packs total (browser, web, research, slides, GitHub, repo,
  filesystem, shell, HTTP, document, desktop, vision, language families).
- Phase 6.5 validation script (`scripts/validate-phase-6-5.sh`).
- Multi-provider AI gateway adapters: Groq, Mistral.
- gitleaks secret-scanning CI workflow with allowlist.

### Changed
- README leads with the weak-model success story; v0.8.0 + 36-pack catalog
  refresh.
- Trivy CI scan scope narrowed to vuln+misconfig (secrets owned by gitleaks).

## [0.5.1] - 2026-04-08

### Fixed
- npm trusted publishing: bump npm + add `--provenance` so
  `@helmdeck/mcp-bridge` releases include attestations.

## [0.5.0] - 2026-04-08

### Added
- AES-256-GCM Credential Vault with placeholder-token injection
  (login, session cookies, API keys, OAuth-with-refresh, SSH/git).
- CDP cookie injection at session start.
- HTTP gateway intercept-and-substitute for outbound agent traffic.
- `repo.fetch`, `repo.push`, `web.login_and_fetch`, `web.fill_form`,
  `slides.video` packs (vault-dependent).
- NetworkPolicy egress allowlist + metadata IP / RFC 1918 block.
- Sandbox baseline: non-root, drop-all-caps, seccomp.
- OpenTelemetry GenAI semantic conventions on every span.
- Trivy CRITICAL gate in CI.

## [0.3.0] - 2026-04-08

### Added
- MCP registry with stdio/SSE/WebSocket transports.
- Built-in MCP server auto-derived from the pack catalog.
- `helmdeck-mcp` bridge binary distributed via Homebrew, Scoop, npm
  (`@helmdeck/mcp-bridge`), GHCR OCI image, and signed GitHub Releases.
- CI smoke matrix verifying `browser.screenshot_url` from Claude Code,
  Claude Desktop, OpenClaw, and Gemini CLI.

### Fixed
- `release.yml`: gate binary jobs to push events only.

## [0.2.0] - 2026-04-08

### Added
- OpenAI-compatible `/v1/chat/completions` and `/v1/models`.
- Provider adapters: Anthropic, Gemini, OpenAI, Ollama, Deepseek.
- Encrypted key store with rotation API.
- Fallback routing rules (rate-limit / error / timeout triggers).
- Pack Execution Engine with input/output schema validation.
- Typed error code enforcement (closed set per pack).
- Pack registry with versioned dispatch.
- Three reference packs: `browser.screenshot_url`, `web.scrape_spa`,
  `slides.render`.
- Object store integration with signed-URL artifacts.
- A2A Agent Card at `/.well-known/agent.json`.

### Hardware exit gate met
- ≥90% success rate on `browser.screenshot_url` and `web.scrape_spa`
  against MiniMax-M2.7 and Llama 3.2 7B.

## [0.1.1] - 2026-04-07

### Fixed
- `sidecar.yml`: publish amd64 only until Marp ships an arm64 tarball.

## [0.1.0] - 2026-04-07

### Added
- Go control plane binary (Gin + chromedp + Docker SDK).
- Browser sidecar image with Chromium, Marp, Tesseract, ffmpeg, xdotool,
  Xvfb, XFCE4, noVNC.
- Ephemeral session lifecycle (`POST /api/v1/sessions` …
  `DELETE /api/v1/sessions/{id}`).
- CDP REST endpoints: navigate, extract, screenshot, execute, interact.
- JWT bearer auth on every endpoint.
- Audit log (write-only).
- Single-node Compose deployment (`deploy/compose/compose.yaml`).
- `make smoke` end-to-end harness in CI.

[Unreleased]: https://github.com/tosin2013/helmdeck/compare/v0.9.0...HEAD
[0.9.0]: https://github.com/tosin2013/helmdeck/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/tosin2013/helmdeck/compare/v0.5.1...v0.8.0
[0.5.1]: https://github.com/tosin2013/helmdeck/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/tosin2013/helmdeck/compare/v0.3.0...v0.5.0
[0.3.0]: https://github.com/tosin2013/helmdeck/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/tosin2013/helmdeck/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/tosin2013/helmdeck/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/tosin2013/helmdeck/releases/tag/v0.1.0
