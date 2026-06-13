---
description: "ADR-051: Failure-Mode-Aware Dispatch for Mixed-Tier Model Deployments — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 51. Failure-Mode-Aware Dispatch for Mixed-Tier Model Deployments

**Status**: Accepted (slice 1 shipped: reasoning-token stripping, JSON parser parity, research-calibrated tier table. PRs #2–#4 remain.)
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

### PR #3 — Provider-side strict JSON / structured-output mode (shipped 2026-06-02)

Most provider APIs now support a `response_format` field constraining the model to syntactically valid JSON. Today our `gateway.ChatRequest` doesn't expose this — we rely entirely on prompt-engineering. On Tier A models that natively support strict mode, this eliminates trailing-prose and markdown-injection failure classes. On Tier C weak open-weights models running through quantized inference engines, the report describes a "constrained-decoding deadlock" failure mode where strict mode aborts the generation entirely; strict mode is contraindicated on these models.

- `ResponseFormat` field on `gateway.ChatRequest` — values `""` (current behavior), `"json_object"`, `"json_schema"`. String-based so forward-compat values land additively without touching every adapter.
- Per-provider translation: OpenAI sends `response_format.json_object` (Mistral / Groq / Fireworks / OpenRouter inherit it for free via `NewOpenAIProvider`); Gemini sets `generationConfig.responseMimeType`; Anthropic ignores it (uses tool-call structure); Ollama passes through unconstrained. Unknown ResponseFormat values fall through unconstrained at every adapter.
- `helmdeck.plan` and `helmdeck.route` opt in by passing `ResponseFormat: "json_object"` when `Budget.WantsStrictJSON` is true AND `Budget.Tier != TierC` — the tier guard is the safety belt for crash-prone quantized inference on weak open-weights.

### PR #4 — Prefix-cache routing for the catalog block (shipped 2026-06-02)

Move the catalog block from the user message into the system prompt when the model's tier entry advertises `SupportsPrefixCache`. The system prompt then stays byte-identical across every helmdeck.plan / helmdeck.route call for that model — catalog is global engine policy, not per-caller — so provider prompt-prefix caches (Anthropic 50% hit discount, Gemini 75%, DeepSeek 96.7%) reward the stable prefix. Per-call variation (defaults projection + intent + optional context) lives in the user message tail.

- Default (`SupportsPrefixCache=false`, e.g. Tier C fallback): zero diff from PR #3. Single user message carries catalog + defaults + intent. Wire-shape parity with pre-PR-4 calls.
- Cache path (`SupportsPrefixCache=true`, ~15 Tier A entries + the DeepSeek V4 Pro Tier B entry): system prompt = `planSystemPrompt + "\n\nCATALOG (helmdeck routing-guide):\n<full catalog>"`. User message = defaults + intent + optional context only. Catalog is the largest chunk by token count (~3–30KB depending on tier), so caching it saves the bulk of input-token spend on repeat calls within the provider's TTL.
- Cascade-with-cache interaction: when the ADR 050 PR #4 filter cascade fires on a `SupportsPrefixCache` model (only `openrouter/deepseek/deepseek-v4-pro` today carries both flags), the restricted catalog goes into the system prompt for that call. The filter pass keeps its own system prompt — we do not consolidate the two system prompts in this PR (deferred; the filter system prompt and planning system prompt have different role instructions). The cascade restructuring sketched in the original ADR plan is therefore narrower in scope: PR #4 ships the cache routing only.

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

## Amendment (2026-06-05, [ADR 052](052-av-output-validation-post-step.md))

The validation arc's check findings — `silence_runs`, `loudness_lufs`, `audio_video_duration`, etc. — are **NOT routed through `FailureClass`** and do not participate in the failure-mode-aware dispatch this ADR builds. The two systems target different concerns and stay deliberately separate. `FailureClass` exists to disambiguate empty-completion symptoms across hybrid models with different root causes (safety-filter redaction vs. length truncation vs. constrained-decoding deadlock vs. timeout-on-reasoning) so the dispatcher can route retry / fallback / surface decisions. Validation findings are quality observations on a successfully-produced AV artifact — the pack completed, the artifact exists, the validation describes its content. Failing the producing pack over a `silence_runs` advisory would not only defeat the soft-surface contract from [ADR 052](052-av-output-validation-post-step.md); it would surface as a `FailureClass.TransientUnknown` to the dispatcher, which would then route retries that re-encode the entire video to chase a heuristic finding — burning encode time the validation step was built to save. The bridge between the two systems is `av.validate`'s `strict:true` input: when an operator explicitly opts into fail-fast, `fail`-severity findings surface as `CodeArtifactFailed`, which IS routed via `FailureClass` (as `FailureClass.ArtifactInvalid`). The default-on integration on `slides.narrate` / `podcast.generate` does NOT expose strict mode — the integration's load-bearing payoff is the agent reading the structured `validation` field, and a routed retry would hide that field from the run record.

## See also

- ADR 050 — Retrieval-augmented tool selection. PR #1 of this ADR consumes its `BudgetFor` lookup and `Trim` record; PR #4 restructures its two-pass cascade.
- ADR 049 — Intent decomposition. `helmdeck.plan` is the primary integration target; the parser changes in PR #1 apply to its response path directly.
- ADR 047 — Catalog metadata + memory-driven routing. The catalog projection ADR 050 trims and ADR 051 PR #4 re-architects originates here.
- `internal/llmcontext/reasoning.go`, `internal/packs/builtin/json_response.go` — the new helpers shipped in PR #1.
- `internal/llmcontext/budgets.go` — the tier table with research-calibrated classifications.
- `internal/packs/builtin/webtest.go:537` — the existing `extractFirstJSONObject` balanced-brace helper the new path reuses.
