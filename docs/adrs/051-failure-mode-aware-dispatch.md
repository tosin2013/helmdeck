# 51. Failure-Mode-Aware Dispatch for Mixed-Tier Model Deployments

**Status**: Accepted (slice 1 shipped: reasoning-token stripping, JSON parser parity, research-calibrated tier table. PRs #2–#5 remain.)
**Date**: 2026-06-02
**Domain**: gateway, packs, llmcontext, agent-integrations

## Context

ADR 050 shipped a four-PR retrieval-augmented selection cascade and validated it against a specific failure: the MiniMax M3 launch paste + 3-action ask, empty-completing on `openrouter/openrouter/free`. The cascade closed that gate and we tagged v0.22.0. Then a research report on LLM capabilities for the helmdeck MCP architecture landed, and a live test with `openrouter/moonshotai/kimi-k2.6` immediately exposed three classes of failure ADR 050 didn't address.

**1. The "empty completion" failure has four distinct root causes**, and ADR 050's JSON-decoder tolerance handles only one. The report identifies:

| Cause | What it looks like on the wire | What today's helmdeck sees |
| --- | --- | --- |
| Safety-filter redaction | HTTP 200, zero content, `finish_reason="content_filter"` | `"gateway returned an empty plan response"` |
| Length truncation (aggregator crash) | HTTP 200, malformed JSON, `finish_reason="length"` | `"model output is not valid JSON"` |
| Constrained-decoding deadlock | HTTP 200, JSON-shaped but unparseable | Substring fallback may or may not recover |
| Connection timeout on reasoning models | HTTP 200, zero content, no finish_reason | `"gateway returned an empty plan response"` |

The four causes have different right responses (retry vs. fallback model vs. surface error to user vs. shorten prompt), but to a pack handler today they look identical. Operators see opaque errors and can't diagnose.

**2. Hybrid reasoning models emit `<think>` / `<reasoning>` blocks before the structured payload.** Live test with `openrouter/moonshotai/kimi-k2.6`: 296s of streaming inside a `<think>` block, OpenClaw cut it off at the 5-minute timeout, the model never reached the JSON. Even if the model HAD finished, our parsers would have hit the `<think>` block first and failed. No code in helmdeck strips these tags anywhere. The affected model classes:

- Claude 3.7 Sonnet thinking mode
- OpenAI o3-mini (and the broader o-series)
- DeepSeek V4 Pro (inference-time reasoning)
- Moonshot Kimi K2.5 / K2.6 / K2.6:free

**3. The PR #4 two-pass filter cascade actively defeats provider-side prefix caching.** Gemini 2.5 Pro caches at $0.125/M (vs. $1.25/M base — 10× discount). Anthropic caches at $1.50/M (vs. $3.00/M — 2× discount). DeepSeek V4 Pro caches at $0.0145/M (vs. $0.435/M — 30× discount). At 10K-token catalog prefixes hitting every plan/route call, cache hit rate dominates economics. Our second pass sees a different catalog subset from the first, so neither pass caches cleanly.

The research report's framing is "use frontier models." Helmdeck's accessibility goal includes making weak/free models usable. We pursue both: align the tier table with empirical benchmark data the report cites, AND build the resilience layer so the cascade keeps working as model fleets diversify.

## Decision

Four-PR roadmap, each independently mergeable. PR #1 closes the immediate gaps causing user-observable failures today. PRs #2–#4 build the structural resilience the report frames as necessary for sustained mixed-tier operation.

### PR #1 — Reasoning-model output handling + JSON parser parity + research-calibrated tier table (this PR)

Three coupled fixes shipped together because Kimi-K2.6 (which the operator just added to OpenClaw fallbacks) requires all three to work:

- **`internal/llmcontext/reasoning.go`** (new) — `StripReasoningTokens(s string) string` removes `<think>…</think>`, `<reasoning>…</reasoning>`, `[REASONING]…[/REASONING]` blocks (case-insensitive, multi-line, idempotent). `HasReasoningTokens(s string) bool` is the cheap predicate for "did we strip anything" log lines.
- **`internal/packs/builtin/json_response.go`** (new) — `DecodeStructuredResponse(rawBody, packName, v)` consolidates the defensive parsing pipeline: strip reasoning tokens → trim whitespace → unwrap code fences → `json.Decoder.Decode` (tolerates trailing prose/HTML/markdown that weak models emit) → balanced-brace `extractFirstJSONObject` substring fallback (reuses the helper from `webtest.go` that already handles `}` inside JSON string literals). Returns `*packs.PackError` with `CodeHandlerFailed` and a packName-threaded Message.
- **Migrate `plan.go`, `route.go`, `content_ground.go` (parseClaimPlan) to the shared helper.** Before this PR: `plan.go` had the ADR 050 PR #4 tolerance fix, `route.go` still used strict `json.Unmarshal`, `content_ground.go` had its own substring fallback. After this PR: all three use `DecodeStructuredResponse`. Net code reduction; uniform behavior.
- **Tier-table refresh** — `internal/llmcontext/budgets.go` gains 12 new entries calibrated from the research report's BFCL / Aider / pricing data: Tier A (`o3-mini`, `gemini-2.5-pro`, `gemini-2.5-flash`, `claude-3.7-sonnet`); Tier B (`deepseek-v4-pro`, `deepseek-v3.2`, `deepseek-chat`, `grok-` prefix); Tier C (`kimi-k2`, `kimi-` prefix, `tencent/` prefix). Each entry's classification source is named in its trailing comment so future operators can trace the call.

### PR #2 — Cause-typed empty completions + capability flags on Budget

Promote helmdeck's empty-completion handling from "string error message" to "typed cause with retry hint." Pack handlers will branch on cause to decide whether to retry, fall back to a different model, or surface the error to the user.

- Add `gateway.ChatResponse.FinishReason` propagation: the dispatcher already captures finish_reason per provider for the `provider_calls` table; expose it on the returned struct.
- Typed errors: `ErrSafetyFiltered`, `ErrLengthTruncated`, `ErrConstrainedDeadlock`, `ErrLikelyTimeout`. Each carries enough detail (finish_reason, raw body length, model id) for downstream telemetry to bucket failures.
- New `Budget` fields:
  - `IsHybridReasoning bool` — model emits `<think>` blocks; hint to apply `StripReasoningTokens` even when compaction would otherwise pass-through on Tier A models.
  - `WantsStrictJSON bool` — model supports provider-side strict JSON mode (hook for PR #3).
  - `SupportsPrefixCache bool` — model's provider offers prompt caching (hook for PR #4).
  - `CachedInputCostUSDPerMTok float64` — surfaced via `helmdeck://context-budgets` for cost-aware routing.
- The Tier A entries PR #1 introduces (`o3-mini`, `claude-3.7-sonnet`, `deepseek-v4-pro` despite being Tier B) get `IsHybridReasoning: true` in PR #2; the rest stay false by default.

### PR #3 — Provider-side strict JSON / structured-output mode

Most provider APIs now support a `response_format` field constraining the model to syntactically valid JSON. Today our `gateway.ChatRequest` doesn't expose this — we rely entirely on prompt-engineering. On Tier A models that natively support strict mode, this eliminates trailing-prose and markdown-injection failure classes. On Tier C weak open-weights models running through quantized inference engines, the report describes a "constrained-decoding deadlock" failure mode where strict mode aborts the generation entirely; strict mode is contraindicated on these models.

- `ResponseFormat` field on `gateway.ChatRequest` — values `""` (current behavior), `"json_object"`, `"json_schema"`.
- Per-provider translation in `gateway.go`: OpenAI `response_format.json_object`, Anthropic tool-call contract, Gemini `responseMimeType`. Unsupported providers fall through to unconstrained dispatch with a debug warning.
- `helmdeck.plan` and `helmdeck.route` opt in by passing `ResponseFormat: "json_object"` when `Budget.WantsStrictJSON` is true.

### PR #4 — Prefix-cache-aware two-pass cascade

Restructure the PR #4 (ADR 050) cascade to preserve the catalog prefix verbatim across both LLM calls, enabling near-100% cache hit rate on the static catalog tokens.

- Today's flow: full catalog → compact (Tier C trims aggressively) → if lexical low-confidence → filter pass with TRIMMED catalog → planning pass with RESTRICTED catalog. Three different catalog projections in the two LLM calls; no cache reuse.
- New flow when `Budget.SupportsPrefixCache` is true: filter pass sees the FULL catalog (the small filter prompt makes catalog size irrelevant — the system prompt + names-only catalog listing is ~3KB regardless); planning pass sees the FULL catalog as system prompt, with the filter's id list communicated in the USER message as a "prefer these tools" hint. Both passes hit the same system-prompt cache.
- Document the contract: catalog projection is now a STABLE prefix; user-variable content (intent, filter hints, defaults projection) lives in the user message tail.

### PR #5 — Model-tier calibration tooling + maintenance docs

The tier table PR #1 introduces is a snapshot from one research synthesis on 2026-06-02. Model fleets churn fast — DeepSeek shipped v3.2-exp two weeks ago, Moonshot is on K2.6 already, Anthropic ships a minor Claude every month. Without a documented calibration process the table will be stale within a quarter and operators won't know how to extend it. PR #5 fixes that by shipping the methodology + automation we ourselves used during PR #1, so the *next* tier addition is a 5-minute task instead of an afternoon of reverse-engineering.

- **`docs/howto/calibrate-model-tiers.md`** (new) — operator-facing methodology walkthrough. When a new model appears in OpenRouter's `/v1/models` (or a new provider is added), here's how to:
  - Find the model on relevant leaderboards: Berkeley Function-Calling Leaderboard (`gorilla.cs.berkeley.edu/leaderboard.html`), Aider polyglot (`aider.chat/docs/leaderboards`), Artificial Analysis (`artificialanalysis.ai/models`) for pricing + speed.
  - Identify the model's architectural quirks from its provider docs: is it a hybrid reasoning model (emits `<think>` blocks)? Does it support strict JSON mode? Is it on a prompt-caching provider? These map directly to the `IsHybridReasoning` / `WantsStrictJSON` / `SupportsPrefixCache` flags PR #2 introduces on `Budget`.
  - Run the calibration prompt suite (see script below) and interpret the results: success rate on trivial + multi-action + paste-heavy intents, average latency per tier, observed empty-completion rate, reasoning-token presence.
  - Decide on a tier per the rules already documented in `internal/llmcontext/budgets.go` package comment.
  - Add an entry with the source-of-classification trailing comment so the next maintainer can verify your call.
- **`scripts/calibrate-model.sh`** (new) — automation helper. Takes a model id (and optional max-cost-per-call cap). Runs a fixed suite of helmdeck-specific prompts against it via the live REST `/api/v1/packs/helmdeck.plan` and `/api/v1/packs/helmdeck.route` endpoints. Measures success / failure / typed-error rate per prompt class. Emits a recommended tier + a draft `budgets.go` entry the operator pastes into a branch. Pure shell + curl + jq — no new dependencies. The prompt suite includes:
  - Trivial single-action intent (baseline latency)
  - Multi-action 3-step intent (tests structured output reliability)
  - Long-paste intent matching the original MiniMax M3 motivating prompt (tests catalog-pressure failure modes)
  - A prompt that intentionally has no good answer (tests gap-warning behavior + hallucination guard)
- **`scripts/calibrate-model.test.sh`** (new) — self-test invoking the calibrator against a known-Tier-A model (`openrouter/anthropic/claude-haiku-`) and a known-Tier-C model (`openrouter/openrouter/free`) and asserting the tier recommendation matches. Catches regressions in the calibration heuristics.
- **Reminder mechanism**: PR #5 also adds a quarterly-review note to `docs/RELEASES.md` §"Agent sync checklist" — the same checklist already covers SKILL.md stamp refresh and changelog rotation; tier-table review fits the same cadence. New release checklist line: *"Have any new models shipped to OpenRouter or any of the configured providers? If yes, scan `openrouter/v1/models` for additions, calibrate via `scripts/calibrate-model.sh`, open a docs PR adding the new tier entries with source comments."*

PR #5 deliberately ships BEFORE PRs #2–#4 because:

1. **It documents the methodology that produced PR #1's table** while it's freshest in maintainer memory.
2. **It gives the operator who hits "I want to add `gemini-3-pro`" next month a 5-minute path** instead of an afternoon of reverse-engineering.
3. **PR #2's capability flags (`IsHybridReasoning`, `WantsStrictJSON`, `SupportsPrefixCache`) need calibration data** to populate accurately for each model. The script feeds that data to whoever's writing the PR.
4. **Calibration changes can ship asynchronously** with the typed-errors / strict-JSON / prefix-cache architecture work. Decoupling the tier table from the cascade architecture lets each evolve at its own pace.

## Consequences

**Positive (PR #1 shipped today).**
- Kimi-K2.6 and other hybrid-reasoning models in the OpenClaw fallback chain become usable. The reasoning-token strip makes their output parseable; the tier table assigns them a sensible budget; the consolidated helper surfaces the same kind of recovery `plan.go` already had.
- `helmdeck.route` gets parity with `helmdeck.plan` — trailing prose and reasoning tokens no longer cause silent route failures.
- The tier table now traces to public benchmark scores (BFCL, Aider polyglot) cited per-entry, so future reviewers can verify each classification independently rather than trusting our hand-rolled 3-model live tests.
- One JSON-parse helper, one set of behaviors, one place to update when models drift. Today's three independent fallback paths converge to one.

**Positive (PRs #2–#4 designed).**
- Typed failure causes let operators distinguish "model timed out reasoning" from "provider safety filter redacted the output." Both look identical today.
- Prefix-cache architecture cuts per-call input cost 2×–30× depending on provider, which dominates the economics at 10K-token catalog prefixes.

**Negative.**
- Sharing the JSON helper changes existing error message strings. `plan.go`, `route.go`, `content_ground.go` each emitted slightly different text today; consolidating produces one text. Mitigated by preserving the original `CodeHandlerFailed` code (anything matching on code keeps working) and threading the pack name into the helper so operators see "empty plan response" vs. "empty routing response" depending on caller.
- Adding `kimi-k2.6`, `kimi-` prefix, and `tencent/` to Tier C means the two-pass filter cascade fires on calls to those models. Operators with simple prompts will see ~10s extra latency from the filter pass versus a hypothetical direct call. Acceptable tradeoff because the alternative is the 5-minute timeout we observed today.
- The research report's tier classifications are themselves derivative — some scores cited are described as "proxy" rather than direct measurements. We're trusting a synthesis as the calibration source. Mitigated by the source-of-classification comment in `budgets.go` pointing at each underlying benchmark; the right next step after PR #1 is empirical re-validation against helmdeck-specific prompts, not perpetual trust in a third-party report.

**Out of scope of this roadmap.**
- Cross-model retry on empty completion ("if A returns empty, try B"). OpenClaw does this upstream of helmdeck for chat sessions; a helmdeck-internal version is its own separate ADR if operators want non-OpenClaw clients to get the same behavior.
- Embeddings-based semantic catalog retrieval (replacing the lexical pre-filter in PR #3 of ADR 050). Would add an inference dependency; reconsider when lexical recall is measurably failing for operators in the field.
- Streaming responses in the `helmdeck.plan` output. Would help perceived latency on hybrid-reasoning models but introduces a new round of pack-handler complexity. Not on this roadmap.

## See also

- ADR 050 — Retrieval-augmented tool selection. PR #1 of this ADR consumes its `BudgetFor` lookup and `Trim` record; PR #4 restructures its two-pass cascade.
- ADR 049 — Intent decomposition. `helmdeck.plan` is the primary integration target; the parser changes in PR #1 apply to its response path directly.
- ADR 047 — Catalog metadata + memory-driven routing. The catalog projection ADR 050 trims and ADR 051 PR #4 re-architects originates here.
- `internal/llmcontext/reasoning.go`, `internal/packs/builtin/json_response.go` — the new helpers shipped in PR #1.
- `internal/llmcontext/budgets.go` — the tier table with research-calibrated classifications.
- `internal/packs/builtin/webtest.go:537` — the existing `extractFirstJSONObject` balanced-brace helper the new path reuses.
