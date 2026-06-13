---
description: "Recipe for an OpenClaw concept-animator agent on `openai/gpt-oss-120b:free` that turns a single concept into a narrated animated MP4 (15s–720s) via `podcast.generate` → `hyperframes.compose` → `hyperframes.render`."
---

# How to build a Concept Animator agent on `openai/gpt-oss-120b:free`

This recipe shows how to set up an OpenClaw agent running `openai/gpt-oss-120b:free` that turns a single-sentence concept into a narrated animated video (16:9, 9:16, or 1:1), with an explicit max-length option that unlocks the full 12-minute `hyperframes.compose` cap. It closes part of [issue #496](https://github.com/tosin2013/helmdeck/issues/496) — the video-agents reference recipes for `gpt-oss-120b:free`.

The recipe is **model-family-specific**. Workflow mechanics are the same that PR [#470](https://github.com/tosin2013/helmdeck/pull/470) validated for the iterative-blog use case — single objective, explicit constraints, machine-checkable invalidation rules — but the chain is shorter (3 pack calls) and the artifact is video instead of markdown.

## When to use this recipe

Use it when you want a Tier C concept-animator agent that reliably:

- Calls `podcast.generate` to produce the narration audio (with `av.validate` running automatically inside the pack per [ADR-052](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/052-av-output-validation-post-step.md) Phase 3)
- Calls `hyperframes.compose` with the returned `audio_url`, scaling `duration_seconds` to match the audio (up to **720s / 12 min**)
- Calls `hyperframes.render` with the resulting `composition_html`, producing a sub-512 MiB MP4
- Closes the chain with an audit-callback (`artifact.verify_manifest`) so the operator gets a machine-checkable confirmation the MP4 actually exists — not just a text claim of success

It does NOT replace a hand-authored animation skill — it's the small, opinionated worked example of getting a Tier C model to drive a 3-call video chain without hallucinating intermediate output.

## Worked example — Maya, security researcher

This recipe uses **Maya**, a hypothetical security researcher who publishes short explainers about kernel observability and memory-corruption mitigations on Mastodon and YouTube, as the worked persona. Maya is sanitized — no real operator's identity, employer, or platform list. Adapt the persona to your own context.

## Pre-flight

- [ ] OpenRouter API key set; `openai/gpt-oss-120b:free` confirmed reachable
- [ ] Helmdeck packs available: `helmdeck__podcast-generate`, `helmdeck__hyperframes-compose`, `helmdeck__hyperframes-render`, `helmdeck__artifact-verify_manifest`, `helmdeck__artifact-get`
- [ ] ElevenLabs API key configured (otherwise `podcast.generate` must be called with `allow_silent_output: true`)
- [ ] Per-model profile YAML reviewed: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml). Particular sections to internalize before writing the AGENTS.md: `prompting_style`, `anti_patterns`, `chain_call_reliability`.

## Step 1 — Create the workspace

In OpenClaw, create a new agent workspace (e.g., `~/.openclaw/workspace-maya-animator/`). Add the canonical OpenClaw files: `SOUL.md`, `IDENTITY.md`, `USER.md`, `AGENTS.md`. The persona files (SOUL / IDENTITY / USER) are yours to define; the recipe below focuses on `AGENTS.md`, which is the load-bearing file for `gpt-oss-120b`'s prompting fit.

## Step 2 — Configure the model route

In OpenClaw's per-agent model config, set:

```
provider: openrouter
model: openai/gpt-oss-120b:free
sampling:
  temperature: 0.7
  top_p: 0.95
reasoning_effort: medium
```

Why these values: `gpt-oss-120b` exposes a graded reasoning-effort knob (`low` / `medium` / `high`). The concept-animator chain involves duration math (audio length → composition seconds) plus pack-selection plus invalidation-rule self-check — that's `medium` work. Bumping to `high` is unnecessary and slow; dropping to `low` raises the risk of the "plausibility-shaped output" failure mode documented in the profile YAML where the model claims a tool call as text instead of executing it.

## Step 3 — AGENTS.md template

Copy the template below to `~/.openclaw/workspace-maya-animator/AGENTS.md`. The template uses `gpt-oss-120b`'s preferred style — single OBJECTIVE, explicit CONSTRAINTS, machine-checkable SUCCESS CRITERIA framed as INVALIDATION RULES (per the model profile's `prompting_style: objectives_constraints_success_criteria` setting):

````markdown
# AGENTS.md — Maya's concept animator on openai/gpt-oss-120b:free

This workspace produces short narrated animated videos on a Tier C agent
running gpt-oss-120b. The AGENTS.md prose is tuned to gpt-oss's
Objectives + Constraints + Success-Criteria style per the helmdeck profile
models/openai-gpt-oss-120b-free.yaml — NOT a numbered step-by-step
procedure. The chain is 3 pack calls plus an audit-callback. Per the
profile's chain_call_reliability table, this is a medium-length chain;
framing pack calls as part of success criteria (not separable steps)
is the critical lever.

# OBJECTIVE

Convert the operator's concept into a hosted MP4 animated video with
narration. Default duration is 60 seconds (social-first). When the
operator requests "max length", scale to the 12-minute cap.

# SOURCE PRIORITY

1. The operator's most recent message (concept + optional max-length flag).
2. Prior turns in this conversation (for follow-up edits to the same concept).
3. General knowledge (only for animation conventions, e.g., aspect-ratio
   norms for vertical / horizontal output).

# CONSTRAINTS

- Do not micromanage rendering details. The packs handle their own internals.
- If the operator requests "max length", pass `duration_target_min: 12` to
  `podcast.generate` and `duration_seconds: 720` to `hyperframes.compose`.
  Otherwise default to a 60-second target on `podcast.generate`.
- Pass `podcast.generate`'s `audio_url` field — the presigned URL, NOT
  `audio_artifact_key` (the sidecar key) — to `hyperframes.compose` as the
  `audio_url` input. Two related fields exist on the response; the
  presigned URL is the one `hyperframes.compose` consumes.
- When `ELEVENLABS_API_KEY` is unavailable, call `podcast.generate` with
  `allow_silent_output: true` so the chain still produces a silence-padded
  MP3 the composer can frame against.
- Pass `hyperframes.compose`'s returned `composition_html` to
  `hyperframes.render` verbatim. Do not modify the HTML.

# SUCCESS CRITERIA (Invalidation Rules — applied strictly)

The response is INVALID and must NOT be reported as success when:

- `helmdeck__podcast-generate` was not called.
- `helmdeck__hyperframes-compose` was not called with the `audio_url`
  returned by `podcast.generate`.
- `helmdeck__hyperframes-render` was not called with the `composition_html`
  returned by `hyperframes.compose`.
- `helmdeck__artifact-verify_manifest` was not called with the rendered
  MP4's `video_artifact_key`, OR the response field `all_present` is not
  `true`.
- The "max length" flag was set but `duration_target_min` ≠ 12 or
  `duration_seconds` ≠ 720.
- Any pack result is paraphrased or invented as text instead of cited
  from the actual tool return.

# NOTE ON av.validate

- `av.validate` runs automatically inside `podcast.generate` (per ADR-052
  Phase 3 default-on integration). The audio is validated; no explicit
  call needed on the audio leg.
- `av.validate` does NOT run automatically after `hyperframes.render`.
  If a post-render quality check is wanted (faststart, codec pin, packet
  contiguity), call it explicitly with the rendered MP4's
  `video_artifact_key`. For the default chain this is optional —
  `artifact-verify_manifest` is the load-bearing audit-callback.

# OUTPUT FORMAT

When the chain succeeds, report:

- The concept (one line).
- Audio duration (seconds) from `podcast.generate`.
- Composed duration (seconds) from `hyperframes.compose`.
- Rendered MP4 `video_artifact_key` and `verify_manifest` `all_present` result.
- A short note on aspect ratio chosen if the operator left it unspecified.

Do not include any URL the operator did not see in a tool result.
````

## Step 4 — Test prompt

After bootstrapping the agent, run this prompt to verify the workflow fires end-to-end:

```
Animate: eBPF tracepoint observability lets you watch kernel module
loads without writing a kernel module yourself. Show the trace flow.

(no max-length flag — defaults to 60s)
```

And a max-length variant:

```
Animate: How modern Linux kernels detect rootkits via tracepoint
attestation and signed module measurement — explain in depth.

(max length)
```

**Expected behavior**: three pack calls + one audit-callback, with each subsequent call consuming the prior call's typed output. The 60s prompt should produce `podcast.generate` with `duration_target_min` ≈ 1. The max-length prompt should produce `podcast.generate` with `duration_target_min: 12` and `hyperframes.compose` with `duration_seconds: 720`. The final `verify_manifest` must report `all_present: true`.

If the model:

- skips a pack call,
- paraphrases a tool result instead of citing the actual response,
- claims `all_present: true` without showing the verify-manifest call,
- or sets a duration value other than 60 (default) or 720 (max-length),

that's a `gpt-oss-120b`-specific finding worth capturing in the [profile YAML's](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml) `community_traces[]` — see [`docs/howto/add-free-models.md` §7](../add-free-models.md) for the contribution path.

## Capture an empirical trace

After running both prompts (default + max-length) against the agent, extract a community trace via the `helmdeck-trace` CLI:

```bash
./scripts/helmdeck-trace/helmdeck-trace extract \
  --session ~/.openclaw/agents/<workspace-name>/sessions/<session-id>.jsonl \
  --use-case concept-animator \
  --contributor <your-github-handle> \
  --decision <profile-works|profile-helps-partially|profile-not-enough> \
  --url 'https://github.com/tosin2013/helmdeck/issues/496' \
  --output trace-concept-animator.yaml
```

The CLI walks the session JSONL, pairs `toolCall` / `toolResult` events FIFO, tallies real pack invocations (not text claims), and emits a schema-compliant `community_traces[]` entry ready to paste into `models/openai-gpt-oss-120b-free.yaml`. Open a follow-on PR with the appended entry.

## What to capture for the empirical trace

For the YAML's `community_traces[]` entry:

| Metric | Notes |
|---|---|
| `real_pack_calls` | Total real pack invocations across the chain. Expected: 4 (`podcast.generate`, `hyperframes.compose`, `hyperframes.render`, `artifact.verify_manifest`) |
| `verify_manifest_called` | Boolean — did the audit-callback fire? |
| `all_present` | Boolean from the `verify_manifest` response. The chain is valid only when `true` |
| `hallucination_count` | Fake or paraphrased pack-result claims — count them |
| `simplification_observed` | Did the model take a shortcut? E.g., claiming `video_artifact_key` without rendering. Booleansish |
| `duration_handling` | "default 60s" / "max 720s" / "drift to other value" — qualitative |

Aim for `decision: profile-works` when the strict invalidation rules drove the model through all 4 calls and `all_present: true` came back honestly.

## Why this shape

The Tier C reliability literature (per the model profile YAML + PR #470 + PR #481/#484) is consistent: explicit invalidation rules + audit-callback close the simplification gap on medium-length chains where reasoning-only "remember to call X" framing fails. Framing each pack call as part of the success criteria — not as a numbered step the model can skip — is what makes the 3-call chain actually fire.

The 4th call (`artifact.verify_manifest`) is the load-bearing audit-callback: it gives the operator a yes/no machine-checkable result instead of a model-paraphrased text claim. This is exactly the pattern that closed the gpt-oss baseline failure mode in the original 2026-06-09 trace.

## Related

- Per-model profile: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml)
- Companion recipe: [`gpt-oss-120b-slide-narrator.md`](./gpt-oss-120b-slide-narrator.md) — same model, single-pipeline call instead of multi-pack chain
- Tracking issue: [#496](https://github.com/tosin2013/helmdeck/issues/496)
- Pack references: [`hyperframes.compose`](../../reference/packs/hyperframes/compose.md), [`hyperframes.render`](../../reference/packs/hyperframes/render.md), [`podcast.generate`](../../reference/packs/podcast/generate.md)
- ADR-052 (`av.validate` Phase 3 default-on integration): [`docs/adrs/052-av-output-validation-post-step.md`](../../adrs/052-av-output-validation-post-step.md)
- Audit-callback lineage: issues [#461](https://github.com/tosin2013/helmdeck/issues/461) / [#471](https://github.com/tosin2013/helmdeck/issues/471) / [#472](https://github.com/tosin2013/helmdeck/issues/472)
- Free-model recipe: [`docs/howto/add-free-models.md`](../add-free-models.md)
