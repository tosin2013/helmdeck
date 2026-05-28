---
title: swe.solve
description: Run a mini-swe-agent loop inside a session sidecar to produce a reviewable code change.
keywords: [helmdeck, repo, swe-solve, swe-agent, mini-swe-agent, MCP]
---

# `swe.solve`

`swe.solve` takes a repository URL and a natural-language task and runs a
[mini-swe-agent](https://github.com/SWE-agent/mini-swe-agent) loop **inside a
helmdeck session sidecar** to produce a reviewable code change. It is the
single-call orchestrator for the full edit loop: clone the repo, seed the agent
with a symbol map, run the agent, capture the diff and the agent's trajectory,
and — depending on the output `mode` — stop at a patch, push a new branch, or
open a pull request.

The agent runs **local-in-session**: `mini` executes with the sidecar's own
bash inside the clone, and its LLM calls go to helmdeck's OpenAI-compatible AI
gateway via litellm. The resolved git credential and the gateway API key are
**never visible to the agent** — they are injected through the same
`GIT_ASKPASS` / `GIT_SSH_COMMAND` / stdin patterns used by `repo.fetch` and
`repo.push`, and they never appear in the trajectory, the diff, the logs, or
the pack output.

## Why it works this way (design + what's novel)

**Host, don't rebuild.** helmdeck doesn't ship its own coding agent — it runs
the open-source [mini-swe-agent](https://github.com/SWE-agent/mini-swe-agent)
loop and wraps it with what a hosted control plane already does well:
credential injection, sandboxing, audit, and artifact capture. The pack is the
*integration*, not the agent — the same "host upstream tools, don't reimplement
them" stance as `research.deep` (Firecrawl) and `slides.render` (Marp).

**The agent is credential-blind — this is the load-bearing property.** The
agent process gets exactly two things: a writable clone and an
OpenAI-compatible endpoint. The git token and the gateway key are injected
out-of-band (`GIT_ASKPASS` / `GIT_SSH_COMMAND` / stdin → `OPENAI_API_KEY` set
in the sidecar only) and never enter the agent's argv, the environment it can
read back, the trajectory, the diff, the logs, or the pack output. An agent
that goes off the rails can't exfiltrate a secret it never saw — the usual
worry about "letting an LLM run `git`/`bash`" is structurally removed.

**It runs in a throwaway sidecar, not on your host.** The loop executes inside
a per-call mini-swe sidecar (2 GiB, 20-minute budget) over the clone, so any
`bash` the agent decides to run is contained and the session is torn down
after — the same isolation `repo.fetch` and the language packs rely on.

**It never merges its own work.** `branch`/`pull_request` modes always cut a
fresh `helmdeck/swe-solve-<sha>` branch and stop at a PR for a human to review.
There is no auto-merge path; the invariant is enforced by a unit test (see
[Never pushes to the default branch](#never-pushes-to-the-default-branch)).

**Label an issue, get a PR.** The headline workflow ([auto-trigger](#auto-trigger-from-github-issues-adr-033-233-phase-6))
closes the loop end-to-end — a labeled GitHub issue dispatches `swe.solve` in
`pull_request` mode on a background context and comments the resulting PR back,
with no human keystroke between "file the issue" and "review the patch."

**Failures stay legible.** A run that fails is attributed like any pack: a bad
`repo_url`/`task`, or an agent that produced no change, is `invalid_input`
(caller-fixable — fix and re-run), not a `pack_bug` you should file an issue
for. See [Error codes](#error-codes).

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `repo_url` | `string` | yes | — | Git URL to clone. SSH (`git@host:owner/repo`) or HTTPS. |
| `task` | `string` | yes | — | Natural-language task for the agent. |
| `ref` | `string` | no | clone HEAD | Base ref to clone / check out. |
| `base_branch` | `string` | no | `ref` or `main` | PR base branch (`pull_request` mode). |
| `credential` | `string` | no | `github-token` | Vault credential name for HTTPS clone/push and PR auth. |
| `model` | `string` | no | `HELMDECK_SWE_MODEL` or `gpt-4o` | litellm model id for the agent loop. |
| `gateway_base` | `string` | no | `HELMDECK_GATEWAY_BASE` or `http://localhost:8080/v1` | OpenAI-compatible gateway base URL the sidecar reaches. |
| `max_steps` | `number` | no | `30` | Agent step bound (maps to `mini --step-limit`). |
| `mode` | `string` | no | `patch` | `patch` \| `branch` \| `pull_request`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `success` | `boolean` | True when the loop completed and a diff was captured. |
| `summary` | `string` | Best-effort summary from the agent's trajectory. |
| `patch` | `string` | Unified diff of the change (staged working tree vs. cloned HEAD). |
| `commit` | `string` | Commit sha (`branch` / `pull_request` modes). |
| `branch` | `string` | New branch name, `helmdeck/swe-solve-<short-sha>` (`branch` / `pull_request`). |
| `pr_url` | `string` | PR HTML URL (`pull_request` mode). |
| `trajectory_artifact_key` | `string` | Artifact key for the stored trajectory (`application/json`). |
| `steps_executed` | `number` | Best-effort step count from the trajectory. |

The response also includes a top-level `session_id` (the pack is session-coupled
and `PreserveSession`) and an `artifacts` array containing the trajectory.

## Output modes

- **`patch`** (default, safe): clone → seed → agent loop → capture diff +
  trajectory. **No push.** Use this to review the agent's proposed change before
  it touches any remote.
- **`branch`**: everything `patch` does, then create a **new** branch
  (`helmdeck/swe-solve-<short-sha>`), commit the change, and push the branch via
  vault credentials. Does not open a PR.
- **`pull_request`**: everything `branch` does, then open a PR (`head` = the new
  branch, `base` = `base_branch`) via `github.create_pr`. A human reviews and
  merges the PR — the agent's work is **never merged automatically**.

### Never pushes to the default branch

`branch` and `pull_request` modes always create a fresh
`helmdeck/swe-solve-<short-sha>` branch with `git switch -c` (which fails if the
name already exists) and push **that** branch. swe.solve never commits to or
pushes the cloned/default branch. This is a hard invariant, covered by a unit
test.

## Vault credentials needed

- **Git clone/push** — for private HTTPS repos, pass `credential` (default
  `github-token`, type `api_key`). For SSH URLs, an `ssh` credential matching
  the host is resolved automatically. Public HTTPS repos clone with no
  credential.
- **AI gateway** — a credential named `helmdeck-gateway` (type `api_key`,
  override via `HELMDECK_GATEWAY_CRED`) holding the OpenAI-compatible token for
  the gateway. If absent, the agent runs against an auth-optional gateway. The
  key is piped via stdin into the agent run script and exported as
  `OPENAI_API_KEY` inside the sidecar only.
- **Pull request** — `pull_request` mode reuses the same `credential` PAT for
  `github.create_pr`.

In all cases the credential value is injected via stdin / `GIT_ASKPASS` /
environment-from-file and never reaches the agent argv, the trajectory, the
logs, or the pack output.

## Gateway wiring (operator note)

There is no internal constant for the gateway base URL reachable from inside a
session container, so swe.solve accepts it via the `gateway_base` input and
falls back to `HELMDECK_GATEWAY_BASE`. **Operators must set
`HELMDECK_GATEWAY_BASE`** in the control-plane environment to the in-cluster
control-plane `/v1` URL (e.g. `http://control-plane:8080/v1`) so the sidecar
can reach the gateway. The default `http://localhost:8080/v1` only works when
the sidecar shares the control plane's network namespace.

## Sidecar image

swe.solve pins `ghcr.io/tosin2013/helmdeck-sidecar-mini-swe:latest` (override via
`HELMDECK_SIDECAR_MINI_SWE`), built by `make sidecar-mini-swe-build`. The image
extends the Python sidecar with `mini-swe-agent` (pinned, per ADR 037),
`universal-ctags` (for the repo.map seed), and the vendored
`contrib/helmdeck-environment` adapter. SessionSpec: `MemoryLimit: 2g`,
`Timeout: 20m`.

## Use it from your agent (OpenClaw chat-UI worked example)

In the OpenClaw chat UI (which loads the `helmdeck__*` MCP tools), ask in plain
language — the agent maps it to the `helmdeck__swe_solve` tool:

> **You:** Use helmdeck swe.solve to clone
> `https://github.com/octocat/Hello-World.git` and add a top-level
> `CONTRIBUTING.md` with a one-paragraph intro. Use `mode: patch` so nothing is
> pushed — just show me the diff.

`swe.solve` is async, so the agent gets a task envelope back, polls it, and then
reports the run's `summary` and `patch` (the unified diff). Review the diff in
chat; nothing has touched the remote.

To open a PR instead, point it at a repo you hold a `github-token` for and say
*"…use `mode: pull_request`"* — the reply then includes the `pr_url` for a human
to review and merge. (Tool-calling currently works through the **chat UI**, not
the `openclaw agent` CLI — see [the OpenClaw integration note](/integrations/openclaw).)

## Developer reference (`curl`)

`swe.solve` is **async** — `tools/call` (or the REST endpoint) returns a task
envelope; poll for the result. Mint a JWT first:

```bash
ADMIN_PW=$(grep HELMDECK_ADMIN_PASSWORD /root/helmdeck/deploy/compose/.env.local | cut -d= -f2)
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
```

Happy path (patch mode):

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/swe.solve \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "repo_url": "https://github.com/octocat/Hello-World.git",
    "task": "Add a top-level CONTRIBUTING.md with a one-paragraph intro.",
    "mode": "patch"
  }'
```

## Error codes

The closed-set codes are defined in
[`internal/packs/errors.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/errors.go):
`invalid_input`, `invalid_output`, `schema_mismatch`, `session_unavailable`,
`handler_failed`, `artifact_failed`, `timeout`, `internal`.

| Code | Triggers |
|---|---|
| `invalid_input` | Missing `repo_url`/`task`; unknown `mode`; refless remote; agent produced no change to commit (branch/PR modes). |
| `session_unavailable` | Engine has no session runtime/executor. |
| `handler_failed` | Clone/mini/commit/push exec failed; vault credential not found; PR creation failed. |
| `schema_mismatch` | Non-fast-forward push rejected. |
| `artifact_failed` | Trajectory artifact store failed. |

## Session chaining

**Required.** The pack acquires its own mini-swe sidecar session and keeps it
alive (`PreserveSession`). The returned `session_id` can be reused by follow-on
`fs.*` / `cmd.run` / `git.*` packs to inspect or extend the clone.

## Auto-trigger from GitHub issues (ADR 033, #233 Phase 6)

`swe.solve` can run automatically when an issue is **labeled** on a connected repo — "label an issue, get a PR." The GitHub webhook receiver (`POST /api/v1/webhooks/github`) verifies the delivery's HMAC-SHA256 signature, then dispatches `swe.solve` on a detached background context (the agent loop takes minutes; it never blocks GitHub's ~10s delivery timeout) and posts the resulting PR + summary back as an issue comment.

Configure it with two env vars on the control plane:

```bash
HELMDECK_GITHUB_WEBHOOK_SECRET=<the secret you set on the GitHub webhook>
HELMDECK_GITHUB_WEBHOOK_RULES='[
  {
    "event":  "issues",
    "action": "labeled",
    "label":  "swe-solve",
    "pack":   "swe.solve",
    "args":   { "mode": "pull_request", "credential": "github-token", "model": "gpt-4o" }
  }
]'
```

- The webhook builds the pack input from the event: `repo_url` = the repo clone URL, `task` = the issue title + body (or the comment body for an `issue_comment` rule). Fields in `args` are merged **over** that, so `mode`/`credential`/`model` are operator-controlled.
- `mode` defaults to `pull_request` for issue events — the headline flow opens a PR.
- The result comment is posted via `github.post_comment` using the same `credential`; omit it and swe.solve still runs, just without the comment-back.
- Point the GitHub webhook at `https://<your-host>/api/v1/webhooks/github` with content-type `application/json` and the same secret, subscribed to the **Issues** event.

Guardrails carry over: the `label` filter means only explicitly-labeled issues trigger a run, and `swe.solve` still never pushes to the default branch.

## Async behavior

**Asynchronous.** `swe.solve` sets `Async: true` — the initial call returns a
SEP-1686 task envelope and the agent loop (up to the 20-minute session budget)
runs in the background. Poll `tasks/get` (or the `pack.status` / `pack.result`
trio) for completion.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/swe_solve.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/swe_solve.go).
- Companion packs: `repo.fetch`, `repo.map`, `repo.push`, `git.commit`, `github.create_pr`.
- Adapter: [`contrib/helmdeck-environment`](https://github.com/tosin2013/helmdeck/tree/main/contrib/helmdeck-environment) (Phase 1).
- Epic #233 — swe.solve. Memory-recall hook for #257 (Universal Memory Delivery Layer).
- [ADR 033 — GitHub webhook listener](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/033-github-webhook-listener.md) — the auto-trigger receiver (#233 Phase 6).
