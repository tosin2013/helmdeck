# 22. Pack: `repo.fetch`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design, security

## Context
The 2026-04-06 git SSH failure is the canonical motivation for Capability Packs: an agent attempted `git clone git@github.com:...`, hit SSH host-key verification with no `known_hosts` and no key material, and could not reason through the multi-line stderr blob. Weak models cannot reliably recover from this surface (PRD §3.1 Failure 3, §6.6).

## Decision
Ship `repo.fetch` as a built-in pack.

**Input:** `{ url: string (https or git@ form), ref?: string (default "HEAD"), subpath?: string }`
**Output:** `{ local_path: string, commit_sha: string, files_count: integer }`
**Errors:** `auth_failed`, `not_found`, `timeout`, `network_error`

The handler normalizes the URL (HTTPS↔SSH), resolves credentials from the Credential Vault by host pattern, sets `GIT_SSH_COMMAND` with `StrictHostKeyChecking=accept-new` if only an SSH key is available, retries on transient failures, and returns a typed result. The agent never sees SSH stderr, never chooses between HTTPS and SSH, and never touches `known_hosts`.

## Consequences
**Positive:** the failure mode that motivated this whole architecture is closed in one pack.
**Negative:** must keep up with git server quirks (LFS, partial clones, mono-repo subpath fetches).

## Related PRD Sections
§3.1 Failure Mode 3, §6.6 Capability Packs (`repo.fetch` example), §14 Credential Vault
