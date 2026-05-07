---
title: fs.list
description: Find files under a session-local clone path with optional recursive flag and glob filter. Capped at 5000 entries.
keywords: [helmdeck, fs.list, find, glob, MCP]
---

# `fs.list`

Enumerates files under a session-local clone path. Supports an optional `glob` (substring or shell-glob) to filter, and an optional `recursive` flag (default true). Returns up to **5000 entries** — past the cap, the response is truncated and the agent should narrow its query.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded clone root. |
| `path` | `string` | no | `.` | Sub-path to list, relative to `clone_path`. `.` lists the clone root. |
| `glob` | `string` | no | — | Shell glob filter (`*.md`, `**/*.go`). When unset, all files match. |
| `recursive` | `boolean` | no | `true` | Recurse into subdirectories. |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `files` | `array` | Each entry is the relative path from `clone_path`. |
| `count` | `number` | Number of files returned (≤ 5000). |

## Vault credentials needed

**None.**

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested prompt:

  "Clone helmdeck and list every markdown file under docs/, then tell me
   how many ADRs there are."

Agent should chain repo.fetch → fs.list with glob "*.md".
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/fs.list \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{
    \"_session_id\":\"$SESSION\",
    \"clone_path\":\"$CLONE\",
    \"path\":\".\",
    \"glob\":\"*.md\"
  }"
```

Captured response:

```json
{
  "pack": "fs.list",
  "version": "v1",
  "output": {
    "count": 6,
    "files": [
      "SECURITY.md",
      "CLAUDE.md",
      "README.md",
      "CONTRIBUTING.md",
      "CHANGELOG.md",
      "CODE_OF_CONDUCT.md"
    ]
  },
  "session_id": "f905a56c-f573-4c0f-b2b5-c73ca26ee318"
}
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | path-safety violations |
| `invalid_input` | result exceeds 5000-entry cap |
| `session_unavailable` | session expired |

## Session chaining

`needs_session: true`. Often the second step after `repo.fetch` (envelope first turn → list to drill down → read individual files).

## Async behavior

Synchronous. Glob matching is `find` under the hood; whole-repo listings finish in <200 ms even on big repos.

## See also

- [`fs.read`](./read.md) — read each file from the result.
- [`repo.fetch`](/PACKS) — the envelope returns `tree`, `entrypoints`, `doc_hints` so a single `fs.list` is often unnecessary.
- Source: [`internal/packs/builtin/fs_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/fs_packs.go).
