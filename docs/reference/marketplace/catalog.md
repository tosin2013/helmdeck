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

## What this catalog endpoint does NOT do (yet)

The T810 (#28) endpoint is **read-only**. The following land in subsequent PRs:

- **Install packs** — `POST /api/v1/marketplace/install` (T812, [#30](https://github.com/tosin2013/helmdeck/issues/30)).
- **Uninstall packs** — same surface (T812).
- **Pack detail view** — `GET /api/v1/marketplace/packs/<name>` returning the full `helmdeck-pack.yaml` (T813, [#31](https://github.com/tosin2013/helmdeck/issues/31)).
- **Cosign verification** — runs at install time per ADR 034 §"Trust model" (T812).
- **Hot-load into the pack registry** — `tools/list` updates immediately on install, no restart (T812).

For the v0.13.0 beta, the catalog endpoint is enough to power discovery. The next PR adds install.

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
