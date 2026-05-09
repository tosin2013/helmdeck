---
title: Manage credentials in the vault
description: Create, grant, and audit credentials of all 5 supported types (login, cookie, api_key, oauth, ssh) for use by capability packs. Worked examples for GitHub deploy keys, OpenRouter API keys, ElevenLabs, Ghost Admin, and OAuth-protected sites.
keywords: [helmdeck, credential vault, ACL, AES-256-GCM, GitHub, OpenRouter, ElevenLabs, Ghost, ssh-git, oauth, api_key]
---

# Manage credentials in the vault

Capability packs that touch external services (GitHub, Ghost, OpenRouter, ElevenLabs, password-protected sites) reach for credentials through the **credential vault**. The vault stores 5 types of credentials, encrypts them at rest with AES-256-GCM, and only releases the plaintext to a pack handler that has been explicitly granted access via an ACL.

The agent **never** sees the credential. The vault resolves it inside the control-plane process and injects it into the outbound call.

This guide walks the lifecycle: **create → grant → use → audit**.

## Prerequisites

- A running helmdeck stack with `HELMDECK_VAULT_KEY` set (32 hex bytes; `make install` autogenerates one in dev mode and prints a warning)
- A JWT bearer token from the **API Tokens** UI panel
- The `jq` and `curl` tools

```bash
JWT="<your helmdeck JWT>"
HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
```

If you ever rotate `HELMDECK_VAULT_KEY`, you'll need the rotation procedure tracked at [#110](https://github.com/tosin2013/helmdeck/issues/110) — until that ships, **do not change** `HELMDECK_VAULT_KEY` on a stack with stored credentials, or every credential becomes unreadable.

## The 5 credential types

| Type | What it stores | Used by |
|---|---|---|
| `login` | Username + password (with optional URL pattern) | `web.scrape_spa` (login flows), `desktop.run_app_and_screenshot` (apps that prompt for username/password) |
| `cookie` | A raw cookie header or signed-session blob | `web.scrape_spa` (post-login session reuse) |
| `api_key` | Single secret string (token, bearer, key) | `blog.publish` (Ghost Admin), `podcast.generate` (ElevenLabs), every LLM provider key (though those go through the *separate* keystore — see [Configure LLM providers](./configure-llm-providers.md)) |
| `oauth` | OAuth tokens (access, refresh, expiry) | OAuth-flow packs (GitHub OAuth apps, Google APIs) |
| `ssh` | SSH private key (for git over SSH) | `repo.fetch`, `repo.push` against private repos |

Pick the type that matches the **shape of the secret**, not the service. A GitHub Personal Access Token is `api_key` (single string), a GitHub *deploy key* is `ssh` (RSA/Ed25519 private key), an OAuth-app GitHub identity is `oauth` (token bundle).

## Create a credential

```bash
# Generic shape — covers all 5 types. Type-specific examples below.
curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials" \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "<friendly-name>",
    "type": "<login|cookie|api_key|oauth|ssh>",
    "host_pattern": "<glob, e.g. *.github.com>",
    "path_pattern": "<optional URL prefix, e.g. /repos/>",
    "secret": "<the actual secret value>",
    "metadata": { "<non-secret hints, e.g. username>": "<value>" }
  }' | jq .
```

Returns the credential's `id` (UUID) plus a `fingerprint` (sha256 prefix of the plaintext, safe to log). The `secret` field never echoes back — once created, you can only resolve it through a pack call.

### Worked example 1 — GitHub Personal Access Token (`api_key`)

For `github.create_issue`, `github.list_prs`, etc. that need a fine-grained PAT:

```bash
curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "name": "github-token",
    "type": "api_key",
    "host_pattern": "*.github.com",
    "secret": "ghp_yourTokenHere…",
    "metadata": { "scopes": "repo,issues" }
  }' | jq -r .id
```

### Worked example 2 — GitHub SSH deploy key (`ssh`)

For `repo.fetch` / `repo.push` against a private repo over SSH:

```bash
SSH_KEY=$(cat ~/.ssh/helmdeck-deploy-key)
curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg secret "$SSH_KEY" '{
    name: "gh-deploy-key",
    type: "ssh",
    host_pattern: "github.com",
    secret: $secret,
    metadata: { "purpose": "private repo deploy" }
  }')" | jq -r .id
```

The deploy key's *public* half goes on the GitHub repo's **Deploy keys** page. Helmdeck only stores the private half.

### Worked example 3 — Ghost Admin API key (`api_key`)

For `blog.publish` to a Ghost blog. Ghost Admin keys have the shape `<id>:<secret>`:

```bash
curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "name": "ghost-admin-key",
    "type": "api_key",
    "host_pattern": "yourblog.com",
    "secret": "67aabb…:5b4c8d…",
    "metadata": { "ghost_api_url": "https://yourblog.com/ghost/api/admin/" }
  }' | jq -r .id
```

### Worked example 4 — ElevenLabs API key (`api_key`)

For `podcast.generate` and `slides.narrate`:

```bash
curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "name": "elevenlabs-api-key",
    "type": "api_key",
    "host_pattern": "api.elevenlabs.io",
    "secret": "sk_…",
    "metadata": {}
  }' | jq -r .id
```

### Worked example 5 — Site login pair (`login`)

For `web.scrape_spa` against a SaaS that requires sign-in:

```bash
curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "name": "salesforce-login",
    "type": "login",
    "host_pattern": "*.salesforce.com",
    "path_pattern": "/login",
    "secret": "your-password",
    "metadata": { "username": "agent@example.com" }
  }' | jq -r .id
```

The username goes in `metadata` (non-secret); the password goes in `secret`.

### Worked example 6 — OAuth token bundle (`oauth`)

For OAuth-flow packs. Helmdeck doesn't run OAuth flows itself — you bring the tokens:

```bash
curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "name": "google-drive-oauth",
    "type": "oauth",
    "host_pattern": "*.googleapis.com",
    "secret": "{\"access_token\":\"ya29…\",\"refresh_token\":\"1//…\",\"expires_at\":\"2026-12-01T00:00:00Z\"}",
    "metadata": { "client_id": "…", "scopes": "drive.readonly" }
  }' | jq -r .id
```

The `secret` is a JSON-encoded token bundle. Pack handlers that consume `oauth` know how to refresh it (where supported).

## Grant access (ACL)

A newly-created credential is **invisible to packs** until at least one ACL grant exists. Grants are per-credential and identify *which subjects* (and optionally which clients) may resolve it.

```bash
CRED_ID="<credential id from create>"
SUBJECT="alice@example.com"   # the JWT 'sub' claim of the agent that needs access

curl -fsS -X POST "$HELMDECK_URL/api/v1/vault/credentials/$CRED_ID/grants" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{ \"actor_subject\": \"$SUBJECT\" }"
```

Wildcards work:

```bash
# Any subject, any client:
-d '{ "actor_subject": "*" }'

# A specific subject, restricted to a single client:
-d '{ "actor_subject": "alice@example.com", "actor_client": "claude-code" }'
```

**The wildcard `actor_subject: "*"` is convenient for single-operator dev installs but a footgun in shared environments.** Every pack invocation by any authenticated subject would resolve the credential. Pin to specific subjects in production.

The `actor_client` field maps to a JWT custom claim — if you don't issue per-client JWTs, leave it empty (matches any client).

List grants:

```bash
curl -fsS -H "Authorization: Bearer $JWT" \
  "$HELMDECK_URL/api/v1/vault/credentials/$CRED_ID/grants" | jq .
```

## Use a credential in a pack call

Most packs accept a `credential` field naming the credential to resolve:

```bash
curl -fsS -X POST "$HELMDECK_URL/api/v1/packs/blog.publish" \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "destination": "ghost",
    "ghost_admin_url": "https://yourblog.com/ghost/api/admin/",
    "credential": "ghost-admin-key",
    "title": "Hello world",
    "markdown": "# Hello"
  }' | jq .
```

The vault resolves `"ghost-admin-key"` against your subject's grants. Three outcomes:

- **Allowed** — secret resolved, pack proceeds, `credential_usage_log` row written with `result: 'allowed'`
- **Denied** — subject doesn't have a grant; pack returns a typed error, log row written with `result: 'denied'`
- **No match** — the credential doesn't exist (typo); pack returns 404, log row written with `result: 'no_match'`

In every case, the pack call writes a row to the audit log too — see [Inspect audit logs](./inspect-audit-logs.md).

## Audit credential usage

```bash
# Per-credential usage log
curl -fsS -H "Authorization: Bearer $JWT" \
  "$HELMDECK_URL/api/v1/vault/credentials/$CRED_ID/usage" | jq .
```

Each row shows: `actor_subject`, `actor_client`, `host_matched`, `path_matched`, `result`, `ts`. Append-only — survives credential deletion, so a forensic trail outlives the credential it tracks.

For broader queries (e.g. *all denied resolves in the last 24h across all credentials*), drop into SQLite:

```bash
docker compose -f deploy/compose/compose.yaml exec control-plane \
  sqlite3 /var/lib/helmdeck/helmdeck.db \
  "SELECT credential_id, actor_subject, result, COUNT(*) AS n
   FROM credential_usage_log
   WHERE ts >= datetime('now','-1 day')
   GROUP BY credential_id, actor_subject, result;"
```

## Update or rotate a credential

There's no in-place "update secret" endpoint by design — to rotate a credential, **delete and recreate**:

```bash
# Delete (also cascades to ACL grants)
curl -fsS -X DELETE -H "Authorization: Bearer $JWT" \
  "$HELMDECK_URL/api/v1/vault/credentials/$CRED_ID"

# Recreate with the new secret + the same name
# (re-issue ACL grants — they don't survive deletion)
```

This is intentional: rotation is a *new credential* with a fresh fingerprint, not a quiet swap. Operators auditing the system can see the rotation as a delete+create pair in the audit log.

## Common pitfalls

- **Forgot the ACL grant** — the credential exists, the pack 404s on resolution. Symptom: `credential_usage_log` row with `result: 'no_match'` (which is misleading; it's actually a no-grant outcome). [#110](https://github.com/tosin2013/helmdeck/issues/110) tracks better diagnostics here.
- **`host_pattern` too narrow** — set `github.com` but the pack tries `api.github.com`. Use `*.github.com` to cover both.
- **`secret` field accidentally logged** — helmdeck never logs it; if it appears in your operator logs, it leaked through *outside* helmdeck (a `set -x` shell trace, a curl command in your shell history). Audit your invocation surface, not helmdeck.
- **Wildcard ACLs in production** — see the warning above. Pin to specific subjects.

## Known limitations

- **No master-key rotation tooling** — [#110](https://github.com/tosin2013/helmdeck/issues/110). Rotating `HELMDECK_VAULT_KEY` requires re-encrypting every credential; the procedure isn't scripted yet.
- **No credential-version history** — delete + recreate is the rotation path, but there's no audit-friendly "this is the v3 of credential X" view. The fingerprint changes on each create, which is the closest signal.
- **No external-vault provider** — credentials live in helmdeck's SQLite. Integration with HashiCorp Vault, AWS KMS, etc. is on the v1.x roadmap.

## Related

- [Configure LLM providers](./configure-llm-providers.md) — LLM API keys live in a *separate* keystore (T203), not the credential vault. Different REST surface, different encryption key (`HELMDECK_KEYSTORE_KEY` vs `HELMDECK_VAULT_KEY`).
- [Inspect audit logs](./inspect-audit-logs.md) — query patterns for the audit + vault-usage tables
- [ADR 007 — Credential vault with placeholder-token injection](../adrs/007-credential-vault-with-placeholder-token-injection.md) — the architectural decision behind the vault shape
- [Architecture overview §4 Trust boundaries](../reference/architecture.md#4-trust-boundaries) — where the vault fits in helmdeck's trust model
