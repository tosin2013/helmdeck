---
slug: pipeline-output-shape-vs-publication-target
title: When the pipeline is right but the output shape is wrong
authors: [tosin]
tags: [friction, agent-architecture]
description: A built-in helmdeck pipeline produced clean blog articles for an external agent — but the output shape was internal-docs ([1] citations, no CTAs), not blog. Notes on what the planner should compose instead.
image: /img/social-card.png
date: 2026-06-02
draft: false
---

## Hook

An external agent picked the right helmdeck pipeline for a "promote this project" intent — `builtin.scrape-rewrite-blog` — and got back two high-quality articles. Neither had a single promotional link, and both were strewn with `[1]` citations. The pipeline did exactly what it was built for. The agent had the wrong tool selected for the wrong job.

## Context

The work that surfaced this: a user asked an external agent driving helmdeck (via the OpenClaw bridge) to "scrape this project's docs page and write a blog promoting it." The agent reached for `builtin.scrape-rewrite-blog` — a four-step pipeline that scrapes a URL to markdown, rewrites it as an original article for a stated audience, runs `content.ground` for fact-checking citations, and saves the result as a blog artifact. Two articles came out, both publishable on dev.to and Medium with light edits.

Two things were off:

1. **No promotional links anywhere.** The user's intent was *promote the project*, but `blog.rewrite_for_audience` is a ghostwriter, not a marketer — it has no `cta_links` parameter. It produced narrative; it never lands a URL.
2. **`[1]`, `[5]`, `[source]` markers throughout the prose.** `content.ground` is a fact-checker — its contract is verifiability, not narrative flow. Visible citations are correct output for internal docs and research notes. On dev.to they read as stiff and academic.

Both issues are the same shape: the pipeline's *contract* was right for its job, but its *output shape* didn't match the *publication target* the user actually wanted.

## Finding

The external agent's self-diagnosis nailed the fix: don't ask one pipeline to do everything; let `helmdeck.plan` decompose the intent into pipeline-run + post-processing steps.

| What ran | What should have run |
|---|---|
| `scrape-rewrite-blog` (4 steps; ends with `content.ground` + `blog.publish`) | `helmdeck.plan` → `scrape-rewrite-blog` → strip citations → append CTA → `blog.publish` |

That's not a knock on the pipeline. Built-ins are tight on purpose — they encode one contract end-to-end, which is what makes them reusable. The composition layer for cross-pipeline intents lives in `helmdeck.plan` (ADR 049), the intent-decomposer that turns "promote this project" into an ordered tool call sequence.

This PR closes the simpler half of the gap directly: a new pack `blog.append_cta` that's no-op when no promotional inputs are passed, LLM-backed (so the closing section matches the article's voice) when at least one of `project_url`, `github_url`, or `cta_source_url` is set. The four `*-rewrite-blog` pipelines now slot it in between `content.ground` and `blog.publish` — opt-in, zero cost when not asked for.

```text
# scrape-rewrite-blog before this PR
scrape → rewrite → ground → publish

# After
scrape → rewrite → ground → cta (no-op unless promotional inputs set) → publish
```

The pipeline descriptions in `internal/pipelines/seed.go` also gained an explicit warning that `content.ground` injects inline `[1]` citations — strip them in post-processing for conversational publication targets (dev.to / Medium / company blog). The honest-description-vs-mechanism principle has been a project memory for months; this is one more place it lands.

Citation stripping itself stays out of scope here. It deserves its own pack (`blog.strip_citations` or a `presentation_mode` parameter on `content.ground`) because the design question is sharper than "remove `[N]` markers" — sometimes you want footnotes, sometimes you want them inline as hyperlinks, sometimes you want them gone but the references list to stay. That's a separate decision worth surfacing properly.

## Why this matters to you

If you're driving helmdeck (or any agent platform with a catalog of multi-step tools) from an LLM:

- **Pipelines are tight contracts**, on purpose. Their output shape encodes the use case they were calibrated against. When the user's *publication target* doesn't match that use case, you'll get the wrong shape even when the pipeline ran perfectly.
- **The composition layer is where you fix it.** Don't ask a pipeline to take on a responsibility it wasn't designed for. Decompose the intent, run the pipeline for what it's good at, then post-process. `helmdeck.plan` is the canonical bridge in this codebase; in other architectures it's whatever does multi-step orchestration.
- **Pack descriptions earn their keep when they warn about output shape.** The user reading `builtin.scrape-rewrite-blog` should learn *both* what the pipeline does *and* what the output looks like — not discover after the fact that conversational targets need cleanup.

The pattern shows up beyond blogs: any tool optimized for verifiability (audit logs, contract diffs, ML feature stores) produces output that reads as machine-aimed by default. If you want it human-aimed, the planner needs to know.

## See also

- [PR — `blog.append_cta` + pipeline wiring + description tightening](https://github.com/tosin2013/helmdeck/pull/TBD)
- [ADR 049 — `helmdeck.plan` intent decomposer](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/049-helmdeck-plan-intent-decomposer.md)
- Project memory: pipeline descriptions must match the mechanism — the predecessor of this gap, captured the same theme months ago.
