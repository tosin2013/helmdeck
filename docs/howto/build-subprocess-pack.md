---
title: Build a subprocess pack
description: Drop an executable + YAML manifest into the command-packs directory to expose a typed pack in any language.
---

# Build a subprocess pack

helmdeck packs are normally Go functions compiled into the control-plane binary. **Subprocess packs** let you ship a pack as a standalone executable in any language (Python, Node, Bash, Rust, anything that can read JSON from stdin and write JSON to stdout). A sibling YAML manifest declares the pack's typed schemas and optional execution limits.

This how-to walks through writing one end-to-end with a working `cmd.upper` example.

## Prerequisites

- helmdeck control-plane running (Compose stack or `make run`).
- An executable file you can `chmod +x`.
- The control-plane environment variable `HELMDECK_COMMAND_PACKS_DIR` pointed at a directory the control plane can read.

## 1. Pick a directory and tell the control plane

Subprocess packs live in `$HELMDECK_COMMAND_PACKS_DIR`. The control plane scans this directory once at startup and registers one pack per executable.

```sh
export HELMDECK_COMMAND_PACKS_DIR=/etc/helmdeck/command-packs
mkdir -p "$HELMDECK_COMMAND_PACKS_DIR"
```

Restart the control plane after changing this variable. (Hot reload is a v1.x followup; today, restart is the only refresh path.)

## 2. Write the executable

The subprocess protocol is minimal:

| Channel | Direction | Format |
|---|---|---|
| `stdin` | control-plane → pack | One JSON value (the validated input) |
| `stdout` | pack → control-plane | One JSON value (matches the output schema) |
| `stderr` | pack → control-plane | Free-form. Surfaced on non-zero exit. |
| exit code | pack → control-plane | `0` = success. Anything else = `handler_failed`. |

A two-line shell example that uppercases the `text` field:

```sh
#!/bin/sh
# /etc/helmdeck/command-packs/upper
text="$(jq -r .text)"
jq -n --arg text "$text" '{text: ($text | ascii_upcase)}'
```

```sh
chmod +x /etc/helmdeck/command-packs/upper
```

## 3. Write the manifest

Drop a sibling YAML file at `<basename>.helmdeck-pack.yaml`. The basename matches the executable with the extension stripped (so `upper`, `upper.sh`, and `upper.py` all map to the same `upper.helmdeck-pack.yaml`).

```yaml
# /etc/helmdeck/command-packs/upper.helmdeck-pack.yaml
name: cmd.upper
version: v1
description: Uppercase a string.
author: "Your name or org"

input_schema:
  required: [text]
  properties:
    text: string

output_schema:
  required: [text]
  properties:
    text: string

# Optional execution overrides (omit for sensible defaults):
timeout_s: 30
max_output_bytes: 1048576
env:
  - PYTHONUNBUFFERED=1
```

## Manifest field reference

| Field | Required | Type | Default | Notes |
|---|---|---|---|---|
| `name` | no | string | (auto-derived) | Decorative; the registered pack name is always `cmd.<sanitized-basename>`. A mismatch logs a warning. |
| `version` | no | string | `v1` | Surfaced via `GET /api/v1/packs`. |
| `description` | no | string | (binary path) | Shown in the Management UI and MCP tool listing. |
| `author` | no | string | (empty) | Free-form attribution. |
| `input_schema.required` | no | string list | `[]` | Field names that must be present on every call. |
| `input_schema.properties` | no | map<string,type> | `{}` | Type per field — see allowed types below. |
| `output_schema.required` | no | string list | `[]` | Same shape as `input_schema`. |
| `output_schema.properties` | no | map<string,type> | `{}` | |
| `timeout_s` | no | integer | `60` | Wall-clock cap. Must be ≥ 0. The pack caller's context still wins if it cancels first. |
| `max_output_bytes` | no | integer | `16777216` (16 MiB) | stdout cap. Excess bytes are silently truncated. |
| `env` | no | string list | (none) | Per-call env vars in `KEY=VALUE` form, appended to the control-plane's inherited environment. Do **not** put secrets here — route credentials through the vault and pass them via stdin JSON. |

### Allowed schema types

`input_schema.properties` and `output_schema.properties` accept these type names:

- `string`
- `number`
- `boolean`
- `object`
- `array`

Anything else (`integer`, `null`, `date`, etc.) causes the pack to be **skipped at startup** with an error logged. The validator is intentionally minimal — see `internal/packs/schema.go` for the contract.

## 4. Smoke-test the pack

Restart the control plane and look for the registration log line:

```
INFO command pack registered name=cmd.upper binary=/etc/helmdeck/command-packs/upper manifest=/etc/helmdeck/command-packs/upper.helmdeck-pack.yaml
```

Then invoke it:

```sh
curl -s http://localhost:3000/api/v1/packs/cmd.upper \
  -H 'Content-Type: application/json' \
  -d '{"text":"hello"}'
# {"text":"HELLO"}
```

Calling with a missing required field surfaces the typed validation error:

```sh
curl -s http://localhost:3000/api/v1/packs/cmd.upper \
  -H 'Content-Type: application/json' \
  -d '{}'
# {"error":{"code":"invalid_input","message":"missing required field \"text\""}}
```

## Behavior when the manifest is absent or invalid

| State | Outcome |
|---|---|
| No manifest file | Pack registers with **passthrough** schemas (any JSON in, any JSON out) — the v0.12.x MVP behavior. |
| Manifest unreadable / malformed YAML | Pack **skipped**. Control plane logs `command pack skipped (manifest invalid)` with the parse error. |
| Manifest declares an unknown type (e.g. `age: integer`) | Pack **skipped**. Log mentions the offending property and the allowed type set. |
| Manifest sets `timeout_s: -5` or `max_output_bytes: -1` | Pack **skipped**. |
| Manifest's `name:` disagrees with the auto-derived name | Pack **registers** under the auto-derived name. Log emits a warning. |

The "skip on invalid manifest" rule is deliberate: dropping a manifest is an explicit declaration of typed schemas, and silently degrading to passthrough would mask the operator's bug.

## Security notes

- **Subprocess egress is not sandboxed.** The pack's network reach is whatever the host gives it. helmdeck's `EgressGuard` intercepts HTTP calls inside Go packs but not arbitrary `exec()` invocations. A subprocess egress allowlist is tracked separately.
- **Secrets do not belong in `env`.** Anything you put in the manifest's `env:` list is plain-text on disk and visible in `ps` output. Use the vault — request a credential from the agent, pass the resolved value via stdin JSON, and the pack never sees the secret on disk.
- **Run the control-plane unprivileged.** The subprocess inherits the control-plane's UID/GID. Confining the control-plane (own user, container, network namespace) is the operator's job.

## Related

- `docs/integrations/SKILLS.md` — agent-facing skill catalog. Subprocess packs appear under the `cmd.*` namespace once registered.
- `docs/reference/packs/` — per-pack reference pages for the built-in packs. Operator-supplied packs are not auto-documented here; consider adding a `docs/reference/packs/cmd-<name>.md` page if you ship a subprocess pack publicly.
- [#173](https://github.com/tosin2013/helmdeck/issues/173) — the manifest-format proposal this guide implements.
