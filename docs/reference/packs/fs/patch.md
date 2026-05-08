---
title: fs.patch
description: Literal search-and-replace in a session-local file. Not regex. Returns count of replacements + post-write sha256.
keywords: [helmdeck, fs.patch, search replace, code edit, MCP]
---

# `fs.patch`

Performs **literal** search-and-replace in a single file inside a session-local clone path. Not regex — the agent specifies exact bytes to find and replace. Returns the number of replacements made plus the post-patch sha256.

Why literal not regex: weak open-weight models routinely produce broken regex (escaping issues, greedy patterns, lookaheads they don't understand). Literal substring substitution is unambiguous, and combining it with sha256 verification (`fs.read` before, sha256 check, `fs.patch`, `fs.read` after) gives the agent a reliable feedback loop.

## Inputs

Two shapes are accepted (issue [#90](https://github.com/tosin2013/helmdeck/issues/90)). Pick whichever matches the model you're running — gpt-oss / Claude default to the Anthropic batch shape, helmdeck-aware prompts can use the native shape.

**Shape 1 — helmdeck native (single edit):**

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded. |
| `path` | `string` | yes | — | Relative file path. |
| `search` | `string` | yes | — | The exact bytes to find. **Literal**, not regex. |
| `replace` | `string` | yes | — | The replacement bytes. |
| `occurrences` | `number` | no | unlimited | Cap replacements (per edit when batched). |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

**Shape 2 — Anthropic CodingAgent batch (multi-edit):**

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Same as above. |
| `path` | `string` | yes | — | Same as above. |
| `edits` | `array` | yes | — | Array of `{search, replace}` OR `{oldText, newText}` items. Edits apply in order to the same in-memory copy of the file before write-back. |
| `occurrences` | `number` | no | unlimited | Applies as a cap to **each** edit independently. |
| `_session_id` | `string` | yes (chained) | — | Same as above. |

If both shapes appear in one call, `edits[]` wins. If any edit's search string isn't found, the entire batch fails before write-back — the file is left untouched.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `applied` | `number` | Replacements actually performed. `0` is not an error — the file is left untouched and the agent should re-`fs.read` to understand why. |
| `sha256` | `string` | Hex-encoded sha256 after the patch. |

## Vault credentials needed

**None.**

## Use it from your agent (OpenClaw chat-UI worked example)

> 📌 **The transcript below predates issues [#90](https://github.com/tosin2013/helmdeck/issues/90) and [#92](https://github.com/tosin2013/helmdeck/issues/92)'s fixes.** It shows the model burning 4 retries against a strict schema (Anthropic `edits[].{oldText,newText}` was rejected) AND silently losing its working file because `_session_id` was dropped between `fs.write` and `fs.patch`. Both issues are fixed now: the pack accepts the Anthropic shape natively, and SKILLS.md teaches the session-chaining contract. Captured here for historical evidence — re-run on a current build and a fresh agent should converge in one call.

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Clone https://github.com/tosin2013/helmdeck.git via helmdeck__repo-fetch, write a temp file named demo-patch.md with content "version: alpha" using helmdeck__fs-write, then use helmdeck__fs-patch to literally replace "alpha" with "beta" in that file. Tell me the post-patch sha256 and the applied count.

**Tool call** (9 calls, no failures):

```json
{
  "name": "helmdeck__repo-fetch",
  "arguments": {
    "url": "https://github.com/tosin2013/helmdeck.git"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"repo.fetch","version":"v1","output":{"clone_path":"/tmp/helmdeck-clone-oZRqf0","commit":"9c4bb08ce5f7f96478728fc8952b9a41a8e0d8e8","credential":"","doc_hints":["README*","docs/**/*.md","docs/**/*.adoc","docs/**/*.rst","content/**/*.md","content/**/*.adoc"],"entrypoints":[{"kind":"build","path":"Makefile"},{"kind":"go","path":"go.mod"},{"kind":"agent-doc","path":"CLAUDE.md"},{"kind":"contributing","path":"CONTRIBUTING.md"}],"files":371,"readme":{"content":"# helmdeck\n\n\u003e Most browser agents require GPT-4o or Claude Sonnet to work reliably.\n\u003e Helmdeck is built for the other 99% of deployments — **local 7B models,\n\u003e air-gapped environments, and teams that can't send credentials to a\n\u003e cloud API.** It wraps every browser, desktop, git, and code action\n\u003e i
  ...[truncated]
```

```json
{
  "name": "helmdeck__fs-write",
  "arguments": {
    "clone_path": "/tmp/helmdeck-clone-oZRqf0",
    "path": "demo-patch.md",
    "content": "version: alpha"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"fs.write","version":"v1","output":{"sha256":"f2474553f41fc4b611244045709f3ad5d1437a8725ae281f29be281c6a840f28","size":14},"duration_ms":2646683167,"session_id":"dc2bfd86-005f-47bd-9db1-92d152b63a1d"}
```

```json
{
  "name": "helmdeck__fs-patch",
  "arguments": {
    "clone_path": "/tmp/helmdeck-clone-oZRqf0",
    "path": "demo-patch.md",
    "edits": [
      {
        "oldText": "alpha",
        "newText": "beta"
      }
    ]
  }
}
```

**Pack response** (truncated):

```json
Validation failed for tool "helmdeck__fs-patch":
  - search: must have required properties search, replace

Received arguments:
{
  "clone_path": "/tmp/helmdeck-clone-oZRqf0",
  "path": "demo-patch.md",
  "edits": [
    {
      "oldText": "alpha",
      "newText": "beta"
    }
  ]
}
```

```json
{
  "name": "helmdeck__fs-patch",
  "arguments": {
    "clone_path": "/tmp/helmdeck-clone-oZRqf0",
    "path": "demo-patch.md",
    "edits": [
      {
        "search": "alpha",
        "replace": "beta"
      }
    ]
  }
}
```

**Pack response** (truncated):

```json
Validation failed for tool "helmdeck__fs-patch":
  - search: must have required properties search, replace

Received arguments:
{
  "clone_path": "/tmp/helmdeck-clone-oZRqf0",
  "path": "demo-patch.md",
  "edits": [
    {
      "search": "alpha",
      "replace": "beta"
    }
  ]
}
```

```json
{
  "name": "helmdeck__fs-patch",
  "arguments": {
    "clone_path": "/tmp/helmdeck-clone-oZRqf0",
    "path": "demo-patch.md",
    "edits": [
      {
        "oldText": "alpha",
        "newText": "beta"
      }
    ]
  }
}
```

**Pack response** (truncated):

```json
Validation failed for tool "helmdeck__fs-patch":
  - search: must have required properties search, replace

Received arguments:
{
  "clone_path": "/tmp/helmdeck-clone-oZRqf0",
  "path": "demo-patch.md",
  "edits": [
    {
      "oldText": "alpha",
      "newText": "beta"
    }
  ]
}
```

```json
{
  "name": "helmdeck__fs-patch",
  "arguments": {
    "clone_path": "/tmp/helmdeck-clone-oZRqf0",
    "path": "demo-patch.md",
    "replace": "beta",
    "search": "alpha"
  }
}
```

**Pack response** (truncated):

```json
{"error":"invalid_input","message":"file not readable: sh: 1: cannot open /tmp/helmdeck-clone-oZRqf0/demo-patch.md: No such file"}
```

```json
{
  "name": "helmdeck__fs-list",
  "arguments": {
    "clone_path": "/tmp/helmdeck-clone-oZRqf0",
    "path": ".",
    "glob": "*.md"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"fs.list","version":"v1","output":{"count":0,"files":[]},"duration_ms":2283860132,"session_id":"d37c5b79-3366-43d2-99d2-8cbde8c1c69d"}
```

```json
{
  "name": "exec",
  "arguments": {
    "command": "echo -n \"version: beta\" | sha256sum",
    "timeout": 10000
  }
}
```

**Pack response** (truncated):

```json
56a36fc31d6b060d0f0b9e7574baf02bd47789d4c0f8f381fa8b45a3c07e9293  -
```

```json
{
  "name": "helmdeck__fs-list",
  "arguments": {
    "clone_path": "/tmp/helmdeck-clone-oZRqf0",
    "path": "."
  }
}
```

**Pack response** (truncated):

```json
{"pack":"fs.list","version":"v1","output":{"count":0,"files":[]},"duration_ms":2534979025,"session_id":"d2eecb22-6c9e-4fc7-a9b6-b22653e74bd4"}
```

**Agent's final reply**:

> The patch could not be applied because the temporary file `demo-patch.md` was not present in the clone. Consequently I couldn’t produce a post‑patch SHA‑256 or an applied‑count.

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0705).*

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
