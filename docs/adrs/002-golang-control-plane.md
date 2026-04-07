# 2. Golang for the Control Plane

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design, microservices

## Context
The control plane must concurrently manage many CDP WebSocket connections, spawn/destroy Docker containers via the Docker SDK, serve a REST API and the compiled React UI, and run as a small static binary suitable for distroless images (PRD §5.2, §20.9).

## Decision
Implement the control plane in Go using Gin for HTTP, `chromedp` for CDP, and the official Docker SDK / `client-go` for orchestration. Compile to a fully static `CGO_ENABLED=0` binary that embeds the React assets and runs on `gcr.io/distroless/static`.

## Consequences
**Positive:** goroutines map naturally onto per-session CDP loops; <30 MB final image; ~150 MB control-plane memory budget achievable; one binary serves API + UI.
**Negative:** team must be comfortable with Go; pack handlers written in Go require recompile (mitigated by WASM executor — see ADR 011).

## Related PRD Sections
§5.2 The Golang Advantage, §7 Detailed API Design, §20.9 GitOps and CI/CD
