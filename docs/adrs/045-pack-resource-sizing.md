---
description: "ADR-045: Pack Resource Sizing via CPU Profiles — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 45. Pack Resource Sizing via CPU Profiles

**Status**: Accepted (initial slice shipped: profiles + hyperframes.render + slides.narrate migrated)
**Date**: 2026-05-29
**Domain**: pack-engine, session-runtime, deployment

## Context

Every pack that needs a session container runs against `session.Spec`, which carries `MemoryLimit`, `Timeout`, `CPULimit`, etc. Historically `CPULimit` was a single number, and packs left it zero — so the docker runtime defaulted to **1.0 cores for everything**. That's fine for an I/O-bound pack (a Playwright login spends 99% of its time waiting on network), but it cripples a CPU-bound pack: `hyperframes.render` is headless Chromium driving a GSAP composition while ffmpeg encodes frames — both wildly parallel — and pegging it at 1 core meant a 1080p render took ~25 minutes on a host with 7 idle cores, and routinely raced the 30-minute pipeline timeout.

The naive fix is to hardcode `CPULimit: 4` into the offending pack. But the next compute-bound pack we add (or a community pack from the marketplace) has to remember to do the same thing, and the right number depends on the host — 4 cores on a 4-core dev machine is the whole box, 4 on a 32-core CI runner is conservative. Different deployments need different numbers, and packs don't know which deployment they're running in.

What packs CAN know is what *class* of work they do. That's the abstraction we surface.

## Decision

Add **`session.CPUProfile`** — a coarse, runtime-portable workload-class hint a pack declares on its `SessionSpec`. The runtime resolves it to a concrete cap based on the host. Two profiles are defined initially:

| Profile           | When to use                                           | Default cap                              |
| ----------------- | ----------------------------------------------------- | ---------------------------------------- |
| `ProfileIO`       | I/O-bound: HTTP, Playwright, shell-out to short CLIs  | `1.0` core                               |
| `ProfileCompute`  | CPU-bound: video encode, large render, in-proc OCR    | `clamp(host_cores - 1, 1, 6)`            |

The compute heuristic — `host_cores - 1`, clamped to `[1, 6]` — leaves one core for the control-plane and host, and caps at six because ffmpeg + Chromium saturate around there (more cores sit idle). Concretely: a 4-core host gives 3 cores to compute packs, an 8-core host gives 6, a 64-core host still gives 6.

Resolution order in `runtime.withDefaults`:

1. Pack set `CPULimit` explicitly (non-zero) → use that number. Pinned bypass for packs that genuinely need exact sizing.
2. Else read `Spec.CPUProfile`, resolve via `session.ResolveCPUProfile`. Empty string defaults to `ProfileIO` (the legacy 1-core behavior — backwards-compatible).

Operators override per profile via env, not per pack:

- `HELMDECK_IO_CPU_LIMIT` — fractional cores for `ProfileIO`
- `HELMDECK_COMPUTE_CPU_LIMIT` — fractional cores for `ProfileCompute`

Per-profile env wins over the heuristic. Garbage or non-positive values fall back to the heuristic, not silently to zero.

The control-plane logs the resolved caps at startup so operators don't have to inspect a session container to see what their packs got:

```
{"msg":"session CPU profile caps","io_cores":1,"compute_cores":6}
```

### Initial migration

- `hyperframes.render` → `ProfileCompute`
- `slides.narrate` → `ProfileCompute` (Marp + per-segment ffmpeg encode + concat)

Every other session-using pack stays on the implicit `ProfileIO` default — no change in behavior. New CPU-bound packs declare `CPUProfile: session.ProfileCompute` instead of reimplementing the host-aware logic.

### Kubernetes deployment (ADR 009)

The docker runtime translates `CPULimit` into `HostConfig.NanoCpus`. A Kubernetes `session.Runtime` implementation translates the same `CPULimit` into the Pod spec's `resources.limits.cpu` (and `requests.cpu` at the same value, to guarantee scheduling). The profile system is runtime-portable: a pack still just declares `CPUProfile`, and the K8s runtime's `withDefaults` calls the same `session.ResolveCPUProfile` to get the number. `runtime.NumCPU()` inside a Pod honors cgroup constraints, so a control-plane Pod with `cpu: "2"` will autodetect a compute cap of `min(2-1, 6) = 1`, which is the right answer for that Pod's neighborhood.

## Consequences

**Positive.** New CPU-bound packs pick a profile and forget the math; a 12-minute video render on an 8-core box now fits well inside the 30-minute pipeline timeout; the policy lives in code + this ADR, not scattered through pack files; operators can pin per-profile without forking the binary.

**Negative.** Two `session.Spec` fields (`CPULimit` + `CPUProfile`) can confuse a pack author about which one to set. Mitigated by the resolution rule: leave `CPULimit` at zero unless you have a specific reason; pick a profile. The runtime defaulting empty-profile-and-zero-CPULimit to 1 core preserves every pre-existing pack's behavior, so the second field is purely additive.

**Out of scope.** A `ProfileMemory` (memory-heavy, modest CPU) — `MemoryLimit` is already explicit on every pack that cares, so a profile would just be a synonym. A per-pack scheduling priority (nice/yield) — Linux cgroup CPU caps already give us the isolation we need. Heterogeneous-host scheduling (give compute packs to fat nodes, IO packs to slim nodes) — that's a Kubernetes-tier concern handled by KEDA + node selectors, not by the pack abstraction.

## Amendment (2026-06-05, [ADR 052](052-av-output-validation-post-step.md))

Phase 3 of the validation arc ([PR #432](https://github.com/tosin2013/helmdeck/pull/432)) added a default-on null-muxer decode pass to every `slides.narrate` and `podcast.generate` run. The pass (`ffmpeg -xerror -err_detect crccheck+bitstream+buffer -f null -`) peaks around **~600 MB** of resident memory on a 1080p × 11-minute video — the worst case observed during the arc's acceptance testing. This sits on top of the encoder's existing peak (libx264 + libass + per-segment ffmpeg processes), so a deck encode that previously fit in 800–900 MB now needs ~1 GB headroom under load. Operators on memory-tight Compose hosts should set `SessionSpec.MemoryLimit: 1g` for the validating packs; the Kubernetes runtime applies the same value to `resources.limits.memory` (and `requests.memory` at the same value, per the existing translation rule). The validation step is **CPU-bound short-burst**, not parallel-heavy, so it does NOT change the `ProfileCompute` resolution — the existing `clamp(host_cores - 1, 1, 6)` cap is still right. Operators who hit memory pressure with validation enabled have an opt-out via the `validate:false` pointer-bool input — preferable to bumping `MemoryLimit` system-wide, since the pressure is per-run rather than per-pack-instance.

## See also

- ADR 002 — Golang control plane (where `session.Spec` lives).
- ADR 009 — Dual-tier deployment (Compose + Kubernetes).
- ADR 011 — Tiered isolation (Docker / gVisor / Firecracker).
- [ADR 052](052-av-output-validation-post-step.md) — AV validation post-step (motivates the +600 MB memory amendment).
- `docs/reference/hardware-sizing.md` — operator-facing numbers and override env vars.
