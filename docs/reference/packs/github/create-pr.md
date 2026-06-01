---
title: github.create_pr
description: Open a pull request on a GitHub repository using a vault-stored PAT.
keywords: [helmdeck, github, github.create_pr, MCP]
---

# `github.create_pr`

`github.create_pr` opens a pull request via the GitHub REST API using a vault-stored PAT. It is the final step of [`swe.solve`](../repo/solve.md)'s `pull_request` output mode: `repo.push` lands the agent's work on a new branch, then this pack opens the PR for a human to review. As with every `github.*` pack, the token is resolved from the vault by name and never travels through the pack input or the agent-visible surface.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `repo` | `string` | yes | — | `owner/name`. |
| `head` | `string` | yes | — | Source branch. |
| `base` | `string` | yes | — | Target branch (e.g. `main`). |
| `title` | `string` | yes | — | PR title. |
| `body` | `string` | no | — | PR description (markdown). |
| `draft` | `boolean` | no | `false` | Open as a draft PR. |
| `credential` | `string` | no | `github-token` | Vault entry name. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `number` | `number` | PR number. |
| `url` | `string` | API URL. |
| `html_url` | `string` | Web URL for review. |

## Vault credentials needed

- **`github-token`** — type `api_key`, scoped to `api.github.com`. Required (PR creation is a write).

## Use it from your agent (OpenClaw chat-UI worked example)

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/github.create_pr \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "repo": "tosin2013/helmdeck",
    "head": "fix-typo",
    "base": "main",
    "title": "docs: fix typo"
  }'
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | `repo`/`head`/`base`/`title` missing. |
| `handler_failed` | GitHub REST error (auth, branch not found, PR already exists). |

## Session chaining

- **No session.** Typically the last step after a session-scoped `swe.solve` / `repo.push`.

## Async behavior

Synchronous only.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/github.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/github.go).
- Companion packs: [`swe.solve`](../repo/solve.md), [`github.get_issue`](get-issue.md), `repo.push`.
- Pipeline: `builtin.issue-to-pr`, `builtin.repo-solve-pr`.
