---
title: <pack.name>
description: <one-line "what it does">
keywords: [helmdeck, <family>, <pack-name>, MCP]
---

<!--
TEMPLATE — copy this file as docs/reference/packs/<family>/<pack-name>.md
and replace every <placeholder>. Delete this comment when you're done.

PR-A established the agent-first / developer-second structure: the
OpenClaw chat-UI worked example is the primary view, the curl block is
the developer reference. See docs/reference/packs/browser/ and
docs/reference/packs/fs/ for worked examples.

Source-of-truth pointers when filling out:
- Pack handler:   internal/packs/builtin/<file>.go
- Closed error set: internal/packs/errors.go
- Catalog row:    docs/PACKS.md
- Agent guidance: docs/integrations/SKILLS.md
- ADR (if any):   docs/adrs/<number>-<slug>.md
-->

# `<pack.name>`

<one paragraph in plain language: what does this pack do, when would an
agent reach for it, what's the typical input/output flow.>

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `<input>` | `string` | yes | — | <description> |
| `<input>` | `boolean` | no | `false` | <description> |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `<output>` | `string` | <description> |

(For session-coupled packs, the response also includes a top-level
`session_id` field. For artifact-producing packs, a top-level
`artifacts` array with signed S3 URLs.)

## Vault credentials needed

<one of:>

- **None.**
- **`<credential-name>`** — type `<api_key|login|cookie|ssh_key|...>`, scoped to host pattern `<pattern>`. Add via the Management UI's *Vault* panel before invoking. If absent, the pack <fails with `credential_unavailable` | degrades gracefully by ...>.

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- Paste the live OpenClaw chat-UI capture here:

  1. The chat prompt the user typed.
  2. The tool call OpenClaw emitted (visible in the "tools used" UI).
  3. The agent's text reply (it should describe what changed in its
     world model, not the raw tool output).
  4. Footer: "Verified via OpenClaw <version> + helmdeck <commit-or-tag>
     on <date>."
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

For engineers wiring agent-free automations or debugging pack contracts directly. Mint a JWT first:

```bash
ADMIN_PW=$(grep HELMDECK_ADMIN_PASSWORD /root/helmdeck/deploy/compose/.env.local | cut -d= -f2)
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
```

Happy path:

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/<pack.name> \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "<input>": "<value>"
  }'
```

Real captured response (paste from `/tmp/captures/<pack>.json`):

```json
{
  "pack": "<pack.name>",
  "version": "v1",
  "output": {
    "<output>": "<value>"
  },
  "duration_ms": 0
}
```

## Error codes

Trigger each documented error live (malformed input, missing required field, etc.) and paste the captured response. The closed-set codes are defined in [`internal/packs/errors.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/errors.go): `invalid_input`, `invalid_output`, `schema_mismatch`, `session_unavailable`, `handler_failed`, `artifact_failed`, `timeout`, `internal`.

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | <field missing> | `{"error":"invalid_input","message":"..."}` |
| `handler_failed` | <upstream failure> | `{"error":"handler_failed","message":"..."}` |

## Session chaining

<one of:>

- **No session.** Stateless; no `_session_id` field accepted; chains freely with anything.
- **Optional session.** Pass `_session_id` to reuse an existing session. Compatible upstream: `<list>`. Compatible downstream: `<list>`.
- **Required.** Always chained — pass `_session_id` from `repo.fetch` (or wherever the upstream session was created).

## Async behavior

<Synchronous only / Sync + webhook (`webhook_url` / `webhook_secret`)>

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/<file>.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/<file>.go).
- Agent guidance: [`SKILLS.md`](/integrations/SKILLS).
- Companion packs: <list>.
- ADR <NNN> — <if applicable>.
