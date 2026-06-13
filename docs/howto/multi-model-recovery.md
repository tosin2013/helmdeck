---
sidebar_label: Multi-model recovery matrix
description: "How to read the weekly multi-model recovery matrix from `.github/workflows/model-discovery.yml` and apply the decision rule for `openrouter/auto` defaults."
---

# Reading the multi-model recovery matrix

This page is the operator-facing companion to `.github/workflows/model-discovery.yml` (v0.26.0). It explains what each matrix row tracks, how the per-model thresholds are calibrated, how to read the weekly summary, and the decision rule for whether `openrouter/auto` should be surfaced as a helmdeck default.

The single-pinned-model regression backstop is the **weekly** workflow at `.github/workflows/model-recovery.yml` (v0.25.0 / PR #417). This page covers the **multi-model** matrix, which runs *Wednesday* (not Sunday) so the two don't compete for the same runner pool.

## What the matrix tracks

Every Wednesday at 06:00 UTC the workflow runs the same 5 typed-error recovery scenarios (`invalid_input_named_field`, `invalid_output_pack_bug`, `handler_failed_transient`, `credential_invalid_escalate`, `message_only_ambiguity`) against four free-tier models:

| Row | Tier | Threshold modifier | Purpose |
|---|---|---|---|
| `openai/gpt-oss-120b:free` | required | 0 | Regression backstop — must pass at the v0.25.0 thresholds. A failure HERE is a real workflow failure. |
| `google/gemma-4-31b-it:free` | observational | −1 | Smallest of the matrix (31B). Tests whether the typed-error contract works at the bottom of the model-size range. |
| `nvidia/nemotron-3-ultra-550b-a55b:free` | observational | 0 | Comparable in usable size to the pinned model (MoE, 55B active). Cross-vendor reliability check. |
| `openrouter/auto` | observational | −1 | The auto-router picks a different model per call within its free-tier pool. Per-attempt variance is inherently higher; this row's score is the input to the helmdeck-default decision below. |

The `required` row's failure fails the workflow. The three observational rows use `continue-on-error: true` — they publish their per-scenario scores to the run summary, but a below-threshold score doesn't block.

## Threshold modifiers — why they exist

The same scenarios are scored against models of very different sizes. A 31B model and a 120B model passing at the same threshold would either set the bar too low for the 120B (false reassurance) or too high for the 31B (writing off useful signal).

The `threshold_modifier` column shifts every scenario's pass threshold by the named amount. The base thresholds defined in `internal/reliability/scenarios.go` are calibrated to the pinned model; each observational row's modifier reflects the size/variance gap honestly.

The modifier is floor-clamped at 1 — even the most accommodating row still requires the model to emit a usable recovery at least once. Anything weaker is empty signal.

## Reading the weekly summary

The matrix posts a combined summary table to the Actions run page:

```
| Model                                  | Scenarios passed | Per-scenario                                                   | Status |
|----------------------------------------|------------------|----------------------------------------------------------------|--------|
| openai/gpt-oss-120b:free               | 5/5              | invalid_input_named_field=10/10 invalid_output_pack_bug=9/10 … | ✓      |
| google/gemma-4-31b-it:free             | 4/5              | invalid_input_named_field=8/10 invalid_output_pack_bug=7/10 …  | ⚠      |
| nvidia/nemotron-3-ultra-550b-a55b:free | 5/5              | invalid_input_named_field=9/10 invalid_output_pack_bug=10/10 … | ✓      |
| openrouter/auto                        | 3/5              | invalid_input_named_field=7/10 invalid_output_pack_bug=4/10 …  | ⚠      |
```

| Status | Meaning |
|---|---|
| ✓ | Every scenario met its (possibly modified) threshold |
| ⚠ | At least one scenario below threshold (but received responses) — normal variance for observational rows |
| ✗ | At least one scenario at 0/N — provider may be deprecated / unreachable; the alert workflow opens an issue |

The ✗ status is the only one that triggers a GitHub issue. ⚠ is informational — observational rows fluctuate week to week, and a single bad week doesn't mean the model is unusable.

## The `openrouter/auto` decision rule

Documented up-front so the rule isn't retrofitted to the data:

> **If `openrouter/auto` averages ≥7/10 across all 5 scenarios for 6 consecutive weekly runs, surface it in helmdeck's UI as the default chat model for users without a configured API key.**
>
> **If it averages <5/10 on ANY scenario, never offer it as a default — the cheap-model bet doesn't hold for it.**
>
> **Between those lines, document the gaps in this page and leave the routing choice to the operator.**

The rule sits here, not in code, because it's a product decision informed by the matrix data. After 6 consecutive weekly runs that meet the threshold, open a PR that:

1. Adds `openrouter/auto` to the model picker in `internal/llmcontext/budgets.go` with a `tier-recommended` annotation,
2. Updates the operator-facing setup docs to call it out as a starter option,
3. Cites the matrix run window that produced the evidence.

If the rule is met for a stretch and then breaks (a row drops back to ⚠), the recommendation comes OUT — the data has changed and the product decision needs to change with it.

## When the alert workflow fires

`.github/workflows/model-discovery-alert.yml` opens (or comments on) a GitHub issue when an observational row goes **fully dark** — at least one scenario scoring 0/N. The trigger is narrow on purpose: a row at 4/10 is normal variance, but a row at 0/N means the provider is gone, the model id has rotated, or the upstream is unreachable.

The issue title carries the model id; subsequent weeks comment on the existing issue rather than open duplicates. Maintainers close the issue once they swap the matrix pin or the provider returns.

## Triggering the matrix manually

```bash
gh workflow run model-discovery.yml
```

Or via the UI: Actions → "model-discovery" → "Run workflow". Optional input `attempts` lets you shorten the per-model attempt count for a faster ad-hoc run (default 10).

## What's NOT in this workflow

- **Cost tracking.** All four models are free-tier; the wall-clock budget is the only real constraint, and even a fully-throttled matrix run fits inside GitHub Actions' 6-hour job ceiling.
- **A long-term trend dashboard.** The 30-day artifact retention is enough to grep recent results. A Docusaurus page rendering per-scenario score trends across weekly runs is a follow-up if maintainers find themselves diffing artifacts often.
- **Auto-swap of the `required` row** when an observational row outperforms it. The pinned-model decision is deliberately manual — it triggers `MODEL_LAST_VERIFIED` updates and requires a maintainer to confirm the new pin is the right cheap-model proxy.

## Related ADRs

The dispatch and observability decisions behind multi-model recovery:

- [ADR-005](../adrs/005-openai-compatible-multi-provider-ai-gateway.md) — OpenAI-compatible multi-provider AI gateway
- [ADR-008](../adrs/008-typed-error-codes-for-weak-model-reliability.md) — Typed error codes for weak-model reliability
- [ADR-010](../adrs/010-keda-autoscaling-on-custom-metrics.md) — KEDA autoscaling on custom metrics
- [ADR-013](../adrs/013-opentelemetry-genai-observability.md) — OpenTelemetry GenAI observability
- [ADR-043](../adrs/043-actionable-gateway-model-errors.md) — Actionable gateway model errors
- [ADR-051](../adrs/051-failure-mode-aware-dispatch.md) — Failure-mode-aware dispatch
