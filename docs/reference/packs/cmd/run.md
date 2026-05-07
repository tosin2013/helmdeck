---
title: cmd.run
description: Run an arbitrary shell command inside a session-local clone path. Non-zero exits are normal pack outcomes, not pack errors. Output capped at 8 MiB combined stdout+stderr.
keywords: [helmdeck, cmd.run, shell, exec, code edit loop, MCP]
---

# `cmd.run`

Runs an arbitrary command inside the sidecar session that holds a clone path. The command is given as an `["argv", "form"]` array — **not a string** — so there's no shell-quoting ambiguity. Non-zero exit codes are returned as data (`exit_code`), not as pack errors. The combined stdout+stderr is capped at **8 MiB** to keep agent context windows bounded.

This is the workhorse pack for the Phase 5.5 code-edit loop: build, test, lint, run a script, anything the LLM wants to verify before `git.commit`.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded session-local clone. |
| `command` | `array` | yes | — | argv-style: `["go", "test", "./..."]`. Pass through `sh -c` explicitly if you need shell features: `["sh","-c","echo $PATH | grep go"]`. |
| `stdin` | `string` | no | — | Stdin bytes if the command reads from it. |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `stdout` | `string` | Captured stdout, UTF-8. Truncated at 8 MiB combined. |
| `stderr` | `string` | Captured stderr. |
| `exit_code` | `number` | The command's exit code. **Not** an error — agents inspect this and decide what to do. |

## Vault credentials needed

**None directly.** If the command needs an auth token (e.g. `gh api ...`), set up a vault credential and reference it via the `${vault:NAME}` placeholder pattern in the command (the same resolver `http.fetch` uses). For most agent code-edit work, no credential is needed.

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested prompt:

  "Clone the helmdeck repo and run `make check` — tell me whether it passes
   and summarize any test failures."

Agent should chain: repo.fetch → cmd.run with ["make","check"] → inspect
exit_code. If non-zero, parse stderr for failures.
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

Happy path — list a directory:

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/cmd.run \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{
    \"_session_id\":\"$SESSION\",
    \"clone_path\":\"$CLONE\",
    \"command\":[\"ls\",\"docs/reference/packs\"]
  }"
```

Captured response:

```json
{
  "pack": "cmd.run",
  "version": "v1",
  "output": {
    "exit_code": 0,
    "stderr": "",
    "stdout": "browser\nindex.md\n_template.md\n"
  },
  "session_id": "022b902e-fcf4-4853-b65e-97cf9896cc81"
}
```

Non-zero exit (still a successful pack call — exit_code is data):

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/cmd.run \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{
    \"_session_id\":\"$SESSION\",
    \"clone_path\":\"$CLONE\",
    \"command\":[\"sh\",\"-c\",\"echo to stderr 1>&2; exit 7\"]
  }"
```

```json
{
  "pack": "cmd.run",
  "version": "v1",
  "output": {
    "exit_code": 7,
    "stderr": "to stderr\n",
    "stdout": ""
  }
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `command` is a string, not an array | `{"error":"invalid_input","message":"field \"command\": expected array, got string"}` |
| `invalid_input` | path-safety violations on `clone_path` | per `safeJoin` |
| `session_unavailable` | session expired | session not found |
| `handler_failed` | container exec itself fails (sidecar dead) | exec error |

A non-zero exit is *not* an error — it's a normal outcome with `exit_code` set. Output truncation past 8 MiB *also* isn't an error; the trailing bytes are silently dropped (consider piping to a file via `>` if you need full output for huge runs).

## Session chaining

`needs_session: true`. The full Phase 5.5 loop: `repo.fetch` → `fs.read` → `fs.patch` → **`cmd.run`** (build/test) → `git.commit` → `repo.push`.

## Async behavior

Synchronous. Bounded by the engine's per-pack deadline (default 60s, override via session `timeout`).

## See also

- [`fs.write`](../fs/write.md), [`fs.patch`](../fs/patch.md) — produce the changes `cmd.run` then verifies.
- [`git.commit`](../git/commit.md) — capture the verified state.
- [`http.fetch`](../http/fetch.md) — for vault placeholder substitution patterns; the same resolver underlies this pack's command env.
- Source: [`internal/packs/builtin/fs_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/fs_packs.go).
