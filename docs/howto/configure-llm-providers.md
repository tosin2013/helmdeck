---
title: Configure LLM providers
description: Register provider API keys, route models through the gateway, configure fallback chains, and read the success-rate panel. Worked examples for OpenRouter, Anthropic, OpenAI, Gemini, Ollama, Groq, Mistral, Deepseek.
keywords: [helmdeck, LLM gateway, provider keys, fallback rules, OpenRouter, Anthropic, OpenAI, Gemini, Ollama, Groq, Mistral, T607, success rate]
---

# Configure LLM providers

Helmdeck's `/v1/chat/completions` is OpenAI-compatible, but the *real* configuration surface lives at `/api/v1/providers/keys`. This page covers registering keys, routing model IDs to providers, and reading the success-rate panel that tells you when a provider is flapping.

For the architectural shape (request flow, fallback semantics), see [Architecture overview §2.b](../reference/architecture.md#2b-llm-gateway--one-chat-completion-end-to-end) and [ADR 005](../adrs/005-openai-compatible-multi-provider-ai-gateway.md).

## Prerequisites

- A running helmdeck stack with `HELMDECK_KEYSTORE_KEY` set (32 hex bytes; **distinct from `HELMDECK_VAULT_KEY`** — separate encryption keys for separate purposes per [ADR 007](../adrs/007-credential-vault-with-placeholder-token-injection.md))
- A JWT with `providers:*` scope
- An API key from at least one upstream provider

```bash
JWT="<your helmdeck JWT>"
HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
```

## Supported providers

Each provider has a stable identifier — that's the prefix you'll use in model IDs (`<provider>/<model>`):

| Provider id | Upstream | Auth shape |
|---|---|---|
| `anthropic` | api.anthropic.com | `x-api-key` header |
| `openai` | api.openai.com | `Authorization: Bearer` |
| `gemini` | generativelanguage.googleapis.com | `?key=…` query param |
| `openrouter` | openrouter.ai | `Authorization: Bearer` |
| `ollama` | (your Ollama URL) | None — local, network-reachable |
| `deepseek` | api.deepseek.com | `Authorization: Bearer` |
| `groq` | api.groq.com (community adapter, v0.8.1+) | `Authorization: Bearer` |
| `mistral` | api.mistral.ai (community adapter, v0.8.1+) | `Authorization: Bearer` |

A provider must be registered (key present) **and** have at least one model exposed before it appears in `GET /v1/models`.

## Register a provider key

```bash
curl -fsS -X POST "$HELMDECK_URL/api/v1/providers/keys" \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "provider": "openrouter",
    "label": "primary",
    "key": "sk-or-v1-…"
  }' | jq .
```

Response: a record with `id` (UUID), `provider`, `label`, `created_at`. The key itself is encrypted with `HELMDECK_KEYSTORE_KEY` and never echoes back.

The gateway **hot-reloads** after the create — the new key is immediately usable on `/v1/chat/completions` without restarting the control plane.

### Worked examples for the common providers

```bash
# Anthropic
-d '{"provider":"anthropic","label":"primary","key":"sk-ant-…"}'

# OpenAI
-d '{"provider":"openai","label":"primary","key":"sk-…"}'

# Gemini (use the API key from aistudio.google.com)
-d '{"provider":"gemini","label":"primary","key":"AIza…"}'

# OpenRouter (recommended default — exposes 200+ models behind one key)
-d '{"provider":"openrouter","label":"primary","key":"sk-or-v1-…"}'

# Ollama (local, no key needed but the registration is still required for routing)
-d '{"provider":"ollama","label":"local","key":"http://host.docker.internal:11434"}'
# ^ for Ollama, the "key" field is the base URL of your Ollama server
```

For Groq and Mistral (community adapters), the same shape:

```bash
# Groq (community, v0.8.1+)
-d '{"provider":"groq","label":"primary","key":"gsk_…"}'

# Mistral (community, v0.8.1+)
-d '{"provider":"mistral","label":"primary","key":"…"}'
```

## List, get, rotate, test, delete

```bash
# List all registered keys (provider + label + last-used; never the secret)
curl -fsS -H "Authorization: Bearer $JWT" "$HELMDECK_URL/api/v1/providers/keys" | jq .

# Get one
curl -fsS -H "Authorization: Bearer $JWT" "$HELMDECK_URL/api/v1/providers/keys/$ID" | jq .

# Rotate — replaces the secret in place; the key id stays stable so any
# config referencing it (label-based fallback rules, etc.) keeps working
curl -fsS -X POST -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{"key":"<new-secret>"}' \
  "$HELMDECK_URL/api/v1/providers/keys/$ID/rotate" | jq .

# Test — fires a tiny no-op chat completion against the upstream to
# verify the key works. Returns success/failure + provider-side error.
curl -fsS -X POST -H "Authorization: Bearer $JWT" \
  "$HELMDECK_URL/api/v1/providers/keys/$ID/test" | jq .

# Delete
curl -fsS -X DELETE -H "Authorization: Bearer $JWT" \
  "$HELMDECK_URL/api/v1/providers/keys/$ID"
```

## Use the gateway

Any OpenAI SDK or `curl` works:

```bash
curl -fsS -X POST "$HELMDECK_URL/v1/chat/completions" \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "openrouter/anthropic/claude-haiku-4.5",
    "messages": [{"role":"user","content":"What is 2+2?"}]
  }' | jq .
```

Model IDs follow `provider/model`:

- `openrouter/anthropic/claude-haiku-4.5` → routed to `openrouter`, forwarded as `anthropic/claude-haiku-4.5`
- `anthropic/claude-sonnet-4.6` → routed directly to `anthropic`
- `ollama/llama3.1:8b` → routed to your local Ollama
- `gemini/gemini-3-flash` → routed to Gemini

`GET /v1/models` lists everything currently routable across all registered providers — combine its output with your registered keys to build a model picker UI.

Streaming works end-to-end (`"stream": true` in the body, SSE chunks back).

## Configure fallback rules

Today's fallback layer is **pluggable but not REST-configurable** — fallback chains are wired in code via `internal/gateway/fallback.go` and consumed by the gateway at startup. To add or change a chain you edit the config file the control plane reads (typically `deploy/compose/gateway-fallback.json` or set inline via `HELMDECK_GATEWAY_FALLBACK_JSON`).

The chain shape:

```json
[
  {
    "primary": "openrouter/anthropic/claude-haiku-4.5",
    "fallbacks": [
      "openrouter/openai/gpt-4o-mini",
      "anthropic/claude-haiku-4.5"
    ],
    "triggers": ["rate_limit", "timeout", "error"]
  }
]
```

Three trigger types from the closed set:

| Trigger | Fires on |
|---|---|
| `rate_limit` | HTTP 429 from the upstream |
| `timeout` | Request-context deadline hit before a response |
| `error` | Any other non-timeout, non-429 failure (5xx, network, auth, decode) |

Empty `triggers` slice means *advance on anything*. Fallbacks are tried in order; the first that succeeds wins; the chain bails after the last fallback fails.

**Each attempt** (primary + every fallback hop) writes a row to the `provider_calls` table with `fallback_used: 1` for every non-primary attempt — so you can query *how often a primary is degrading enough to trip the chain* (see [Inspect audit logs](./inspect-audit-logs.md)).

A REST surface for fallback rules is on the roadmap — until then, edit the JSON config and restart the control plane.

## Read the success-rate panel

The Management UI's **AI Providers → Model Success Rates** tab shows a rolled-up view of `provider_calls` per `(provider, model)` over a configurable time window — success count, fail count, average latency, p95 latency. This is T607's deliverable.

The same data is available over REST:

```bash
curl -fsS -H "Authorization: Bearer $JWT" \
  "$HELMDECK_URL/api/v1/providers/stats?since=24h" | jq .
```

Returns one row per `(provider, model)` with success/fail counts and latency rollups. Useful for ops dashboards or external monitoring.

For raw access (custom queries, exports for compliance), drop to SQLite:

```bash
docker compose -f deploy/compose/compose.yaml exec control-plane \
  sqlite3 /var/lib/helmdeck/helmdeck.db \
  "SELECT provider, model, COUNT(*) AS n,
          SUM(CASE WHEN status='success' THEN 1 ELSE 0 END) AS ok,
          AVG(latency_ms) AS avg_ms
   FROM provider_calls
   WHERE ts >= datetime('now','-1 day')
   GROUP BY provider, model
   ORDER BY n DESC;"
```

## Helmdeck-as-LLM-gateway for your agents

Three integration patterns:

- **Hermes Agent** — point Hermes' `base_url` at `http://localhost:3000/v1`. Helmdeck observes every chat completion + every MCP tool call. See [Hermes integration](../integrations/hermes-agent.md).
- **Custom OpenAI-SDK code** — set `OPENAI_BASE_URL=http://localhost:3000/v1` and `OPENAI_API_KEY=<helmdeck JWT>`. Works with any OpenAI Python/Node SDK.
- **Claude Code / Claude Desktop / OpenClaw / Gemini CLI** — these clients hard-wire to their respective upstreams (Anthropic, Google) and don't expose an OpenAI-compatible base-URL escape hatch. Helmdeck only sees their MCP tool calls, not their chat completions.

## Common pitfalls

- **Wrong key shape** — pasting a Gemini key into an OpenAI registration silently registers it; the failure surfaces only when a chat call returns 401 from the upstream. Use `POST /api/v1/providers/keys/{id}/test` after registering to verify.
- **Model ID typo** — `claude-haiku-4` vs `claude-haiku-4.5` is a 400 from the upstream, not a 404 from helmdeck. Helmdeck doesn't validate model IDs against the upstream catalog (some providers add models faster than we could whitelist them).
- **Provider key for an unregistered provider** — `POST /api/v1/providers/keys` with `provider: "groq"` on a build that doesn't include the Groq adapter returns 400. Confirm the adapter is in the build with `GET /v1/models` (Groq models will appear if the adapter is present).
- **Forgot `HELMDECK_KEYSTORE_KEY`** — auto-generated keys appear in dev mode but a fresh one is generated on every restart, so registered keys become unrecoverable across restarts. Set `HELMDECK_KEYSTORE_KEY` explicitly for any sustained install.

## Known limitations

- **Fallback rules are not REST-configurable** — JSON config + restart only. REST CRUD for fallback chains is on the v1.x roadmap.
- **No multi-key load balancing** — registering two keys for the same provider (label "primary" + "secondary") doesn't load-balance. The first match wins; the second is reserve.
- **No quota / rate-limit awareness inside helmdeck** — the gateway forwards verbatim and only fails over on the upstream's response. Helmdeck doesn't pre-emptively throttle to stay under a quota.
- **Master-key rotation requires manual re-registration** — rotating `HELMDECK_KEYSTORE_KEY` invalidates all stored keys. Rotation tooling is part of the same track as [#110](https://github.com/tosin2013/helmdeck/issues/110) (vault key rotation), but for the keystore.

## Related

- [Architecture overview §2.b — LLM gateway flow](../reference/architecture.md#2b-llm-gateway--one-chat-completion-end-to-end) — the request-flow diagram for chat completions
- [ADR 005 — OpenAI-compatible multi-provider AI gateway](../adrs/005-openai-compatible-multi-provider-ai-gateway.md) — design rationale, fallback semantics, provider adapter shape
- [Inspect audit logs](./inspect-audit-logs.md) — query patterns for `provider_calls` (the table behind the success-rate panel)
- [Manage credentials in the vault](./manage-vault-credentials.md) — for non-LLM credentials (GitHub tokens, Ghost keys, ElevenLabs, etc.) which use the *separate* credential vault
