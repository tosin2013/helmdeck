---
slug: tool-layer-vs-sandbox-layer
title: "Tool layer vs. sandbox layer: why helmdeck + NVIDIA OpenShell is non-duplicative"
authors: [tosin]
tags: [architecture, enterprise, integration, openshell, sandboxing]
description: Most "secure agent platform" stories conflate tool execution with sandbox enforcement. They're different layers with different failure modes. Helmdeck owns the tool layer (39 packs, MCP, vault, artifact store). NVIDIA OpenShell owns the sandbox layer (MicroVMs, L7 OPA policy, Landlock). The post-v1.0 roadmap composes them — and the four-phase community contribution path is open.
date: 2026-05-13
---

## Two layers, two failure modes

When an enterprise asks "is your agent platform secure?", the question is almost always a bundle of two distinct concerns:

1. **Tool layer:** Can the agent only call the tools we approved? Are the tool inputs/outputs validated? Are credentials kept out of the LLM's context? Are calls audited?
2. **Sandbox layer:** When a tool runs code, browses the web, or shells out — is that execution isolated from the host? Can it reach internal networks? Can it write outside its workdir?

These look adjacent but they fail differently. A tool layer fails when an agent calls something it shouldn't have access to — fixable by tightening the tool registry. A sandbox layer fails when an approved tool gets compromised mid-execution — fixable only by reducing what the execution environment can reach.

<!-- truncate -->

Helmdeck's thesis from day one has been that the bottleneck for production-grade agent platforms is the tool layer: schema-validated capability packs, an MCP server that exposes them uniformly, a vault that injects credentials into outbound HTTP without the agent ever seeing them, and an artifact store that captures every output. We're at 39 packs covering browser automation, vision, document parsing, GitHub, content production, and code execution — with ≥90% success rates on 7B–30B-class open-weight models because the multi-step complexity lives behind a single typed JSON call.

[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) (alpha, Rust-based, Apache 2.0) is solving the *other* problem. Declarative YAML policies describe what a sandbox can reach. OPA enforces them at L7 with hot-reload. libkrun provides MicroVM compute. Landlock enforces filesystem rules at the kernel level. From an OpenShell deployment, "run the agent process in here, with this network policy" is one CLI invocation.

Both projects ship something the other doesn't have. We are not solving the same problem.

## What changes if you compose them

The standalone helmdeck story today: agents call our 39 packs via MCP. The packs run in Docker containers with seccomp profiles and dropped capabilities. The egress guard rejects outbound URLs against a blocklist. That's solid for most operators, and Phase 7 (Kubernetes & GA, in progress as of v0.12.1) adds the production hardening — NetworkPolicies, KEDA autoscaling, External Secrets, the works.

The composed story doesn't replace any of that. It changes one specific thing: **helmdeck's `SessionRuntime` interface — the seam between the pack engine and execution backends — gains a third backend.**

```
SessionRuntime
├── DockerSessionRuntime          (today)
├── KubernetesSessionRuntime      (v1.0, Phase 7)
└── OpenShellSessionRuntime       (v1.x, Phase 3 of this roadmap)
```

When helmdeck's pack engine needs to spawn a browser sidecar to run `browser.screenshot_url`, today it shells out to the Docker SDK. After Phase 3, it calls OpenShell's Gateway API instead, which provisions the sidecar in a MicroVM with a pack-family-specific OPA policy attached. The pack code doesn't change. The MCP surface doesn't change. The agent doesn't know.

What the agent — and the enterprise reviewing the architecture — *does* notice:

- **Browser sidecar runs in a dedicated kernel.** A Chromium zero-day exploited via a malicious page can't escape to the host because the libkrun MicroVM boundary is a hardware-virtualization line, not a namespace.
- **L7 policy applies per pack family.** `python.run`'s sidecar can be policy-restricted to deny *any* outbound HTTP — even to internal services — while `browser.screenshot_url`'s sidecar can be allowed to reach exactly the user-supplied target plus the artifact store. The policy is YAML, hot-reloadable, and per-binary.
- **Landlock applies per pack family.** `python.run` gets RW access to `/sandbox` and `/tmp`, R-only on everything else. Even if the LLM generates code attempting to read `/etc/passwd`, the kernel returns EACCES before the process can act.

## The credential split that worried me, but shouldn't

Both stacks already do credential injection. When I first sketched the integration, I worried about a tug-of-war. After mapping it carefully, the responsibilities are non-overlapping:

| Credential | Owner | Mechanism |
|---|---|---|
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, NVIDIA inference key | OpenShell | Provider-injected env vars at agent-sandbox start |
| K8s service account, cloud creds | OpenShell | Provider-injected at sandbox provisioning |
| GitHub PAT, ElevenLabs key, Ghost admin key, Firecrawl key | helmdeck | AES-256-GCM vault; `${vault:NAME}` placeholder substitution at pack-dispatch time |
| Pack output artifact signing | helmdeck | Existing artifact store, unchanged |

OpenShell injects into the **process environment**. Helmdeck injects into the **outbound HTTP request body**. The layers never collide because they intercept at different points in the request lifecycle. Operators need to understand both, but the configuration surface is small in each layer.

## Why this is post-v1.0

This was the question I sat with longest before publishing the roadmap. Phase 3 — the `OpenShellSessionRuntime` — is technically writable today. Why wait?

Two reasons:

**`SessionRuntime` is the seam to v1.0.** The interface is the integration point between helmdeck's pack engine and its execution backends. Phase 7 (Kubernetes & GA) is adding `KubernetesSessionRuntime` *right now*. Forking the test matrix to add a *third* backend before the second one is stable doubles the smoke surface and slows the path to GA. Post-v1.0, adding a third backend is purely additive — the seam is well-understood, the matrix is established, the cost is local.

**OpenShell is alpha.** Production-grade enterprise integrations need a stable Gateway API. The OpenShell team has been clear about rough edges in their current release; binding helmdeck to an alpha contract creates coordinated-release pain neither team needs.

The post-GA timing also benefits the enterprise audience evaluating helmdeck. The decision tree is: "Do we trust helmdeck for production agent workloads?" → install via the v1.0 Helm chart → walk the standalone story → *then* layer OpenShell when hardware isolation becomes the bottleneck. That's a much better adoption path than "evaluate two alpha-stage products simultaneously."

## What we shipped today

PR #192 lands the design — not the code:

- **`docs/integrations/openshell.md`** — full four-phase roadmap, credential split, value table, risks.
- **`docs/RELEASES.md` → v1.x Enterprise integration tracks** — new section in the post-GA release plan.
- **Five GitHub issues** — [tracking epic #193](https://github.com/tosin2013/helmdeck/issues/193), and one per phase: [#194](https://github.com/tosin2013/helmdeck/issues/194) (Phase 1), [#195](https://github.com/tosin2013/helmdeck/issues/195) (Phase 2), [#196](https://github.com/tosin2013/helmdeck/issues/196) (Phase 3), [#197](https://github.com/tosin2013/helmdeck/issues/197) (Phase 4).

The four phases break cleanly by skill profile:

- **Phases 1 and 2** are docs + example YAML policy. Anyone with Docker + OpenShell + a couple of hours can contribute. They're tagged `good first issue` for exactly this reason.
- **Phase 3** is the heavy one — Go work writing the OpenShell Gateway client + the `SessionRuntime` implementation + a new ADR. Help wanted from someone with agent-platform background.
- **Phase 4** is OTel collector + OPA + Grafana. Niche, but well-scoped — connect two existing telemetry streams on the sandbox ID.

We're not shipping an "OpenShell version of helmdeck." We're shipping a roadmap that says: if you want the hardware-isolated, policy-governed, MicroVM-backed version of every helmdeck sidecar, here's the four-phase path. Pick the phase that fits your team. The standalone helmdeck story stays solid for everyone who doesn't need the extra layer.

## The thing I'd push back on, if I were the reader

The honest counter-argument: "Why not just merge the projects? If the layers are so complementary, why not one stack?"

Two reasons. First, OpenShell is NVIDIA's project — they're optimizing for their inference router and their hardware. Helmdeck is optimizing for self-hosted teams who may run on commodity Linux without an NVIDIA GPU. The audiences overlap but don't coincide.

Second, and more importantly: the two-stack story is *more honest* about what each layer does. A merged product would have to abstract both layers behind one API, and the abstractions would leak. An enterprise reviewing helmdeck-with-OpenShell can audit each layer independently — read OpenShell's policy YAML, read helmdeck's pack schemas, see exactly what each layer is responsible for. That's a security property of the architecture, not just an aesthetic preference.

If you're an architect evaluating either project today: read [helmdeck's design doc](/integrations/openshell) for the composed story, read [OpenShell's openclaw.md example](https://github.com/NVIDIA/OpenShell) for the standalone sandbox story, and decide which layer is your current bottleneck. Then watch the four issues — Phases 1 and 2 will land first, and you can be the operator who reports on the production deployment.
