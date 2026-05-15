# ADR 038 — Marketplace pack execution via dedicated sidecar

**Status:** Accepted
**Date:** 2026-05-15
**Author:** Tosin Akinosho
**Domain:** marketplace, runtime, security

## Context

[ADR 034](034-pack-marketplace.md) lays out the pack-marketplace surface and identifies four handler types — `builtin`, `command`, `composite`, `wasm`. The v0.13.0 beta install path ([T812 / #30](https://github.com/tosin2013/helmdeck/issues/30)) implements `command` handlers: a manifest plus an executable handler script (bash, Python, Node, etc.) that reads JSON from stdin and writes JSON to stdout.

The first cut of T812's installer constructed `packs.NewCommandPack` directly, which produces a handler that uses `exec.CommandContext` to spawn the script **inside the control-plane process**. The unit tests passed because they run in the Go test binary on a dev machine that has bash + jq available.

**The production environment doesn't.** [`deploy/docker/control-plane.Dockerfile`](../../deploy/docker/control-plane.Dockerfile) runs on `gcr.io/distroless/static:nonroot` — the runtime is exclusively the Go binary, `/data`, and the static-distroless minimum (no shell, no jq, no Python, no curl). This is the right choice for the control plane: minimal CVE surface, smaller image, and no extra moving parts in the security-sensitive layer that arbitrates auth + audit.

But it means **community marketplace command-handler packs cannot execute today**. The first call into a freshly-installed `cmd.upper` from `tosin2013/helmdeck-marketplace` would crash with `exec: "bash": executable file not found in $PATH`.

The built-in packs that need a toolchain dodge this problem by routing through sidecars:
- `python.run` runs inside `helmdeck-sidecar-python`
- `node.run` runs inside `helmdeck-sidecar-node`
- `slides.render` / `slides.narrate` route through the base sidecar (which has Chromium + Marp + ffmpeg)
- `hyperframes.render` (v0.13.0) runs inside `helmdeck-sidecar-hyperframes` (Node 22 + ffmpeg)

Each of those packs sets `NeedsSession: true` + `SessionSpec.Image: <image>` and dispatches via `ec.Exec(ctx, session.ExecRequest{...})`, which the session runtime resolves to a command run inside the sidecar container — not in the control-plane process.

Marketplace command-handler packs need the same plumbing, or they aren't installable in any real deployment.

## Decision

**Marketplace command-handler packs route their handler execution through a dedicated `helmdeck-sidecar-marketplace` image.** The control-plane stays distroless. The sidecar bundles the common toolchain community packs are expected to need.

### Default sidecar image

`ghcr.io/tosin2013/helmdeck-sidecar-marketplace:<version>` ships bundling:

| Toolchain | Version | Why |
|---|---|---|
| `bash` (Debian default) | 5.x | Default shell for handler scripts. |
| `jq` | latest pinned | Standard JSON IO for command-handler packs (every seed pack uses it). |
| `curl` | latest pinned | HTTP from handler scripts. |
| `python3` | 3.11+ | Python community packs. Includes stdlib only — packs that need pip deps must declare their own sidecar. |
| `nodejs` | 20 LTS | Node community packs. Same caveat re: dep declaration. |
| `sha256sum`, `sed`, `awk`, `grep`, `tr`, `cut`, `head`, `tail`, `wc` | coreutils | Standard Unix utilities every shell-based pack assumes. |

Built from `deploy/docker/sidecar-marketplace.Dockerfile`, published by `.github/workflows/sidecar-marketplace.yml`, exact-pinned per [ADR 037](037-upstream-package-version-management.md).

### Per-pack sidecar override

The marketplace manifest's `handler` block gains an optional `sidecar` field:

```yaml
handler:
  type: command
  command: ["./handler.py"]
  sidecar:
    image: ghcr.io/some-author/helmdeck-sidecar-imagemagick:v1
```

When absent, the installer defaults to `helmdeck-sidecar-marketplace:latest` (or whatever `HELMDECK_SIDECAR_MARKETPLACE` env var on the control plane resolves to). When present, the install path uses the specified image for that pack's `SessionSpec.Image`.

This lets community pack authors ship packs that need heavier toolchains (image processing, video, ML inference) without bloating the default marketplace sidecar.

**Trust:** custom sidecars must be cosign-signed by the same identity as the pack manifest, OR operators must opt-in with `HELMDECK_MARKETPLACE_TRUST_UNSIGNED_SIDECARS=1`. The detail of this gate lands in the cosign-verify follow-up; for the v0.13.0 beta the override field is honored unconditionally and surfaces in the install response so operators can audit.

### Install path changes

Per-pack install (still in `internal/marketplace/install.go`):

1. Resolve pack from catalog.
2. Fetch manifest.
3. Validate `handler.type == "command"`.
4. `git clone` the marketplace repo + copy `packs/<name>/` to disk.
5. **New**: resolve the sidecar image (manifest override → default).
6. **New**: construct a pack with `NeedsSession: true` + `SessionSpec.Image: <resolved>`. The handler is a small closure that:
   - Uses `ec.Exec` with stdin to upload the pack's handler script to a fresh tmp path inside the session (`/tmp/helmdeck-pack-<name>/`).
   - Uses `ec.Exec` to `chmod +x` and execute the handler with the pack's input piped to stdin.
   - Returns the script's stdout as the pack output.
7. Register with `packReg`. Live tools/list updates.

The handler-upload happens per-call (matching the `slides.narrate` / `hyperframes.render` pattern). For marketplace packs at typical sizes (1-10 KB of handler script), this is microsecond overhead.

### What this does NOT decide

- **Sidecar pre-pulling.** v0.13.0 beta lazy-pulls when the first install hits an image that isn't local. v1.x may pre-pull during marketplace catalog refresh; that's a separate optimization.
- **Cosign verification of the sidecar itself.** The image is built by helmdeck CI and signed under the same identity that signs the marketplace pack catalog. Verification at pull time is the operator runtime's job (Docker config-level cosign verification, or a follow-up sigstore integration).
- **Composite-handler execution.** Composite packs orchestrate other packs and don't directly need a sidecar. Their execution model is a separate concern, deferred.
- **WASM-handler execution.** Phase 8 work. Runs in a WASI sandbox in the control-plane process.

## Consequences

**Positive:**

- Control plane stays distroless — minimal CVE surface, faster to scan + sign.
- Community packs can ship in any language the curated sidecar supports without operator-side toolchain installs.
- Custom-sidecar override gives sophisticated pack authors an escape hatch without breaking the default UX.
- Reuses the existing session/sidecar/audit infrastructure — no new security primitive.

**Negative:**

- One extra image to maintain (~80-150 MB pulled per deployment that uses the marketplace).
- Per-call sidecar spawn adds 500 ms - 2 s latency vs. in-process exec. Acceptable for marketplace packs that typically wrap external SaaS calls (network latency dominates anyway); painful for hot inner-loop packs that may want to declare a different execution model in a future ADR.
- Custom-sidecar override expands the supply-chain surface — every operator install pulls an image controlled by a third-party author. The trust model needs to catch up before v1.0.

**Trade-off we accept:**

Marketplace command-handler packs are slower to invoke than built-in packs (per-call session spawn vs. in-process). For the v0.13.0 beta this is acceptable because the marketplace is positioned for "occasional integration" packs (Jira issue creation, Slack message posting, weather lookup) where network round-trip latency already dominates. Hot inner-loop packs (parse this string, hash that thing) should stay built-in.

## Migration

`internal/marketplace/install.go` — refactor `buildPackFromManifest` to produce a session-routed pack. Existing tests (which use `recordingExecutor`-style fakes) need to update their assertion shape from "subprocess spawned" to "session.Exec invoked with handler upload + execute pair."

`internal/marketplace/types.go` — add `Sidecar` field to `HandlerSpec`.

`deploy/docker/sidecar-marketplace.Dockerfile` — new.

`.github/workflows/sidecar-marketplace.yml` — new, mirrors `sidecar-hyperframes.yml`.

Existing seed packs in `tosin2013/helmdeck-marketplace` (cmd.upper / cmd.lower / cmd.wordcount) need no manifest change — they use the default sidecar.

The helmdeck-marketplace repo's `schemas/helmdeck-pack.schema.json` gains an optional `handler.sidecar` block. Companion PR to the marketplace repo.

## Related

- [ADR 001](001-sidecar-pattern-for-browser-isolation.md) — the sidecar pattern this ADR reuses.
- [ADR 034](034-pack-marketplace.md) — the marketplace's overall design.
- [ADR 037](037-upstream-package-version-management.md) — the pin + sentinel discipline the new sidecar image inherits.
- [#28 / T810](https://github.com/tosin2013/helmdeck/issues/28) — catalog endpoint (merged).
- [#30 / T812](https://github.com/tosin2013/helmdeck/issues/30) — install/uninstall REST; this ADR is the execution-model decision baked into the same PR.
- [tosin2013/helmdeck-marketplace](https://github.com/tosin2013/helmdeck-marketplace) — the catalog repo whose seed packs this execution path serves.

## PRD sections

§6.6 Capability Packs, §6.7 Pack Authoring.
