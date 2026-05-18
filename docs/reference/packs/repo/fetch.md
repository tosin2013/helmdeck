---
title: repo.fetch
description: Clone a git repo into a session container using vault-resolved SSH or HTTPS credentials. Returns a context envelope (tree, README, entrypoints, signals) so the agent can orient on the first turn.
keywords: [helmdeck, repo, fetch, git, clone, vault, MCP]
---

# `repo.fetch`

The "clone a git repo into the session" pack. Caller supplies a git `url`; the pack writes vault-resolved credentials to a temp file inside the session, runs `git clone` with the appropriate transport config (SSH key via `GIT_SSH_COMMAND`, HTTPS PAT via `GIT_ASKPASS`), and shreds the credential before returning. The agent never sees the secret.

What sets `repo.fetch` apart from a plain `git clone` shell call is the **context envelope** in the response: tree (up to 300 paths), README content, recognized entrypoints (`Makefile`, `go.mod`, `package.json`, `CLAUDE.md`, etc.), doc-discovery hints, and a `signals` block (`has_readme`, `has_docs_dir`, `has_code`, `sparse`, `doc_file_count`, `code_file_count`). One tool call returns enough orientation that the agent doesn't need a follow-up `fs.list` to figure out what's in the repo.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `url` | `string` | yes | — | Git URL. Three forms accepted: `git@github.com:owner/repo.git` (scp-like SSH), `ssh://git@github.com/owner/repo.git` (URL SSH), `https://github.com/owner/repo.git` (HTTPS, public or with vault credential). |
| `ref` | `string` | no | HEAD | Branch or tag to check out after cloning. |
| `depth` | `number` | no | full clone | Pass `1` for a shallow clone (faster, smaller). |
| `credential` | `string` | no | — | Vault credential name. Required for private HTTPS repos; for SSH the pack resolves a key by host match (no name needed). |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `url` | `string` | Echo of the input. |
| `ref` | `string` | Resolved ref. |
| `commit` | `string` | HEAD SHA after the clone (and post-checkout if `ref` was set). |
| `clone_path` | `string` | `/tmp/helmdeck-clone-<rand>`. Pass this back as `clone_path` to every follow-up `fs.*` / `cmd.run` / `git.*` / `repo.push` call, **plus** propagate `_session_id` (see Session chaining). |
| `session_id` | `string` | **Issue #232.** The session container the clone lives in. Pass this as `_session_id` in every follow-up pack call — otherwise the engine spins up a fresh session whose `/tmp` does not contain this clone, and `fs.read` / `cmd.run` will see "No such file" errors. This value also appears on the response envelope as `session_id`; it's duplicated here so it can't be missed by callers reading only `output`. |
| `credential` | `string` | Vault record name actually used (or `""` for public clones). |
| `files` | `number` | Total `git ls-files` count. |
| `tree` | `array` | Up to 300 relative paths. Sorted. |
| `tree_total` | `number` | True count even if `tree` was truncated. |
| `tree_truncated` | `boolean` | `true` when `files > 300` — narrow follow-ups with `fs.list` + a glob from `doc_hints`. |
| `readme` | `object` | `{path, content (≤4096 bytes), truncated}` for the auto-detected top-level README. **`null` when no README exists.** |
| `entrypoints` | `array` | `[{path, kind}]` — `kind` is one of `build`, `go`, `node`, `python`, `rust`, `java`, `gradle`, `devfile`, `container`, `compose`, `agent-doc`, `contributing`. |
| `doc_hints` | `array` | Glob suggestions for `fs.list` (`docs/**/*.md`, `content/**/*.adoc`, etc.). Static — no per-repo computation. |
| `signals` | `object` | Coarse classifier the agent branches on: `{has_readme, has_docs_dir, has_code, doc_file_count, code_file_count, sparse}`. See [SKILLS.md §"Repo discovery pattern"](/integrations/SKILLS#repo-discovery-pattern) for the decision table. |

## Vault credentials needed

**Optional** — public HTTPS clones work without a credential.

For private repos:

- **SSH** — type `ssh-git`, host pattern matches the git host (e.g. `github.com`). Resolved by the pack via host-match; no `credential` argument needed.
- **HTTPS PAT** — type `api_key`, host pattern `api.github.com` (or your provider's API host). Pass `credential: "<vault-name>"` in the input.

The PAT/key is written to a 0600 temp file inside the session, used via `GIT_ASKPASS` / `GIT_SSH_COMMAND`, and `shred`'d on exit. The agent never sees the secret value.

## Use it from your agent (OpenClaw chat-UI worked example)

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Use helmdeck__repo-fetch to clone https://github.com/tosin2013/helmdeck.git. Tell me the commit SHA, total file count, whether the readme was found, and which entrypoints (Makefile, go.mod, CLAUDE.md, etc.) the response surfaced.

**Tool call** (1 call, no failures):

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
{"pack":"repo.fetch","version":"v1","output":{"clone_path":"/tmp/helmdeck-clone-66wkAG","commit":"0eb718893a0a49431c34292d23fee7412e4a828b","credential":"","doc_hints":["README*","docs/**/*.md","docs/**/*.adoc","docs/**/*.rst","content/**/*.md","content/**/*.adoc"],"entrypoints":[{"kind":"build","path":"Makefile"},{"kind":"go","path":"go.mod"},{"kind":"agent-doc","path":"CLAUDE.md"},{"kind":"contributing","path":"CONTRIBUTING.md"}],"files":407,"readme":{"content":"# helmdeck\n\n\u003e Today's helmdeck install ran a full 6-step code-edit loop (clone, read, patch,\n\u003e test, commit, push) on `gpt-oss-120b` for **$0.07**. The same loop on Cursor\n\u003e or Claude Code direct via Sonnet would have cost **$0.30+**. Same outcome,\n\u003e ~5× cheaper — and the \"expensive\" stack isn't even th
  ...[truncated]
```

**Agent's final reply**:

> **Repo clone details for `https://github.com/tosin2013/helmdeck.git`**
> 
> - **Commit SHA:** `0eb718893a0a49431c34292d23fee7412e4a828b`  
> - **Total file count:** **407** files in the repository (tree total).  
> - **README found:** Yes – `README.md` was present (truncated excerpt included in the response).  
> - **Entry‑point files surfaced by the pack:**
>   - `Makefile` (kind: **build**)  
>   - `go.mod` (kind: **go**)  
>   - `CLAUDE.md` (kind: **agent‑doc**)  
>   - `CONTRIBUTING.md` (kind: **contributing**)  
> 
> These are the primary entry points the `helmdeck__repo-fetch` pack identified.

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0015).*

## Developer reference (`curl`)

```bash
ADMIN_PW=$(grep HELMDECK_ADMIN_PASSWORD /root/helmdeck/deploy/compose/.env.local | cut -d= -f2)
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

# Public HTTPS clone:
curl -fsS -X POST http://localhost:3000/api/v1/packs/repo.fetch \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{"url":"https://github.com/tosin2013/helmdeck.git"}'

# Private clone with vault PAT:
curl -fsS -X POST http://localhost:3000/api/v1/packs/repo.fetch \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{"url":"https://github.com/your-org/private.git","credential":"github-token"}'
```

Captured response shape (truncated):

```json
{
  "pack": "repo.fetch",
  "version": "v1",
  "output": {
    "clone_path": "/tmp/helmdeck-clone-Xxxxx",
    "commit": "abc1234567...",
    "credential": "",
    "files": 385,
    "tree": ["CLAUDE.md", "CONTRIBUTING.md", "Makefile", "..."],
    "tree_total": 385,
    "tree_truncated": true,
    "readme": {"path": "README.md", "content": "# helmdeck\n...", "truncated": false},
    "entrypoints": [
      {"kind": "build",       "path": "Makefile"},
      {"kind": "go",          "path": "go.mod"},
      {"kind": "agent-doc",   "path": "CLAUDE.md"},
      {"kind": "contributing","path": "CONTRIBUTING.md"}
    ],
    "doc_hints": ["README*", "docs/**/*.md", "docs/**/*.adoc"],
    "signals": {"has_readme": true, "has_docs_dir": true, "has_code": true, "sparse": false}
  },
  "session_id": "..."
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `url` missing | `{"error":"invalid_input","message":"url is required"}` |
| `invalid_input` | URL host resolves to a blocked range (metadata, RFC 1918, loopback) | `{"error":"invalid_input","message":"egress denied: …"}` |
| `invalid_input` | Remote has **no branches** (newly-created empty repo) | `{"error":"invalid_input","message":"remote https://… has no branches; push at least one commit before cloning"}` *(fast-fail per [issue #94](https://github.com/tosin2013/helmdeck/issues/94))* |
| `invalid_input` | `credential` set but vault has no matching record | `{"error":"invalid_input","message":"vault credential \"name\" not found"}` |
| `handler_failed` | git clone exit non-zero (network, auth, missing repo) | `git clone exit N: …` (stderr truncated to 1024 chars) |
| `session_unavailable` | Engine has no session executor | `engine has no session executor` |

## Session chaining

**Required (creates the session).** `repo.fetch` is the canonical session-creating pack — every follow-on `fs.read`, `fs.write`, `fs.list`, `fs.patch`, `fs.delete`, `cmd.run`, `git.commit`, `git.diff`, `git.log`, `repo.push`, `repo.map`, `content.ground` (in file mode) needs both `_session_id` AND `clone_path` from this response. See [SKILLS.md §"Session chaining contract"](/integrations/SKILLS#session-chaining-contract--read-before-chaining-fs--cmdrun--git) for what happens when you forget.

`PreserveSession: true` — the session persists 5 minutes after the last call (watchdog cleanup) so chained workflows reuse the same Chromium / sidecar instance. Subsequent calls within that window are warm (~1–3s); first call is cold (~10–30s for sidecar boot + clone).

## Async behavior

Synchronous. Wall-clock = sidecar boot (~5–15s on cold session) + `git clone` over the network + envelope computation (~1s for a 300-file tree). Heavy repos (Linux kernel, large monorepos) can hit the 5-minute session-creation timeout — pass `depth: 1` to bound the work.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `repo.fetch`.
- Source: [`internal/packs/builtin/repo_fetch.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/repo_fetch.go).
- ADR 022 — Repo packs.
- Companion packs: [`repo.map`](./map.md), [`repo.push`](./push.md), [`fs.read`](../fs/read.md), [`git.commit`](../git/commit.md).
- The Phase 5.5 code-edit loop walkthrough: [`SKILLS.md`](/integrations/SKILLS#worked-example--the-phase-55-code-edit-loop).
