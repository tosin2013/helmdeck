---
title: git.diff
description: Show the diff of changes in a session-local clone. Empty when working tree is clean. Untracked files don't appear — use `cmd.run git status` if you need them.
keywords: [helmdeck, git.diff, code edit loop, MCP]
---

# `git.diff`

Returns `git diff` output for the working tree of a session-local clone. The diff covers **modified tracked files** by default — untracked files don't appear (a quirk of `git diff`'s default behavior, not helmdeck's; use `cmd.run` with `git status` if the agent needs to see untracked files too).

Useful as the verify-before-commit step in the code-edit loop: `fs.read` → `fs.patch` → `git.diff` → if reasonable, `git.commit`.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded session clone. |
| `path` | `string` | no | — | Limit the diff to a single file or directory. Relative to `clone_path`. |
| `staged` | `boolean` | no | `false` | When true, runs `git diff --cached` (shows what's staged for the next commit instead of the working-tree changes). |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `diff` | `string` | The unified-diff output. Empty when no changes. |
| `files_changed` | `number` | Count of files with changes in the diff. |

## Vault credentials needed

**None.**

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested prompt:

  "Clone helmdeck, modify the README to add a note about the new docs site at
   the top, and show me the diff before committing."

Agent should chain: repo.fetch → fs.patch → git.diff → respond with the diff
content (typically wrapped in a markdown code block).
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/git.diff \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{\"_session_id\":\"$SESSION\",\"clone_path\":\"$CLONE\"}"
```

Captured response on a clean working tree:

```json
{
  "pack": "git.diff",
  "version": "v1",
  "output": {
    "diff": "",
    "files_changed": 0
  },
  "session_id": "022b902e-fcf4-4853-b65e-97cf9896cc81"
}
```

After modifying a tracked file, the response would include the unified-diff content under `diff`.

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | path-safety violations |
| `session_unavailable` | session expired |
| `handler_failed` | `git diff` itself errors (e.g. clone is corrupt) |

## Session chaining

`needs_session: true`. Always between an fs change and `git.commit`.

## Async behavior

Synchronous. Sub-200 ms.

## See also

- [`git.commit`](./commit.md), [`git.log`](./log.md).
- [`cmd.run`](../cmd/run.md) — when you need `git status` (which DOES show untracked files) instead of `git diff`.
- Source: [`internal/packs/builtin/fs_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/fs_packs.go).
