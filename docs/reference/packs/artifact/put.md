---
title: artifact.put
description: Deterministic final-step deposit for skill outputs. Replaces the prose "save this to artifacts" instruction with a typed pack call that works across every model tier — including Tier C free models that silently ignore skill-level guidance.
keywords: [helmdeck, artifact, put, deposit, skill, tier-c, deterministic, mcp]
---

# `artifact.put`

Deposit a final skill output into the artifact store and return a stable `artifact_key`. Use this as the **last step** in any skill that produces content for an operator to download or chain into another pack (`blog.publish`, `email.send`, follow-on `content.ground`, etc.).

This pack exists for one reason: **Tier C free models silently treat skill prose as advisory**. A SKILL.md that says "remember to push the final markdown to Artifacts" works on Tier A/B (Claude / Gemini / GPT-5). On `openai/gpt-oss-120b:free`, `meta-llama/llama-3.3-70b-instruct:free`, and similar free OpenRouter models, the model treats that instruction as a suggestion and returns the markdown inline in the chat response instead — content trapped in the conversation log, not retrievable as an artifact.

The fix is the same shape as [ADR 052's av-validate decision](/adrs/av-output-validation-post-step): turn an advisory step into a typed pack call. A skill that ends with `helmdeck__artifact-put { kind: "blog", content: "..." }` gets deterministic deposit regardless of model tier.

> **Pipeline-run shortcut** (observed 2026-06-09): Tier C models given multi-deposit success criteria often choose `pipeline-run` (with packs that include `blog.publish` as an auto-deposit terminal step) over explicit `artifact.put` chains. This is **valid behavior** — the artifact lands in the store the same way, just via a different producer. The audit-callback pattern (`artifact.verify_manifest`) verifies the result regardless of which producer was used. See [the empirical validation field report](/blog/empirical-validation-per-model-profile) for the trace data.

## When to use

- **Final step in a content-producing skill** — e.g. `tech-blog-publisher` should always end with `artifact.put` after generating each platform variation, before any `blog.publish` call.
- **Mid-skill checkpoints** — when a skill produces multiple intermediate artifacts (research summary, outline, draft, final) that the operator might want to download independently.
- **Tier-C-safe pipelines** — when you're building a skill that needs to work on free OpenRouter models, every "save to artifacts" instruction should be replaced with an `artifact.put` call.

## When NOT to use

- **The pack you're already chaining to writes the artifact itself.** `slides.narrate`, `podcast.generate`, `blog.publish`, `image.generate`, etc. produce artifacts as a side effect — calling `artifact.put` on their output is redundant.
- **Binary content over ~10 MB.** The default `MemoryArtifactStore` in Compose deployments holds bytes in process RAM; large binaries should go through a backend-specific path (S3-backed store).

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `content` | string | yes | — | The bytes (or text) to deposit. For binary, base64-encode and set `encoding:"base64"`. |
| `kind` | string | no | `text` | Semantic hint — drives default `filename` + `content_type`. One of: `blog`, `markdown`, `transcript`, `summary`, `json`, `text`, `html`, `csv`, `binary`. Unknown kinds fall back to `text`. |
| `filename` | string | no | derived from `kind` | Display filename inside the artifact key. Path components are sanitized (`..` segments stripped). |
| `content_type` | string | no | derived from `kind` | Explicit MIME type. Overrides the kind default. |
| `encoding` | string | no | `utf-8` | Either `utf-8` (default — content is text) or `base64` (content is a base64-encoded binary blob). |
| `namespace` | string | no | `artifact.put` | Pack namespace under which the artifact is filed. Set to the producing skill's logical name (e.g. `blog.publish`) when you want the artifact to live alongside other outputs from that workflow. |

### Kind defaults

| `kind` | Default `filename` | Default `content_type` |
|---|---|---|
| `blog` / `markdown` / `summary` | `content.md` / `summary.md` | `text/markdown` |
| `transcript` | `transcript.txt` | `text/plain` |
| `json` | `content.json` | `application/json` |
| `text` (fallback) | `content.txt` | `text/plain` |
| `html` | `content.html` | `text/html` |
| `csv` | `content.csv` | `text/csv` |
| `binary` | `content.bin` | `application/octet-stream` |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `artifact_key` | string | Stable handle of the form `<namespace>/<rand>-<filename>`. Pass this to follow-on packs (`blog.publish`, `email.send`, etc.) that accept artifact keys. |
| `url` | string | Resolvable URL for the artifact (memory store: `memory://<key>`; S3-backed: signed URL). |
| `size` | number | Bytes written. |
| `content_type` | string | The actual MIME type recorded by the store. |
| `filename` | string | Filename used (after sanitization). |
| `namespace` | string | Namespace the artifact was filed under. |

## Errors

| Code | When |
|---|---|
| `invalid_input` | Missing `content`, malformed JSON, unsupported `encoding`, bad base64 payload. |
| `artifact_failed` | No artifact store wired to the engine, or the store's `Put` failed. |

## Example — Tier-C-safe blog deposit

```json
{
  "tool": "helmdeck__artifact-put",
  "arguments": {
    "kind": "blog",
    "filename": "tier-c-fragility.md",
    "content": "# Tier C models silently downgrade skill instructions...\n\n..."
  }
}
```

Returns:

```json
{
  "artifact_key": "artifact.put/8a3f...c4-tier-c-fragility.md",
  "url": "memory://artifact.put/8a3f...c4-tier-c-fragility.md",
  "size": 4287,
  "content_type": "text/markdown",
  "filename": "tier-c-fragility.md",
  "namespace": "artifact.put"
}
```

The chain-next pack (e.g. `blog.publish`) consumes the `artifact_key` instead of re-pasting the content into a JSON argument — keeping the token cost of the publishing step bounded.

## Skill pattern

Every skill in `~/.openclaw/skills/` that produces audience-facing content should end its procedure with an explicit `artifact.put` instruction:

```markdown
## Final step: deposit (mandatory, NOT advisory)

Before returning to the operator, call `helmdeck__artifact-put` with:
- `kind`: matches the output type (`blog` / `transcript` / `summary` / ...)
- `content`: the final markdown/text
- `namespace`: the publishing target (e.g. `blog.publish`) if the next pack expects to find the artifact under that namespace

Do NOT inline the final content in your chat response. The chat is for status/explanation; the artifact is for the operator to download.
```

This pattern was introduced after observing that `openai/gpt-oss-120b:free` and similar Tier C models consistently ignored the prose deposit step. See the [Tier C skill-instruction failure mode field report](../../../blog) for the underlying mechanism.
