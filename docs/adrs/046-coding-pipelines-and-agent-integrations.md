# 46. Coding Pipelines + Agent-Integration Roadmap

**Status**: Accepted (slice 1 shipped: Coding pipelines category + four beta pipelines using `swe.solve`; future agent integrations: roadmap)
**Date**: 2026-05-30
**Domain**: pack-engine, pipelines, agent-integrations

## Context

helmdeck has shipped sixteen content-output pipelines (ADR 041) covering Video, Slides, Blog, and Podcast — categorized for discoverability in the Pipelines UI. It has one coding-agent integration today: `swe.solve` ([epic #233](https://github.com/tosin2013/helmdeck/issues/233)), a pack that runs the open-source `mini-swe-agent` inside a session container and accepts `{repo_url, task, mode ∈ patch | branch | pull_request}`, emitting a unified diff / pushed branch / opened PR depending on `mode`. But `swe.solve` was exposed only as a pack, with no pipeline surfacing the common workflows on top of it (the most obvious being "read a GitHub issue → open a PR").

There's also a roadmap question this ADR answers: **which other coding agents do we add as packs next?** Open-source projects exist in this space — OpenHands, Aider, Cline, the full SWE-agent — and each has a different invocation contract. Without a written policy, every future maintainer who wonders "should we add Aider?" re-derives the analysis.

## Decision

### 1. Add a `Coding` category to the Pipelines page

UI-side category inference (the same pattern shipped in PR #339 for Video / Slides / Blog / Podcast). A pipeline's terminal-step pack determines its category:

- `swe.solve`, `repo.push`, `github.create_pr` → **Coding** (output badge: `Code`).

No SQL migration, no MCP wire-shape change. An unknown terminal pack still falls through to `Other`.

### 2. Ship four beta pipelines using `swe.solve` + `github.get_issue`

| Pipeline                          | Inputs                              | Terminal pack | What it produces            |
| --------------------------------- | ----------------------------------- | ------------- | --------------------------- |
| `builtin.issue-to-pr`             | `{repo, issue_number}`              | `swe.solve`   | `pr_url` (PR opened from issue) |
| `builtin.repo-solve-pr`           | `{repo_url, task}`                  | `swe.solve`   | `pr_url` (ad-hoc task → PR)     |
| `builtin.repo-solve-patch`        | `{repo_url, task}`                  | `swe.solve`   | unified diff (no push)          |
| `builtin.repo-solve-branch`       | `{repo_url, task}`                  | `swe.solve`   | pushed branch (no PR)           |

All four declare `(beta)` in their `Name` and `[beta]` in their description, so the UI renders a beta Badge and MCP-listing agents see the marker too. Beta acknowledges:

- Only one coding agent is wired today.
- The pipeline runner is linear; a real production "process every open issue" loop requires conditional branching, scheduled in ADR 044 slice 2.
- The GitHub-side surface is still narrow — for example there is no `github.list_open_unassigned_issues` pack to drive a batch loop, only one-shot reads and writes.

`github.get_issue` is a new lightweight pack that mirrors `github.list_issues`: it takes `{repo, issue_number}`, resolves a vault PAT via the existing `githubHandler`, and emits `{number, title, body, state, labels, html_url, user}`. Required because `github.list_issues` filters by state/labels/assignee but not by number, so a single-issue read previously required templating `http.fetch` by hand.

### 3. Pack contract for future coding-agent integrations

Any new agent added as a helmdeck pack must satisfy the existing pack contract:

- **Typed JSON input**: at minimum `{repo_url, task, model?, credential?}`. Additional fields per agent (e.g., `mode`, `max_steps`) are fine.
- **Typed JSON output**: at minimum `{success: bool, summary: string, patch: string}`. Optionally `{branch, commit, pr_url}` when the agent pushes.
- **Runs to completion in one call**: no interactive prompts, no persistent UI process the operator has to keep open.
- **Optionally inside a session container**: agents that need a workspace use `NeedsSession: true` like `swe.solve`; agents that are pure HTTP (delegating to a hosted service) run in-process.

A pipeline that switches between agents (e.g., `swe-vs-cline-bake-off`) is then just a matter of step-template variable substitution.

## Future integrations

This is the answer to "which coding agents do we add next?" — research summarized for each, with a fit rating against the pack contract above.

### Cline (https://github.com/cline/cline) — **recommended as v2**

- **License**: Apache-2.0. ✓
- **Invocation**: CLI (`npm i -g cline`), JetBrains/VS Code extensions, or **programmatic via the `@cline/sdk` NPM package**. Documents a headless mode with JSON output for CI/CD.
- **Output**: code diffs, terminal logs, structured JSON in headless mode.
- **Fit**: **Good.** The SDK + headless JSON contract maps onto the pack envelope without a TTY shim. The pack would shell out to `cline` with `{repo_url, task, model}` and parse the structured result.
- **Next step**: a proof-of-concept `cline.solve` pack in a follow-up PR. Verify the SDK's exact contract (we read the README, not the SDK source) before committing.

### OpenHands (https://github.com/All-Hands-AI/OpenHands) — **moderate, needs a spike**

- **License**: MIT (Apache-compatible for our purposes). ✓
- **Invocation**: SDK + CLI + GUI + hosted cloud. The multi-interface surface suggests the project wasn't designed around a single `{json} → {json}` contract; the deterministic programmatic interface is not well documented in the README.
- **Output**: undocumented in the surveyed materials — could be in-place file edits, could be diffs.
- **Fit**: **Moderate.** Potentially viable, but the integration cost is bounded by an unknown — a maintainer needs to spike against the SDK before we commit to a pack.
- **Next step**: a one-day spike issue. If the SDK can take `{repo, task}` and return a structured patch, escalate to a pack PR. If it can't, the alternative is the Docker-based runner (`docker run all-hands/openhands ...`) which is heavier but already isolation-friendly.

### Aider (https://github.com/Aider-AI/aider) — **does not fit**

- **License**: unspecified in surveyed materials (likely MIT-class, but unconfirmed).
- **Invocation**: CLI, **interactive by design**. Expects a running terminal and a user typing prompts.
- **Output**: in-place edits in the working tree, with `git commit` messages.
- **Fit**: **Poor.** A pack would have to fake a TTY and feed scripted input through stdin — a brittle wrapper for a tool whose value (interactive pair programming) is exactly what we'd strip away. Skip unless upstream ships a documented non-interactive mode.

### Full SWE-agent (https://github.com/SWE-agent/SWE-agent) — **not needed (mini supersedes)**

- The full SWE-agent is the heavier predecessor of `mini-swe-agent` (the one inside our `swe.solve`). Mini reaches ~65% SWE-bench in ~100 lines of Python with a smaller surface and a simpler config story.
- **Fit**: not a useful sibling to `swe.solve`. If a maintainer hits a specific case mini handles poorly, the right response is to upstream a fix or swap the inner implementation behind `swe.solve`'s existing pack contract — not to expose two near-identical packs.

## Consequences

**Positive.** Coding is a discoverable category in the UI. Issue-to-PR works in one call against any repo whose PAT is in the vault. The roadmap question has a documented answer so future maintainers don't re-survey the field.

**Negative.** The beta marker is a name-suffix convention (`" (beta)"`), not a typed wire field. If we later want filterable beta status in MCP, that's a follow-up schema bump. Acceptable trade for zero-migration shipping today.

**Out of scope.** Conditional pipeline branching (ADR 044 slice 2). A `github.list_open_unassigned_issues` batch-driver pack. PR review pipelines (the inverse — fetch a PR, comment on it). Any non-`swe.solve` coding-agent pack — those land as their own PR after the spike for each.

## See also

- ADR 041 — Pipelines as a first-class resource.
- ADR 044 — CI/CD-like pipeline execution (resume, retry, **conditional**).
- ADR 045 — Pack resource sizing via CPU profiles (a future `cline.solve` would declare `ProfileCompute`).
- Epic [#233](https://github.com/tosin2013/helmdeck/issues/233) — `swe.solve` integration.
