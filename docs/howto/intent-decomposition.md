# How agents decompose multi-intent prompts with `helmdeck.plan`

Helmdeck's catalog answers *"which one tool fits this intent?"* well — `helmdeck://routing-guide` and `helmdeck.route` (ADR 047) make that explicit. But real conversational prompts often span several intents in one message. ADR 049 PR #1 introduced **`helmdeck.plan`** for that case: it decomposes a multi-intent prompt into an ordered sequence of tool/pipeline calls, plus a re-written natural-language step list an agent can execute line-by-line.

This page covers when to call it, the wire shape, the pipeline-aware behavior, and the self-learning story.

## When to call `helmdeck.plan`

Call it FIRST when the user's request spans more than one action. Signals:

- The prompt contains coordinating verbs joining tool-shaped clauses — *"remember this AND draft a blog AND illustrate it"*.
- The prompt references a concrete payload AND a downstream artifact — *"here's a launch announcement; turn it into a blog post and persist the source"*.
- The prompt names an outcome (a blog, a slide deck, a video) AND raw inputs (a brief, a URL, a paste) that don't trivially feed one pack.

For single-intent prompts (*"summarize this PDF"*, *"render these slides"*), prefer `helmdeck.route` — it's tighter and cheaper.

## Input shape

```json
{
  "user_intent": "remember this MiniMax M3 launch, write a blog about it, then make an illustration",
  "model": "openrouter/openrouter/free",
  "context": { "optional": "any extra structured context you want the planner to see" },
  "max_tokens": 3000
}
```

- **`user_intent`** (required) — the user's request in their own words. The model treats this as the intent to decompose.
- **`model`** (required) — provider/model id. See `helmdeck://models`.
- **`context`** (optional) — any structured payload that informs the plan but isn't part of the intent string itself (e.g. a paste the user provided alongside the prompt). The planner surfaces it to the model as `OPTIONAL CONTEXT`.
- **`max_tokens`** (optional, default 3000) — caps the model's response. Wide enough for 5-7 steps with rationales.

## Output shape

```json
{
  "steps": [
    {
      "order": 1,
      "tool": "helmdeck.memory_store",
      "args": {"key": "launches/minimax-m3", "value": "...", "category": "launches"},
      "rationale": "persist the source so future asks can recall it"
    },
    {
      "order": 2,
      "tool": "helmdeck__pipeline-run",
      "args": {"id": "builtin.brief-rewrite-blog", "inputs": {"brief": "...", "audience": "AI engineers"}},
      "rationale": "pipeline supersedes the manual rewrite-ground-publish chain"
    },
    {
      "order": 3,
      "tool": "helmdeck.image_generate",
      "args": {"prompt": "An illustration of the launch announcement"},
      "rationale": "user explicitly asked for an illustration alongside the blog"
    }
  ],
  "rewritten_prompt": "Plan for: remember this launch...\nStep 1: call helmdeck.memory_store with args ... — persist the source...\nStep 2: ...\nExecute the steps in order. Stop and surface any tool error to the user before proceeding to the next step.",
  "complexity": "pack-chain",
  "reasoning": "Three-action intent. Step 2 prefers the brief-rewrite-blog pipeline over chaining its three constituent packs (per pipeline.metadata.supersedes).",
  "model": "openrouter/openrouter/free"
}
```

- **`steps[]`** — 1-indexed, strictly ordered. Each `tool` is either a pack name verbatim from `helmdeck://routing-guide` OR the literal string `"helmdeck__pipeline-run"` with `args.id` naming a pipeline.
- **`rewritten_prompt`** — the same plan rendered as a natural-language step list. The handler **derives** this from `steps` post-LLM so the two surfaces can't drift. Use it when your model struggles to consume `steps[]` structurally — feed it back as the next system-prompt-style instruction.
- **`complexity`** — one of:
  - `single-action` — one step. The planner is overkill; consider `helmdeck.route` next time.
  - `pipeline-direct` — one step calling a pipeline that covers the whole intent.
  - `pack-chain` — two or more steps. May include a `pipeline-run` as one of those steps; the chain is what makes it `pack-chain`.
- **`reasoning`** — 1-3 sentences the model wrote. Cites pipeline supersedes links when used.

## Pipeline-aware decomposition

`helmdeck.plan` sees both packs and pipelines through the same catalog projection `helmdeck.route` uses. The system prompt teaches three rules:

1. **Pipeline wins when one fits.** If a pipeline's `metadata.accepts` covers the source kind AND `metadata.produces` covers the target format, the plan emits ONE step calling `helmdeck__pipeline-run` rather than re-decomposing the pipeline's internal chain. Pipelines exist *because* maintainers proved a particular sequence works — re-deriving it pack-by-pack is wasted effort and a regression-prone surface.
2. **Honor `supersedes`.** A pipeline whose `metadata.supersedes` lists packs the user mentioned by name wins automatically. Example: a user asking *"rewrite this brief, ground it, then publish it as a blog"* gets a single `pipeline-run id=builtin.brief-rewrite-blog` step — because that pipeline's `supersedes` lists the three packs by name.
3. **Decompose only when no pipeline fits.** Fall back to a pack-by-pack sequence when no pipeline matches by `accepts` / `produces` / `intent_keywords`. The `complexity` field distinguishes the three shapes so downstream consumers can route accordingly.

## Hallucination + recursion guards

The handler enforces two guards post-LLM, replacing offending steps with `"tool": "unknown"` and a populated `rationale`:

- **Unknown tool id.** Any `tool` that doesn't resolve to a registered pack or pipeline gets demoted. The agent must surface `unknown` steps to the user (or to `helmdeck.route`'s gap-warning flow) — do NOT dispatch them.
- **Recursive `helmdeck.plan` calls.** A plan step CAN'T be `helmdeck.plan` or `helmdeck__plan`. Hardcoded rejection.

Partial demotion is fine: a 4-step plan can have 3 valid steps and one `unknown`. The valid steps still execute; the agent decides how to handle the gap.

## Self-learning — `plan_history`

Every successful plan writes one compact audit row to the caller's bare namespace under category `plan_history`:

```json
{
  "intent_sha": "a1b2c3d4e5f60718",
  "complexity": "pack-chain",
  "steps": [
    {"order": 1, "tool": "helmdeck.memory_store", "args_sha": "9f8e7d6c5b4a3210"},
    {"order": 2, "tool": "helmdeck__pipeline-run", "args_sha": "13579bdf2468ace0"},
    {"order": 3, "tool": "helmdeck.image_generate", "args_sha": "fedcba0987654321"}
  ],
  "outcome": "ok",
  "at_unix": 1717209600,
  "duration_ms": 1240,
  "model": "openrouter/openrouter/free"
}
```

Audit rows persist the tool sequence + a hash of each step's args, **not** the full rewritten prompt or the rationales. This keeps rows small and keeps user data out of the audit surface. The default 30-day TTL applies; `helmdeck.memory_forget` with `scope: all` clears them on demand.

ADR 049 PR #2 will project these rows into `helmdeck://my-plans` so future plan calls can mine past decompositions as priors — same self-learning loop `helmdeck://my-defaults` provides for routing.

## When the agent should fall back to `rewritten_prompt`

If your runtime can iterate `steps[]` and dispatch each tool, use the structured form. If your model produces brittle tool-calls when given a long JSON spec, feed the `rewritten_prompt` string back as the next user message — it's a single natural-language instruction the model can execute line-by-line without re-reasoning about which tool to pick.

Both surfaces encode the same plan; they can't drift because the handler derives `rewritten_prompt` from `steps` after the LLM responds.

## Related

- `helmdeck.route` — pick ONE tool for a single-intent prompt (ADR 047 PR #3).
- `helmdeck://my-defaults` — learned per-caller defaults for `suggested_inputs` pre-fills.
- `helmdeck://routing-guide` — the catalog projection both `route` and `plan` build on.
- ADR 049 — the three-PR roadmap (PR #1 ships the pack; PR #2 surfaces `my-plans`; PR #3 adds frontier-gap detection).
