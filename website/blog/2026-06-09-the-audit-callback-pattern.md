---
slug: the-audit-callback-pattern
title: "The audit-callback pattern: verify-against-ground-truth as anti-hallucination middleware"
authors: [tosin]
tags: [agent-architecture, mcp, weak-models, field-report]
description: For any pack call an LLM might transform in its text response, ship a paired audit pack that reads ground truth. The architecture is the same shape as ADR 052 av-validate — applied at the chat-response layer instead of the artifact layer.
image: /img/social-card.png
date: 2026-06-09
draft: false
---

## Hook

Three architectural fixes from a single morning closed three different Tier C failure modes. A fourth — the agent producing a confidently-formatted manifest of fictitious deposits — survived all three. The structural answer isn't another fix at the producer side. It's a typed audit pack that reads ground truth after the fact, with the skill forced to surface the gap.

## Context

Helmdeck's been on a Tier C reliability arc for a week. Three patterns kept recurring:

| Pattern | Example | Fix shape |
|---|---|---|
| Skill prose ignored | "Save to artifacts" → markdown returned inline | Turn the advisory into a typed pack call ([PR #450](https://github.com/tosin2013/helmdeck/pull/450)) |
| Required arg omitted | `content.ground` rejects when `model` missing | Resolve a default at the pack layer ([PR #453](https://github.com/tosin2013/helmdeck/pull/453)) |
| Mechanism vs. persona mixed | Tier C overwhelmed by 17 KB monolithic SKILL.md | Split per OpenClaw's [canonical agent-workspace model](https://docs.openclaw.ai/concepts/agent-workspace) — [issue #457](https://github.com/tosin2013/helmdeck/issues/457) and follow-ups |

We shipped all three, plus the layered workspace refactor, and retested on `openai/gpt-oss-120b:free`. The first three fixes worked — the agent loaded the layered files correctly, applied the decision rules from AGENTS.md, picked the right publishing mode, and made one successful `blog.rewrite_for_audience` call without specifying `model`. Then it [produced a six-entry deposit manifest table for artifacts that didn't exist](./plausibility-shaped-output). The skill was in context. The pack was reachable. The model invented the calls as text.

That class of failure can't be fixed at the producer side — the producer was never called. It needs a verifier at the consumer side.

## Finding

### The shape that worked

[`artifact.verify_manifest`](https://helmdeck.dev/reference/packs/artifact/verify-manifest):

```json
{
  "tool": "helmdeck__artifact-verify-manifest",
  "arguments": {
    "expected": [
      { "artifact_key": "blog.publish/abc-mcp-adr-canonical.md" },
      { "artifact_key": "blog.publish/def-mcp-adr-linkedin.md" }
    ]
  }
}
```

Returns:

```json
{
  "verified": [
    { "artifact_key": "blog.publish/abc-mcp-adr-canonical.md",
      "filename": "mcp-adr-canonical.md",
      "namespace": "blog.publish",
      "size": 7421,
      "content_type": "text/markdown" }
  ],
  "missing": [
    { "artifact_key": "blog.publish/def-mcp-adr-linkedin.md", "reason": "artifact not found" }
  ],
  "all_present": false,
  "summary": "1 of 2 claimed artifacts verified; 1 missing"
}
```

Handler: pure passthrough to `ArtifactStore.Get` per claimed key, dedup before lookup, accumulate found vs. not-found. ~150 LOC, 100% per-function coverage on 15 tests.

The skill update is two paragraphs:

```markdown
### 4b. Verify deposit — MANDATORY, NOT ADVISORY

After producing the deposit-manifest table in §4, you MUST call
helmdeck__artifact-verify-manifest with every artifact_key from
the table. This is an anti-hallucination audit.

If `all_present: false` — DO NOT claim the deposit succeeded.
Report the missing[] entries explicitly and propose retrying the
deposit step for those specifically.
```

That's it. The audit pack is a tool name, not advisory prose — Tier C invokes it ~most of the time because it's a concrete tool call, not a "remember to" reminder. When it does invoke it, the returned `missing[]` is in the LLM's context window for the next response turn, making "all six deposited" implausible to assert.

### Why this is the same shape as ADR 052

[ADR 052 (av-output-validation-post-step)](https://helmdeck.dev/adrs/av-output-validation-post-step) made `av.validate` a default-on post-step on `slides.narrate` and `podcast.generate`. The token-savings claim was concrete: every "the video has issues" diagnostic burns ~3,000 tokens of bash output and analysis; reading the `validation` field from the run record collapses that to ~200 tokens. The architecture: turn an *implicit trust* in the artifact ("looks fine, ship it") into a *typed pack output* the agent reads in O(200) tokens.

`artifact.verify_manifest` is the same shape at a different layer:

| Layer | What's verified | Trust replaced |
|---|---|---|
| ADR 052 (artifact layer) | The artifact's structural integrity (codec, faststart, packet contiguity, RMS) | "the encoder produced a usable file" → typed `validation.checks[]` |
| `artifact.verify_manifest` (chat-response layer) | The agent's claims about what's in the store | "the agent said it deposited" → typed `verified[] / missing[]` |

Both move from implicit trust to explicit verification, both surface findings in O(200) tokens, both pin the failure mode at a place where it can't drift back.

### Phase 2 — generalize

The pattern fits a lot of helmdeck packs. Anywhere the LLM might transform a producer's output in its text response, you can pair the producer with an audit pack that re-reads authoritative state:

| Producer | Auditor (planned) | Verifies |
|---|---|---|
| `artifact.put` | `artifact.verify_manifest` *(shipped)* | Keys exist in store |
| `repo.fetch` | `repo.verify-clone` | Claimed `clone_path` exists, commit SHA matches |
| `blog.publish` | `blog.verify-published` | Published URL is reachable, content matches |
| `pack.start` (async) | `pack.verify-completed` | `job_id` is `completed`, not `working` |
| `slides.narrate` | `slides.verify-rendered` | MP4 exists + passes `av.validate` |
| `content.ground` | `content.verify-grounded` | `claims_grounded_count` matches `grounded[]` length |
| `pipeline-run` | `pipeline.verify-completion` | Claimed step outputs match run record |

Each follows the same shape: input is the agent's claim, output is `{verified[], missing[], summary}`. Handler reads authoritative state and reports the gap. Tracking in [#461](https://github.com/tosin2013/helmdeck/issues/461).

### Phase 3 — engine-level hook (deferred)

The skill-prose dependency in Phase 1 ("after the deposit step, you MUST call verify-manifest") is itself a Tier C failure surface — small chance the model ignores it. The next architectural step is an engine-level post-call hook: when a producer pack completes, the engine auto-invokes the registered auditor, attaches the result to the same response envelope, and the LLM sees both without skill-prose dependency.

That's its own ADR. Not shipping it until Phase 1 + 2 prove the pattern is generally useful. Premature middleware is a way to build a complicated system you can't justify.

## Why this matters to you

If you're building an agent on weak models, the producer-audit pair is a more durable shape than trying to make the model infallible.

Three principles that fall out of the work:

1. **Trust the producer; verify the consumer.** Packs are reliable when they're called. The unreliability is the agent's claims about what it called. Verifying the consumer side closes that gap regardless of model tier.
2. **Make the audit a typed tool, not prose.** "Remember to verify" is a Tier C failure mode. "Call `helmdeck__artifact-verify-manifest`" is a tool dispatch. The tool's existence in the catalog AND the skill's mandatory-step prose together raise the floor.
3. **The audit response has to be in context when the agent writes its final text.** If verification runs out-of-band and the result lands in a log, the agent never sees it and continues asserting compliance. The audit must be a tool call whose result the LLM reads before its next text turn.

The pattern transfers to any MCP-tooling system, not just helmdeck. The MCP spec's tool-call envelope is exactly the surface this pattern uses. If your agent produces structured claims about world state (deposits, sends, publishes, mutations), pair each producer with an auditor and require the auditor in your skill template.

## See also

- The PR that shipped Phase 1: [#462 — artifact.verify_manifest](https://github.com/tosin2013/helmdeck/pull/462)
- The companion field report this design responds to: [Plausibility-shaped output](./plausibility-shaped-output)
- The architectural cousin: [ADR 052 — av-output validation post-step](/adrs/av-output-validation-post-step)
- Phase 2 / 3 tracking: [#461](https://github.com/tosin2013/helmdeck/issues/461)
- Reference doc with worked example: [`artifact.verify_manifest`](/reference/packs/artifact/verify-manifest)
