---
title: git.log
description: Show recent commits in a session-local clone. Default last 10. Each line is `<short-sha> <subject>`.
keywords: [helmdeck, git.log, MCP]
---

# `git.log`

Lists recent commits. Default `limit` is 10. Output format is one line per commit: `<short-sha> <subject>`. Useful for the agent to orient on what's recent before deciding what to change — and to verify after `git.commit` that the new commit landed.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `clone_path` | `string` | yes | — | Path-safety-guarded session clone. |
| `limit` | `number` | no | `10` | Max commits to return. |
| `_session_id` | `string` | yes (chained) | — | From `repo.fetch`. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `log` | `string` | Newline-separated `<short-sha> <subject>` lines. |
| `count` | `number` | Number of lines (≤ `limit`). |

## Vault credentials needed

**None.**

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste a transcript. Suggested prompt:

  "Clone helmdeck and show me the last 5 commits — anything related to the
   docs site?"

Agent should: repo.fetch → git.log limit=5 → grep / interpret commit subjects.
-->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/git.log \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{\"_session_id\":\"$SESSION\",\"clone_path\":\"$CLONE\",\"limit\":3}"
```

Captured response (truncated to 3 commits despite 10 available — the limit was honored loosely; real captured count was 10):

```json
{
  "pack": "git.log",
  "version": "v1",
  "output": {
    "count": 10,
    "log": "9c4bb08 Merge pull request #67 from tosin2013/release-v0.9.0\nb505cab chore(release): v0.9.0 — port Unreleased to dated section + plan v0.10/v0.11/v1.0-rc1\naf3c328 Merge pull request #66 from tosin2013/fix-vercel-clean-urls\na622dd4 fix(vercel): cleanUrls=true so /PACKS resolves to /PACKS.html\nd8939c8 Merge pull request #65 from tosin2013/seo-readiness-and-helmdeck-dev\n71b0952 feat(seo): full SEO polish for Google Search Console submission at helmdeck.dev\n59109b2 Merge pull request #52 from tosin2013/add-per-pack-docs\ndbf0217 Merge pull request #51 from tosin2013/fix-install-image-pull-and-tutorials\n1988a5c docs: per-pack reference framework with browser family fully written\n1ec875e Merge pull request #50 from tosin2013/plan-tie-releases-milestones"
  },
  "session_id": "022b902e-fcf4-4853-b65e-97cf9896cc81"
}
```

> Note the captured `count: 10` despite a `limit: 3` request — there's a known mismatch where the handler returns more than requested. Tracked as a `priority/P2` issue against the helmdeck repo.

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | path-safety violations, `limit` ≤ 0 |
| `session_unavailable` | session expired |
| `handler_failed` | underlying `git log` errors |

## Session chaining

`needs_session: true`. Often the first call after `repo.fetch` for orientation; also useful after `git.commit` to verify the new commit is at HEAD.

## Async behavior

Synchronous. Sub-200 ms.

## See also

- [`git.commit`](./commit.md), [`git.diff`](./diff.md).
- [`repo.fetch`](/PACKS) — context envelope already includes `commit` (HEAD) so a single `git.log` is often unnecessary.
- Source: [`internal/packs/builtin/fs_packs.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/fs_packs.go).
