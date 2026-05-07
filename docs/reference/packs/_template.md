---
title: <pack.name>
description: <one-line "what it does">
---

<!--
TEMPLATE — copy this file as docs/reference/packs/<family>/<pack-name>.md
and replace every <placeholder>. Delete this comment when you're done.

Source-of-truth pointers when filling out:
- Pack handler:   internal/packs/builtin/<file>.go
- Catalog row:    docs/PACKS.md
- Agent guidance: docs/integrations/SKILLS.md
- ADR (if any):   docs/adrs/<number>-<slug>.md
-->

# `<pack.name>`

<one paragraph in plain language: what does this pack do, when would an
operator or agent reach for it, what's the typical input/output flow.>

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `<input>` | `string` | yes | — | <description> |
| `<input>` | `boolean` | no | `false` | <description> |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `<output>` | `string` | <description> |
| `artifact_key` | `string` | When this pack produces an artifact (PNG, PDF, MP4, scrape result, etc.). Fetch via `GET /api/v1/artifacts/<key>`. |
| `size` | `number` | Artifact size in bytes. |

## Vault credentials needed

<one of:>

- **None.** This pack runs against the public internet (or session-local data) and doesn't reach the credential vault.
- **`<credential-name>`** — type `<api_key|login|cookie|ssh_key|...>`, scoped to host pattern `<pattern>`. Add via the Management UI's *Vault* panel before invoking. If absent, the pack <fails with `credential_unavailable` | degrades gracefully by ...>.

## CLI invocation

Mint a JWT (admin or any actor with the right grant) and `curl` the pack:

```bash
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$(grep ADMIN deploy/compose/.env.local | cut -d= -f2)\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

curl -fsS -X POST http://localhost:3000/api/v1/packs/<pack.name> \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "<input>": "<value>"
  }'
```

Realistic response (truncated):

```json
{
  "<output>": "<value>",
  "artifact_key": "<pack.name>/abc123-screenshot.png",
  "size": 12345
}
```

## UI invocation

<one of:>

- **Available.** Navigate to *Capability Packs → `<pack.name>` → Test Runner*. Fill the form (auto-generated from the input schema), click **Run**, see the typed output and any artifact links inline.
- **Not yet — read-only catalog.** The Capability Packs panel today shows the schema but has no execution UI. Tracked as [T606a](/TASKS#phase-6--management-ui-weeks-1720). Use the CLI invocation above until that ships.

## Error codes

| Code | When | Operator action |
|---|---|---|
| `invalid_input` | `<input>` missing or malformed. | Re-check the schema. |
| `session_unavailable` | The pack needs a session and the engine had no CDP factory wired (`HELMDECK_PLAYWRIGHT_MCP_ENABLED=false` etc.) or the session timed out. | Verify the sidecar is running; restart with `docker compose restart`. |
| `handler_failed` | Upstream service returned an error. | Check the audit log for the upstream HTTP status. |
| `artifact_failed` | Garage / S3 wouldn't accept the artifact. | Check Garage health and disk pressure. |

The closed set of typed error codes is in [`internal/packs/errors.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/errors.go).

## Session chaining

<one of:>

- **No session.** This pack runs stateless; no `_session_id` field accepted.
- **Optional session.** Pass `_session_id` to reuse an existing session (e.g. one created by an upstream `repo.fetch`). Compatible with: `<list of packs>`.
- **Required.** This pack only runs against a session and will fail with `session_unavailable` if no session is implicit or supplied.

## Async behavior

<one of:>

- **Synchronous only.** The handler returns when the pack completes; expect <duration> latency.
- **Sync + webhook.** Pass `webhook_url` and `webhook_secret` to receive an async POST when the work completes. Useful for long-running packs (`slides.narrate`, `research.deep`, `content.ground`).

## See also

- Catalog row in [`PACKS.md`](/PACKS#<pack-name>)
- Source: [`internal/packs/builtin/<file>.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/<file>.go)
- Agent prompt guidance in [`SKILLS.md`](/integrations/SKILLS#<pack-name>)
- Tracking ADR: [ADR <NNN>](/adrs/<adr-slug>) <if applicable>
