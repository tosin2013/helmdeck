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
- Documentation site (Docusaurus 3, Diataxis-organized) deployed to Vercel.
- Top-level `CHANGELOG.md`, `SECURITY.md`, and `CODE_OF_CONDUCT.md` for OSS hygiene.

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

[Unreleased]: https://github.com/tosin2013/helmdeck/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/tosin2013/helmdeck/compare/v0.5.1...v0.8.0
[0.5.1]: https://github.com/tosin2013/helmdeck/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/tosin2013/helmdeck/compare/v0.3.0...v0.5.0
[0.3.0]: https://github.com/tosin2013/helmdeck/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/tosin2013/helmdeck/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/tosin2013/helmdeck/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/tosin2013/helmdeck/releases/tag/v0.1.0
