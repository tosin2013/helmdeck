# Sidecar language images

Helmdeck packs that need a programming-language toolchain run inside
**dedicated sidecar images** that extend the base browser sidecar
with the toolchain pre-installed. Each pack pins its own image via
`SessionSpec.Image`, so multiple language sidecars coexist in the
same helmdeck deployment without bloating any single image.

This is the "Option B" pattern from the design discussion: pack code
stays language-agnostic at the API layer, but each pack acquires
exactly the toolchain it needs at session-acquire time.

## Currently supported

| Language    | Pack                 | Image                                                    | Override env var              |
| :---------- | :------------------- | :------------------------------------------------------- | :---------------------------- |
| Python 3    | `python.run`         | `ghcr.io/tosin2013/helmdeck-sidecar-python:latest`       | `HELMDECK_SIDECAR_PYTHON`     |
| Node.js     | `node.run`           | `ghcr.io/tosin2013/helmdeck-sidecar-node:latest`         | `HELMDECK_SIDECAR_NODE`       |
| HyperFrames | `hyperframes.render` | `ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:latest`  | `HELMDECK_SIDECAR_HYPERFRAMES` |

Both images are built from the canonical helmdeck base sidecar and
inherit every browser/desktop/scrot/git capability the base ships.
A Python or Node session is a strict superset of a base session.

### What's in each image

**Python sidecar** (`deploy/docker/sidecar-python.Dockerfile`):

- CPython 3 (Debian 12 system Python)
- pip, venv, build-essential
- Pre-installed: `pytest`, `ruff`, `mypy`, `requests`, `httpx`, `pyyaml`, `rich`
- Everything in the base sidecar (Chromium, git, ssh, scrot, xdotool, …)

**Node sidecar** (`deploy/docker/sidecar-node.Dockerfile`):

- Node.js 20 LTS via NodeSource
- npm, pnpm, yarn (via corepack)
- Pre-installed globally: `typescript`, `ts-node`, `eslint`, `prettier`, `vitest`
- Everything in the base sidecar

**HyperFrames sidecar** (`deploy/docker/sidecar-hyperframes.Dockerfile`):

- FFmpeg (system) + libavcodec-extra + libx264 for the deterministic encode pass
- `@hyperframes/cli` (pinned; Chromium-BeginFrame composition renderer)
- Producer-pipeline env contract pre-applied (`PRODUCER_DISABLE_GPU=true`, `PRODUCER_FORCE_SCREENSHOT=true`, `PRODUCER_PUPPETEER_LAUNCH_TIMEOUT_MS=120000`)
- Everything in the base sidecar (Chromium, Node 20, Marp, Playwright MCP)

HyperFrames isn't a "language" sidecar — it's a media-pipeline sidecar — but it ships through the same per-pack-image mechanism because the encode toolchain is heavy enough that pulling it on every deployment would be wasteful for operators who don't render video.

## Building locally

```sh
make sidecars                # build base + python + node
make sidecar-python-build    # python only
make sidecar-node-build      # node only
```

The language sidecars depend on the base sidecar tag
(`helmdeck-sidecar:dev`); `make sidecar-python-build` will build the
base first if it isn't present.

## Calling the language packs

Once the right image is reachable by the runtime (locally tagged or
pulled from ghcr), the packs are immediately available via the
standard pack endpoint:

```sh
# Inline Python expression
curl -X POST http://localhost:3000/api/v1/packs/python.run \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"code":"import json; print(json.dumps({\"answer\": 42}))"}'

# Run pytest in a cloned repo
curl -X POST http://localhost:3000/api/v1/packs/python.run \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"command":["pytest","-v"],"cwd":"/tmp/helmdeck-clone-X1"}'

# Inline Node expression
curl -X POST http://localhost:3000/api/v1/packs/node.run \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"code":"console.log(JSON.stringify({sum: 2+2}))"}'

# Run npm test in a cloned repo
curl -X POST http://localhost:3000/api/v1/packs/node.run \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"command":["npm","test"],"cwd":"/tmp/helmdeck-clone-X1"}'
```

The response shape is identical for both packs:

```json
{
  "stdout":    "...",
  "stderr":    "...",
  "exit_code": 0,
  "runtime":   "python"
}
```

A non-zero exit code is **not** a pack error — failing tests are a
normal pack outcome and the LLM can branch on `exit_code` directly.

## Adding a new language

Adding Rust, Go, Ruby, Java, or anything else is a four-file change:

1. **Dockerfile** — `deploy/docker/sidecar-<lang>.Dockerfile`. Copy
   one of the existing two and replace the apt/install lines. The
   `ARG BASE_IMAGE` boilerplate at the top must stay so `make` can
   point at a locally-built base.
2. **Makefile target** — add a `sidecar-<lang>-build` target next to
   `sidecar-python-build` and append it to the `sidecars` aggregate.
3. **Pack** — `internal/packs/builtin/<lang>_run.go`. Mirror
   `python_run.go`. Three things to change: the pack name
   (`<lang>.run`), the inline-code argv (`["rustc","-",…]` etc.),
   and the `<lang>SidecarImage()` helper.
4. **Registration** — register the new pack in
   `cmd/control-plane/main.go` next to `PythonRun()`/`NodeRun()`.

The shared helpers (`runWithCwd`, `validateLangRunInput`,
`marshalLangRunResult`) are reusable across every language pack — you
shouldn't have to copy any of them.

If you'd rather **request** a language than build one yourself, file
an issue using the **"Sidecar language request"** template at
[github.com/tosin2013/helmdeck/issues/new/choose](https://github.com/tosin2013/helmdeck/issues/new/choose).
The template asks for the language, the toolchain you need
pre-installed, and the workflows you'd want to drive — enough
context for a maintainer to spec out the new sidecar image and pack
in one round trip.

## Operator overrides

Both built-in language sidecars accept an env-var override on the
**control plane** so you can point the pack at a forked image
without recompiling:

```sh
export HELMDECK_SIDECAR_PYTHON=registry.internal/our-python-3.12:v4
export HELMDECK_SIDECAR_NODE=registry.internal/our-node-22:v2
./control-plane
```

The override is read at startup; restart the control plane after
changing it. The new image must still satisfy the base sidecar
contract (HELMDECK_MODE handling, the entrypoint script, the
non-root helmdeck user) — the easiest way to guarantee that is to
keep your fork `FROM ghcr.io/tosin2013/helmdeck-sidecar:<tag>`.

## Why per-pack images instead of one big image

Three reasons:

1. **Pull time.** A polyglot image with go + rust + node + python +
   java + ruby is ~3-5 GB. Operators who only run Python workloads
   shouldn't pay that.
2. **Reproducibility.** Each language pinned to a specific image
   tag means you can roll Python forward to 3.13 without forcing
   your Node workloads onto a different Node version at the same
   time.
3. **Security blast radius.** A vulnerability in one language
   toolchain doesn't ground the rest of the pack catalog. You can
   roll a single sidecar without pinning a release on the others.

The tradeoff is operator surface area: you have N images instead of
one. For helmdeck deployments that's acceptable because every image
is built from the same base sidecar and the base is the only thing
that has security-critical code in it (Chromium, the entrypoint
script, the non-root user setup).
