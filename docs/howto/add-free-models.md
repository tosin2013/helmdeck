---
title: Add free models to your agent
description: Strict recommendation — Tier C free / open-weight models require per-(model × use-case) prompt customization. Generic skill prose fails in measurable ways. Use a model profile as a starting point and fork.
keywords: [helmdeck, free models, tier c, prompting, openclaw, customization, openrouter]
---

# Add free models to your agent

If you want to drive helmdeck pack workflows with a free open-weight model — `openai/gpt-oss-120b:free`, `meta-llama/llama-3.3-70b-instruct:free`, `nvidia/nemotron-3-super-120b-a12b:free`, `google/gemma-2-9b-it:free`, `z-ai/glm-4.5-air:free`, and similar — this guide is the operator's strict recommendation: **do not use the generic skill prose as-is**. Customize per (model × use-case). Here's the recipe.

## Why free models need custom prompts

Empirical evidence from helmdeck's debugging arc, all on `openai/gpt-oss-120b:free`:

| Failure mode | What it looks like | Pack-layer fix | Skill-layer fix |
|---|---|---|---|
| **Skill prose ignored** | Agent skips "save to artifacts" instruction; content lives only in chat | [PR #450](https://github.com/tosin2013/helmdeck/pull/450) — typed `artifact.put` pack | — |
| **Required arg missing** | Pack rejects `content.ground` with "model is required" | [PR #453](https://github.com/tosin2013/helmdeck/pull/453) — default-arg resolver | — |
| **Multi-step chain hallucinated** | Agent produces a confidently-formatted manifest table for artifacts it never deposited; cites "(mandatory per SKILL.md)" as if to demonstrate compliance | [PR #462](https://github.com/tosin2013/helmdeck/pull/462) — `artifact.verify_manifest` audit pack | — |
| **Wrong-fit prompting shape** | Even with all of the above fixed, the agent still ignores parts of the skill (e.g. produces 2 of 9 platform variations) | — | **Per-model profile + per-use-case AGENTS.md** (this guide) |

The pack-layer fixes are necessary but not sufficient. The fourth row is what this guide addresses.

## Strict recommendation

For any Tier C model:

1. **Do NOT** rely on the generic helmdeck SKILL.md as the only operator-visible prompt.
2. **DO** use the model profile in [`models/<provider>-<model>.yaml`](https://github.com/tosin2013/helmdeck/tree/main/models) as the starting point.
3. **DO** fork SOUL.md, USER.md, and AGENTS.md per the profile's `prompt_template`.
4. **DO** encode use-case-specific commitments (number of variations, deposits, verification keys) as machine-checkable success criteria in AGENTS.md. The model will optimize for the objective; unpinned counts get simplified.
5. **DO** run a verification trace on YOUR prompt before relying on the agent in production.

Skipping any step means generic prose, model misalignment, or simplified output — empirically documented in [the field reports](/blog).

## Recipe — worked example with a hypothetical persona

This recipe uses **Maya**, a hypothetical security researcher who publishes on Mastodon, a personal Substack, and Phrack. None of Maya's persona is anyone real — the goal is to show the shape of operator customization without leaking the maintainer's personal workspace details. Substitute YOUR persona, platforms, and projects throughout.

### Step 1 — Pick a target model and locate its profile

For Maya's use case (security-research writing), `openai/gpt-oss-120b:free` is a reasonable Tier C candidate. The profile lives at [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml).

Open the profile. Read it end-to-end. Note the `prompt_template` field — that's the shape the model responds to. For gpt-oss it's:

```text
## Objective
## Source priority
## Constraints
## Output format
## Success criteria
## Reasoning
```

For a different model, the shape will differ — Llama prefers plain-English structured steps; Nemotron prefers explicit ordered procedures. Each profile encodes the difference.

### Step 2 — Set up an OpenClaw agent + workspace

Create a workspace folder for the new agent. Use `~/.openclaw/workspace-<short-label>/`. Seed it with the four canonical files (SOUL.md, IDENTITY.md, USER.md, AGENTS.md) per [OpenClaw's agent-workspace docs](https://docs.openclaw.ai/concepts/agent-workspace).

Then add the agent:

```bash
docker exec openclaw-openclaw-cli-1 openclaw agents add maya-security-press \
  --workspace /home/node/.openclaw/workspace-maya-security \
  --model openrouter/openai/gpt-oss-120b:free \
  --non-interactive --json
```

For details on the four canonical workspace files and OpenClaw's hot-reload behavior, see [`docs/integrations/openclaw.md`](../integrations/openclaw.md).

### Step 3 — Write the four workspace files using the profile's shape

For SOUL.md (voice and stance — small, opinionated):

```markdown
# SOUL.md
Practitioner-first. Hostile to hype. Will say "this is a bad fit" instead
of producing the asked-for output. No "Great question!" filler.
```

For IDENTITY.md (name + emoji + one-line role — keep small):

```markdown
# IDENTITY.md
- **Name:** Maya
- **Vibe:** Security researcher's publishing strategist
- **Emoji:** 🔐
```

For USER.md (operator profile — fill in YOUR persona):

```markdown
# USER.md

## Operator
- **Name:** Maya Chen
- **Role:** Security researcher

## Default publishing platforms
- Mastodon — short thread format, link to long-form
- Personal Substack — newsletter, deep technical
- Phrack — only when topic fits (exploit research, low-level security)

## Voice rules
- Architect-level register, never click-baity
- Cite primary sources (CVE entries, original advisories)
- Refuse to invent benchmarks or attribution
```

For AGENTS.md (the load-bearing one — use the profile's `prompt_template` shape):

```markdown
# AGENTS.md

## 🔐 Security-publishing-strategist — Objective + Constraints + Success criteria

### Objective
Turn one piece of source material (CVE advisory / exploit research / vendor
blog / conference talk transcript) into a publishing strategy + the actual
assets, where every asset lands in the helmdeck artifact store and every
asset's existence is verified before reporting success.

### Source priority
1. Operator-pasted content (CVE entries, advisories, research notes)
2. Helmdeck pack outputs (repo.fetch, web.scrape, content.ground)
3. General knowledge — only when 1+2 are silent
4. If a claim is not supported by 1-3, state what's missing instead of guessing

### Constraints
- No fabricated CVE IDs, exploit details, or attribution
- Cite primary sources (NVD entries, vendor advisories, original research)
- Phrack: only when the topic is genuinely low-level / exploit research
- Mastodon: ≤500 chars, link to long-form on Substack

### Output format
Every response MUST include six sections:
1. Source classification
2. Chosen mode (Canonical+Syndication / Platform Variations / Hybrid)
3. Per-platform variation blocks
4. Deposit step (artifact.put per variation)
5. Verify step (artifact.verify_manifest with every key from §4)
6. Final check (synthesis)

### Success criteria
A response is **valid only when**:
- ✅ All six sections present
- ✅ helmdeck__artifact-put called once per variation in §3
- ✅ helmdeck__artifact-verify_manifest called with every key from §4
- ✅ verify_manifest returns all_present:true OR §5 honestly reports missing[]
- ✅ Every CVE, advisory, exploit detail traces to operator input or pack output

A response is **invalid** when:
- ❌ Section 3 lists N variations but artifact.put was called fewer than N times
- ❌ Section 5 claims all_present:true without calling verify_manifest

### Reasoning
Reasoning: medium for typical strategy. high for CVE-impact analysis or
exploit-research framing decisions.
```

Note how the success criteria are **machine-checkable** and the deposit + verify steps are framed as **invalidation conditions**, not as separable advisory steps. This is the load-bearing move: on Tier C, "MANDATORY" prose gets treated as advisory; "INVALID if not done" gets treated as part of the objective.

### Step 4 — Verify with an A/B trace

Before relying on the new agent, run the same prompt on TWO agents:

1. **Baseline**: a vanilla OpenClaw agent on the same model with the generic helmdeck skill (no profile-aware AGENTS.md)
2. **Profile-aware**: your new Maya agent

Compare:

| Metric | Where to find it |
|---|---|
| `artifact.put` (or `pipeline-run` that auto-deposits) calls per session | Session jsonl at `~/.openclaw/agents/<id>/sessions/*.jsonl` — count `tool_use` events with the name |
| `artifact.verify_manifest` call + result | Same trace — look for the call and its returned `all_present` field |
| Hallucinated manifest entries | Compare the agent's text claims against the actual artifact store (`GET /api/v1/artifacts`) |
| Use-case-specific commitments honored | Did you ask for 5 platform variations? Did it produce 5 deposits, or fewer? |

If the profile-aware agent meaningfully outperforms baseline (real deposits + real verify + zero hallucination), the profile is working for your use case. If it doesn't, you have evidence to tighten AGENTS.md further OR to file a refinement to the model profile.

## Library is a starting point, not a finished product

The helmdeck `models/` library encodes prompting style sourced from each model's official docs. It tells you what SHAPE to use. It does NOT tell you what your USE CASE specifically requires.

Empirically (from the 2026-06-09 publishing-strategist trace), even with a profile-aware AGENTS.md, the agent simplified the skill's 9-platform table to 2 variations because the success criteria didn't pin a specific N. The model optimized for the objective ("artifacts in store") via the most efficient route (`pipeline-run` with auto-deposit) — that's correct behavior per the profile, AND it's not what the operator asked for.

**The profile gets you reliability of the audit-callback shape. It does not get you a specific use-case implementation.** Operators MUST encode use-case-specific commitments (exact platform list, exact deposit count, exact verification key set) in AGENTS.md success criteria — pinned with hard numbers, framed as invalidation conditions.

One library entry won't fit every use case. Don't expect it to.

## Available profiles

Today the library contains:

- [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml) — empirically validated 2026-06-09

Planned per [issue #464](https://github.com/tosin2013/helmdeck/issues/464):

- `meta-llama/llama-3.3-70b-instruct:free`
- `nvidia/nemotron-3-super-120b-a12b:free`
- `google/gemma-2-9b-it:free`
- `z-ai/glm-4.5-air:free`

Each waits for empirical validation evidence — see § 7 for how to contribute.

## Share your findings (community research)

Every operator running a custom Tier C agent is producing data the rest of the community needs. Helmdeck explicitly invites trace reports per use case. Three contribution paths:

### Profile contribution

If you customize a profile for a new model (or refine an existing one), open a PR to `models/<provider>-<model>.yaml` with your trace evidence in the `community_traces[]` field. The schema:

```yaml
community_traces:
  - contributor: <your-github-handle or "anonymous">
    use_case: <short label, e.g., "security-research-publishing">
    session_date: <YYYY-MM-DD>
    metric_summary:
      real_pack_calls: <int>
      verify_manifest_called: <bool>
      all_present: <bool>
      hallucination_count: <int>
      simplification_observed: <bool>
    decision: <"profile-works" | "profile-helps-partially" | "profile-not-enough" | "no-profile-needed">
    notes: <one or two lines>
    pr_or_issue_url: <link>
```

### Use-case contribution

If you used an existing profile on a new use case (research summarizer, code reviewer, compliance auditor, etc.) with different results from the maintainer's baseline use case, open an issue on [tosin2013/helmdeck](https://github.com/tosin2013/helmdeck/issues) with the trace excerpt and comparison metrics. Tag with `field-report`.

### Failure-mode contribution

If you hit a new failure mode (not skipped / hallucinated / simplified — those are the three we've documented), file an issue tagged `field-report` with the trace data. We're building a vocabulary of Tier C failure modes; novel ones strengthen the whole community's understanding.

You'll be credited per [`CONTRIBUTING.md`](https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md). Contribution is optional, not gated — helmdeck remains usable without it. We're all doing this together.

## See also

- The model profile YAML schema: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml)
- The audit-callback pattern reference: [`docs/reference/packs/artifact/verify-manifest.md`](../reference/packs/artifact/verify-manifest.md)
- Tier classification + tier-aware behaviors: [`docs/reference/models.md`](../reference/models.md)
- For Tier B candidates (paid mid-tier or strong free models that don't fit the open-weight Tier C profile): [`docs/howto/experiment-with-tier-b-models.md`](./experiment-with-tier-b-models.md)
- Field-report blog series capturing the failure modes: [Plausibility-shaped output](/blog/plausibility-shaped-output), [The audit-callback pattern](/blog/the-audit-callback-pattern), [Empirical validation](/blog/empirical-validation-per-model-profile)
