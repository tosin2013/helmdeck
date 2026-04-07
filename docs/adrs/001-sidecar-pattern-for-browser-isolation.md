# 1. Sidecar Pattern for Browser Isolation

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: microservices, distributed-systems

## Context
Headless Chromium embedded directly in the AI agent's container creates 300–500 MB of image bloat, 60–90 s CI penalty per build, frequent OOMKills after ~20 h uptime, and a sandbox surface that prompt injection can attack. Agent reasoning code and browser execution have radically different resource, lifecycle, and trust profiles (PRD §2, §5).

## Decision
Deploy the headless browser as a separate sidecar container, decoupled from the application/agent container. The agent calls the browser via the platform's REST/MCP API; it never imports or co-locates Chromium. The two containers share an internal Docker bridge network (`baas-internal`) and communicate over CDP on port 9222, never exposed to the host.

## Consequences
**Positive:** independent scaling, blast-radius containment for prompt injection, agent images stay small, CI builds fast, browser leaks recycle without restarting the agent.
**Negative:** more moving parts at deploy time, network hop adds <10 ms latency, requires orchestration (Compose or Kubernetes).

## Related PRD Sections
§2 Problem Statement, §3.1 Architectural Decoupling, §5.1 Component Overview, §10 Security Model
