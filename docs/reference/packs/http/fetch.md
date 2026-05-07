---
title: http.fetch
description: Make an HTTP request to any allow-listed host with vault placeholder substitution and built-in egress guard. The agent never sees the real credential.
keywords: [helmdeck, http, fetch, vault, placeholder, egress guard, MCP]
---

# `http.fetch`

The canonical "let the agent talk to a REST API without ever holding the API key" pack. The agent sends a request with `${vault:credential-name}` placeholders in URL, headers, or body; helmdeck substitutes the real credential just before the request leaves the control plane and forwards the response — so the agent reads the response but never sees the secret in its context window. Combined with the egress guard (which blocks RFC 1918, cloud metadata IPs, and loopback ranges), this is the safe default whenever an agent needs to hit a service helmdeck doesn't have a dedicated pack for.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | `string` | yes | — | Absolute URL. Validated by the egress guard. Placeholder tokens like `${vault:NAME}` are substituted **before** parsing. |
| `method` | `string` | no | `GET` | One of `GET`, `POST`, `PUT`, `DELETE`, `PATCH`, `HEAD`. Other methods (`CONNECT`, `OPTIONS`, exotic verbs) return `invalid_input`. |
| `headers` | `object` | no | `{}` | Header values support `${vault:NAME}` placeholders. The default `User-Agent` is `Helmdeck/0.6.0 (+https://github.com/tosin2013/helmdeck)` if not overridden. |
| `body` | `string` | no | `""` | Request body. Placeholder substitution applies. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `status` | `number` | HTTP status code from upstream. |
| `headers` | `object` | Flattened response headers (first value of each multi-valued header). |
| `body` | `string` | Response body bytes. **Capped at 16 MiB**; larger responses are truncated and the `truncated` flag is set so the agent knows to narrow its query. |
| `truncated` | `boolean` | True when the response was capped at 16 MiB. |

## Vault credentials needed

**Optional, depends on the request.** The pack itself doesn't require any credential — but if the URL, headers, or body contain `${vault:NAME}` references, those credentials must:

1. Exist in the vault (Management UI → *Vault* → *Add Credential*).
2. Be granted to the calling actor (default install grants `*` to admin-issued JWTs).

A common pattern is to add a `github-token` credential of type `api_key` and use it as `Authorization: Bearer ${vault:github-token}`. The `repo.fetch` and dedicated `github.*` packs are usually preferable for GitHub-specific work; reach for `http.fetch` when no dedicated pack fits.

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste an OpenClaw chat-UI transcript here. Suggested prompt:

  "Use http.fetch to GET https://httpbin.org/get?demo=helmdeck and tell me what
   IP address the request came from (look at the `origin` field in the response)."

Capture and paste:
  1. The exact prompt sent to OpenClaw.
  2. The tool call OpenClaw emits (visible in the "tools used" UI).
  3. The text answer the agent gives back to you.
  4. Add the verification footer: "Verified via OpenClaw 2026.4.18 + helmdeck v0.9.0 on 2026-05-07."
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

For engineers wiring agent-free automations or debugging pack contracts directly. Mint a JWT first (admin password lives in `deploy/compose/.env.local`):

```bash
ADMIN_PW=$(grep HELMDECK_ADMIN_PASSWORD /root/helmdeck/deploy/compose/.env.local | cut -d= -f2)
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
```

Happy path — GET against httpbin.org:

```bash
curl -sS -X POST http://localhost:3000/api/v1/packs/http.fetch \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "url": "https://httpbin.org/get?demo=helmdeck",
    "headers": {"X-Helmdeck-Demo": "true"}
  }'
```

Real captured response (lightly trimmed):

```json
{
  "pack": "http.fetch",
  "version": "v1",
  "output": {
    "status": 200,
    "truncated": false,
    "headers": {
      "Content-Type": "application/json",
      "Server": "gunicorn/19.9.0",
      "Date": "Thu, 07 May 2026 18:32:29 GMT"
    },
    "body": "{\n  \"args\": {\n    \"demo\": \"helmdeck\"\n  },\n  \"headers\": {\n    \"User-Agent\": \"Helmdeck/0.6.0 (+https://github.com/tosin2013/helmdeck)\",\n    \"X-Helmdeck-Demo\": \"true\",\n    ...\n  },\n  \"origin\": \"176.9.223.218\",\n  \"url\": \"https://httpbin.org/get?demo=helmdeck\"\n}\n"
  },
  "duration_ms": 294
}
```

Note the response envelope: every pack call returns `{pack, version, output, duration_ms}`. The pack-specific fields live under `output`.

### Calling an authenticated API with a vault placeholder

Add a credential first (via the *Vault* UI), then reference it with `${vault:<name>}`. The placeholder is resolved in URL, headers, and body — never written to logs, never returned to the agent.

```bash
curl -sS -X POST http://localhost:3000/api/v1/packs/http.fetch \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "url": "https://api.github.com/user",
    "headers": {"Authorization": "Bearer ${vault:github-token}"}
  }'
```

The agent sees the response body (its GitHub user JSON) but never sees the PAT.

## Error codes

The pack returns the closed-set codes from [`internal/packs/errors.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/errors.go). Captured live against the running install:

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `url` missing or empty | `{"error":"invalid_input","message":"missing required field \"url\""}` |
| `invalid_input` | unsupported `method` (e.g. `OPTIONS`) | `{"error":"invalid_input","message":"unsupported method \"OPTIONS\""}` |
| `invalid_input` | URL fails the egress guard (metadata IP) | `{"error":"invalid_input","message":"egress denied: security: destination is in a blocked address range: 169.254.169.254 is in 169.254.169.254/32"}` |
| `invalid_input` | host doesn't resolve | `{"error":"invalid_input","message":"egress denied: security: dns lookup ...: no such host"}` |
| `invalid_input` | `${vault:NAME}` unknown or ACL-denied | placeholder resolver returns `unknown placeholder` / `denied` |
| `handler_failed` | network error mid-request (TLS, RST, timeout under context deadline) | `{"error":"handler_failed","message":"http request: ..."}` |

**What the agent sees in chat** when it hits `invalid_input`: OpenClaw surfaces the typed code + message. A well-behaved model interprets it ("the URL I tried hits a blocked range") and adjusts ("let me try a public host instead") rather than retrying the same call.

## Session chaining

`http.fetch` is **stateless** — no `_session_id` required and the engine doesn't acquire a session for it. Chain it freely with any other pack: `repo.fetch` → `http.fetch` → `fs.write` to download a file from a vault-protected API into a session-local clone, for example.

## Async behavior

Synchronous only. The pack returns when the upstream returns (or the caller's context deadline fires). Typical latency is the upstream's latency + ~10ms.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `http.fetch`.
- Source: [`internal/packs/builtin/http_fetch.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/http_fetch.go).
- ADR 007 — credential vault with placeholder tokens (the design that makes this pack possible).
- ADR 011 / T508 — application-layer egress guard.
- Agent guidance: [`SKILLS.md`](/integrations/SKILLS) — `http.fetch` section.
- Related packs: [`repo.fetch`](/PACKS) (preferred for git), [`github.*`](/PACKS) (preferred for GitHub REST), [`web.scrape`](/PACKS) (preferred for content extraction).
