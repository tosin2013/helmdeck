# 16. Pack: `web.login_and_fetch`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design, security

## Context
Fetching authenticated content normally requires session creation, cookie management, vault lookup, navigation, content extraction, and session recycling — six steps that compound failure probability for weak models (PRD §6.6, §14).

## Decision
Ship `web.login_and_fetch` as a built-in pack.

**Input:** `{ url: string, credential_id: string, selector?: string, format?: "text"|"html"|"json" }`
**Output:** `{ content: string|object, final_url: string, status_code: integer }`
**Errors:** `auth_failed`, `not_found`, `timeout`, `network_error`, `schema_mismatch`

The handler resolves the credential from the vault, injects cookies via CDP `Network.setCookies` (or performs form login if only username/password is stored), navigates to the URL, waits for network idle, and extracts content. The agent never sees the credential.

## Consequences
**Positive:** authenticated scraping in one call; credential never leaves the platform.
**Negative:** vault lookup adds latency; site-specific login flows may need per-host quirks.

## Related PRD Sections
§6.6 Capability Packs, §14 Credential Vault, §14.4 Injection Flow
