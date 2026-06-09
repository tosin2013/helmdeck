---
title: artifact.list
description: List artifacts in the store, optionally filtered by namespace or filename substring. Returns metadata only — call artifact.get to fetch bytes.
keywords: [helmdeck, artifact, list, browse, discovery, skill, mcp]
---

# `artifact.list`

List artifacts in the store, with optional namespace and filename filtering. The agent's introspection capability for the artifact surface — pair it with [`artifact.get`](./get.md) to read bytes once you've found what you want.

## When to use

- **Discover operator uploads.** When the session may include files the operator dropped via REST upload / CLI / management UI, the agent calls `artifact.list` (no filter) at the start of work to see what's available.
- **Enumerate multi-pack outputs.** After a long-running skill produces multiple artifacts (draft, summary, citations, final), `artifact.list` with the matching namespace yields a manifest the agent can hand back to the operator.
- **Find a specific output without remembering the key.** `filename: "validation"` filters by case-insensitive substring — useful when an earlier turn produced a `validation.json` sidecar and the current turn needs to reference it without the key being in context.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `namespace` | string | no | (list all) | When set, restricts to artifacts whose key starts with `<namespace>/`. Common values: `user.upload`, `blog.publish`, `slides.narrate`, `podcast.generate`, `av.validate`. |
| `filename` | string | no | — | Case-insensitive substring match against the filename portion of the key. Not a glob. |
| `limit` | number | no | 100 | Max entries returned. `0` (or missing) uses the default 100. Negative values also reset to 100 (unbounded is not supported — see Limitations). |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `artifacts` | array | Newest-first metadata records. See shape below. |
| `count` | number | Length of the returned `artifacts` array. |
| `truncated` | boolean | `true` when the matching set exceeded `limit` and was clipped. |

### `artifacts[]` shape

```json
{
  "artifact_key": "blog.publish/8a3f...c4-post.md",
  "filename": "post.md",
  "namespace": "blog.publish",
  "content_type": "text/markdown",
  "size": 4287,
  "created_at": "2026-06-09T14:23:01Z",
  "url": "memory://blog.publish/8a3f...c4-post.md"
}
```

`created_at` is the store-recorded creation time. Backends that don't track timestamps (rare) return a zero time, which sorts last under the newest-first order.

## Errors

| Code | When |
|---|---|
| `invalid_input` | Malformed JSON input. |
| `artifact_failed` | Store not wired, backend list call failed. |

## Example — find what the operator uploaded

```json
{
  "tool": "helmdeck__artifact-list",
  "arguments": {
    "namespace": "user.upload"
  }
}
```

Returns:

```json
{
  "artifacts": [
    {
      "artifact_key": "user.upload/8a3f...c4-research-notes.md",
      "filename": "research-notes.md",
      "namespace": "user.upload",
      "content_type": "text/markdown",
      "size": 4287,
      "created_at": "2026-06-09T14:23:01Z",
      "url": "memory://user.upload/8a3f...c4-research-notes.md"
    }
  ],
  "count": 1,
  "truncated": false
}
```

The agent now has the key needed for [`artifact.get`](./get.md) — no operator copy-paste of file contents required.

## Limitations

- **Metadata only.** This pack does not return content. Use `artifact.get` once you've identified the key.
- **Filename filter is a substring, not a glob.** `*.md` is interpreted literally; use `.md` to match a suffix.
- **Sort is best-effort newest-first.** The in-memory store sets `CreatedAt` on `Put`; if a future backend (e.g. S3-with-prefix-listing) doesn't track timestamps, the order may be arbitrary.
- **No pagination cursor.** When `truncated: true` you've hit the limit — there's no opaque cursor to fetch the next page. Refine with a narrower `namespace` / `filename` filter or pass a larger `limit` when you genuinely need more rows. (A real cursor lands when the store backend supports it.)
