---
slug: the-render-that-pegged-1-of-8-cores
title: "The render that pegged 1 of 8 cores"
authors: [tosin]
tags: [friction, agent-architecture, field-report]
description: A pipeline render sat at 100% CPU for 25 minutes while seven cores idled. The fix wasn't a bigger box — it was teaching the runtime which packs deserve them.
image: /img/social-card.png
date: 2026-05-30
draft: true
---

A `prompt-narrated-video` run on an 8-core / 62 GiB host wedged at 100% CPU for 25 minutes while seven cores sat idle. The render finished about 6 minutes after we fixed it — same host, same composition.

<!-- truncate -->

## Context

We'd just shipped live per-step progress for running pipelines ([#333](https://github.com/tosin2013/helmdeck/pull/333)) — so a long run now surfaces each `ec.Report(pct, message)` call from the active pack in the UI. The very first thing it surfaced was: `10% rendering 1920×1080 @ 30fps (preset=landscape)`, and then it sat there for several minutes.

`docker stats` on the sidecar showed `101% CPU / 626 MiB`. Eight cores on the host, one being used.

## Finding

Every pack that needs a session container runs against `session.Spec`. The Docker runtime defaults `CPULimit` to `1.0` when a pack leaves it at zero — which every pack did. So `web.scrape` (Playwright sessions, 99% I/O wait) and `hyperframes.render` (Chromium + ffmpeg, wildly parallel) both got the same single core.

The naive fix is to hardcode `CPULimit: 4` into `hyperframes_render.go`. But the next compute-bound pack — and the marketplace packs an operator drops in tomorrow — would all have to remember the same dance. And the right number depends on the host: 4 cores is the whole machine on a dev laptop and conservative on a 32-core CI runner.

What packs **can** know is what *class* of work they do. So that's the abstraction we surfaced:

```go
// hyperframes_render.go
SessionSpec: session.Spec{
    Image:       hyperframesSidecarImage(),
    MemoryLimit: "4g",
    Timeout:     60 * time.Minute,
    CPUProfile:  session.ProfileCompute,  // ← new
},
```

The runtime resolves the profile based on the host:

```go
// internal/session/profile.go
func computeCPUFromHost(hostCores int) float64 {
    if hostCores < 2 { return 1.0 }
    cores := hostCores - 1
    if cores > 6 { cores = 6 }
    return float64(cores)
}
```

`clamp(host_cores - 1, 1, 6)` — leave one core for the host, cap at 6 because ffmpeg + Chromium saturate around there (encode tests showed flat throughput past ~6 cores). Operators tune per-profile via `HELMDECK_COMPUTE_CPU_LIMIT` for the cases the heuristic gets wrong.

The numbers, same composition, same host:

| Host cores | `ProfileCompute` cap | Render time, 60s narrated 1080p clip |
| ---------- | -------------------- | ------------------------------------ |
| 4 (laptop) | 3                    | ~9 min                               |
| 8 (this box) | 6                  | ~6 min                               |
| Before this PR (any host) | 1     | ~25 min (and racing the 30-min pipeline timeout) |

Two packs migrated: `hyperframes.render` and `slides.narrate` (Marp + per-segment ffmpeg encode). Every other session pack — web.\*, repo.\*, fs.\*, screenshot, doc.ocr, podcast.generate, swe.solve, vision.\*, slides.render — stays on the implicit `ProfileIO` default. No behavior change for them, and none of them benchmarked faster with more cores anyway.

## Why this matters to you

If you're running heterogeneous workloads in containers — agent platforms doing both I/O-bound web scraping and CPU-bound media encoding from the same control plane — don't hardcode the CPU envelope per container, and don't trust the runtime default. Either:

- **Let the orchestrator decide** (Kubernetes with `resources.limits.cpu` per Pod, sized by node selectors), or
- **Declare the workload class** and let your runtime resolve it host-aware.

The trap we walked into is a common one: a single sensible default (1 core) that works fine for 90% of packs becomes invisible for the 10% that need an order of magnitude more. The fix is not a bigger default — it's surfacing the *class* of work so the platform can size each pack appropriately for the host it's actually on.

There's also a more boring lesson worth naming: a pack stuck at 10% for minutes used to be invisible. Once we shipped live progress, **the bug got loud**, and the fix landed the same day. Observability earns its keep by making latent waste obvious. If you've got a long-running step in production and you can't see what it's doing, you have at least two bugs: the slow one, and the silent one.

## See also

- The PR: [#335 — CPU profiles for session sizing](https://github.com/tosin2013/helmdeck/pull/335)
- The ADR: [ADR 045 — Pack resource sizing via CPU profiles](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/045-pack-resource-sizing.md)
- Operator-facing numbers: [Hardware sizing](https://github.com/tosin2013/helmdeck/blob/main/docs/reference/hardware-sizing.md)
- The live-progress feature that made the bug loud: [#333 — Live per-step progress + hard cancel](https://github.com/tosin2013/helmdeck/pull/333)
