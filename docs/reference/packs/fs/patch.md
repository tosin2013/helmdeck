---
title: fs.patch
description: Literal search-and-replace in a session-local file. Not regex. Returns count of replacements + post-write sha256.
keywords: [helmdeck, fs.patch, search replace, code edit, MCP]
---

# `fs.patch`

Performs **literal** search-and-replace in a single file inside a session-local clone path. Not regex — the agent specifies exact bytes to find and replace. Returns the number of replacements made plus the post-patch sha256.

Why literal not regex: weak open-weight models routinely produce broken regex (escaping issues, greedy patterns, lookaheads they don't understand). Literal substring substitution is unambiguous, and combining it with sha256 verification (`fs.read` before, sha256 check, `fs.patch`, `fs.read` after) gives the agent a reliable feedback loop.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded. |
| `path` | `string` | yes | — | Relative file path. |
| `search` | `string` | yes | — | The exact bytes to find. **Literal**, not regex. |
| `replace` | `string` | yes | — | The replacement bytes. |
| `max_occurrences` | `number` | no | unlimited | Cap the number of replacements (useful when the agent only wants to change the first N). |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `applied` | `number` | Replacements actually performed. `0` is not an error — the file is left untouched and the agent should re-`fs.read` to understand why. |
| `sha256` | `string` | Hex-encoded sha256 after the patch. |

## Vault credentials needed

**None.**

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested chained prompt:

  "In the helmdeck README, change the version from 'v0.8.0' to 'v0.9.0' and
   show me what you did."

Agent should: repo.fetch → fs.read README.md → fs.patch with the exact text →
fs.read again to verify → respond with the diff.
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/fs.patch \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{
    \"_session_id\":\"$SESSION\",
    \"clone_path\":\"$CLONE\",
    \"path\":\"docs-test-tmp.md\",
    \"search\":\"test\",
    \"replace\":\"capture\"
  }"
```

Captured response (1 occurrence replaced):

```json
{
  "pack": "fs.patch",
  "version": "v1",
  "output": {
    "applied": 1,
    "sha256": "5d1ad738e3ae20fee43f2df6cf951d71a8917dc833daf5d474a71317f371b540"
  },
  "session_id": "f905a56c-f573-4c0f-b2b5-c73ca26ee318"
}
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | path-safety violation, file unreadable, `search` empty |
| `session_unavailable` | session expired |
| `handler_failed` | underlying file write fails |

When `applied=0`: the search string didn't appear in the file. Not an error — the agent re-reads the file and decides what to do.

## Session chaining

`needs_session: true`. Almost always between `fs.read` (verify-before) and `fs.read` (verify-after). Pair with `git.commit` to capture the patched state.

## Async behavior

Synchronous. ~100 ms per patch.

## See also

- [`fs.read`](./read.md) — verify before/after via sha256.
- [`git.commit`](../git/commit.md) — capture the patched state.
- Source: [`internal/packs/builtin/fs_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/fs_packs.go).
- ADR 022 §2026-04-15 — agent-facing literal-not-regex rationale.
