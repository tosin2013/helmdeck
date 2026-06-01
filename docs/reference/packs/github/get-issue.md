---
title: github.get_issue
description: Fetch one GitHub issue by repo and number; pairs with swe.solve for issue→PR flows.
keywords: [helmdeck, github, github.get_issue, MCP]
---

# `github.get_issue`

`github.get_issue` reads a single GitHub issue by `repo` + `issue_number`, returning the subset of the issue REST shape that downstream coding-agent packs chain into: title and body feed [`swe.solve`](../repo/solve.md)'s task field, while state/labels let conditional flows skip closed/wontfix issues. The response is read-through cached for 5 minutes, so a pipeline that retries or batches by number won't re-hit the REST API. For listing/filtering use `github.list_issues` instead.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `repo` | `string` | yes | — | `owner/name`. |
| `issue_number` | `number` | yes | — | The issue number. |
| `credential` | `string` | no | `github-token` | Vault entry name; required for private repos. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `number` | `number` | Issue number. |
| `title` | `string` | Issue title. |
| `body` | `string` | Issue body (markdown). |
| `state` | `string` | `open` / `closed`. |
| `labels` | `array` | Label objects. |
| `html_url` | `string` | Web URL. |
| `user` | `object` | Author. |

## Vault credentials needed

- **`github-token`** — type `api_key`, scoped to `api.github.com`. Optional for public repos; required for private repos.

## Use it from your agent (OpenClaw chat-UI worked example)

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/github.get_issue \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{ "repo": "tosin2013/helmdeck", "issue_number": 233 }'
```

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | `repo` or `issue_number` missing. |
| `handler_failed` | GitHub REST error (404, auth). |

## Session chaining

- **No session.** Stateless.

## Async behavior

Synchronous only.

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/github.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/github.go).
- Companion packs: [`swe.solve`](../repo/solve.md), [`github.create_pr`](create-pr.md).
- Pipeline: `builtin.issue-to-pr`.
