---
title: Experiment with Tier B models
description: Tier B (mid-tier paid or strong free) is an open research question. Helmdeck has empirical data on Tier C (need customization) and assumes Tier A (out-of-box). For Tier B, you're contributing the data we need.
keywords: [helmdeck, tier b, prompting, openclaw, customization, research]
---

# Experiment with Tier B models

If you're using a Tier B model — mid-tier paid (Claude Haiku 3.5, GPT-4o-mini, Gemini Flash, Mistral Medium) or a strong free model that doesn't fit the open-weight Tier C profile (some recent free-tier offerings from larger labs) — you're in a documentation gap. Helmdeck has empirical evidence for the Tier C case (prompt customization required) and assumes the Tier A case (frontier models work out of the box). For Tier B, **we don't have empirical evidence yet**.

This page is honest about that gap. It also gives you the methodology to fill it in, and asks you to share what you find. You're not consuming a settled answer — you're producing data the project needs.

## Why this is an open research question

| Tier | Status | Evidence |
|---|---|---|
| Tier A (frontier) | Assumed: works out of the box. Verify on your specific model. | Helmdeck assumes Tier A handles generic skill prose. No empirical helmdeck head-to-head yet — you can produce one. |
| Tier B (mid-tier / strong free) | **Unknown.** Some Tier B models likely behave like Tier A; some likely behave like Tier C. We don't know where the line is. | **Zero empirical helmdeck traces.** Your A/B test is the first data point. |
| Tier C (free open-weight) | Documented: customization required. | Multiple empirical traces in PR #462, the field-report blog series, and the [add-free-models howto](./add-free-models.md). |

If the Tier B model behaves like Tier A on your use case, use the generic skill prose and skip the profile work. If it behaves like Tier C, adopt the closest Tier C profile and fork. The way to know is to test.

## A/B test methodology

The goal: run the same prompt on TWO agents — one with the generic skill, one with a borrowed Tier C profile — and compare three metrics:

- Real `artifact.put` (or auto-depositing producer like `pipeline-run`) calls per session
- `artifact.verify_manifest` call + result (`all_present: true` is the structural-compliance signal)
- Hallucination count: how many text-only deposit claims appear that don't correspond to real artifacts in the store

### Step 1 — Pick a candidate Tier B model

A few representative shapes:

- **Mid-tier paid**: `anthropic/claude-haiku-3.5`, `openai/gpt-4o-mini`, `google/gemini-flash-2.0`, `mistralai/mistral-medium-3.1`
- **Strong free (sometimes Tier B-classed)**: depends on context — `meta-llama/llama-3.3-70b-instruct:free` is currently Tier C by helmdeck classification but some operators have reported Tier A-like behavior on specific use cases. That's exactly the kind of data we're looking for.

### Step 2 — Set up two agents on the same model

In OpenClaw, create two agents pointed at the same model but with different workspaces:

```bash
# Baseline: generic skill, no profile
openclaw agents add baseline-tierb \
  --workspace ~/.openclaw/workspace-baseline \
  --model <your-tier-b-model> --non-interactive --json

# Profile-aware: borrow the closest Tier C profile
openclaw agents add profiled-tierb \
  --workspace ~/.openclaw/workspace-profiled \
  --model <your-tier-b-model> --non-interactive --json
```

Workspace setup is identical to the [add-free-models howto](./add-free-models.md) Step 3 — SOUL/IDENTITY/USER files identical between agents, AGENTS.md differs:

- Baseline workspace: minimal AGENTS.md (categorical sections, four publishing modes, decision rules — the helmdeck default shape).
- Profile-aware workspace: AGENTS.md structured per the Tier C profile that most closely matches your candidate (start with `openai/gpt-oss-120b:free`'s profile if your candidate is a GPT-family Tier B; start with the Llama profile if your candidate is a Llama-family Tier B; etc.).

### Step 3 — Run the same prompt on both

Pick ONE prompt that exercises a multi-deposit chain — something where the agent has to produce N artifacts and ideally call `artifact.verify_manifest` against them. Suggested template:

```text
Generate publishing variations for <SOURCE URL or repo> on these N platforms:
<list 3-5 platforms>
```

Run it on both agents. Capture the session jsonls:

```bash
cp $(docker exec openclaw-openclaw-gateway-1 ls /home/node/.openclaw/agents/baseline-tierb/sessions/*.jsonl -t | head -1) /tmp/baseline.jsonl
cp $(docker exec openclaw-openclaw-gateway-1 ls /home/node/.openclaw/agents/profiled-tierb/sessions/*.jsonl -t | head -1) /tmp/profiled.jsonl
```

### Step 4 — Count the metrics

Parse each session jsonl for tool-call events. For each agent, count:

- Real `artifact.put` calls (or `pipeline-run` calls that produce deposits — count both as "real deposit producers")
- `artifact.verify_manifest` calls
- The pack's returned `all_present` value
- Hallucinated entries: agent text says "deposited N artifacts" but `GET /api/v1/artifacts` shows fewer

Compare:

| Metric | Baseline | Profile-aware | Decision signal |
|---|---|---|---|
| Real deposits | A | B | If A ≈ B (both ≥ N), Tier B behaves like Tier A; profile not needed |
| `verify_manifest` called | A | B | If A ≈ B, Tier A-like |
| `all_present: true` | A/B | A/B | A 'true' on baseline strongly suggests Tier A behavior |
| Hallucinated entries | A | B | If baseline has hallucinations and profile-aware doesn't, Tier C-like — profile helps |

## Decision tree

Based on the metric comparison:

```
                    ┌─── Baseline produces all expected deposits + verify ──→ Tier A behavior. Use generic skill, no profile.
                    │
                    ├─── Baseline produces SOME deposits, profile-aware produces more ──→ Tier B "in between". Use profile + custom AGENTS.md (recommended).
                    │
                    └─── Baseline produces 0 deposits OR hallucinates, profile-aware fires verify ──→ Tier C behavior. Adopt closest Tier C profile and fork per [add-free-models](./add-free-models.md).
```

Decide based on YOUR trace. Different use cases may push the same Tier B model toward different decisions — a content-generation use case might tolerate generic prose, a multi-step CI/CD use case might need the profile. Test each use case independently.

## Share your findings — mandatory ask

This guide cannot fill the Tier B documentation gap without contributions. Three submission shapes; pick whichever fits your finding:

### Tier B model report — most common case

Open an issue or PR comment on [issue #464](https://github.com/tosin2013/helmdeck/issues/464) with:

```text
Model: <provider/model:variant>
Tier classification expected: B (mid-tier paid / strong free)
Use case: <short label>
Session date: <YYYY-MM-DD>

Baseline (generic skill):
  - Real pack calls: <int>
  - verify_manifest called: yes/no
  - all_present: yes/no/n_a
  - Hallucinated entries: <int>

Profile-aware (borrowed from <which-Tier-C-profile>):
  - Real pack calls: <int>
  - verify_manifest called: yes/no
  - all_present: yes/no/n_a
  - Hallucinated entries: <int>

Decision: profile-works / profile-helps-partially / profile-not-enough / no-profile-needed
Recommendation: <ship as Tier A reference / ship as Tier C with this profile / open research question still>
```

### New failure mode discovered

If you find a Tier B failure mode that doesn't fit the three we've documented (skipped / hallucinated / simplified), file as a new issue tagged `field-report` with the trace excerpt. New failure modes expand the vocabulary the whole community uses.

### Confirmation that Tier B = Tier A for your model

If your candidate clearly behaves like Tier A (baseline works, profile adds nothing), please confirm by submitting a one-line entry against the proposed `models/<provider>-<model>.yaml`:

```yaml
tier: B
customization_needed: false
community_traces:
  - contributor: <your-handle>
    use_case: <label>
    session_date: <YYYY-MM-DD>
    decision: no-profile-needed
    notes: Baseline matched profile-aware on all metrics
```

"No signal" results matter as much as positive ones — they let helmdeck remove that model from the customization-required list and free other operators from unnecessary work.

## Why this is good science

A few principles worth naming, since helmdeck explicitly treats this as a community-research effort:

- **Independent reproductions strengthen empirical claims.** One operator's trace is interesting; three matching traces is evidence. If the maintainer's Tier C trace on `gpt-oss-120b:free` is published and you reproduce it with similar metrics on YOUR use case, that's a stronger architectural claim than either trace alone.
- **Novel findings expand the vocabulary.** The three Tier C failure modes (skipped / hallucinated / simplified) are not exhaustive. A fourth, fifth, sixth — your finding might be one. File it.
- **Null results matter.** "We A/B tested model X on use case Y and the profile added no value" is genuinely useful — it lets helmdeck remove a model from the customization-required list and saves other operators time. Please contribute null results too.
- **No gatekeeping.** Helmdeck doesn't require contribution to remain usable. The library is a starting point for everyone, contributors and consumers. If you'd rather use it without contributing, that's fine.

## See also

- For Tier C models (strict recommendation to customize): [`docs/howto/add-free-models.md`](./add-free-models.md)
- Tier classification + tier-aware behaviors: [`docs/reference/models.md`](../reference/models.md)
- Per-model profile schema: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml)
- Audit-callback pattern reference: [`docs/reference/packs/artifact/verify-manifest.md`](../reference/packs/artifact/verify-manifest.md)
- Open issue for the library: [#464](https://github.com/tosin2013/helmdeck/issues/464)
