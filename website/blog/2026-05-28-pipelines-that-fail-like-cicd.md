---
slug: pipelines-that-fail-like-cicd
title: "Pipelines that fail like CI/CD: whose fault, and what to do"
authors: [tosin]
tags: [agent-architecture]
description: A failed pipeline used to give you a red badge and a flattened error string. Now each failure is attributed — a pack bug (file an issue), a bad input the agent can fix, or a transient blip worth a re-run — the way a CI job tells you which step broke and why.
image: /img/social-card.png
date: 2026-05-28
draft: false
---

When a CI job fails, you don't just learn *that* it failed — you learn which step, with what error, and usually whether it's your code, a flaky runner, or a config problem. helmdeck pipelines didn't give you that. A failed run recorded a flattened string —

```
step "render": timeout: handler deadline exceeded
```

— and a red badge. Useful, but it left the most important question unanswered: **whose fault, and what do I do now?** That question matters more when the thing reading the failure is an agent, because the wrong answer is "try the exact same thing again."

## Attribution, not just an error

Every pack failure already carries a [typed error code](/adrs/008-typed-error-codes-for-weak-model-reliability). The pipeline runner now reads that code at the point a step fails and attaches a **failure class** plus a one-line reason. There are four:

- **`caller_fixable`** — the inputs or model handed to the step were wrong (e.g. a model the gateway can't route). Fix them and re-run. The agent that built the run can usually fix this itself.
- **`pack_bug`** — a code-level error inside helmdeck: a handler failed in an uncategorized way, violated its own output contract, or hit an engine invariant. This is *not* your input's fault, so the reason hands you a prefilled GitHub issue link — pack name, error code, and message already filled in — to report it in one click.
- **`transient`** — a timeout, a session that couldn't be acquired, an artifact-store blip. Re-running may simply work.
- **`state_changed`** — the world moved under the step (a non-fast-forward push, say). Refresh and re-run.

The class and reason show up everywhere the run does: `GET /api/v1/pipelines/{id}/runs/{runId}`, the `helmdeck__pipeline-run-status` MCP tool, and the Management UI's run view — with a colored badge and, for a `pack_bug`, a **Report bug** button.

## And then: re-run

Once you know *why*, you want to act. The first action is the simplest one CI gives you: **re-run**. `POST …/runs/{runId}/rerun` (and the `helmdeck__pipeline-rerun` tool, and a button) starts a fresh run with the same pipeline and inputs. Fixed a `caller_fixable` input? Re-run. Hit a `transient` blip? Re-run.

This is deliberately a *fresh* run, not a resume — every step executes again. Resuming from the failed step (replaying the successful steps' already-persisted outputs) and auto-retrying transient failures are the next slice; they carry real edges — session lifetimes expire, and re-running a step that already sent an email or published a post can double the side effect — that deserve their own design pass ([ADR 044](/adrs/044-cicd-like-pipeline-execution) lays them out). Attribution comes first, because you can't safely automate recovery from a failure you can't classify.

## Why attribution before automation

It would have been tempting to jump straight to auto-retry — that *feels* like the CI-like feature. But auto-retry without classification is how you turn a `caller_fixable` bad-model error into an infinite loop, and how you silently paper over a `pack_bug` that should have been reported. The honest first step is the boring one: make every failure say whose fault it is and what to do. The automation is only safe on top of that.

See [ADR 044](/adrs/044-cicd-like-pipeline-execution) for the design and roadmap.
