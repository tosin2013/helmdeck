# 30. `helmdeck-mcp` Bridge Packaging and Distribution

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: distributed-systems

## Context
ADR 025 commits helmdeck to first-class MCP client integration with Claude Code, Claude Desktop, OpenClaw, and Gemini CLI via a single shared `helmdeck-mcp` Go binary that proxies stdio MCP traffic to the platform's WebSocket MCP endpoint. The integration story is only as good as the install story: if users have to clone a repo, install Go, and `go build`, adoption dies. Each target client lives in a different OS ecosystem and expects a different installation idiom.

## Decision
Distribute `helmdeck-mcp` through the package manager native to each target client's typical install environment, all built from a single source tree and CI pipeline. The platform's "Connect" buttons in the Management UI (ADR 025) emit the exact one-liner for the user's detected OS.

### Distribution channels

| Channel | Target OS / Client | Install command | Notes |
| :--- | :--- | :--- | :--- |
| **Homebrew tap** (`tosin2013/helmdeck`) | macOS / Linux (Claude Code, Claude Desktop, Gemini CLI on dev machines) | `brew install tosin2013/helmdeck/helmdeck-mcp` | Primary path for the largest user segment |
| **npm** (`@helmdeck/mcp-bridge`) | Cross-platform via `npx` (Claude Desktop config, Gemini CLI) | `npx @helmdeck/mcp-bridge --url ... --token ...` | Lets users avoid a permanent install; postinstall script downloads the right prebuilt binary for the host arch |
| **Scoop bucket** (`tosin2013/helmdeck`) | Windows (Claude Desktop, Gemini CLI on Windows dev) | `scoop bucket add helmdeck https://github.com/tosin2013/scoop-helmdeck && scoop install helmdeck-mcp` | Native Windows install path |
| **GitHub Releases** | All platforms; OpenClaw self-hosted servers; air-gapped environments | Download tarball / zip from `github.com/tosin2013/helmdeck/releases` | Authoritative source; signed checksums; the channel all others pull from |
| **OCI image** (`ghcr.io/tosin2013/helmdeck-mcp`) | Containerized agent runtimes; OpenClaw Docker deployments; CI pipelines | `docker run --rm -i ghcr.io/tosin2013/helmdeck-mcp:latest` | stdio over `docker run -i` works for any MCP client that can spawn a subprocess |
| **Go install** (`go install github.com/tosin2013/helmdeck/cmd/helmdeck-mcp@latest`) | Go developers; reproducible from-source path | — | Doc-only; not the primary recommendation |

### Build and release process
- A single `goreleaser` config builds `helmdeck-mcp` for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, and `windows/amd64`.
- Each release pushes artifacts to GitHub Releases (signed with cosign), updates the Homebrew tap formula, publishes the npm package (with the postinstall binary downloader pointing at the just-published GH Release), updates the Scoop manifest, and pushes the multi-arch OCI image to ghcr.io.
- Version numbers track the platform release exactly — `helmdeck-mcp v1.4.2` is built from the same commit as the platform v1.4.2 server, guaranteeing protocol compatibility.

### Configuration discovery
Regardless of channel, the binary reads `HELMDECK_URL` and `HELMDECK_TOKEN` from environment variables. The Management UI's "Connect" flow generates client-specific snippets that pre-fill these in the right config file location for each client (`~/.claude/mcp.json`, `claude_desktop_config.json`, `~/.gemini/settings.json`, `openclaw mcp set ...`).

### Updates and deprecation
The bridge logs an MCP-protocol-level notification at session start when its version is older than the connected platform's recommended minimum, so users see "helmdeck-mcp v1.3.0 is outdated; please upgrade to v1.4.x" in their normal agent UI without having to check the release notes.

## Consequences
**Positive:** every target user can install the bridge with one command in their native idiom; the npm `npx` path eliminates "permanent install" friction entirely; OCI image makes containerized and CI use trivial; signed releases satisfy enterprise supply-chain review; goreleaser keeps maintenance bounded to one config.
**Negative:** six distribution channels to keep in sync — automated by goreleaser but still a real operational surface; postinstall binary downloads in the npm package will be flagged by some corporate proxies; OCI stdio over `docker run -i` has subtle TTY edge cases on Windows; version pinning across platform and bridge releases must be enforced strictly to avoid protocol skew.

## Related PRD Sections
§13 Agent Consumer Ecosystem, §13.2 OpenClaw Integration, §6.5 MCP Server Management

## Related ADRs
- ADR 025: First-Class MCP Client Integrations (defines the bridge architecture this packaging delivers)
- ADR 002: Golang Control Plane (the bridge is built from the same Go monorepo)
- ADR 024: User-Authored Pack Extensibility (bridge advertises packs from the platform's catalog dynamically)
