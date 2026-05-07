---
title: fs.delete
description: Delete a single file in a session-local clone path. Always-pair-with-git-commit recommendation; the path-safety guard refuses anything outside the clone root.
keywords: [helmdeck, fs.delete, MCP, code edit loop]
---

# `fs.delete`

Removes a single file inside a session-local clone path. Same path-safety guards as the rest of the `fs.*` family — `clone_path` rooted under `/tmp/helmdeck-clone-*` or `/home/helmdeck/work/*`, no `..`, no absolute paths.

The pack returns `{"deleted": true, "path": "..."}` on success. **Pair with [`git.commit`](../git/commit.md) immediately after** so the deletion is captured; otherwise an agent that crashes mid-loop leaves the clone in a half-modified state that the next session won't see (each session is ephemeral).

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded. |
| `path` | `string` | yes | — | Relative file path. Directories: not supported (use `cmd.run` with `rm -r` if you need a tree delete). |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `deleted` | `boolean` | Always `true` on success. |
| `path` | `string` | Echo of the relative path that was removed. |

## Vault credentials needed

**None.**

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested chained prompt:

  "Clone helmdeck, then remove the file `docs/sitemap.xml` (which is auto-generated)
   and commit the change with the message 'chore: drop generated sitemap'."

Agent should chain: repo.fetch → fs.delete → git.commit.
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/fs.delete \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{
    \"_session_id\":\"$SESSION\",
    \"clone_path\":\"$CLONE\",
    \"path\":\"docs-test-tmp.md\"
  }"
```

Captured response:

```json
{
  "pack": "fs.delete",
  "version": "v1",
  "output": {
    "deleted": true,
    "path": "docs-test-tmp.md"
  },
  "session_id": "f905a56c-f573-4c0f-b2b5-c73ca26ee318"
}
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | path-safety violations |
| `invalid_input` | file doesn't exist |
| `invalid_input` | path is a directory (use `cmd.run` for tree deletes) |
| `session_unavailable` | session expired |

## Session chaining

`needs_session: true`. Almost always paired with `git.commit` immediately after.

## Async behavior

Synchronous. Sub-50ms.

## See also

- [`git.commit`](../git/commit.md) — capture the deletion.
- [`cmd.run`](../cmd/run.md) — for directory deletes (`rm -rf`) or globs.
- [`fs.list`](./list.md) — find files before deleting.
- Source: [`internal/packs/builtin/fs_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/fs_packs.go).
