---
title: When a pipeline fails
description: Read a failed pipeline run like a CI job — the failure_class (caller_fixable, pack_bug, transient, state_changed) tells you whose fault it is and what to do; re-run with one call once you've fixed it.
keywords: [helmdeck, pipelines, failure, error_code, failure_class, rerun, troubleshooting, ADR 044]
---

# When a pipeline fails

A failed pipeline run tells you not just *that* it failed, but **which step**, **why**, and **what to do** — the way a CI job does. This page covers how to read that, and how to recover.

## Where to look

Every run carries per-step history. Fetch it by run id:

```bash
curl -s -H "Authorization: Bearer $JWT" \
  http://localhost:3000/api/v1/pipelines/<pipeline-id>/runs/<run-id>
```

Agents get the same shape from the `helmdeck__pipeline-run-status` MCP tool, and the Management UI `/pipelines` panel renders it with a colored badge per failure class. A failed run looks like:

```json
{
  "status": "failed",
  "failure_class": "caller_fixable",
  "failure_reason": "The inputs given to this step were invalid. Fix them and re-run — the step's error message says what was wrong.",
  "steps": [
    { "step_id": "ground", "status": "succeeded", "...": "..." },
    {
      "step_id": "render",
      "status": "failed",
      "error_code": "invalid_input",
      "failure_class": "caller_fixable",
      "failure_reason": "…",
      "error": "invalid_input: …"
    }
  ]
}
```

The failing step has the typed `error_code`; the run mirrors the failing step's `failure_class` and `failure_reason` at the top level so you don't have to scan every step.

## The four failure classes

| `failure_class` | What it means | What to do |
|---|---|---|
| **`caller_fixable`** | The inputs or model handed to the step were invalid — e.g. a model the gateway can't route, or a bad `${{ }}` reference in the pipeline definition. | Fix the input (the step's `error` says what was wrong) and **re-run**. For a bad model, read [`helmdeck://models`](../integrations/SKILLS.md) for routable IDs. |
| **`pack_bug`** | A code-level error inside helmdeck: a pack handler failed in an uncategorized way, broke its own output contract, or hit an engine invariant. Not your input's fault. | **File an issue** — the `failure_reason` includes a prefilled `github.com/tosin2013/helmdeck/issues/new` link with the pack, code, and message already filled in. |
| **`transient`** | A timeout, a session that couldn't be acquired, or an artifact-store blip. | **Re-run** — it may simply succeed. |
| **`state_changed`** | The external state the step acted on moved under it (e.g. a non-fast-forward push). | Refresh the target state and **re-run**. |

The class is derived from the step's [typed error code](../reference/architecture.md) (`invalid_input` → `caller_fixable`, `handler_failed`/`internal`/`invalid_output` → `pack_bug`, `timeout`/`session_unavailable`/`artifact_failed` → `transient`, `schema_mismatch` → `state_changed`).

## Re-running

Once you've addressed the cause, re-run with the same pipeline and inputs:

```bash
curl -s -X POST -H "Authorization: Bearer $JWT" \
  http://localhost:3000/api/v1/pipelines/<pipeline-id>/runs/<run-id>/rerun
# → { "run_id": "<new-run-id>", "status": "pending" }
```

Agents use `helmdeck__pipeline-rerun`; the UI has a **Re-run** button on each run. This starts a **fresh** run — every step executes again from the top.

## Roadmap

Re-run is the first recovery action. Two more are designed in [ADR 044](../adrs/044-cicd-like-pipeline-execution.md) and land in a later release:

- **Resume from the failed step** — replay the already-succeeded steps' outputs and re-execute only from the failure, so you don't redo expensive work. (The sharp edges: an expired session from a `repo.fetch` step, and not double-firing a side effect like `email.send`.)
- **Auto-retry transient failures** — retry `transient`-class steps a bounded number of times before failing, so an environment blip doesn't surface as a hard failure.

Both build on the attribution above: you can only safely automate recovery from a failure you can first classify.
