# ADR 035 — MCP Server Hosting & Capability Pack Evolution

**Status:** Accepted
**Date:** 2026-04-10
**Author:** Tosin Akinosho

## Context

Helmdeck's browser and scraping packs (`browser.screenshot_url`, `web.scrape_spa`, `doc.ocr`) implement their functionality from scratch using chromedp and Tesseract. While functional, this approach has two problems:

1. **UX burden** — `web.scrape_spa` requires CSS selectors, meaning the user must already know the page structure to extract data. An LLM calling "scrape this page and tell me the headlines" has to first figure out the right selector (`span.titleline > a` for Hacker News, `h2.h3 a` for GitHub trending, etc.) — which defeats the purpose of automation.

2. **Maintenance cost** — chromedp changes with every Chromium release; Tesseract's OCR is limited compared to modern document understanding models. Helmdeck's value is in being the secure sidecar platform (vault, audit, egress control, session management), not in reimplementing web scraping or OCR engines that larger teams maintain full-time.

Research surfaced four open-source projects that solve these problems and are actively maintained:

| Project | Maintainer | License | What it does | MCP server? |
| :--- | :--- | :--- | :--- | :--- |
| [Playwright MCP](https://github.com/microsoft/playwright-mcp) | Microsoft | Apache-2.0 | Browser automation via accessibility tree snapshots — no selectors, no vision model needed. Structured, LLM-friendly output. | ✅ stdio + SSE |
| [Firecrawl](https://github.com/mendableai/firecrawl) | Mendable | AGPL-3.0 | Any URL → clean markdown/JSON. Auto-extracts without selectors. Handles JS-heavy pages. Self-hostable via Docker. | ✅ MCP server included |
| [browser-use](https://github.com/browser-use/browser-use) | Community | MIT | Python framework for AI-driven browser automation. Vision-based action loops. | ❌ (library, not MCP) |
| [Docling](https://github.com/docling-project/docling) | IBM / Community | MIT | Document parsing: PDF, DOCX, PPTX, images with OCR, layout detection, table extraction, code/formula recognition. Air-gapped support. | ✅ MCP server included |

## Decision

**Don't rebuild solutions that already exist. Host them.**

Helmdeck's value proposition evolves from "we implement the scraping" to "we provide the right MCP server + config + credentials for the job, inside a secure sidecar with audit logging and egress control." The operator never has to configure Playwright, Firecrawl, or Docling directly — helmdeck's capability packs abstract it.

### Pack classification evolves

| Classification | Description | Examples |
| :--- | :--- | :--- |
| **Core packs (native)** | Pure Go, no external MCP server. Simple HTTP/exec calls where helmdeck IS the engine. | `github.*`, `fs.*`, `cmd.*`, `git.*`, `repo.*`, `http.fetch`, `slides.render` |
| **Core packs (MCP-backed)** | Thin Go wrappers that call through to a hosted MCP server running in the sidecar or as a compose service. Ship by default. | `browser.*` → Playwright MCP, `web.scrape` → Firecrawl, `doc.parse` → Docling |
| **Marketplace packs** | Community-authored, installed on demand per ADR 034. | `gitlab.*`, `slack.*`, `aws.*` |

### MCP Server Hosting model

The control plane manages the lifecycle of hosted MCP servers:

1. **Sidecar-bundled** (Playwright MCP) — installed in the browser sidecar Dockerfile, starts with the session, communicates via stdio. Zero config for the operator.

2. **Compose service** (Firecrawl, Docling) — separate container in the compose stack, toggled by an env var (`HELMDECK_FIRECRAWL_ENABLED=true`). The control plane discovers and health-checks it at startup.

3. **On-demand installed** (Phase 8, T816) — `helmdeck mcp install <server>` pulls a community MCP server from the marketplace and runs it inside the sidecar. The marketplace (ADR 034) and MCP hosting converge: marketplace packs can declare an MCP server dependency and helmdeck auto-installs it.

### Evolution path per pack

| Today's pack | v0.6.0 (now) | v1.0 (Phase 7) | v1.x (Phase 8) |
| :--- | :--- | :--- | :--- |
| `web.scrape_spa` | CSS selectors, chromedp | Add `web.scrape` (Firecrawl-backed, no selectors). Old pack stays for backward compat. | Playwright MCP replaces chromedp code path for all browser packs. |
| `browser.screenshot_url` | chromedp | Optionally delegate to Playwright MCP if installed in sidecar | Playwright MCP is the default path |
| `doc.ocr` | Tesseract | Add `doc.parse` (Docling-backed, multi-format, layout-aware). Old pack stays. | Docling is the default path for all document understanding |
| `vision.*` | chromedp + gateway LLM | Consider browser-use integration for AI-native action loops | browser-use or equivalent framework |

### What helmdeck keeps building

- **Vault credential injection** (`${vault:NAME}`) — unique to helmdeck, no MCP server does this
- **Egress guard** (T508) — SSRF protection across all packs and hosted MCP servers
- **Audit logging** — every tool call, every credential use, regardless of which MCP server executed it
- **Session lifecycle** — spawn, pin, timeout, terminate sidecar containers
- **MCP transport bridge** (T302/T302a) — the SSE/WS surface that clients connect to
- **The pack abstraction** — operators say "scrape this URL" and helmdeck picks the right tool

### What helmdeck stops building

- Web scraping engine (Firecrawl does it better)
- Browser automation primitives (Playwright MCP does it better)
- OCR + document parsing (Docling does it better)
- Browser vision loops (browser-use does it better)

## PRD Sections

§6.6 Capability Packs, §3.1 Primary Goals, §19.10 Progressive Disclosure

## Container topology

```
helmdeck-control-plane      ← orchestrates everything, hosts the pack engine
helmdeck-sidecar            ← Chromium + Playwright MCP (shared browser, same process)
helmdeck-firecrawl          ← optional, separate container, AGPL-isolated
helmdeck-docling            ← optional, separate container, MIT
helmdeck-garage             ← S3 artifact store (already exists)
```

**Playwright MCP goes IN the existing sidecar** because it shares Chromium — installing it separately would mean two Chromium instances per session (~600 MB RAM wasted). `npm install @playwright/mcp` in the sidecar Dockerfile is the right path.

**Firecrawl and Docling run as separate compose services** for three reasons:
1. **Resource isolation** — an OOM in Firecrawl doesn't kill Chromium or the control plane
2. **Independent scaling** — heavy scraping workloads scale Firecrawl without scaling sidecar sessions
3. **License isolation** — Firecrawl is AGPL-3.0; running it as a separate container with an API boundary (same model as Garage) avoids copyleft obligations on helmdeck's Apache-2.0 codebase

Both are toggled via env vars (`HELMDECK_FIRECRAWL_ENABLED`, `HELMDECK_DOCLING_ENABLED`) and health-checked by the control plane at startup. When disabled, the corresponding packs (`web.scrape`, `doc.parse`) return a clear error pointing the operator at the env var.

## Consequences

- Helmdeck becomes a **hosting platform for MCP servers** rather than a monolithic tool implementor. This is a fundamental architectural evolution that scales the pack catalog without scaling the core team.
- Firecrawl is AGPL-3.0 — running it as a separate compose service (not linking into the Go binary) avoids AGPL obligations on helmdeck itself. Same deployment model as Garage (separate container, API call).
- Adding Playwright MCP to the sidecar increases the sidecar image size (Playwright installs ~200 MB of browser deps, though helmdeck's sidecar already has Chromium — need to verify if they share the install or double up).
- The accessibility tree approach (Playwright MCP) is deterministic and doesn't require a vision model, making it cheaper and more reliable than screenshot-based extraction. This is a significant quality improvement for weak models (the 7B–30B target audience from ADR 003).
- Community MCP servers get helmdeck's security posture (vault, egress, audit) for free — the pack wrapper adds the controls that raw MCP servers don't have.

## 2026 Landscape Revision (T807f, April 2026)

The original ADR identified four "host, don't rebuild" candidates: Playwright MCP, Firecrawl, browser-use, and Docling. By mid-2026 the landscape shifted:

**All three frontier providers now ship native computer-use tool schemas:**
- Anthropic: `computer_20251124` (WebArena SOTA for single-agent systems)
- OpenAI: `computer-use-preview` function tool
- Google: `gemini-2.5-computer-use-preview-10-2025` + `gemini-3-flash-preview` built-in

All three are **client-runtime** — the provider hosts the model and the caller hosts the click/type/screenshot execution environment. Helmdeck's existing T401 desktop sidecar (Xvfb + XFCE4 + xdotool + scrot + Chromium) already IS that environment.

**Decision: upgrade vision.*, don't wrap browser-use or Skyvern.**

Wrapping browser-use (Python, MIT) or Skyvern (Python, AGPL) would embed someone else's agent loop inside helmdeck's Go pack engine for functionality modern frontier models now provide natively. T807f instead upgrades the gateway to carry tool definitions (ChatRequest.Tools/ToolChoice) through to each provider adapter, and upgrades vision.* packs to route native tool schemas when the target model supports them. The result:

1. **Cross-provider schema abstraction** — one `ComputerUseAction` type, three provider parsers (Anthropic, OpenAI, Gemini including 0-1000 normalized coordinate scaling), same desktop runtime
2. **Vault-aware credential injection** — `${vault:NAME}` substitution happens at the xdotool layer; the model never sees the secret in context
3. **Audit-backed replay** — `EventComputerUse` entries + per-step screenshot artifacts to Garage S3
4. **noVNC witness mode** — `AgentStatus` on the VNC info endpoint, real-time human observation
5. **No new runtime** — zero Python, zero new containers, zero supply-chain surface

Ollama and Deepseek stay on the JSON-prompt fallback path (the legacy `vision.Action` schema). `vision.Action` is preserved as the documented local-model path indefinitely, not deprecated.

The evolution path column from the original ADR for `vision.*` is satisfied: browser-use/Skyvern was the assumed vehicle; native provider tool schemas turned out to be the correct one. The same destination, different transport.
