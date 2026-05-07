---
title: Changelog
description: Release history for helmdeck.
---

# Changelog

All notable changes to helmdeck are documented here. The format follows
[Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html) starting
at v1.0.0; pre-1.0 minor versions may break compatibility (documented per release).

For the forward-looking *release plan* — what is targeted for upcoming versions
and the hard exit gates for each — see
[`docs/RELEASES.md`](https://github.com/tosin2013/helmdeck/blob/main/docs/RELEASES.md).

## [Unreleased]

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
