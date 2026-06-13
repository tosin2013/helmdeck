---
description: "How to add a new chat-completion model to helmdeck's tier table (A/B/C) and tune the cascade trim, two-pass filter, and context budget for production behavior."
---

# How to calibrate a new model for helmdeck's tier table

Helmdeck classifies every chat completion model into one of three tiers (A, B, C) plus an implicit "unsupported" bucket. The tier determines how aggressively the cascade trims the catalog projection before the model sees it, whether the two-pass LLM filter pass fires, and what budgets `helmdeck://context-budgets` advertises to MCP clients. Tier assignments live in `internal/llmcontext/budgets.go`.

This page explains how to add a new model to that table, or revise an existing entry when its production behavior drifts.

## When you need this

- A new model just shipped on OpenRouter (or another configured provider) and your operators want to try it.
- An existing tier entry is producing failures you wouldn't expect — empty completions on a model you thought was Tier A, for example.
- You're cutting a helmdeck release and want to refresh the table per the `docs/RELEASES.md` §"Agent sync checklist" quarterly review.
- You hit the "unmapped model" fallback (`tierC` defaults) and want to be more specific.

## Methodology

### 1. Find the model on benchmark leaderboards

Tier assignments are not guesses. They're traceable to public benchmark data, listed in the trailing comment on each `budgetTable` entry. The two anchors that matter most for helmdeck workloads:

- **Berkeley Function-Calling Leaderboard (BFCL)** — `gorilla.cs.berkeley.edu/leaderboard.html`. Measures the model's ability to invoke external APIs / tools. Helmdeck's pack-dispatch pattern matches this benchmark almost directly. Look for the model's overall score plus its multi-turn (sequential) score — helmdeck.plan emits ordered chains of tool calls where step N+1's args depend on step N's output, so the multi-turn score is the better predictor.
- **Aider polyglot edit-format adherence** — `aider.chat/docs/leaderboards`. Aider measures whether models can produce structurally complex output (diff blocks) without injecting prose or markdown. The "Percent Using Correct Edit Format" score is a strong proxy for "this model can produce a `{recommendation, alternatives, gap_warning}` JSON object reliably."

Secondary anchors:

- **Artificial Analysis** — `artificialanalysis.ai/models`. Cost + speed comparisons across providers. Helps pick `InputTokens` / `OutputTokens` realistic for the model's true performance vs. its advertised maximums.
- **The provider's own documentation** — for tool-use API support, JSON-mode support, prompt-caching support. These determine the capability flags ADR 051 PR #2 introduces (`IsHybridReasoning`, `WantsStrictJSON`, `SupportsPrefixCache`).

### 2. Identify architectural quirks from provider docs

Even capable models have surprising failure modes. Read the provider's model card and changelog for:

- **Hybrid reasoning / extended thinking.** Models that emit `<think>...</think>` blocks before their response (Claude 3.7+ Sonnet thinking mode, OpenAI o3-mini, DeepSeek V4 Pro, the Moonshot Kimi K2 series). PR #1 of ADR 051 strips these automatically, but the operator-facing budget entry should carry `IsHybridReasoning: true` so PR #2's typed-error path knows to expect them. **Heuristic**: if the model's name contains `o3`, `o4`, `thinking`, `reasoner`, `kimi-k2`, or `deepseek-v4`, it is almost certainly hybrid.
- **Strict JSON / structured output mode.** OpenAI's `response_format`, Anthropic's tool-call contract, Gemini's `responseMimeType`. PR #3 of ADR 051 will route through this when `Budget.WantsStrictJSON` is true. **Heuristic**: any model from OpenAI, Anthropic, Google, Mistral, or Cohere offered through their native API. Open-weights routes via third-party providers (Together, Groq, etc.) usually do NOT support strict mode and SHOULD NOT have `WantsStrictJSON` set.
- **Prompt caching.** Provider stores the KV-cache of frequently-sent prefixes (the catalog projection is exactly this) so subsequent requests reuse the work. Gemini caches 10× cheaper than uncached; Anthropic 2×; DeepSeek 30×. PR #4 will exploit this. **Heuristic**: Gemini, Anthropic, OpenAI, DeepSeek native routes have it; almost no one else (yet) does.
- **Connection-timeout behavior.** Serverless reasoning models can spend tens of thousands of hidden tokens chain-of-thought-ing before they emit anything. Providers occasionally cap inference time at 30 minutes; if the model is still thinking when the cap hits, the client sees an empty completion. DeepSeek V4 Pro and the Kimi K2 series are documented to do this.

### 3. Run the calibration prompt suite

Run `scripts/calibrate-model.sh <model-id>`. The script dispatches three prompts via the live `/api/v1/packs/helmdeck.plan` REST endpoint:

| Prompt | What it tests |
|---|---|
| Trivial single-action ("take a screenshot of github.com") | Baseline — does this model respond at all, and how fast? |
| Multi-action (3-step pack-chain) | Structured-output reliability when the model has to decompose into 3+ tool calls |
| Paste-heavy + multi-action (the original ADR 050 motivating prompt) | The worst case — long user paste + complex output. Exposes the failure mode `helmdeck.plan` was designed around. |

For each prompt the script measures: HTTP status, wall-clock duration, whether the response parsed as a valid plan, and which cascade stages fired (visible in the response's `compaction.dropped` field).

**Always run the calibrator twice.** Free-tier reliability is noisy — one run might see a trivial intent empty-complete while the next succeeds. The script prints "Next steps" recommending you re-run to confirm reproducibility; treat single-run results as suggestive, not authoritative.

### 4. Interpret the recommendation

The script's output ends with a recommended tier. The mapping it applies:

- **Tier A** — All three prompts succeed AND no compaction fires (the model handles the full 30KB catalog directly). Latency under 20s on the trivial prompt rules out hybrid reasoning.
- **Tier B** — All three prompts succeed AND compaction trims metadata, but lexical truncation does NOT fire. The model handles ~25KB of catalog reliably.
- **Tier C** — All three prompts succeed but lexical truncation OR the LLM filter pass fired on at least one. The model needs the full ADR 050 retrieval pipeline.
- **Tier C-unstable** — Trivial works but multi-action OR paste-heavy fails. Usable for single-tool routing but not for plan decomposition.
- **Unsupported** — Even the trivial prompt fails. Do NOT add to `budgets.go`; the unmapped-model `tierC` default already covers this case if an operator passes the id anyway.

### 5. Set the capability flags

Use the script's "Manual flags suggested" output plus provider documentation to populate:

| Flag | When to set |
|---|---|
| `IsHybridReasoning: true` | Model emits `<think>` / `<reasoning>` blocks. PR #1 strip helper handles them, but PR #2 typed-errors need the flag to bucket "model timed out reasoning" failures correctly. |
| `WantsStrictJSON: true` | Model is from OpenAI, Anthropic, Google, Mistral, or Cohere native route AND supports their structured-output mode. |
| `SupportsPrefixCache: true` | Gemini, Anthropic, OpenAI, or DeepSeek native route. PR #4 will route the two-pass filter to preserve cache hits. |
| `CachedInputCostUSDPerMTok: <value>` | If `SupportsPrefixCache: true`, look up the cached-input rate on Artificial Analysis or the provider's pricing page. |

PR #1 of ADR 051 does NOT introduce these flags yet — they ship in PR #2. Until PR #2 lands, leave the flags off the entry and add them as a follow-up. The capability information is still useful in the trailing comment.

### 6. Write the entry

Add a row to `budgetTable` in `internal/llmcontext/budgets.go`. Format:

```go
{Model: "<provider/family/name>", InputTokens: <safe_ceiling>, OutputTokens: <max_structured_output>, MaxCatalogBytes: <see_below>, Tier: <TierA|TierB|TierC>, AllowsLLMFilter: <bool>},  // <source comment>
```

`MaxCatalogBytes` by tier:

- **Tier A**: 0 (compaction off — full catalog passes through)
- **Tier B**: 22000 (metadata trimmed but most of catalog survives)
- **Tier C**: 10000 (aggressive trim plus lexical pre-filter + optional LLM filter)

`AllowsLLMFilter`: `true` for Tier C (filter cascade is the safety net for these models), `false` for Tier A and B (cheaper / faster without it).

The trailing comment should name the evidence — BFCL score and Aider score for new entries; observed live behavior for revisions. Example:

```go
{Model: "openrouter/anthropic/claude-3.7-sonnet", InputTokens: 200000, OutputTokens: 8000, MaxCatalogBytes: 0, Tier: TierA},  // BFCL 73.24%, Aider 84.2%; hybrid thinking mode — emits <think>, stripped by ADR 051 helper
```

### 7. Add a test assertion

Extend `internal/llmcontext/budgets_test.go` (specifically `TestBudgetFor_PrefixMatch`) to assert that the new model id maps to the expected tier. This catches accidental prefix-table shadowing — adding a too-broad prefix that swallows previously-classified models.

### 8. Open a PR

Reference this how-to in the PR description. Include the calibration script's output as evidence. The PR should be reviewable in <10 minutes by anyone familiar with the table format.

## Quarterly review cadence

Per `docs/RELEASES.md` §"Agent sync checklist", every release cut includes a tier-table refresh check:

> Have any new models shipped to OpenRouter or any of the configured providers? If yes, scan `openrouter/v1/models` for additions, calibrate via `scripts/calibrate-model.sh`, open a docs PR adding the new tier entries with source comments.

The discovery step is intentionally manual — there's no helmdeck cron job watching provider catalogs. The tradeoff is: the maintainer who runs the release also notices when their fallback chain has new options worth investigating.

## Related

- [Models reference](/reference/models) — operator-facing tier table that the calibration methodology produces entries for
- [`scripts/calibrate-model.sh`](../../scripts/calibrate-model.sh) — the calibrator
- [`internal/llmcontext/budgets.go`](../../internal/llmcontext/budgets.go) — the tier table
- ADR 050 — Retrieval-augmented tool selection (the cascade the tier table drives)
- ADR 051 — Failure-mode-aware dispatch (PR #1 reasoning-token strip + tier additions; PR #2 introduces the capability flags this guide references)
- [ADR 053](/adrs/tier-aware-plan-prompt-variants) — tier-aware `PromptVariant` for `helmdeck.plan`; what `Budget.PromptVariant` does when a calibration entry sets it
- The Berkeley Function-Calling Leaderboard (`gorilla.cs.berkeley.edu/leaderboard.html`)
- The Aider polyglot edit-format leaderboard (`aider.chat/docs/leaderboards`)
- Artificial Analysis (`artificialanalysis.ai/models`)
