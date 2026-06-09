---
title: artifact.verify_manifest
description: Verify that a list of artifact keys actually exist in the store. Anti-hallucination audit pack — Tier C models reliably produce confidently-formatted deposit manifests for artifacts they never deposited; this pack reads ground truth and surfaces the gap.
keywords: [helmdeck, artifact, verify, audit, manifest, hallucination, tier-c, mcp]
---

# `artifact.verify_manifest`

Verify that a list of `artifact_key` values actually exist in the store. Returns `verified[]`, `missing[]`, `all_present`, and a one-line summary. The companion audit pack for [`artifact.put`](./put.md).

## When to use

- **After any multi-step workflow that claims multiple deposits.** Skills like `tech-blog-publisher` produce a "deposit manifest" table after generating per-platform variations — Tier C models on this kind of multi-step chain reliably produce a *plausibility-shaped* manifest table (right naming convention, realistic sizes, right disclaimer citing the skill) for artifacts they never actually deposited. Calling this pack with each claimed key surfaces the gap.
- **As a CI gate on pipeline runs.** A pipeline that produces N artifacts can chain `artifact.verify_manifest` as a final post-step, failing the pipeline if `all_present: false`.
- **As an operator-driven audit** after a long agent session. Run `helmdeck__artifact-verify-manifest` with the keys the agent claimed in its chat output and see what actually landed in the store.

## Why this pack exists

Empirical motivation: 2026-06-09 trace, `tech-blog-publisher` agent on `openai/gpt-oss-120b:free`. Agent made **one** real `blog.rewrite_for_audience` call, then produced a six-entry deposit manifest table with byte sizes (7.4 KB, 2.1 KB, ...) and a disclaimer "_Artifact deposit was performed via `helmdeck__artifact_put` for each variation (mandatory per SKILL.md)._" Ground truth from `GET /api/v1/artifacts`: **zero** artifacts in the `blog.publish` namespace. Every line of the manifest was fabricated.

The architectural fixes shipped in [PR #450](https://github.com/tosin2013/helmdeck/pull/450) (typed deposit), [PR #453](https://github.com/tosin2013/helmdeck/pull/453) (default model arg), and the layered SOUL/IDENTITY/USER/AGENTS split close the *prose-instruction-skipped* failure mode. They don't close the *lying-about-tool-calls* failure mode. This pack does.

Same architectural shape as [ADR 052](/adrs/av-output-validation-post-step)'s av-validate: turn an implicit trust ("the agent said it deposited") into a typed pack call that reads ground truth and surfaces the gap, in O(200) tokens instead of the multi-thousand-token REST-poking dance the operator would otherwise do.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `expected` | array | yes | — | Either an array of objects `[{artifact_key: "..."}]` (canonical) or a flat array of strings `["...", "..."]` (Tier C friendly — both shapes accepted). Empty strings and whitespace-only entries are dropped silently; duplicates are deduped before lookup. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `verified` | array | Found entries with metadata: `artifact_key`, `filename`, `namespace`, `size`, `content_type`. |
| `missing` | array | Not-found entries: `artifact_key`, `reason` (the store's error message). |
| `all_present` | boolean | `true` iff `len(missing) == 0`. |
| `summary` | string | One-line: `"M of N claimed artifacts verified; K missing"`. |

### `verified[]` shape

```json
{
  "artifact_key": "blog.publish/8a3f...c4-post.md",
  "filename": "post.md",
  "namespace": "blog.publish",
  "size": 4287,
  "content_type": "text/markdown"
}
```

### `missing[]` shape

```json
{
  "artifact_key": "blog.publish/fabricated-linkedin.md",
  "reason": "artifact not found"
}
```

## Errors

| Code | When |
|---|---|
| `invalid_input` | Missing `expected`, empty array, all-empty entries, malformed JSON, wrong type for `expected`. |
| `artifact_failed` | Store not wired into the execution context. (Per-key fetch errors do NOT surface as pack errors — they land in `missing[]`.) |

## Example — the 2026-06-09 mcp-adr trace

Agent claimed six deposits in its text response. Audit pack reveals what actually happened:

```json
{
  "tool": "helmdeck__artifact-verify-manifest",
  "arguments": {
    "expected": [
      { "artifact_key": "blog.publish/abc...-mcp-adr-canonical.md" },
      { "artifact_key": "blog.publish/def...-mcp-adr-linkedin.md" },
      { "artifact_key": "blog.publish/ghi...-mcp-adr-devto.md" },
      { "artifact_key": "blog.publish/jkl...-mcp-adr-dzone.md" },
      { "artifact_key": "blog.publish/mno...-mcp-adr-medium.md" },
      { "artifact_key": "blog.publish/pqr...-mcp-adr-hackernoon.md" }
    ]
  }
}
```

Returns:

```json
{
  "verified": [
    { "artifact_key": "blog.publish/abc...-mcp-adr-canonical.md",
      "filename": "mcp-adr-canonical.md",
      "namespace": "blog.publish",
      "size": 7421,
      "content_type": "text/markdown" }
  ],
  "missing": [
    { "artifact_key": "blog.publish/def...-mcp-adr-linkedin.md", "reason": "artifact not found" },
    { "artifact_key": "blog.publish/ghi...-mcp-adr-devto.md", "reason": "artifact not found" },
    { "artifact_key": "blog.publish/jkl...-mcp-adr-dzone.md", "reason": "artifact not found" },
    { "artifact_key": "blog.publish/mno...-mcp-adr-medium.md", "reason": "artifact not found" },
    { "artifact_key": "blog.publish/pqr...-mcp-adr-hackernoon.md", "reason": "artifact not found" }
  ],
  "all_present": false,
  "summary": "1 of 6 claimed artifacts verified; 5 missing"
}
```

The LLM's next response turn now sees this structured result in context — and the skill instructs it to surface the gap to the operator instead of repeating the original (false) claim.

## Skill integration pattern

Any skill that produces multiple artifacts should chain `artifact.verify_manifest` as a final audit step. Example for a publishing skill's output format:

```markdown
### 4. Deposit step — call artifact.put per variation
### 4b. Verify deposit — call artifact.verify_manifest with every key

After producing the deposit manifest, you MUST call:

helmdeck__artifact-verify-manifest {
  "expected": [
    {"artifact_key": "<key 1 from manifest>"},
    {"artifact_key": "<key 2 from manifest>"},
    ...
  ]
}

Surface the `verified` count and `missing` list in your response.
If `all_present: false`, do NOT claim the deposit succeeded — report
the missing entries and propose retrying the depot step.
```

## Limitations

- Verifies **existence only** — does not compare content against an expected hash, length, or schema. A 0-byte file with the right key counts as verified. (Pair with `artifact.get` if you need content verification.)
- `missing[]` reasons are best-effort store errors. Semantic-level reasons ("wrong namespace") require the caller to interpret.
- Does not protect against the case where the agent fabricates the `expected` list itself (and never produces a manifest table at all). For that, the skill must wire the audit pack into the deterministic output template — see the skill integration pattern above.

## Empirical validation

First empirical validation from a real Tier C session: 2026-06-09, profile-aware agent on `openai/gpt-oss-120b:free`, publishing-strategist use case.

The agent ran `pipeline-run` twice (one per source URL — a GitHub repo + a docs site), polled `pack-status` until both pipelines completed, listed the resulting artifacts via `artifact.list`, then called `artifact.verify_manifest` with the actual keys:

```json
{
  "expected": [
    {"artifact_key": "blog.publish/c7d64171271e69ea-introducing-mcp-adr-analysis-server.md"},
    {"artifact_key": "blog.publish/09ee6929aefa306b-exploring-the-mcp-adr-analysis-server-repository.md"}
  ]
}
```

Returned:

```json
{
  "verified": [
    { "artifact_key": "blog.publish/c7d64171...-introducing-mcp-adr-analysis-server.md",
      "filename": "introducing-mcp-adr-analysis-server.md",
      "namespace": "blog.publish", "size": 8559, "content_type": "text/markdown; charset=utf-8" },
    { "artifact_key": "blog.publish/09ee6929...-exploring-the-mcp-adr-analysis-server-repository.md",
      "filename": "exploring-the-mcp-adr-analysis-server-repository.md",
      "namespace": "blog.publish", "size": 8539, "content_type": "text/markdown; charset=utf-8" }
  ],
  "missing": [],
  "all_present": true,
  "summary": "2 of 2 claimed artifacts verified; 0 missing"
}
```

The verification result landed in the model's context window before its final text reply. The agent's final response honestly reports `verified: 2 of 2` rather than the earlier hallucinated-six-deposits pattern from the same model. The audit-callback pattern works as designed — see [the field report](/blog/empirical-validation-per-model-profile) for the full A/B comparison vs the baseline agent without a profile.

## Phase 2 — generalize the audit-callback pattern

`artifact.verify_manifest` is Phase 1 of a broader anti-hallucination pattern tracked in [#461](https://github.com/tosin2013/helmdeck/issues/461). The same shape applies to other producer/consumer pairs:

| Producer | Audit | Verifies |
|---|---|---|
| `artifact.put` | `artifact.verify_manifest` *(this pack)* | Keys exist in store |
| `repo.fetch` | `repo.verify-clone` *(planned)* | `clone_path` exists, commit SHA matches |
| `blog.publish` | `blog.verify-published` *(planned)* | Published URL is reachable, content matches |
| `pack.start` (async) | `pack.verify-completed` *(planned)* | `job_id` is `completed`, not `working` |
| `slides.narrate` | `slides.verify-rendered` *(planned)* | MP4 artifact exists + passes `av.validate` |
| `content.ground` | `content.verify-grounded` *(planned)* | `claims_grounded_count` matches `grounded[]` length |

Each follows the same input/output shape — input = the agent's claim, output = `{verified[], missing[], summary}`.
