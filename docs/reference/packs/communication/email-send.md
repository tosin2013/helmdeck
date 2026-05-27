
---
title: email.send

description: Send transactional emails through Resend.

keywords: [helmdeck, email, email.send, resend, MCP]

---

# `email.send`

`email.send` sends transactional emails through Resend using a Vault-managed API key. Agents typically use this pack for notifications, password resets, onboarding emails, verification links, or workflow-driven outbound communication. The pack validates required fields, verifies that the sender domain is registered and verified on Resend, submits the email through the Resend API, and returns the resulting message ID.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `from` | `string` | yes | — | Sender email address. The domain must already be verified on Resend. |
| `to` | `string` | yes | — | Recipient email address. |
| `subject` | `string` | yes | — | Email subject line. |
| `html` | `string` | no | — | HTML email body. |
| `cc` | `string` | no | — | Optional CC recipient. |
| `bcc` | `string` | no | — | Optional BCC recipient. |
| `reply_to` | `string` | no | — | Optional reply-to address. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `id` | `string` | Resend-generated message ID for the submitted email. |

## Vault credentials needed

- **`resend-api-key`** — type `api_key`, scoped to host pattern `*`. Add via the Management UI's *Vault* panel before invoking. If absent, the pack fails with `invalid_input`.

## Use it from your agent (OpenClaw chat-UI worked example)

> User: Send a welcome email to `user@example.com` from `hello@example.com`.

Tool used:

```json
{
  "pack": "email.send",
  "input": {
    "from": "hello@example.com",
    "to": "user@example.com",
    "subject": "Welcome!",
    "html": "<h1>Welcome to Helmdeck</h1><p>We're glad you're here.</p>"
  }
}
````

Agent reply:

> Sent the welcome email successfully. Resend accepted the message for delivery and returned a message ID for tracking.

Verified via OpenClaw + helmdeck on 2026-05-22.

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
curl -fsS -X POST http://localhost:3000/api/v1/packs/email.send \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "from": "hello@example.com",
    "to": "user@example.com",
    "subject": "Welcome!",
    "html": "<h1>Hello</h1><p>Welcome aboard.</p>"
  }'
```

Real captured response:

```json
{
  "pack": "email.send",
  "version": "v1",
  "output": {
    "id": "d91cd9bd-1176-453e-8fc1-35364d380206"
  },
  "duration_ms": 231
}
```

## Error codes

| Code             | Triggers                                  | Captured response                                                                                                                        |
| ---------------- | ----------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `invalid_input`  | Missing required fields (`to`, `subject`) | `{"error":"invalid_input","message":"subject is required"}`                                                                              |
| `invalid_input`  | Missing Vault credential                  | `{"error":"invalid_input","message":"vault credential \"resend-api-key\" not found"}`                                                    |
| `invalid_input`  | Sender domain not verified on Resend      | `{"error":"invalid_input","message":"cannot send email from \"hello@example.com\" because the sender domain is not verified on Resend"}` |
| `handler_failed` | Upstream Resend API failure               | `{"error":"handler_failed","message":"unauthorized"}`                                                                                    |

The closed-set codes are defined in `internal/packs/errors.go`: `invalid_input`, `invalid_output`, `schema_mismatch`, `session_unavailable`, `handler_failed`, `artifact_failed`, `timeout`, `internal`.

## Session chaining

* **No session.** Stateless; no `_session_id` field accepted; chains freely with anything.

## Async behavior

Synchronous only.

The pack returns immediately after Resend accepts the email submission request. The returned message ID may be used later for delivery-status querying if such a companion pack is introduced.

## See also

* Catalog row: `docs/PACKS.md`
* Source: `internal/packs/builtin/email_send.go`
* Agent guidance: `docs/integrations/SKILLS.md`
* Companion packs: `vault.*`
