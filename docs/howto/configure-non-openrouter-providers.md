---
description: "Walkthrough for running helmdeck-driven OpenClaw agents against HuggingFace, Together, Groq, Cerebras, SambaNova, or self-hosted vLLM/SGLang/TGI providers."
---

# Configure a non-OpenRouter LLM provider for helmdeck

A walkthrough for operators wanting to run helmdeck-driven OpenClaw agents against providers OTHER than OpenRouter — HuggingFace Inference Providers, Together AI direct, Groq, Cerebras, SambaNova, or self-hosted vLLM / SGLang / TGI.

This unblocks the [#482](https://github.com/tosin2013/helmdeck/issues/482) community contribution track: submit model profiles + traces for non-OpenRouter routes. See [`docs/reference/model-profiles-schema.md`](../reference/model-profiles-schema.md) for the YAML schema and [`docs/howto/add-free-models.md`](add-free-models.md) for the contribution workflow.

## Why bypass OpenRouter

The OpenRouter `:free` pool is congested. Three of five Phase 1 models were unreachable today (2026-06-10) purely because of upstream rate limits:

- `google/gemma-4-26b-a4b-it:free` — 429 from Google AI Studio (per error metadata); affects all `google/*:free` slugs simultaneously
- `meta-llama/llama-3.3-70b-instruct:free` — 429; error metadata cited "Venice" provider
- `qwen/qwen3-coder:free` — 429; same "Venice" provider attribution

OpenRouter's own [rate-limit docs](https://openrouter.zendesk.com/hc/en-us/articles/39501163636379) explicitly recommend cross-slug fallback when one free model returns 429. Operators running sustained empirical work hit the wall fast. The alternative routing layers below offer:

- **Transparent provider selection** — HF Inference Providers lets you pick `:fastest` / `:cheapest` / `:preferred` and see which upstream is serving each request
- **Independent rate-limit pools** — Together AI / Groq / Cerebras direct each have their own quotas; bypass shared-pool congestion
- **Operator-controlled SLA** — self-hosted vLLM / SGLang / TGI runs on your infrastructure; you set the limits

Empirical evidence for these patterns is in PRs [#481](https://github.com/tosin2013/helmdeck/pull/481) + [#484](https://github.com/tosin2013/helmdeck/pull/484) (Nemotron baseline-vs-hardened A/B that demonstrated per-use-case AGENTS.md hardening as the workflow-shape lever — provider doesn't change that lesson).

## HuggingFace Inference Providers (primary path)

[HF Inference Providers](https://huggingface.co/docs/inference-providers/index) at `router.huggingface.co/v1` is the OpenAI-compatible router that fronts multiple partner providers (Cerebras, Fireworks, Groq, SambaNova, Together, Novita, Hyperbolic, DeepInfra, Nscale, HF Inference itself). Same `chat/completions` API surface OpenRouter uses; transparent provider-selection policies.

### 1. Get an HF API key

1. Sign up at https://huggingface.co (free tier exists; PRO and Team plans have higher quotas)
2. Generate an API key at https://huggingface.co/settings/tokens
3. Save the token — you'll paste it into OpenClaw

### 2. Configure OpenClaw with the HF endpoint

OpenClaw needs the base URL + API key for the HF router. In OpenClaw's UI:

1. Open the **Models** panel → **Add Provider**
2. Set:
   - **Provider type**: `openai-compatible`
   - **Base URL**: `https://router.huggingface.co/v1`
   - **API Key**: the HF token from step 1
3. Save the profile

For CLI-based OpenClaw setup, edit `~/.openclaw/openclaw.json`:

```json
{
  "agents": {
    "defaults": {
      "models": {
        "huggingface/openai/gpt-oss-120b": {
          "alias": "HF gpt-oss-120b",
          "baseUrl": "https://router.huggingface.co/v1",
          "apiKey": "<your-HF-token>"
        }
      }
    }
  }
}
```

### 3. Provider-selection policies

HF Inference Providers exposes three routing policies. Choose by appending the policy to the model ID (or set in OpenClaw's per-agent config):

| Policy | Behavior | When to use |
|---|---|---|
| `:fastest` | Routes to the lowest-latency partner currently available | Interactive sessions, dev/test loops |
| `:cheapest` | Routes to the lowest-cost partner | Batch jobs, long-running pipelines |
| `:preferred` | Uses your account's preferred partner (set in HF settings) | Sustained work with quota planning |

Pinning a specific partner is also supported — use the model ID format `<repo-id>:<partner>` (e.g., `openai/gpt-oss-120b:cerebras`).

### 4. Free-tier credit ceiling

The HF Inference Providers free tier is small (writeups quote **~$0.10/month** in inference credits — call it ~25k tokens of gpt-oss-120b). PRO and Team plans have substantially larger quotas. For sustained empirical work, **PRO is the realistic minimum**.

### 5. Worked example — switch the trace-test agent to HF

The [trace-test agent](https://github.com/tosin2013/helmdeck/blob/main/scripts/helmdeck-trace/README.md#validating-with-a-test-agent) pattern works identically on HF. To run the same three-turn iterative blog-drafter prompt that PR #480 validated on the OpenRouter route:

1. Set the trace-test agent's model to `openai/gpt-oss-120b` via the HF provider (configured above)
2. Run the standard `BLOG DRAFT` trigger prompt (same one the OpenRouter route used)
3. Walk the three turns
4. Run `helmdeck-trace extract --session ~/.openclaw/agents/trace-test/sessions/<id>.jsonl --use-case blog-drafter-hf-test --contributor <your-handle> --decision profile-works --url 'https://github.com/tosin2013/helmdeck/issues/482'`
5. Open a PR adding the `community_traces[]` entry to [`models/huggingface-openai-gpt-oss-120b.yaml`](../../models/huggingface-openai-gpt-oss-120b.yaml)

That's a complete cross-provider A/B contribution: same model, same prompt, different routing layer. The empirical data tells operators whether HF route reliability matches OpenRouter for gpt-oss.

## Together AI / Groq / Cerebras / SambaNova direct

All four expose OpenAI-compatible chat-completions endpoints. The OpenClaw setup is identical to the HF pattern above with different base URLs + API keys.

| Provider | Base URL | Auth docs |
|---|---|---|
| Together AI | `https://api.together.xyz/v1` | https://docs.together.ai/docs/quickstart |
| Groq | `https://api.groq.com/openai/v1` | https://console.groq.com/docs/quickstart |
| Cerebras | `https://api.cerebras.ai/v1` | https://inference-docs.cerebras.ai/quickstart |
| SambaNova | `https://api.sambanova.ai/v1` | https://docs.sambanova.ai/cloud/docs/get-started/overview |

Each has its own free-tier policy:

- **Together AI**: new-account credits reported variously as $5 or $25 depending on the program. Per the [rate-limit docs](https://docs.together.ai/docs/rate-limits), dynamic rate limits adjust with usage. Good for prototyping; check `x-ratelimit-reset` headers for sustained work.
- **Groq**: free developer tier with daily token limits per model. Hardware-tuned for gpt-oss-120b.
- **Cerebras**: free developer tier; also tuned for gpt-oss-120b on their CS-3 hardware.
- **SambaNova**: free tier with model-specific limits.

When you contribute a profile for one of these providers, name the YAML accordingly:

- `models/together-meta-llama-llama-3.3-70b-instruct.yaml`
- `models/groq-openai-gpt-oss-120b.yaml`
- `models/cerebras-openai-gpt-oss-120b.yaml`
- `models/sambanova-openai-gpt-oss-120b.yaml`

Reuse the existing OpenRouter sibling profile's prompting guidance (model behavior is provider-agnostic); the deltas are `provider:`, `model:`, `context_window_notes:`, and the empirical sections.

## Self-hosted (vLLM / SGLang / TGI / Ollama)

For operators running their own inference server, use `provider: custom` in the YAML and set `endpoint_base_url` to your OpenAI-compatible endpoint.

### vLLM example

```bash
# Start vLLM with OpenAI-compatible API
vllm serve openai/gpt-oss-120b \
  --host 0.0.0.0 --port 8000 \
  --tool-call-parser qwen3_coder  # for tool-call parsing
```

In OpenClaw config:

```json
{
  "models": {
    "custom/openai/gpt-oss-120b": {
      "baseUrl": "http://localhost:8000/v1",
      "apiKey": "not-required"
    }
  }
}
```

In the YAML profile:

```yaml
provider: custom
model: openai/gpt-oss-120b
endpoint_base_url: http://localhost:8000/v1
tool_parser: qwen3_coder
```

### Tool-parser configuration

Some models require a specific tool-call parser to translate the model's tool-call format into the OpenAI deltas the OpenClaw harness expects. Per the [`models/nvidia-nemotron-3-super-120b-a12b-free.yaml`](../../models/nvidia-nemotron-3-super-120b-a12b-free.yaml) profile, Nemotron-3 Super uses `qwen3_coder` parser across vLLM / SGLang / TRT-LLM. The Nvidia developer forum has the [definitive thread on this](https://forums.developer.nvidia.com/t/tool-calling-not-working-with-nemotron-3-super-120b-a12b-nvfp4-on-dgx-spark-sm12-1/364804) — symptom is the model emitting plain-text `<tool_call>` XML instead of proper toolCall deltas; resolution per the thread is `"Native fixed it"` (native client-side parsing, not vLLM's).

Set the `tool_parser:` field in the YAML to the parser your inference engine uses so future operators don't guess.

## What community traces look like across providers

The [`community_traces[]`](../reference/model-profiles-schema.md#community_traces-entry-shape) schema is identical regardless of `provider:`. The [`helmdeck-trace`](https://github.com/tosin2013/helmdeck/blob/main/scripts/helmdeck-trace/README.md) CLI extracts the same metric_summary structure from any OpenClaw session jsonl. No provider-specific tooling needed — the CLI just reads the session file structure that OpenClaw writes.

**Anonymization rule stays the same** per the standing memory rule: agent / workspace names redacted to sanitized labels ("Tier C agent on `openai/gpt-oss-120b`, three-turn iterative workflow"); contributor GitHub handle is fine in the `contributor:` field.

## Submission methodology

Same shape as `docs/howto/add-free-models.md` § 7:

1. **Set up the agent** on your preferred routing layer (HF / Together / self-hosted) following the section above
2. **Optional**: copy + adapt an existing per-model AGENTS.md recipe from [`docs/howto/per-model-agents/`](per-model-agents/) for the prompting shape that matches your model
3. **Run the workflow** — the standard three-turn iterative blog-drafter pattern OR your own use case
4. **Capture the session jsonl** at `~/.openclaw/agents/<your-agent>/sessions/<id>.jsonl`
5. **Extract metrics**:
   ```bash
   ./scripts/helmdeck-trace/helmdeck-trace extract \
     --session ~/.openclaw/agents/<agent>/sessions/<id>.jsonl \
     --use-case <label> \
     --contributor <gh-handle> \
     --decision <profile-works | profile-helps-partially | profile-not-enough | no-profile-needed> \
     --url <PR-or-issue-url>
   ```
6. **Submit a PR**:
   - If a profile for your (model × provider) combo already exists, add the trace to its `community_traces[]` array
   - If no profile exists yet, create one following [`docs/reference/model-profiles-schema.md`](../reference/model-profiles-schema.md) and seed it with your trace

## Verification

After setup, verify the routing works:

```bash
# Test the OpenAI-compatible endpoint directly
curl -X POST <base-url>/chat/completions \
  -H "Authorization: Bearer <your-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<model-id>",
    "messages": [{"role": "user", "content": "Reply with the single word: ok"}]
  }'
```

Expected response includes `{"choices": [...]}` with the model's reply. If you get 401, the API key is wrong; if you get 404, the base URL or model ID is wrong; if you get a CORS error, the request is hitting browser security limits — use a server-side call.

If the curl works but OpenClaw still routes through OpenRouter, the per-agent model config in `~/.openclaw/openclaw.json` needs updating to point at the new provider profile (see step 2 above).

## Related

- Schema reference: [`docs/reference/model-profiles-schema.md`](../reference/model-profiles-schema.md)
- Contribution workflow: [`docs/howto/add-free-models.md`](add-free-models.md) § 7
- Trace extraction CLI: [`scripts/helmdeck-trace/README.md`](https://github.com/tosin2013/helmdeck/blob/main/scripts/helmdeck-trace/README.md)
- Per-model recipe pattern: [`docs/howto/per-model-agents/gemma-4-iterative-workflow.md`](per-model-agents/gemma-4-iterative-workflow.md)
- Tier classification: [`docs/reference/models.md`](../reference/models.md)
- First HF template: [`models/huggingface-openai-gpt-oss-120b.yaml`](../../models/huggingface-openai-gpt-oss-120b.yaml)
- Parent issue: [#464](https://github.com/tosin2013/helmdeck/issues/464) (per-model profile library)
- HF community track: [#482](https://github.com/tosin2013/helmdeck/issues/482)
- Empirical motivation: [PR #481](https://github.com/tosin2013/helmdeck/pull/481) + [PR #484](https://github.com/tosin2013/helmdeck/pull/484) (Nemotron baseline-vs-hardened A/B)
