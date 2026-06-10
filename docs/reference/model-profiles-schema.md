# Model profile YAML schema

Reference documentation for the `models/*.yaml` per-model prompting profile format. External contributors adding a new profile should read this first, then fork the closest existing file under [`models/`](https://github.com/tosin2013/helmdeck/tree/main/models) as a starting template.

Per-model profiles are the data structure behind [issue #464](https://github.com/tosin2013/helmdeck/issues/464)'s Phase 1 prompting profile library. Each file describes one (model × provider) combination — what prompting style the model expects, which sampling defaults it ships with, what failure modes it documents, and any empirical traces operators have captured. See [`docs/howto/add-free-models.md`](../howto/add-free-models.md) for the contribution workflow.

## File naming convention

`models/<provider>-<model-slug>.yaml`

| Provider | File-naming example |
|---|---|
| OpenRouter | `models/openai-gpt-oss-120b-free.yaml` (historical — provider prefix elided since OpenRouter was the original default) |
| HuggingFace Inference Providers | `models/huggingface-openai-gpt-oss-120b.yaml` |
| Together AI direct | `models/together-meta-llama-llama-3.3-70b-instruct.yaml` |
| Groq direct | `models/groq-openai-gpt-oss-120b.yaml` |
| Self-hosted | `models/custom-meta-llama-llama-3.3-70b.yaml` |

The 5 historical OpenRouter files (added before this schema doc) keep their original names — the `provider: openrouter` line inside each YAML is the explicit identifier. The convention above applies to NEW files.

## Required top-level fields

Every profile MUST have these fields:

```yaml
provider: <provider-name>      # see "Accepted provider values" below
model: <provider-specific id>  # exactly what you'd pass to the provider's API
family: <model family>          # e.g., "gpt-oss", "gemma-4", "llama-3.3", "nemotron-3"
parameters: <int>               # total params (use underscores: 117_000_000_000)
tier: <A | B | C>               # per docs/reference/models.md tier classification
context_window: <int>           # max token window per the model card
```

## Accepted `provider:` values

The schema accepts a union of these provider identifiers:

| `provider:` value | Routing layer | Notes |
|---|---|---|
| `openrouter` | OpenRouter routes (e.g., `openai/gpt-oss-120b:free`) | Historical default for Phase 1 |
| `huggingface` | HuggingFace Inference Providers at `router.huggingface.co/v1` | Per [issue #482](https://github.com/tosin2013/helmdeck/issues/482); OpenAI-compatible with provider-selection policies (`:fastest`, `:cheapest`, `:preferred`) |
| `together` | Together AI direct (`api.together.xyz`) | OpenAI-compatible direct route |
| `groq` | Groq direct (`api.groq.com`) | OpenAI-compatible direct route |
| `cerebras` | Cerebras direct (`api.cerebras.ai`) | OpenAI-compatible direct route |
| `sambanova` | SambaNova direct | OpenAI-compatible direct route |
| `custom` | Self-hosted vLLM / SGLang / TGI / Ollama / etc. | Operator-managed base URL; see `endpoint_base_url` field below |

If a contributor wants to add a provider not on this list, add it here in the same PR. Don't invent provider values without updating this reference.

## Per-provider optional fields

Depending on `provider:`, optional fields document routing specifics:

### When `provider: huggingface`

```yaml
hf_routing_policy: ":fastest"   # or ":cheapest", ":preferred"
hf_partner: cerebras             # if pinning to a specific HF partner provider
                                 # (Cerebras, Fireworks, Groq, SambaNova, Together,
                                 # Novita, Hyperbolic, DeepInfra, Nscale, HF Inference itself)
```

### When `provider: custom`

```yaml
endpoint_base_url: http://localhost:8000/v1   # the OpenAI-compatible base URL
tool_parser: qwen3_coder                       # inference-engine tool parser if relevant
                                               # (Nemotron-3 Super uses qwen3_coder per Nvidia docs)
```

### When `provider: openrouter`

No additional fields required — the OpenRouter slug (in `model:`) is self-describing. The `:free` suffix indicates the free routing variant.

## Universal optional metadata fields

These apply regardless of `provider:`:

```yaml
active_parameters: <int>         # MoE active params per token (omit for dense models)
context_window_notes: |          # multi-line block explaining provider-specific
  <free-form notes>              # routing, latency, rate-limit caveats
official_docs:                   # primary source URLs
  - https://...
```

## Prompting guidance fields

These describe how the model prefers to be prompted — independent of routing layer:

```yaml
prompting_style: <style-id>             # e.g., "role_turn_conversational",
                                         # "objectives_constraints_success_criteria"
prompting_style_notes: |                 # multi-line explanation
  <free-form>

reasoning_effort_control: <true | false | binary>
reasoning_effort_levels: [<list>]         # if reasoning_effort_control: true
                                          # e.g., [low, medium, high]
reasoning_effort_mechanism: |             # how the level is set in API calls
  <free-form>
reasoning_effort_defaults:                # per-task default levels
  <task-type>: <level>

source_priority_directive: <required | optional>
source_priority_directive_notes: |
  <free-form>

harmony_format: <true | false>            # does the model use OpenAI Harmony format
harmony_format_notes: |
  <free-form>

function_calling_format: |                # how the model emits tool calls
  <free-form>
```

## Skill-prose guidance

Two arrays of operational guidance:

```yaml
best_practices:
  - "Quote-shaped guidance string"
  - "Another one"
  # ...

anti_patterns:
  - "Failure mode description"
  - "Another one"
  # ...
```

Entries prefixed with `EMPIRICAL <YYYY-MM-DD>:` mark items added from empirical evidence rather than docs. See `models/nvidia-nemotron-3-super-120b-a12b-free.yaml` for a worked example.

## Chain-call reliability

```yaml
chain_call_reliability:
  short_chains: <high | medium | low>     # 1-2 pack calls per turn
  medium_chains: <high | medium | low>    # 3-4 pack calls per turn
  long_chains: <high | medium | low>      # 5+ pack calls per turn
  notes: |
    <free-form, can include empirical findings>
```

## Prompt template

```yaml
prompt_template: |
  <model-specific prompt structure>
```

## Empirical sections (REQUIRED, may be empty)

Even when no empirical data exists, these three arrays must be present as `[]` to declare the schema commitment:

```yaml
validated_against: []      # maintainer-curated structured findings
                           # populate when empirical work substantiates the profile
community_traces: []       # community-contributed trace excerpts
                           # populated via PRs from operators running the model
comparison_traces: []      # cross-tier OR cross-provider comparison runs
                           # populated by maintainer-captured comparison sessions
```

### `validated_against[]` entry shape

```yaml
validated_against:
  - skill: <skill-name>          # e.g., "tech-blog-publisher"
    workspace: <sanitized label> # NEVER name personal operator workspaces
    agent: <sanitized label>     # e.g., "Tier C agent on <model>, three-turn iterative workflow"
    baseline: <sanitized label>  # comparison variant
    metric: <one-line>           # what was measured
    trace_dates: [<YYYY-MM-DD>]
    finding: |                   # multi-line synthesis
      <free-form>
```

### `community_traces[]` entry shape

```yaml
community_traces:
  - contributor: <github-handle or "anonymous">
    use_case: <short-label>      # e.g., "publishing-strategist"
    session_date: <YYYY-MM-DD>
    metric_summary:
      real_pack_calls: <int>
      verify_manifest_called: <bool>
      all_present: <bool | null>
      hallucination_count: <int>
      simplification_observed: <bool | null>
    decision: <profile-works | profile-helps-partially | profile-not-enough | no-profile-needed>
    notes: |
      <one or two lines>
    pr_or_issue_url: <URL>
```

The [`scripts/helmdeck-trace`](https://github.com/tosin2013/helmdeck/blob/main/scripts/helmdeck-trace/) CLI generates this block automatically from an OpenClaw session jsonl. Use the CLI rather than hand-formatting.

### `comparison_traces[]` entry shape

```yaml
comparison_traces:
  - tier: <A | B | C>                   # for cross-tier comparisons
    model: <comparison model>            # the other model in the comparison
    session_date: <YYYY-MM-DD>
    issue_url: <URL>
    metric_summary: {...}                # same shape as community_traces metrics
    decision: <one-line classification>
    notes: |
      <strategic finding>
    pr_or_issue_url: <URL>
```

## Anonymization rules (memory-rule compliance)

Per the standing rule for helmdeck-facing docs:

- **Never** name operator-personal agent workspaces (e.g., "Press-Nemotron", "Hat") in published YAML
- **Use sanitized labels** like "Tier C agent on `<model>`, three-turn iterative workflow"
- **Workspace paths** stay out of the YAML entirely
- **Contributor GitHub handles** are fine in the `contributor:` field — that's public attribution
- **Worked examples** in companion howto docs use the Maya security-research persona (see [`docs/howto/per-model-agents/gemma-4-iterative-workflow.md`](../howto/per-model-agents/gemma-4-iterative-workflow.md))

## File size

Each profile should stay under ~20 KB total. CI validation (see [`scripts/validate-model-profiles.py`](https://github.com/tosin2013/helmdeck/blob/main/scripts/validate-model-profiles.py)) enforces a soft cap.

## Validation

The CI gate runs [`scripts/validate-model-profiles.py`](https://github.com/tosin2013/helmdeck/blob/main/scripts/validate-model-profiles.py) on every PR touching `models/*.yaml`. The script checks:

- Required top-level fields present
- `provider:` is one of the accepted union values
- `tier:` is one of `A`, `B`, `C`
- File size under soft cap
- Empirical sections (`validated_against`, `community_traces`, `comparison_traces`) present even if empty arrays

To run locally:

```bash
python3 scripts/validate-model-profiles.py
```

Exit 0 on pass; exit non-zero with a structured report on validation failure.

## Reference profiles

| File | Provider | Empirical state |
|---|---|---|
| [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml) | openrouter | Validated — 2 community_traces + 1 comparison_traces (Tier A baseline) |
| [`models/nvidia-nemotron-3-super-120b-a12b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/nvidia-nemotron-3-super-120b-a12b-free.yaml) | openrouter | Validated — 1 validated_against + 2 community_traces (v1 baseline + v2 hardened) |
| [`models/google-gemma-4-26b-a4b-it-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/google-gemma-4-26b-a4b-it-free.yaml) | openrouter | Stub — empirical sections empty (rate-limited 2026-06-10) |
| [`models/meta-llama-llama-3.3-70b-instruct-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/meta-llama-llama-3.3-70b-instruct-free.yaml) | openrouter | Stub |
| [`models/qwen-qwen3-coder-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/qwen-qwen3-coder-free.yaml) | openrouter | Stub |
| [`models/huggingface-openai-gpt-oss-120b.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/huggingface-openai-gpt-oss-120b.yaml) | huggingface | Stub — first non-OpenRouter template, community contribution invited (#482) |

## Related

- Contribution workflow: [`docs/howto/add-free-models.md`](../howto/add-free-models.md)
- Non-OpenRouter routing setup: [`docs/howto/configure-non-openrouter-providers.md`](../howto/configure-non-openrouter-providers.md)
- Per-model agent recipes: [`docs/howto/per-model-agents/`](../howto/per-model-agents/)
- Tier classification: [`docs/reference/models.md`](models.md)
- Trace extraction CLI: [`scripts/helmdeck-trace/README.md`](https://github.com/tosin2013/helmdeck/blob/main/scripts/helmdeck-trace/README.md)
- Parent issue: [#464](https://github.com/tosin2013/helmdeck/issues/464) (per-model profile library)
- HF contribution track: [#482](https://github.com/tosin2013/helmdeck/issues/482)
