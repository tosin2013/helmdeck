# Extending the Browser Sidecar Image

The browser sidecar image (`ghcr.io/tosin2013/helmdeck-sidecar`, built in T104) is the runtime environment that every browser session container is spawned from. Capability Packs depend on tools being present in this image — if a pack needs `ffmpeg`, `tesseract`, or a Marp theme, it must be in the sidecar, not in the agent's container.

This is the canonical fix for **Gap 1** (missing-binary failures) described in PRD §3.1 and ADR 003.

## Where the image lives

```
deploy/docker/
├── helmdeck-mcp.Dockerfile    bridge binary, distroless (small)
└── sidecar.Dockerfile         browser sidecar (T104 — added in next phase)
```

The sidecar Dockerfile is layered as:

```
FROM ubuntu:24.04
# 1. base system + locale + fonts
# 2. Chromium + driver
# 3. Xvfb + XFCE4 + noVNC (desktop mode)
# 4. Pack dependencies (Marp, Tesseract, ffmpeg, xdotool, scrot)
# 5. Non-root user UID 1000, runtime entrypoint
```

Layer ordering matters: cheap things first (base, fonts), expensive things last (Chromium, language packs). This keeps `docker build` cache hits high when you only change pack dependencies.

## Adding a new system package

Pack `doc.ocr` needs `tesseract-ocr-deu` (German language data). Add it to layer 4:

```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends \
      tesseract-ocr \
      tesseract-ocr-eng \
      tesseract-ocr-deu \           # <-- new
    && rm -rf /var/lib/apt/lists/*
```

Then rebuild and tag:

```bash
make sidecar-build       # builds locally as helmdeck-sidecar:dev
make sidecar-test        # runs the layered smoke matrix
docker tag helmdeck-sidecar:dev ghcr.io/tosin2013/helmdeck-sidecar:vX.Y.Z
docker push ghcr.io/tosin2013/helmdeck-sidecar:vX.Y.Z
```

## Adding a new binary from a release tarball

Pack `slides.video` needs a specific ffmpeg build. Use a multi-stage download to keep the final image clean:

```dockerfile
FROM curlimages/curl:latest AS ffmpeg-dl
ARG FFMPEG_VERSION=7.1
RUN curl -fsSL https://example.com/ffmpeg-${FFMPEG_VERSION}.tar.xz \
    | tar -xJ -C /tmp \
 && cp /tmp/ffmpeg-${FFMPEG_VERSION}/ffmpeg /usr/local/bin/

FROM ubuntu:24.04
# ... rest of sidecar ...
COPY --from=ffmpeg-dl /usr/local/bin/ffmpeg /usr/local/bin/ffmpeg
```

Always pin a version. Never `latest` from upstream.

## Adding a Python or Node tool

If a pack handler needs a Python script or a Node CLI:

1. **Prefer to ship the dependency as a static binary** — static Go, Rust, or single-file Python (PyOxidizer) keeps the image small.
2. If you must install runtime + packages:

```dockerfile
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-pip \
 && pip3 install --no-cache-dir --break-system-packages 'somepkg==1.2.3' \
 && rm -rf /var/lib/apt/lists/*
```

3. Pin every version. Reproducibility is non-negotiable for the success-rate SLO (PRD §18).

## Adding a font

Marp themes and `slides.render` rely on the system font cache. Drop the `.ttf`/`.otf` into `/usr/share/fonts/` and run `fc-cache`:

```dockerfile
COPY fonts/MyBrand.ttf /usr/share/fonts/truetype/mybrand/
RUN fc-cache -f
```

## Image-size budget

| Layer | Soft cap |
| :--- | :--- |
| Base + fonts + locale | 200 MB |
| Chromium + driver | 500 MB |
| Xvfb + XFCE4 minimal + noVNC | 300 MB |
| Pack dependencies (Marp, Tesseract, ffmpeg, xdotool, scrot, socat) | 500 MB |
| **Soft target** | **≤ 1.8 GB uncompressed** |
| **Current actual (T104)** | **~2.2 GB** — trimming follow-up tracked |

The first cut of the image lands at ~2.2 GB, mostly because Chromium's runtime libraries and the minimal XFCE4 components pull more transitive deps than the budget allows. Acceptable for v0.1; the trimming targets are:

- Replace `imagemagick` with a smaller PNG/JPEG-only converter (~80 MB)
- Audit `tesseract-ocr` data files; ship `eng` only and require downstream forks for other languages
- Replace `xfwm4 + xfce4-panel + xfdesktop4` with `openbox` for vision-mode sessions (~150 MB)
- Use multi-stage builds to drop apt list caches and `/usr/share/doc`

The Phase 2 exit gate (≥90% weak-model success) implicitly assumes a fast `docker pull` on the first session — every saved gigabyte buys real cold-start time, especially in the Kubernetes tier where the warm pool may not always be hot enough.

## Validating a change

Before pushing a new sidecar image:

1. **Build:** `make sidecar-build`
2. **Smoke test the substrate:**
   ```bash
   docker run --rm --shm-size=2g -p 9222:9222 \
     helmdeck-sidecar:dev \
     --headless --remote-debugging-port=9222 --remote-debugging-address=0.0.0.0
   curl -fsS http://localhost:9222/json/version
   ```
3. **Smoke test the affected packs** via the control plane:
   ```bash
   ./bin/control-plane -session-network=baas-net &
   curl -X POST http://localhost:3000/api/v1/packs/doc.ocr \
     -d '{"file_url":"https://example.com/test.png","language":"deu"}'
   ```
4. **Run the Model Success Rates regression** against the reference weak-model cohort. If any pack drops below the 80% UI threshold (ADR 008, §8.6), do not promote the image.

## Playwright MCP bundled in the sidecar (T807a, ADR 035)

Layer 4b of `sidecar.Dockerfile` installs Node.js 20 and
`@playwright/mcp@latest` globally. On session start, the entrypoint
launches Playwright MCP alongside the Chromium process it already
manages:

```bash
npx @playwright/mcp@latest \
    --cdp-endpoint http://127.0.0.1:9222 \
    --host 0.0.0.0 \
    --port 8931 \
    --headless \
    --no-sandbox
```

Two design choices matter:

1. **`--cdp-endpoint` attaches, not launches.** Playwright MCP reuses
   the same Chromium process the rest of helmdeck's `browser.*` packs
   talk to. One browser, one cookie jar, one memory footprint.
2. **`PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1`** at `npm install` time
   prevents the Playwright postinstall from pulling ~200 MB of bundled
   Chromium that would never be used — the system chromium from
   layer 2 is the only browser in the image.

The SSE endpoint is exposed on the container at port 8931 and surfaced
to API clients as `playwright_mcp_endpoint` on every session response.
Packs that drive Playwright MCP (for example `web.test`, T807e) read
that field to find the per-session URL — there is no entry in the
`/api/v1/mcp/servers` registry because that registry is for
operator-configured external MCP servers, not auto-launched sidecar
children.

Set `HELMDECK_PLAYWRIGHT_MCP_ENABLED=false` in the session spec env
(or image default) to skip the launch entirely on memory-constrained
hosts; the `playwright_mcp_endpoint` field is then omitted from the
session response so downstream packs can fail with a clear error
instead of trying to connect to a port nothing is listening on.

## Forking the sidecar

If your deployment needs tools that aren't appropriate for the upstream image (proprietary fonts, internal CA bundles, regional language packs), maintain a downstream Dockerfile:

```dockerfile
FROM ghcr.io/tosin2013/helmdeck-sidecar:vX.Y.Z

USER root
RUN apt-get update && apt-get install -y my-internal-package \
 && rm -rf /var/lib/apt/lists/*
COPY internal-ca.crt /usr/local/share/ca-certificates/
RUN update-ca-certificates
USER nonroot
```

Then point the control plane at it via the `image` field in the create-session request, or set the `--default-session-image` flag (added in T108).

## Where Capability Packs declare their dependencies

When you author a pack (ADR 024, §6.7), declare its sidecar requirements in the pack manifest's `requires` block:

```yaml
name: doc.ocr
version: 1.0.0
requires:
  binaries: [tesseract]
  tessdata: [eng, deu, fra]
```

The control plane fails fast at pack registration if the current sidecar image doesn't satisfy the manifest, with a structured `internal_error` carrying the missing component — no more silent stalls (ADR 008).

## Related docs

- **ADR 003** — Capability Packs as the primary product surface
- **ADR 008** — Closed-set typed error codes
- **ADR 011** — Tiered isolation (sidecar runs unprivileged, drop-all-caps + SYS_ADMIN)
- **ADR 024** — User-authored pack extensibility
- **PRD §3.1** — The original Gap 1 (missing-binary) failure mode
- **`docs/TASKS.md` T104** — The sidecar Dockerfile build itself (next task)
