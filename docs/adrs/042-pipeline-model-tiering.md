# 42. Per-Step Model Tiering for Pipelines

**Status**: Proposed
**Date**: 2026-05-27
**Domain**: pack-engine, api-design, ai-gateway

## Context

Pipelines ([ADR 041](041-pipelines-as-first-class-resource.md)) sequence packs, and several of those packs call an LLM through the AI Gateway ([ADR 005](005-openai-compatible-multi-provider-ai-gateway.md)). Today every built-in pipeline step that names a model hardcodes the same one:

```go
step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`)
step("ground",   "content.ground", `{"text":"…","model":"openrouter/auto","rewrite":false}`)
```

That is a missed cost/quality lever. Steps in a chain have very different reasoning demands — extracting text from a scrape or rendering markdown is mechanical; synthesizing research is not — yet there is no first-class way to route the cheap steps to a cheap model and reserve a frontier model for the step that earns it. Two practical gaps:

- **Model selection is buried in each step's opaque input JSON.** It works (the gateway routes on the `provider/model` string already), but it isn't a visible concept: an operator authoring a pipeline can't see or set "the model for this step" without knowing each pack's input schema, and the Management UI has nothing to surface or edit.
- **There is no pipeline-level default and no fallback.** Every step that wants a non-default model must repeat it, and a weak model that fails a step (schema-violating output, a typed `schema_mismatch` from [ADR 008](008-typed-error-codes-for-weak-model-reliability.md)) stalls the run with no automatic promotion to a stronger model.

A separate proposal (Context Injection Envelopes — wrapping each step's input in a `_pipeline_context` system-prompt block so a weak model "knows its place in the workflow") was considered alongside this. It is **not** adopted here; see *Consequences*. Notably, the symptom that prompted the discussion — a grounded slide deck losing its back half — turned out to be a deterministic token-cap truncation in `content.ground`'s rewrite (fixed separately), **not** weak-model amnesia. That fix removes the strongest piece of motivating evidence for envelopes.

## Decision

Promote **model selection to a first-class, optional field** on a pipeline step, with an optional pipeline-level default, resolved by the runner before dispatch.

### Schema

```jsonc
{
  "default_model": "openrouter/auto",          // optional, pipeline-level
  "steps": [
    { "id": "scrape",    "pack": "web.scrape",     "input": { … } },                 // no LLM → model ignored
    { "id": "synthesize","pack": "research.deep",  "input": { … }, "model": "anthropic/claude-sonnet-4-6" },
    { "id": "render",    "pack": "slides.render",  "input": { … } }                  // no LLM → model ignored
  ]
}
```

### Resolution (normative)

Before executing a step, the runner computes the effective model and injects it into the step input's `model` key, **overriding** any value already there:

1. step `model`, if set;
2. else pipeline `default_model`, if set;
3. else leave the step input untouched (the pack applies its own default).

Packs that make no gateway call (e.g. `slides.render`, `web.scrape`) ignore the injected `model` — injection is harmless, so the runner does not need a per-pack "uses-LLM" table. The gateway continues to own the actual `provider/model` routing; this ADR only changes *where the choice is expressed*. This keeps the existing in-input mechanism working (an explicit `model` in the input still loses to a step `model`, which is the intended override semantics) while making the choice visible, defaultable, and editable in the UI.

### Fallback chain (seam, not first slice)

A step that fails with a typed gateway error ([ADR 008](008-typed-error-codes-for-weak-model-reliability.md)) may be retried against a stronger model. The runner is the right home for this (it already owns retry/run state), but the first slice ships **selection only** — a `fallback_model` (or ordered list) is a documented follow-up so the schema can grow without a breaking change.

### Sequencing

| Release | Ships |
| :--- | :--- |
| (implementation issue) | step `model` + pipeline `default_model` field; runner override + precedence; Management UI per-step model control; validation against `helmdeck://image-models` / `helmdeck://voices` where applicable. |
| later | `fallback_model` chains on typed errors; per-step cost accounting in run history. |

## Consequences

**Positive:**
- Cost/quality tiering becomes explicit and per-step: cheap models on mechanical steps, frontier models where reasoning is load-bearing, with a one-line pipeline default.
- Reuses the AI Gateway's existing `provider/model` routing and the pipeline step schema — additive field, no new dependency, no runner rewrite.
- Creates a clean seam for fallback chains without committing to them now.

**Negative / deferred:**
- The runner gains a small pre-dispatch resolution step (well-bounded, unit-testable).
- **Context Injection Envelopes are explicitly deferred.** helmdeck packs today are either deterministic renderers with no LLM (`slides.render`, `web.scrape`, `doc.parse`) or LLM packs driven by **frozen, strict-JSON system prompts** (`content.ground`'s claim extractor, `research.deep`). Injecting "you are step 3 of 4, here's what happened before" into a prompt whose contract is to emit `{"pick": N, …}` adds no signal and risks degrading the strict extraction. Envelopes only pay off once an **open-ended generative pack** exists whose framing changes its output — which it does not yet. Recorded here as a hypothesis to revisit when such a pack lands, not built on the strength of a misdiagnosed symptom.
- Episodic-memory persistence of pipeline context (replay a failed run from where it stopped) is downstream of both envelopes and [ADR 039](039-universal-memory-delivery-layer.md) / Epic #254 (Universal Memory Delivery Layer) and is out of scope.

## Related PRD Sections

§6.6 Capability Packs, §6.x AI Gateway routing.

Related ADRs: [ADR 041](041-pipelines-as-first-class-resource.md) (the pipeline resource this extends), [ADR 005](005-openai-compatible-multi-provider-ai-gateway.md) (`provider/model` routing this rides on), [ADR 008](008-typed-error-codes-for-weak-model-reliability.md) (the typed errors a future fallback chain keys on), [ADR 039](039-universal-memory-delivery-layer.md) (the memory tier a future context-replay would persist into).
