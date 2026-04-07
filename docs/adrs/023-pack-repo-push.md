# 23. Pack: `repo.push`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design, security

## Context
Git push has the same auth/known_hosts/HTTPS-vs-SSH surface as clone, plus remote configuration, refspec choice, and push error handling (non-fast-forward, protected branch, hook failure) — all decisions weak models routinely get wrong (PRD §6.6).

## Decision
Ship `repo.push` as a built-in pack, paired with `repo.fetch` (ADR 022).

**Input:** `{ local_path: string, message: string, branch?: string, files?: string[] }`
**Output:** `{ commit_sha: string, branch: string, pushed: boolean }`
**Errors:** `auth_failed`, `not_found`, `schema_mismatch` (non-fast-forward), `timeout`, `network_error`

The handler stages either `files` or all changes, creates a commit with the supplied message, ensures the remote is configured with the same credential pattern as `repo.fetch`, and pushes to `branch` (or HEAD's upstream). On non-fast-forward it returns `schema_mismatch` with a structured detail rather than retrying blindly.

## Consequences
**Positive:** complete the read/write loop opened by `repo.fetch`; agents can submit PRs without ever touching git plumbing.
**Negative:** must enforce per-credential write scopes carefully — write packs are higher blast radius than read packs.

## Related PRD Sections
§6.6 Capability Packs, §14 Credential Vault, §10 Security Model
