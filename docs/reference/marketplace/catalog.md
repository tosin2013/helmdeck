---
title: Marketplace catalog
description: Operator reference for the helmdeck pack-marketplace catalog — endpoints, config, trust model. T810 / #28.
keywords: [helmdeck, marketplace, catalog, packs, MCP, ADR-034]
---

# Marketplace catalog

helmdeck v0.13.0 introduces a community pack marketplace. The control plane fetches a catalog index from a configured URL at startup and serves it through two REST endpoints. Operators browse the catalog via the Management UI's `/marketplace` panel; agents discover it via the same endpoints.

The **design rationale** (manifest schema, install flow, trust model, future direction) lives in [ADR 034](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/034-pack-marketplace.md). This page is the operator-facing reference for the catalog surface.

## Configuration

Two env vars on the control plane control marketplace behavior:

| Env var | Default | Effect |
|---|---|---|
| `HELMDECK_MARKETPLACE_URL` | `https://github.com/tosin2013/helmdeck-marketplace` | Marketplace base URL the control plane fetches from. |
| `HELMDECK_MARKETPLACE_DISABLE` | (unset) | Set to `1` to turn the marketplace endpoints off entirely. Useful for air-gapped deployments that don't want to make outbound calls at boot. |

The default URL points at the [official community marketplace repo](https://github.com/tosin2013/helmdeck-marketplace). Operators can self-host a marketplace by pointing `HELMDECK_MARKETPLACE_URL` at:

- A different GitHub repo (`https://github.com/<owner>/<repo>`) — automatically translated to its raw `index.yaml`.
- A raw URL directly (`https://internal-mirror.example.com/marketplace/index.yaml`).
- A local file (`file:///opt/helmdeck-marketplace/index.yaml`) for fully offline operation.

## Endpoints

### `GET /api/v1/marketplace/catalog`

Returns the cached catalog snapshot.

```sh
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:3000/api/v1/marketplace/catalog
```

```json
{
  "index": {
    "catalog_version": "v1",
    "packs": [
      {
        "name": "cmd.upper",
        "version": "v1",
        "path": "packs/cmd.upper",
        "description": "Uppercase a string.",
        "author": "tosin2013",
        "category": "developer-tools",
        "tags": ["example", "string"]
      }
    ]
  },
  "meta": {
    "source": "https://github.com/tosin2013/helmdeck-marketplace",
    "resolved_url": "https://raw.githubusercontent.com/tosin2013/helmdeck-marketplace/main/index.yaml",
    "fetched_at": "2026-05-15T16:42:11Z"
  }
}
```

| Response | Code | When |
|---|---|---|
| `200 OK` | The catalog has been fetched at least once. |
| `503 Service Unavailable` (`marketplace_not_ready`) | The control plane has not yet completed its first refresh — the startup fetch is in flight. Retry in a few seconds. |
| `503 Service Unavailable` (`marketplace_disabled`) | `HELMDECK_MARKETPLACE_DISABLE=1` or no marketplace dep was wired at startup. |

### `POST /api/v1/marketplace/refresh`

Forces a fresh fetch of `index.yaml`, replacing the cached catalog.

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:3000/api/v1/marketplace/refresh
```

Returns the same response shape as the GET endpoint after the refresh completes.

| Response | Code | When |
|---|---|---|
| `200 OK` | Refresh succeeded; the response carries the new catalog. |
| `502 Bad Gateway` (`marketplace_fetch_failed`) | Upstream returned a non-2xx, the YAML failed to parse, or the network call timed out. **The previously-cached catalog is preserved** so callers don't see an empty marketplace after a transient blip. |
| `503 Service Unavailable` (`marketplace_disabled`) | Marketplace is disabled. |

## Catalog lifecycle

- **Startup**: control plane spawns a goroutine that calls `Refresh` once with a 30-second deadline. Boot doesn't block on this — a slow or unreachable upstream just means the catalog endpoint returns `marketplace_not_ready` until the operator calls `/refresh` or the next restart succeeds.
- **Steady state**: the catalog is served from in-memory cache. **There is no background polling.** Catalogs change at git-push speed; aggressive polling would just hammer the upstream for no operator-visible benefit.
- **Refresh**: operators or the Management UI explicitly call `POST /api/v1/marketplace/refresh` when they want the latest catalog. The Marketplace UI panel includes a refresh button.

## Install / uninstall (T812 / #30)

### `POST /api/v1/marketplace/install`

Materializes a pack from the marketplace to disk and **hot-loads** it into the pack registry — no restart, no compose recreate. The pack appears in `GET /api/v1/packs` and the MCP `tools/list` immediately.

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:3000/api/v1/marketplace/install \
  -d '{"name":"cmd.upper"}'
```

```json
{
  "pack": {
    "name": "cmd.upper",
    "version": "v1",
    "source": "https://github.com/tosin2013/helmdeck-marketplace",
    "path": "packs/cmd.upper",
    "installed_at": "2026-05-15T17:42:11Z",
    "install_dir": "/home/helmdeck/.helmdeck/packs/cmd.upper",
    "trust_verified": true,
    "trust_note": "sha256 verified (a3f12b…); manifest declares signed_by=tosin2013 (full cosign identity verification deferred to stage B)"
  },
  "hot_loaded_as": "cmd.upper"
}
```

| Response | Code | When |
|---|---|---|
| `200 OK` | Pack materialized + registered. The handler is callable via `POST /api/v1/packs/<name>` and `tools/list`. |
| `404 Not Found` (`pack_not_in_catalog`) | The name isn't in the catalog. Did you spell it right? Try `POST /refresh` if the pack is brand new. |
| `500` (`install_failed`) | Materialization failed (git clone error, copy failure, manifest mismatch, etc.). Message names the actionable bit. |
| `503` (`marketplace_install_disabled`) | `HELMDECK_PACKS_DIR` not set and `~/.helmdeck/packs` not creatable. |

### `POST /api/v1/marketplace/uninstall`

Removes a previously-installed pack from disk + the pack registry, in that order (deregister-then-delete). Core packs (built into the binary) cannot be uninstalled through this surface and return `pack_not_installed`.

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:3000/api/v1/marketplace/uninstall \
  -d '{"name":"cmd.upper"}'
```

```json
{"status": "uninstalled", "name": "cmd.upper"}
```

### `GET /api/v1/marketplace/installed`

Lists every pack the operator has installed via this marketplace surface (NOT built-in core packs).

```json
{
  "installed": [
    {
      "name": "cmd.upper",
      "version": "v1",
      "source": "https://github.com/tosin2013/helmdeck-marketplace",
      "installed_at": "2026-05-15T17:42:11Z",
      "install_dir": "/home/helmdeck/.helmdeck/packs/cmd.upper",
      "trust_verified": false,
      "trust_note": "..."
    }
  ]
}
```

## Install configuration

| Env var | Default | Effect |
|---|---|---|
| `HELMDECK_PACKS_DIR` | `~/.helmdeck/packs` | Root directory for installed packs. Each pack lands in `<dir>/<name>/`. |
| `HELMDECK_SIDECAR_MARKETPLACE` | `ghcr.io/tosin2013/helmdeck-sidecar-marketplace:latest` | Default sidecar image marketplace command-handler packs route through. Per [ADR 038](../../adrs/038-marketplace-pack-execution-via-sidecar.md). |

The installer creates `HELMDECK_PACKS_DIR` at startup if it doesn't exist. If neither it nor `~/.helmdeck/packs` resolves, install endpoints return `503 marketplace_install_disabled` while the catalog endpoint keeps working.

## Execution model (ADR 038)

Marketplace command-handler packs **do not** run in the control-plane container — that runs `gcr.io/distroless/static:nonroot` and ships no shell, no jq, no Python, no Node. Per [ADR 038](../../adrs/038-marketplace-pack-execution-via-sidecar.md), packs route their handler execution through a dedicated `helmdeck-sidecar-marketplace` sidecar that bundles the common toolchain.

| Element | Where it lives |
|---|---|
| Manifest + handler files | `HELMDECK_PACKS_DIR/<name>/` on the control-plane filesystem |
| Sidecar runtime | `helmdeck-sidecar-marketplace` container, spawned per call |
| Handler invocation | uploaded to the sidecar via `sh -c "cat > /tmp/.../handler"`, chmod +x, then executed with the pack input piped to stdin |
| Output | stdout from the sidecar handler, returned as the pack response (validated against `output_schema` by the engine) |

The sidecar image ships `bash` 5.x, `jq`, `curl`, `python3` 3.11+, `nodejs` 20 LTS, and standard Unix utilities (`sha256sum`, `sed`, `awk`, `grep`, `tr`, `cut`, `head`, `tail`, `wc`).

Packs that need a heavier toolchain (image processing, video, ML inference) **can override the sidecar image** via the manifest's `handler.sidecar.image` field:

```yaml
handler:
  type: command
  command: ["./handler.py"]
  sidecar:
    image: ghcr.io/some-author/helmdeck-sidecar-pillow:v1
```

When `handler.sidecar` is absent, the installer uses the default `HELMDECK_SIDECAR_MARKETPLACE` image. The registered pack's `SessionSpec.Image` is observable via `GET /api/v1/packs`.

## Trust model (v0.13.0 GA status)

Per ADR 034:

- **Core packs** — built into the helmdeck binary; implicit trust. Not installable / uninstallable.
- **Signed packs** — cosign-verified at install time. The pack's `helmdeck-pack.yaml` carries a `trust:` block with `signed_by` + `sha256`; the [marketplace `sign.yml`](https://github.com/tosin2013/helmdeck-marketplace/blob/main/.github/workflows/sign.yml) workflow populates these on every release.
- **Unsigned packs** — packs with no `trust:` block. The install response surfaces `trust_verified: false` + a `trust_note` so the UI / CLI can show a warning before the operator confirms.

### Stage A — deterministic content hash (ships v0.13.0 GA)

The installer **hashes the materialized pack files** and compares to the manifest's `trust.sha256`. This is the actual verification call replacing the v0.13.0-beta stub.

**Hash algorithm** (the marketplace `sign.yml` workflow uses the same):

1. Walk the pack directory, skipping directories AND the `helmdeck-pack.yaml` manifest itself (the manifest carries the hash; including it would be circular).
2. For each remaining file in lexical-by-relative-path order, emit `<forward-slash-rel-path>\0<file-sha256-hex>\n` into a rolling SHA256.
3. Hex-encode the final digest.

What stage A catches:

- Handler script or data files modified between author sign and operator install
- Files renamed, added, or removed under `packs/<name>/`
- Corrupt downloads / wrong marketplace URL

What stage A does NOT catch (manifest is excluded):

- A malicious author modifying the manifest itself (e.g., swapping the handler argv to point at an exfiltrating binary). The manifest's identity claim (`trust.signed_by`) is informational at stage A; **stage B** (cosign keyless verification of the manifest signer's identity against `signed_by`) is the fix.

**Behavior at install time:**

| Manifest state | `trust_verified` | Install proceeds? | `trust_note` shape |
|---|---|---|---|
| No `trust:` block | `false` | Yes (unsigned) | `"no trust block in manifest — pack is unsigned"` |
| `trust.sha256` empty | `false` | Yes (unsigned) | `"manifest declares signed_by=X but no sha256 — pack hash not yet populated (stage A); see ADR 034"` |
| `trust.sha256` matches | `true` | Yes | `"sha256 verified (HEX); manifest declares signed_by=X (full cosign identity verification deferred to stage B)"` |
| `trust.sha256` mismatches | n/a | **No — install fails** | error: `"sha256 mismatch: manifest says X, computed Y — pack contents do not match what the marketplace signed"` |

A mismatch is a **hard rejection** — the installer removes the just-materialized files and returns an error to the caller. The pack is NOT registered. The audit log captures the failed-install decision.

### Stage B — sigstore keyless verify (deferred to v1.0)

Replace stage A's identity-claim-is-informational status with actual cosign keyless verification: fetch the signature artifact for the pack tarball from the marketplace's GitHub Release, verify against Fulcio's root CA chain, match the cert's identity claim against `manifest.trust.signed_by`. Tracked as a v1.0 hardening item. Until then, `trust.signed_by` is documented as informational and the UI's "Signed by X" badge is qualified with "(unverified identity)" when stage A is the only check applied.

## Pack detail (T813)

`GET /api/v1/marketplace/packs/<name>` returns the catalog entry **plus** the full `helmdeck-pack.yaml` manifest fetched from the marketplace repo on demand. Used by the `/marketplace` UI panel's detail dialog to render input/output schemas, examples, and the trust block.

```sh
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:3000/api/v1/marketplace/packs/cmd.upper
```

```json
{
  "entry": { "name": "cmd.upper", "version": "v1", "...": "..." },
  "manifest": {
    "name": "cmd.upper",
    "version": "v1",
    "author": "tosin2013",
    "description": "Uppercase a string.",
    "input_schema": { "required": ["text"], "properties": { "text": { "type": "string" } } },
    "output_schema": { "required": ["text"], "properties": { "text": { "type": "string" } } },
    "handler": { "type": "command", "command": ["./upper"], "timeout_s": 5 },
    "examples": [{ "name": "hello", "input": { "text": "hello" }, "expected_output_subset": { "text": "HELLO" } }]
  }
}
```

| Response | Code | When |
|---|---|---|
| `200 OK` | Catalog entry exists and manifest fetched successfully. |
| `404 pack_not_in_catalog` | Name not in the current catalog snapshot. POST `/refresh` if the pack was added upstream recently. |
| `502 manifest_fetch_failed` | Upstream returned 4xx/5xx or the manifest failed to parse. |

The catalog endpoint deliberately doesn't pre-load every manifest (most packs are never opened); the detail endpoint fetches the one being viewed.

## Management UI panel (T813)

Operators browse + install via the `/marketplace` route in the Management UI. The panel calls the REST endpoints documented above:

- Browse-by-category chips + free-text search across name / description / tags
- Pack detail dialog with schema preview + examples + trust badge
- Install / Uninstall buttons with busy-state and `tools/list` cache invalidation
- Refresh button → `POST /refresh`
- Unsigned-pack confirmation dialog before install (per ADR 034 trust model)

## What still lands in follow-up PRs

- **Real cosign-verify call.** Replaces the stub above.
- **`helmdeck pack install/uninstall` CLI binary.** Calls these REST endpoints. Issue [#30](https://github.com/tosin2013/helmdeck/issues/30) tracks the CLI separately.

## Schema references

Both schemas live in the marketplace repo so contributors can validate locally:

- **Per-pack manifest**: [`schemas/helmdeck-pack.schema.json`](https://github.com/tosin2013/helmdeck-marketplace/blob/main/schemas/helmdeck-pack.schema.json)
- **Catalog index**: [`schemas/index.schema.json`](https://github.com/tosin2013/helmdeck-marketplace/blob/main/schemas/index.schema.json)

Validate a manifest locally before contributing:

```sh
npm install -g ajv-cli js-yaml
node -e "const fs=require('fs');const y=require('js-yaml');console.log(JSON.stringify(y.load(fs.readFileSync('helmdeck-pack.yaml','utf8'))))" > /tmp/manifest.json
ajv validate -s schemas/helmdeck-pack.schema.json -d /tmp/manifest.json --strict=false
```

The marketplace repo's [`validate.yml`](https://github.com/tosin2013/helmdeck-marketplace/blob/main/.github/workflows/validate.yml) workflow runs the same check on every PR.

## Related

- [ADR 034 — Pack marketplace](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/034-pack-marketplace.md) — design
- [Issue #28 (T810)](https://github.com/tosin2013/helmdeck/issues/28) — this PR
- [Issue #30 (T812)](https://github.com/tosin2013/helmdeck/issues/30) — install CLI/REST (next PR)
- [Issue #31 (T813)](https://github.com/tosin2013/helmdeck/issues/31) — UI panel
- [Issue #32 (T814)](https://github.com/tosin2013/helmdeck/issues/32) — community marketplace repo (parallel work)
- [`tosin2013/helmdeck-marketplace`](https://github.com/tosin2013/helmdeck-marketplace) — the catalog repo
