# Hardware sizing

Reference numbers for how much CPU and memory the helmdeck control plane and its session sidecars actually use, plus the per-profile knobs you can turn without forking the binary.

For the design rationale (why we use CPU **profiles** instead of per-pack numbers, how the autodetect heuristic was chosen, how it maps to Kubernetes), see [ADR 045](../adrs/045-pack-resource-sizing.md).

## Minimums

| Use case                                 | Cores | Memory | Notes                                                              |
| ---------------------------------------- | ----- | ------ | ------------------------------------------------------------------ |
| Control plane + web + repo packs only    | 2     | 4 GiB  | I/O-bound packs run with ~1 core each; no video work.              |
| Above + slide PDF render                 | 2     | 6 GiB  | `slides.render` is brief Chromium + PDF — fits in the IO profile.  |
| Above + narrated MP4 (`slides.narrate`)  | 4     | 8 GiB  | Encode + concat will use up to 3 cores on a 4-core box.            |
| Above + `hyperframes.render` 1080p       | 8     | 12 GiB | Headless Chromium + ffmpeg saturate at ~6 cores; render is heavy.  |
| `hyperframes.render` 4k                  | 8     | 16 GiB | Bump the pack's `MemoryLimit` override via env (see below).        |

If you're under the recommended cores for a workload, helmdeck still runs — the render just takes proportionally longer. A 1-core host caps every profile at 1.0 cores by design (degraded but functional).

## What each profile gets

The session runtime resolves a pack's `CPUProfile` to a concrete CPU cap at startup. The resolved numbers are logged once on boot:

```
{"msg":"session CPU profile caps","io_cores":1,"compute_cores":6}
```

### `io_bound` (the default)

I/O-bound packs — Playwright sessions, HTTP orchestration, repo clones, OCR-by-HTTP-call. **Default: 1.0 core.** A second core would sit idle waiting on the network.

Used by: `web.scrape`, `web.test`, `web.login`, `web.fetch`, `browser.interact`, `screenshot.url`, `scrape.spa`, `repo.fetch`, `repo.map`, `repo.push`, `fs.*`, `doc.ocr`, `doc.parse`, `podcast.generate`, `swe.solve`, `vision.*`, `desktop.*`, `slides.render`, `blog.publish`, `content.ground`.

### `compute_bound`

CPU-bound packs — video encode, multi-segment render, ffmpeg work. **Default: `clamp(host_cores - 1, 1, 6)`.** Leaves one core for the host; caps at 6 because ffmpeg + Chromium saturate around there.

| Host cores | `compute_bound` cap | Why                                                  |
| ---------- | ------------------- | ---------------------------------------------------- |
| 1          | 1.0                 | Degraded but functional — gives it the only core.    |
| 2          | 1.0                 | Leave one for the host.                              |
| 4          | 3.0                 | Three for the render, one for everything else.       |
| 8          | 6.0                 | Saturation cap kicks in.                             |
| 16+        | 6.0                 | More cores don't speed up the encode meaningfully.   |

Used by: `hyperframes.render`, `slides.narrate`.

## Operator overrides

Pin a cap explicitly via env. Per-profile env wins over the autodetect heuristic. Garbage or non-positive values fall back to the heuristic — you can't accidentally cap a render at zero cores.

| Env var                          | Effect                                                       |
| -------------------------------- | ------------------------------------------------------------ |
| `HELMDECK_IO_CPU_LIMIT`          | Fractional cores for every `ProfileIO` pack.                 |
| `HELMDECK_COMPUTE_CPU_LIMIT`     | Fractional cores for every `ProfileCompute` pack.            |

Examples:

```bash
# Big shared CI box — reserve 4 cores for everything else,
# let the render use the rest.
HELMDECK_COMPUTE_CPU_LIMIT=12

# Constrained dev laptop — keep IO packs on half a core so
# the host stays responsive during a Playwright run.
HELMDECK_IO_CPU_LIMIT=0.5
```

Per-pack `MemoryLimit` is hard-coded on each pack today (e.g. `hyperframes.render` = `4g`, `slides.narrate` = `3g`). If you need to override memory for a specific pack, set the pack's `SessionSpec.MemoryLimit` in code; an env-driven memory profile is a follow-up if it turns out to be needed.

## Kubernetes

The same `CPUProfile` works on Kubernetes. A K8s `session.Runtime` implementation translates the resolved `CPULimit` into the Pod spec's `resources.requests.cpu` and `resources.limits.cpu` (set equal to guarantee scheduling). `runtime.NumCPU()` inside a Pod honors the cgroup cap, so a control-plane Pod running with `cpu: "2"` autodetects a compute cap of `min(2-1, 6) = 1`, which is the right answer for that Pod's neighborhood.

If you're scheduling compute-bound packs onto dedicated nodes (e.g. an autoscaled video-render node pool), put the per-profile env override on those nodes' Pod spec, not on the control plane.

## See also

- [ADR 045 — Pack resource sizing via CPU profiles](../adrs/045-pack-resource-sizing.md)
- [ADR 011 — Tiered isolation (Docker / gVisor / Firecracker)](../adrs/011-tiered-isolation-docker-gvisor-firecracker.md)
- [ADR 009 — Dual-tier deployment](../adrs/009-dual-tier-deployment-compose-and-kubernetes.md)
