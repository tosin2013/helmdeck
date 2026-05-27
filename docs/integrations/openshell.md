---
title: NVIDIA OpenShell integration (roadmap)
description: Post-GA roadmap for running helmdeck sidecars inside NVIDIA OpenShell sandboxes. Hardware-isolated browser/code execution, hot-reloadable L7 network policy, correlated OTel + OCSF observability, and a community contribution path tracked in 5 GitHub issues.
keywords: [helmdeck, OpenShell, NVIDIA, sandbox, MicroVM, libkrun, OPA, policy, Phase 8, integration]
---

# NVIDIA OpenShell integration

> **Status:** 📋 Roadmap, **post-GA (Phase 8)**. No code lands until v1.0 (Kubernetes & GA) ships. This page documents the design and the contribution path so the community can pick up phases incrementally.
>
> **Last reviewed:** 2026-05-13 against [helmdeck v0.12.1](../../CHANGELOG.md#0121---2026-05-13) (39 packs) and [NVIDIA/OpenShell](https://github.com/NVIDIA/OpenShell) alpha.
>
> **Tracking:** Phase 1 ([#194](https://github.com/tosin2013/helmdeck/issues/194)), Phase 2 ([#195](https://github.com/tosin2013/helmdeck/issues/195)), Phase 3 ([#196](https://github.com/tosin2013/helmdeck/issues/196)), Phase 4 ([#197](https://github.com/tosin2013/helmdeck/issues/197)). Tracking epic at [#193](https://github.com/tosin2013/helmdeck/issues/193).

## Why this exists

Helmdeck and OpenShell solve **adjacent but distinct problems** in the agentic-platform stack:

| Layer | Owner | Today |
|---|---|---|
| **Agent logic** (planning, tool selection, reasoning) | Agent (Claude Code, OpenClaw, Codex, Hermes) | — |
| **Tool orchestration** (pack execution, MCP server, AI gateway, vault, artifact store) | **helmdeck** | 43 packs across 11 families; Docker-container session isolation; AES-256-GCM vault |
| **Infrastructure & isolation** (sandbox lifecycle, L7 network policy, hardware isolation, OS-level credential injection) | **OpenShell** | Rust gateway + supervisor + policy proxy; OPA engine; experimental libkrun MicroVM backend; Landlock filesystem |

The integration is not duplicative: each project covers a layer the other doesn't. By the end of Phase 3, helmdeck's `SessionRuntime` interface gains an OpenShell backend, and every browser / Python / Node sidecar that helmdeck spawns can run inside a hardware-isolated MicroVM with hot-reloadable L7 network policy. By the end of Phase 4, a single trace ID joins helmdeck's GenAI OTel spans with OpenShell's OCSF security events for the same sandbox.

## Why post-GA, not pre-GA

The phases are gated behind v1.0 (Kubernetes & GA) for two reasons:

1. **Phase 3 modifies `SessionRuntime`.** That interface is the seam between helmdeck's pack engine and its execution backends (Docker today, `client-go` in Phase 7). Touching it before v1.0 forks the test matrix and slows the path to GA. After v1.0, adding a third backend is purely additive.
2. **OpenShell is alpha.** Production deployments need a stable OpenShell Gateway API. The roadmap targets a co-stabilized v1.x of both projects.

The post-GA timing is also a feature: enterprises evaluating helmdeck for production benefit from the OpenShell story *after* they've already deployed the Compose or Helm path, not before.

## The three-layer integration

The integration is best understood as a stack with three owners:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Agent (Claude Code / OpenClaw / ...)         │
│                    running inside an OpenShell sandbox          │
│                    egress: helmdeck-mcp + inference.local only  │
└──────────────────┬──────────────────────────────────────────────┘
                   │ MCP tool calls (SSE / WebSocket)
                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                    helmdeck control plane                       │
│                    (pack engine, MCP server, AI gateway, vault) │
│                    SessionRuntime backend = "openshell"         │
└──────────────────┬──────────────────────────────────────────────┘
                   │ POST /api/v1/sandboxes (OpenShell Gateway API)
                   │ image: ghcr.io/tosin2013/helmdeck-sidecar:vX
                   │ policy: <pack-family>-sidecar-policy.yaml
                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                    OpenShell Gateway                            │
│                    (compute driver: Docker / K8s / libkrun)     │
│                    (policy proxy: OPA / Landlock)               │
└──────────────────┬──────────────────────────────────────────────┘
                   │ provisions
                   ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Sidecar sandbox (MicroVM)                    │
│                    OpenShell supervisor → helmdeck sidecar      │
│                    All egress through OpenShell policy proxy    │
└─────────────────────────────────────────────────────────────────┘
```

### Credential split

Both stacks already do credential injection. In the integrated topology their responsibilities are non-overlapping:

| Credential | Owner | Mechanism |
|---|---|---|
| Agent's identity tokens (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) | OpenShell | Provider-injected env vars at agent-sandbox start |
| NVIDIA API key / `inference.local` routing | OpenShell | Policy-injected; never written to disk |
| K8s service account / cloud creds | OpenShell | Provider-injected at sandbox provisioning |
| GitHub PAT, ElevenLabs key, Ghost admin key, Firecrawl key | **helmdeck** | AES-256-GCM vault; `${vault:NAME}` placeholder substitution at pack-dispatch time |
| Pack-output artifact signing | **helmdeck** | Existing artifact store, unchanged |

Operators must understand both layers to configure the stack — but the layers never collide because OpenShell injects into the *process environment* and helmdeck injects into the *outbound HTTP request body*.

## Roadmap — four phases

### Phase 1 — Shallow integration (no helmdeck code changes)

Run the helmdeck control plane inside an OpenShell sandbox. Apply an OpenShell policy that restricts the control plane's outbound traffic to known endpoints (configured AI provider APIs, GitHub, the artifact store).

**What lands:**
- New `deploy/openshell/control-plane-policy.yaml` — example OpenShell policy for the helmdeck control plane container.
- New `docs/howto/run-helmdeck-inside-openshell.md` — operator-facing walkthrough.

**What this buys you:** Network-level governance on every outbound API call helmdeck's AI gateway makes. If an LLM provider's API gets quietly compromised and starts redirecting calls, OpenShell's policy proxy blocks them.

**Effort:** Doc + example policy only. Suitable as a first contribution.

### Phase 2 — Agent sandbox integration

Run the agent (OpenClaw, Claude Code, Hermes) inside an OpenShell sandbox with a policy that restricts egress to the helmdeck MCP SSE endpoint and `inference.local`. This is the canonical deployment pattern already documented in [OpenShell's `openclaw.md` example](https://github.com/NVIDIA/OpenShell) (`openshell sandbox create --forward 18789 --from openclaw`).

**What lands:**
- New section in `docs/integrations/openclaw.md` covering the OpenShell sandbox topology (Topology A.5 — "OpenClaw inside OpenShell").
- New `deploy/openshell/agent-sandbox-policy.yaml` — example OpenShell policy for an agent sandbox that allows MCP egress to helmdeck only.
- Verification: `openshell sandbox create --forward 18789 --from openclaw` followed by a helmdeck smoke pack from inside the sandbox.

**What this buys you:** Confined agent process. A prompt-injected agent attempting to exfiltrate to an attacker-controlled URL is blocked by OpenShell before the helmdeck egress guard even sees the request.

**Effort:** Docs + example policy. Suitable as a first contribution.

### Phase 3 — Sidecar sandbox integration (the load-bearing one)

Implement an `OpenShellSessionRuntime` in helmdeck's Go codebase. The pack engine's `SessionRuntime` interface (today implemented by `DockerSessionRuntime` and — Phase 7 — `KubernetesSessionRuntime`) gains a third backend that calls the OpenShell Gateway API for sandbox lifecycle (provision, exec, logs, terminate).

**What lands:**
- New `internal/session/openshell/` package implementing `SessionRuntime` via the OpenShell Gateway gRPC/HTTP API.
- New `internal/session/openshell/policy/` — minimal per-pack-family policy templates (browser, python, node, vision).
- New ADR `docs/adrs/036-openshell-session-runtime-backend.md` capturing the SessionRuntime extension.
- Config flag: `HELMDECK_SESSION_RUNTIME=openshell` + `HELMDECK_OPENSHELL_GATEWAY_URL`.
- Integration tests under `make smoke-openshell` (opt-in; needs OpenShell running).

**What this buys you:** Hardware-isolated sidecars. Every `browser.screenshot_url`, `python.run`, `node.run`, `web.scrape` call lands inside a MicroVM with a dedicated kernel — a Chromium zero-day or a prompt-injected container escape is contained. Plus: per-pack-family L7 network policy enforced by OpenShell's OPA engine, hot-reloadable without restarting the sidecar.

**Effort:** Multi-week Go work + coordination with OpenShell maintainers on API stability. P2 (post-GA). Help wanted with strong agent-platform / Rust-integration background.

### Phase 4 — Correlated observability

Build a correlation layer that joins helmdeck's OTel GenAI traces (existing) with OpenShell's OCSF security events (existing) on the sandbox ID. An operator can trace a single agent task from the initial MCP tool call (helmdeck OTel span) through the network policy decision (OpenShell OCSF event) to the outbound HTTP request (helmdeck vault-injection span).

**What lands:**
- helmdeck control plane emits the OpenShell sandbox ID as an OTel span attribute (`openshell.sandbox.id`).
- New `internal/observability/openshell_correlator.go` — joins traces by sandbox ID at the collector layer.
- Example Grafana dashboard `deploy/openshell/grafana-correlated.json` showing per-task correlated view.
- Doc: `docs/howto/correlate-helmdeck-openshell-traces.md`.

**What this buys you:** End-to-end traces that span tool execution + security decisions in one timeline. Invaluable for debugging "why did this pack fail" when the cause is a policy denial.

**Effort:** Phase 3 prerequisite (sandbox IDs need to flow through helmdeck first). Self-contained from there.

## Value summary

| Dimension | Standalone helmdeck | Standalone OpenShell | helmdeck + OpenShell |
|---|---|---|---|
| **Browser isolation** | Docker container + seccomp | N/A | MicroVM (libkrun) |
| **Code-execution isolation** | Docker container | N/A | MicroVM + Landlock filesystem |
| **Network policy** | URL blocklist (egress guard) | L7 YAML policy | L7 policy on every sidecar |
| **Credential security** | AES-256-GCM vault + placeholders | Provider injection at process start | Both, non-overlapping |
| **Tool availability** | 43 packs via MCP | Bring your own | 43 packs inside policy-governed sandboxes |
| **Local-model reliability** | ≥90% on 7B–30B via pack contracts | Inference routing only | ≥90% on 7B–30B, fully air-gapped |
| **Observability** | OTel GenAI traces | OCSF security events | Correlated OTel + OCSF |
| **Policy feedback loop** | None | Policy Advisor | Policy Advisor extended to tool sandboxes |

## Risks

| Risk | Severity | Mitigation |
|---|---|---|
| **API surface mismatch** — OpenShell Gateway API is gRPC/HTTP; helmdeck's `SessionRuntime` interface is Go. | Medium | Phase 3 writes a thin Go client; maintained per OpenShell release. |
| **Version skew** — both projects in active development. | Medium | Pin OpenShell version in `go.mod` + `deploy/openshell/`. Coordinated release notes call out skew. |
| **Latency overhead** — sidecar provisioning gets an API hop. | Low–Medium | Negligible for short packs (screenshot_url ~2 s); irrelevant for long packs (slides.narrate ~60 s). |
| **OpenShell alpha stability** | High (short-term) | Phase 3 work waits for stable OpenShell. Phases 1–2 are docs-only and safe today. |
| **Dual credential systems confusion** | Low | Documentation split (this page) plus a credential-flow diagram in the Phase 3 ADR. |

## Why this is community-led

The four phases break cleanly into work that doesn't require deep helmdeck-internals knowledge:

- **Phase 1** is a YAML policy + howto doc. Anyone with Docker + OpenShell + 30 minutes can contribute.
- **Phase 2** is the same shape for the agent sandbox.
- **Phase 3** needs Go + Rust API-client experience. Help wanted for someone with agent-platform background.
- **Phase 4** needs OTel collector + OPA knowledge. Niche, but well-scoped.

Each phase is independently mergeable. Phase 1 can land while Phase 3 is still being designed. The roadmap is intentionally additive — none of these phases break existing helmdeck deployments.

## References

- [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) — Rust-based safe runtime for agents (alpha).
- [`docs/integrations/openclaw.md`](./openclaw.md) — the existing agent-side integration that Phase 2 extends.
- [`docs/integrations/nemoclaw.md`](./nemoclaw.md) — NVIDIA's existing helmdeck integration path; OpenShell is the next architectural step beyond NemoClaw.
- [`docs/adrs/011-tiered-isolation-docker-gvisor-firecracker.md`](../adrs/011-tiered-isolation-docker-gvisor-firecracker.md) — helmdeck's existing isolation tier plan; OpenShell's MicroVM is a credible alternative to the gVisor / Firecracker tiers documented there.
- [`docs/RELEASES.md` — v1.x Enterprise integration tracks](../RELEASES.md#enterprise-integration-tracks) — the post-GA release-plan slot.
