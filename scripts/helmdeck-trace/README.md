# `helmdeck-trace` — extract structured metrics from OpenClaw session jsonl

Single-file Python CLI (stdlib only, no PyYAML/requests) that reads an OpenClaw session jsonl + its sibling `.trajectory.jsonl`, computes the metrics defined by the `community_traces[]` schema in `models/openai-gpt-oss-120b-free.yaml`, and emits a YAML block ready to paste into any per-model profile under `models/` (or to attach to one of the [#473–#476](https://github.com/tosin2013/helmdeck/issues/473) empirical-baseline issues).

Built for [issue #464](https://github.com/tosin2013/helmdeck/issues/464) Phase 1 community contributions — lowers the bar from "run a session, hand-tally the tool calls, hand-format the YAML" to "run a session, point the CLI at the jsonl, paste the output."

## Quick start

```bash
# Quick stdout summary (no YAML output)
./scripts/helmdeck-trace/helmdeck-trace summary \
  --session ~/.openclaw/agents/<your-agent>/sessions/<session-id>.jsonl

# Extract a community_traces[] entry ready to paste into a model profile
./scripts/helmdeck-trace/helmdeck-trace extract \
  --session ~/.openclaw/agents/<your-agent>/sessions/<session-id>.jsonl \
  --use-case publishing-strategist \
  --contributor your-gh-handle \
  --decision profile-helps-partially \
  --url 'https://github.com/tosin2013/helmdeck/pull/<your-pr>' \
  --output trace.yaml

# A/B compare a baseline session against a profile-aware session
./scripts/helmdeck-trace/helmdeck-trace compare \
  --baseline      ~/.openclaw/agents/<baseline-agent>/sessions/<id>.jsonl \
  --profile-aware ~/.openclaw/agents/<profile-agent>/sessions/<id>.jsonl
```

## Subcommands

### `extract`

| Flag | Meaning |
|---|---|
| `--session` | (required) path to the OpenClaw session jsonl file |
| `--model` | model id (auto-detected from the sibling `.trajectory.jsonl` if omitted) |
| `--use-case` | short label like `publishing-strategist` / `code-reviewer` / `research-summarizer` |
| `--contributor` | your GitHub handle (default: `anonymous`) |
| `--decision` | one of: `profile-works`, `profile-helps-partially`, `profile-not-enough`, `no-profile-needed` |
| `--url` | PR or issue link the trace supports |
| `--notes` | custom one-liner (default: auto-synthesized from the metrics) |
| `--no-anonymize` | include raw agent name + workspace path (default: anonymized to `Tier C agent on <model>`) |
| `--output`, `-o` | write to file (default: stdout) |

### `compare`

Outputs a markdown table comparing two sessions side-by-side. Useful for the A/B methodology described in each [empirical-baseline issue](https://github.com/tosin2013/helmdeck/issues/473) — baseline (no profile) vs profile-aware (using the per-model YAML's prompt template).

### `summary`

Prints a key:value listing of every metric to stdout, no YAML. For quick eyeballing.

## What gets extracted

The CLI walks the session jsonl forward, pairing each `toolCall` part in an assistant message with the next `toolResult` turn (FIFO). For each pack call it captures the name, arguments, and parsed result. Then it computes:

| Metric | How it's detected |
|---|---|
| `real_pack_calls` | Total count of `toolCall` parts (NOT text claims like "I deposited 6 artifacts") |
| `tool_calls_by_name` | Per-tool tally |
| `verify_manifest_called` | Any tool call named `helmdeck__artifact-verify_manifest` |
| `all_present` | Parses the verify_manifest tool result JSON for the `all_present` field |
| `artifact_put_called` | Any tool call named `helmdeck__artifact-put` |
| `content_ground_called` | Any tool call named `helmdeck__content-ground` |
| `claims_considered` / `claims_grounded` / `claims_skipped` | From the `content.ground` tool result |
| `pipeline_run_called` | Any tool call named `helmdeck__pipeline-run` |
| `citation_urls_in_text` | `[N](url)` and `[source](url)` matches in assistant final text |
| `citation_urls_from_grounding` | URLs returned in `content.ground` response `grounding[]` array |
| `citation_urls_fabricated` | Inline URLs that do NOT appear in any `content.ground` response — the Tier C citation-confabulation failure mode documented in 2026-06-10 traces |
| `hallucination_count` | Heuristic: assistant text claims a deposit / verify outcome but the corresponding tool call never fired |
| `terminal_errors` | Captured from trajectory `model.completed.data.terminalError` |

`simplification_observed` is intentionally NOT auto-detected — the heuristic ("did the model take a shortcut") is too fragile to automate reliably. Set it manually after operator review (the CLI emits `null` so the YAML schema is satisfied).

## Anonymization

Default behavior anonymizes operator-personal data per the standing memory rule that workspace files + agent names stay private:

- `agent_id: press-gemma-4` → comment `# trace agent (anonymized): Tier C agent on <model>`
- `workspace_dir: /home/node/.openclaw/workspace-redhat-blog` → omitted from output

Pass `--no-anonymize` if you're testing locally and want the raw values. The default is safe for community PRs.

## Output format

The YAML output matches the canonical `community_traces[]` schema from `models/openai-gpt-oss-120b-free.yaml`:

```yaml
# trace agent (anonymized): Tier C agent on `<model>`
# trace model: <model>
# trace session id: <session-id>
# tool-call tally (above the schema fields, comment for operator review):
#   helmdeck__artifact-put: 1
#   helmdeck__artifact-verify_manifest: 1
#   helmdeck__content-ground: 1

community_traces:
  - contributor: <gh-handle>
    use_case: <label>
    session_date: <YYYY-MM-DD>
    metric_summary:
      real_pack_calls: 3
      verify_manifest_called: true
      all_present: true
      hallucination_count: 0
      simplification_observed: null  # set manually
    decision: profile-helps-partially
    notes: |
      Audit-callback fired end-to-end (3 real pack call(s); verify_manifest all_present:true).
    pr_or_issue_url: https://github.com/...
```

The comment block above the YAML body carries detail (per-tool tally, terminal errors, citation-fabrication count, hallucination notes) for operator review. The schema-required fields below are drop-in ready for any model profile's `community_traces[]` array.

## Validating with a test agent

Helmdeck's per-model profile work needs a way to validate the CLI without burning operator-personal traces or contaminating test runs with model-family quirks. The recommended pattern: spin up a dedicated `trace-test` agent locally on a known-good model (e.g., `openrouter/openai/gpt-oss-120b:free`), with a generic AGENTS.md that runs the same three-turn iterative workflow shape Hat/Press-Gemma use.

The agent doesn't go in helmdeck — it stays in `~/.openclaw/workspace-trace-test/` on the operator's machine. But the pattern (dedicated test agent for validating helmdeck CLI tooling) is community-useful; the pattern is documented in `docs/howto/per-model-agents/` as a recipe.

## Limitations

- Doesn't fire sessions. OpenClaw's internal IPC protocol isn't documented for external automation. Run the test prompt manually via the OpenClaw UI; then point the CLI at the resulting jsonl. Filed as a research follow-up if upstream OpenClaw ships a documented session-fire API.
- Doesn't compute `simplification_observed`. Heuristic is too fragile; set it manually after review.
- Doesn't compare against a model's expected behavior. The output is the trace; you decide the `decision:` value.

## Related

- Schema source of truth: `models/openai-gpt-oss-120b-free.yaml` `community_traces[]` array
- Contribution paths: `docs/howto/add-free-models.md` § 7
- Per-model agent recipes: `docs/howto/per-model-agents/`
- Empirical-baseline issues: [#473](https://github.com/tosin2013/helmdeck/issues/473) gemma-4 / [#474](https://github.com/tosin2013/helmdeck/issues/474) llama-3.3 / [#475](https://github.com/tosin2013/helmdeck/issues/475) nemotron-3-super / [#476](https://github.com/tosin2013/helmdeck/issues/476) qwen3-coder

## Requirements

Python 3.8+. Stdlib only — no external packages.
