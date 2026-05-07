---
title: git.commit
description: Stage and commit changes in a session-local clone with `helmdeck-agent` author env injection. Returns the new commit SHA.
keywords: [helmdeck, git.commit, code edit loop, MCP]
---

# `git.commit`

Stages working-tree changes (with `all:true`, the default-recommended path) and creates a commit attributed to **`helmdeck-agent <agent@helmdeck.local>`** so commits made by the agent are visually distinguishable from human commits in `git log`. Returns the new commit SHA — the agent typically follows up with `git.diff` to verify what landed, or `repo.push` to publish.

The `nothing to commit` case is treated as `invalid_input`, not a silent no-op — so an agent that misjudges whether a patch actually changed anything gets feedback rather than thinking it succeeded.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded session clone. |
| `message` | `string` | yes | — | Commit message. Multi-line supported (use `\n` in JSON). |
| `all` | `boolean` | no | `false` | When true, equivalent to `git add -A` before commit (includes untracked files). Almost always what you want for an agent loop. |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `commit` | `string` | The new commit's full SHA. |

## Vault credentials needed

**None for the commit itself.** A subsequent `repo.push` may need vault credentials depending on the remote — see [`repo.push`](/PACKS).

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested chained prompt:

  "Clone helmdeck, add a file `notes.md` saying 'capture demo', commit it with
   the message 'docs: capture demo file', and tell me the commit SHA."

Agent should chain: repo.fetch → fs.write → git.commit → response includes the SHA.
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/git.commit \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{
    \"_session_id\":\"$SESSION\",
    \"clone_path\":\"$CLONE\",
    \"message\":\"docs capture test\",
    \"all\":true
  }"
```

Captured response:

```json
{
  "pack": "git.commit",
  "version": "v1",
  "output": {
    "commit": "8ce0780fe218b6c903ec7cf89827b52236ad249c"
  },
  "session_id": "022b902e-fcf4-4853-b65e-97cf9896cc81"
}
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | path-safety violations |
| `invalid_input` | nothing to commit (working tree clean) |
| `invalid_input` | empty `message` |
| `session_unavailable` | session expired |
| `handler_failed` | underlying `git commit` fails (e.g. detached HEAD pointing nowhere) |

## Session chaining

`needs_session: true`. Always after `fs.write` / `fs.patch` / `fs.delete` / `cmd.run` (writes that need capturing). Always before `repo.push`. Use `git.diff` on either side to verify.

## Async behavior

Synchronous. ~50–200 ms.

## See also

- [`git.diff`](./diff.md), [`git.log`](./log.md) — verify before/after.
- [`repo.push`](/PACKS) — publish the commit upstream.
- Source: [`internal/packs/builtin/fs_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/fs_packs.go).
- ADR 023 — repo.push design (incl. agent author env).
