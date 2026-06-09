---
title: artifact.get
description: Fetch an artifact's bytes by key. Returns text content as UTF-8 and binary content as base64 so the LLM can tell which decoding to apply. Symmetric counterpart to artifact.put.
keywords: [helmdeck, artifact, get, fetch, download, skill, mcp]
---

# `artifact.get`

Fetch an artifact's bytes by key. The symmetric counterpart to [`artifact.put`](./put.md) — together they make the artifact store a typed, model-tier-portable I/O surface the LLM can talk to without prose instructions.

## When to use

- **User-uploaded files.** Once the upload endpoint lands (separate work), an operator can `POST /api/v1/artifacts` (or use the management UI / CLI), and the agent picks up the file with `artifact.list` + `artifact.get`. No chat-UI changes needed for the round trip.
- **Sidecar inspection.** After `slides.narrate` emits `validation.json` or `engagement.json` sidecars, the agent can `artifact.get` them to reason over the structured data rather than re-deriving it.
- **Cross-skill handoff.** A multi-step skill that produces intermediate artifacts (research, outline, draft, final) can pass keys between stages and only `get` the actual bytes when needed — keeps the prompt budget bounded.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `artifact_key` | string | yes | — | The stable key the store returned from `artifact.put` or a producing pack. |
| `encoding` | string | no | auto | `utf-8` to force text decoding (caller asserts the bytes are readable), `base64` to force base64 encoding regardless of content_type. Empty/missing → heuristic based on `content_type`. |

### Encoding heuristic

When `encoding` is unset, the handler picks based on the artifact's stored `content_type`:

| `content_type` | Returned `encoding` |
|---|---|
| `text/*` (markdown, plain, html, csv, …) | `utf-8` |
| `application/json`, `application/yaml`, `application/xml`, `application/javascript`, `application/sql`, `application/toml` | `utf-8` |
| `*+json`, `*+xml`, `*+yaml` (RFC 6839 suffixes — e.g. `application/ld+json`, `application/svg+xml`) | `utf-8` |
| Everything else (`image/*`, `audio/*`, `video/*`, `application/octet-stream`, `application/pdf`, empty) | `base64` |

The list is deliberately conservative — unknown content types default to base64 so a misclassified binary doesn't surface as garbled text in the model's context window.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `content` | string | The artifact bytes, decoded per `encoding`. |
| `encoding` | string | `utf-8` or `base64` — tells the model how to interpret `content`. |
| `content_type` | string | The MIME type recorded in the artifact store. |
| `size` | number | Bytes (the original size, not the base64-inflated length). |
| `artifact_key` | string | Echoed back so chained calls don't need to thread it through. |
| `filename` | string | Filename portion of the key (`<namespace>/<rand>-<filename>` → `<filename>`). |
| `namespace` | string | Namespace prefix of the key. |

## Errors

| Code | When |
|---|---|
| `invalid_input` | Missing/empty `artifact_key`, malformed JSON. |
| `artifact_failed` | Store not wired, key not found, store backend error. |

## Example — read a user-uploaded markdown file

```json
{
  "tool": "helmdeck__artifact-get",
  "arguments": {
    "artifact_key": "user.upload/8a3f...c4-research-notes.md"
  }
}
```

Returns:

```json
{
  "content": "# Research notes\n\n...",
  "encoding": "utf-8",
  "content_type": "text/markdown",
  "size": 4287,
  "artifact_key": "user.upload/8a3f...c4-research-notes.md",
  "filename": "research-notes.md",
  "namespace": "user.upload"
}
```

## Example — inspect a slides.narrate validation sidecar

```json
{
  "tool": "helmdeck__artifact-get",
  "arguments": {
    "artifact_key": "slides.narrate/9b2e...01-validation.json"
  }
}
```

The agent can then parse `content` as JSON and decide whether to escalate, retry, or report. See [`av.validate`](../av/validate.md) for the shape of the validation report.
