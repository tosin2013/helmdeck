---
title: python.run
description: Run Python code or commands inside a Python-equipped sidecar (per-pack image override). CPython 3 + pytest + ruff + mypy preinstalled. Non-zero exits are normal pack outcomes.
keywords: [helmdeck, python.run, sidecar, code execution, MCP]
---

# `python.run`

Runs Python code or a command inside a Python-equipped sidecar container. The pack acquires its own session (separate from the browser sidecar) via `SessionSpec.Image: helmdeck-sidecar-python:dev`, so it inherits the path-safety guard but doesn't share state with browser-driven packs.

The Python sidecar ships **CPython 3.11** + `pytest` + `ruff` + `mypy` preinstalled. Either set `code` (run inline via `python3 -c`) or `command` (run an argv-shape command in `cwd`) — exactly one must be set.

> ⚙️ **Setup note**: this pack only works when the language sidecar is built and pinned. Run `make sidecars` to build, then add `HELMDECK_SIDECAR_PYTHON=helmdeck-sidecar-python:dev` to `deploy/compose/.env.local` and recreate the control-plane container (`docker compose up -d --force-recreate control-plane`). Without those, the pack returns `session_unavailable` with `No such image: ghcr.io/tosin2013/helmdeck-sidecar-python:latest`.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `code` | `string` | one of | — | Python code to run via `python3 -c`. Multi-line supported. |
| `command` | `array` | one of | — | argv-style command (e.g. `["pytest", "-v"]`). |
| `cwd` | `string` | no | — | Working directory inside the sidecar (typically a clone path from a chained `repo.fetch`). |
| `stdin` | `string` | no | — | Bytes piped to the process's stdin. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `stdout` | `string` | Captured stdout. |
| `stderr` | `string` | Captured stderr. |
| `exit_code` | `number` | Process exit code. **Not** an error — the agent inspects this to decide. |
| `runtime` | `string` | Always `python` (handy when an agent dispatches multiple language packs and wants to know which it just ran). |

## Vault credentials needed

**None directly.** If the Python code needs a service-API key, use `${vault:NAME}` placeholder substitution at the env-var level (set `command` to `["sh","-c","TOKEN=${vault:my-token} python3 -c '...'"]`).

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested prompt:

  "Use python.run to compute the SHA256 of the string 'helmdeck' and tell me
   the hex digest."

Agent should: python.run with code="import hashlib; print(hashlib.sha256(b'helmdeck').hexdigest())".
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

Happy path — inline code:

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/python.run \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{"code":"import platform; print(\"Python\", platform.python_version()); print(2+2)"}'
```

Captured response:

```json
{
  "pack": "python.run",
  "version": "v1",
  "output": {
    "exit_code": 0,
    "runtime": "python",
    "stderr": "",
    "stdout": "Python 3.11.2\n4\n"
  },
  "session_id": "944bcb55-5b79-4c9a-b4e4-d2347f0faf6f"
}
```

Code that raises (non-zero exit, traceback in stderr — pack call still succeeds):

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/python.run \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{"code":"raise ValueError(\"oops\")"}'
```

```json
{
  "pack": "python.run",
  "version": "v1",
  "output": {
    "exit_code": 1,
    "runtime": "python",
    "stderr": "Traceback (most recent call last):\n  File \"<string>\", line 1, in <module>\nValueError: oops\n",
    "stdout": ""
  }
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | Both `code` and `command` empty | `{"error":"invalid_input","message":"exactly one of \"code\" or \"command\" must be set"}` |
| `invalid_input` | Both `code` and `command` set | `set either code or command, not both` |
| `session_unavailable` | Python sidecar image not present | `container create: Error response from daemon: No such image: ...` |
| `handler_failed` | Container exec itself fails (sidecar OOM, daemon restart) | exec error |

A non-zero `exit_code` is *not* an error — it's a successful pack call where the script chose to exit non-zero.

## Session chaining

`needs_session: true`, but the session is owned by this pack (separate Python sidecar). To run Python against files in a clone, chain `repo.fetch` → use the returned `clone_path` as `cwd` for `python.run`. The clone gets bind-mounted into the Python sidecar — no separate fs.copy needed.

## Async behavior

Synchronous. Container startup adds ~1–2 seconds of cold-start the first time; warm sessions reuse the container.

## See also

- [`node.run`](./node-run.md) — same shape, Node.js runtime.
- [`cmd.run`](../cmd/run.md) — for non-Python work in the browser sidecar (which has bash + git + standard Unix tools).
- Source: [`internal/packs/builtin/python_run.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/python_run.go).
- ADR 001 — sidecar pattern + per-pack image override.
- [`SIDECAR-LANGUAGES.md`](/SIDECAR-LANGUAGES) — adding new language sidecars (Rust, Go, Ruby, …).
