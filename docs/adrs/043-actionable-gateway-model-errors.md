# 43. Actionable Gateway Model/Provider Errors

**Status**: Accepted
**Date**: 2026-05-27
**Domain**: pack-engine, ai-gateway, agent-reliability

## Context

An agent called `content.ground` with `model: "minimax/abab6.5"` and got:

```
handler_failed: claim extractor dispatch: unknown provider: minimax: unknown provider: minimax
```

Three things are wrong, and together they make the agent re-guess (and hallucinate another bad model) instead of recovering:

1. **`minimax` is not a routable provider.** The gateway registers `openai`, `anthropic`, `gemini`, `deepseek`, `ollama` (keystore) + `openrouter`, `groq`, `mistral`, `fireworks` (env). MiniMax is reachable only *through* OpenRouter as `openrouter/minimax/minimax-m2.7`. OpenRouter's own catalog lists `minimax/minimax-m2.7`, so an agent that sees that and drops the `openrouter/` prefix lands on provider `minimax` → `SplitModel` → `Registry.Get("minimax")` miss → `ErrUnknownProvider` (`internal/gateway/gateway.go:237`).
2. **The failure is typed `handler_failed`.** Every LLM pack wrapped a gateway `Dispatch` error as `CodeHandlerFailed`. But [ADR 008](008-typed-error-codes-for-weak-model-reliability.md) reserves `handler_failed` for *buried exceptions / panics* — **not** caller-fixable. A bad model is the textbook caller-fixable case; the agent should be told "fix the model and retry," which is exactly what `invalid_input` signals.
3. **No discovery surface + a doubled message.** There was no `helmdeck://models` resource (unlike `helmdeck://voices` #143 and `helmdeck://image-models` #158) for the agent to pick a valid model from up front. And `PackError.Error()` printed both `Message` (which embedded the error) and `Cause` (the same error), producing the "unknown provider: minimax: unknown provider: minimax" doubling.

## Decision

Make gateway model/provider failures **caller-fixable and discoverable.**

### Reactive — classify and explain

A shared helper (`internal/packs/builtin/dispatch_error.go`) classifies every gateway chat-completion failure:

- `errors.Is(err, gateway.ErrUnknownProvider) || errors.Is(err, gateway.ErrInvalidModel)` → **`CodeInvalidInput`** with a message that points at `helmdeck://models` and shows the correct full-id form (`openrouter/minimax/minimax-m2.7`, not `minimax/…`).
- anything else → `CodeHandlerFailed` (unchanged intent).

Both fold the detail into `Message` (the field REST/MCP surface to the agent) and leave `Cause` nil, so `PackError.Error()` never doubles. Adopted at the fatal chat-LLM entry points: `content.ground` (claim extraction), `research.deep` (synthesis), `blog.publish` (prompt mode), `web.test` (planning). Best-effort dispatches that already degrade gracefully (content.ground rewrite/verify, slides.narrate metadata) are left as-is.

We deliberately **do not auto-fallback to another model** on `ErrUnknownProvider` — that masks the operator's misconfiguration. ADR 005's explicit fallback-chain *rules* remain the sanctioned path for intentional model failover.

### Proactive — `helmdeck://models`

A new MCP resource lists the chat models the gateway can route to **right now**, as full `provider/model` IDs, backed by the gateway registry's `AllModels` (`gateway.go:333`). It mirrors the `helmdeck://voices` / `helmdeck://image-models` wiring and is referenced by the reactive error message, the `helmdeck__pipeline-create` tool description, and `SKILL.md`. The agent reads it before choosing a model.

## Consequences

**Positive:**
- A bad model now returns `invalid_input` with "pick a configured model from helmdeck://models" — the agent self-corrects in one turn instead of hallucinating.
- The model catalog is discoverable the same way voices/image-models already are; no bespoke surface.
- The doubled-message bug is gone for all adopting packs.

**Negative / bounded:**
- The reactive helper keys off two gateway sentinels; a provider that returns its own "model not found" as an opaque 404 still lands as `handler_failed` until that provider surfaces a typed signal (acceptable — the common case is the unrouteable provider, which is caught).
- `helmdeck://models` reflects only *currently registered* providers, so its contents depend on which keys are configured — which is the honest answer to "what can I actually use."

## Related PRD Sections

§6.6 Capability Packs, §6.x AI Gateway routing.

Related ADRs: [ADR 005](005-openai-compatible-multi-provider-ai-gateway.md) (the `provider/model` routing + fallback rules), [ADR 008](008-typed-error-codes-for-weak-model-reliability.md) (the recoverability intent behind `invalid_input` vs `handler_failed`), [ADR 041](041-pipelines-as-first-class-resource.md) (pipeline steps set `model` and benefit most from discovery).
