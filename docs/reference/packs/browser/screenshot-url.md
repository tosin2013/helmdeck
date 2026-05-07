---
title: browser.screenshot_url
description: Navigate a headless browser to a URL and capture a PNG screenshot, returned as a signed-URL artifact.
---

# `browser.screenshot_url`

The reference pack for the helmdeck pack substrate. Drives a headless Chromium session via CDP to navigate to a URL and capture a PNG screenshot, then uploads the PNG to the artifact store and returns a signed-URL key. It's the simplest pack that exercises every layer (input validation → session acquire → CDP → artifact upload → typed result), which is why it's the smoke-test target and ships first in every release ladder.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | `string` | yes | — | Absolute URL the browser should navigate to before capturing. Validated by the egress guard against `HELMDECK_EGRESS_ALLOWLIST` + the metadata-IP block — pointing at `169.254.169.254` or RFC 1918 ranges fails fast with `invalid_input`. |
| `fullPage` | `boolean` | no | `false` | When true, captures the entire scrollable page rather than just the viewport. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `url` | `string` | Echo of the input URL (for client correlation when batching). |
| `artifact_key` | `string` | `browser.screenshot_url/<rand>-screenshot.png`. Fetch via `GET /api/v1/artifacts/<key>` (returns a signed URL or the bytes directly depending on `Accept`). |
| `size` | `number` | PNG size in bytes. |

The PNG bytes are uploaded to the artifact store rather than embedded in the response — weak models calling this pack don't have to handle multi-megabyte base64 payloads.

## Vault credentials needed

**None.** Pure unauthenticated GET → screenshot. If you need to screenshot a page behind a login, use [`web.login_and_fetch`](/PACKS) (vault-backed) or [`web.scrape_spa`](/PACKS) with a vault session-cookie credential.

## CLI invocation

```bash
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$(grep ADMIN deploy/compose/.env.local | cut -d= -f2)\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

curl -fsS -X POST http://localhost:3000/api/v1/packs/browser.screenshot_url \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "url": "https://example.com",
    "fullPage": true
  }'
```

Realistic response:

```json
{
  "url": "https://example.com",
  "artifact_key": "browser.screenshot_url/abc123-screenshot.png",
  "size": 47821
}
```

Fetch the PNG:

```bash
curl -fsS -H "Authorization: Bearer $JWT" \
  "http://localhost:3000/api/v1/artifacts/browser.screenshot_url/abc123-screenshot.png" \
  -o screenshot.png
```

## UI invocation

**Not yet — read-only catalog.** The Capability Packs panel today shows the schema but has no execution UI. Tracked as [T606a](/TASKS#phase-6--management-ui-weeks-1720). Use the CLI invocation above until the Test Runner ships.

When T606a lands, the workflow will be: navigate to *Capability Packs → browser.screenshot_url → Test Runner*, fill the form (auto-generated from the input schema), click **Run**, see the typed output and inline image preview via the Artifact Explorer at `http://localhost:3000/artifacts`.

## Error codes

| Code | When | Operator action |
|---|---|---|
| `invalid_input` | `url` missing or fails the egress guard. | Verify the URL is absolute and not in a blocked range. |
| `session_unavailable` | Engine has no CDP factory wired (sidecar image missing, runtime not started). | Confirm the sidecar image is pulled (`docker images \| grep helmdeck-sidecar`); see [troubleshooting](/howto/troubleshoot-install). |
| `handler_failed` | Chromium navigate or screenshot returned a CDP-level error (DNS failure, TLS error, page never finished loading). | Check the audit log for the wrapped Go error; retry with a different URL to isolate. |
| `artifact_failed` | Garage / S3 wouldn't accept the upload. | Check Garage health (`docker compose logs garage`) and disk pressure (`docker system df`). |

The closed set of typed error codes is enforced by [`internal/packs/errors.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/errors.go).

## Session chaining

`needs_session: true`. The engine acquires an ephemeral session, runs the pack, and tears the session down — no `_session_id` field accepted; sessions are transparent to this pack.

If you need to chain (e.g. `repo.fetch` → patch a file → `browser.screenshot_url` of the staging URL), use the explicit `_session_id` field on packs that support it; this pack does not.

## Async behavior

**Synchronous only.** Typical latency is 1–4 seconds against a warm sidecar (network-dependent). For very heavy pages, the per-session timeout (default 60s) bounds the wait — past that you'll see `handler_failed` with `context deadline exceeded`.

## See also

- Catalog row: [`PACKS.md` browser.screenshot_url](/PACKS)
- Source: [`internal/packs/builtin/screenshot_url.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/screenshot_url.go)
- Agent guidance: [`SKILLS.md`](/integrations/SKILLS)
- Tracking ADR: [ADR 021 — pack-browser-screenshot-url](/adrs/pack-browser-screenshot-url)
