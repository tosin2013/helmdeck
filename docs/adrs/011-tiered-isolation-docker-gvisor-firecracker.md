# 11. Tiered Session Isolation: Docker, gVisor, Firecracker

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: security

## Context
SandboxEscapeBench (Oxford / UK AISI, March 2026) demonstrated that frontier models can reliably escape standard Linux containers through common misconfigurations. Standard Docker shares the host kernel, leaving prompt-injected agents a viable escalation path. Different operators have different tolerance for cold-start latency vs. blast-radius risk (PRD §19.5).

## Decision
Expose a `Session Isolation Level` setting on the Security Policies panel with three tiers:
- **Standard** (default): Docker with seccomp `chrome.json`, drop-all caps + add `SYS_ADMIN`, `runAsNonRoot: 1000`.
- **Enhanced**: gVisor runtime (~50 ms cold start, syscall interception in user space).
- **Maximum**: Firecracker microVM (~125 ms cold start, hardware-enforced isolation, dedicated kernel per workload).

The Go control plane abstracts over the underlying container runtime via a `SessionRuntime` interface implemented by Docker, `runsc` (gVisor), and `firecracker-containerd`. The choice is per-deployment via the Helm `isolation.level` value, with per-session override allowed for high-risk workloads.

## Consequences
**Positive:** operators choose their security/latency tradeoff; defense-in-depth against the SandboxEscapeBench class of attacks.
**Negative:** three runtimes to test and maintain; Firecracker requires bare-metal nodes or nested virtualization; observability and networking differ subtly across tiers.

## Related PRD Sections
§19.5 MicroVM Isolation, §8.7 Security Policies Panel, §10 Security Model, §20.8 Helm chart toggles
