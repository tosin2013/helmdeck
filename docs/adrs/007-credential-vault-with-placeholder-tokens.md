# 7. Credential Vault with Placeholder-Token Injection

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: security

## Context
Agents routinely need to authenticate to websites, third-party APIs, and git remotes. Embedding raw secrets in agent code or env vars creates audit gaps, painful rotation, and exposure to any process with env access. Weak models cannot reliably recover from auth-related stderr blobs (PRD §3.1 Failure 3, §15).

## Decision
Adopt the OneCLI-inspired pattern: agents receive **placeholder tokens**, never real credentials. The Go control plane intercepts outbound HTTP and CDP traffic, matches host/path rules against the vault, and injects the real credential before forwarding. Storage is AES-256-GCM with the encryption key held separately from the credential database and decrypted only at dispatch time. Supported types: website login, session cookies, API key/bearer, OAuth (with auto-refresh), SSH/git. An optional delegation mode forwards credential resolution to an existing OneCLI deployment.

## Consequences
**Positive:** agents never see secrets; rotation is a single UI action with full usage history; the `repo.fetch`/`repo.push` packs become trivially safe.
**Negative:** the gateway is now in the auth critical path; misconfigured host patterns can leak credentials to wrong destinations (mitigated by pattern validation and per-pack scope).

## Related PRD Sections
§14 Credential and Website API Key Vault, §6.6 `repo.fetch` example, §10 Security Model, §11 Data Models
