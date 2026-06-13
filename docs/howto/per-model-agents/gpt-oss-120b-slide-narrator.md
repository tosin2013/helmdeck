---
description: "Recipe for an OpenClaw slide-narrator agent on `openai/gpt-oss-120b:free` that turns a topic, raw text, or GitHub repo URL into a narrated Marp slide MP4 with SRT captions and YouTube engagement metadata via a single `helmdeck__pipeline-run` call."
---

# How to build a Slide Narrator agent on `openai/gpt-oss-120b:free`

This recipe shows how to set up an OpenClaw agent running `openai/gpt-oss-120b:free` that turns a topic, raw text, or GitHub repository URL into a narrated Marp slide-deck MP4 (~8–12 minutes, 20–25 slides) with an SRT captions sidecar and YouTube engagement metadata (title, chapters, hashtags). It closes part of [issue #496](https://github.com/tosin2013/helmdeck/issues/496) — the video-agents reference recipes for `gpt-oss-120b:free`.

The recipe is **model-family-specific**. Where the [concept-animator companion](./gpt-oss-120b-concept-animator.md) drives a 3-call pack chain, this one drives a **single** `helmdeck__pipeline-run` call. The choice is deliberate: Tier C models struggle with long tool chains (the profile YAML's `chain_call_reliability` rates 5+ call chains as low), so this agent offloads the orchestration server-side to one of helmdeck's built-in pipelines.

## When to use this recipe

Use it when you want a Tier C slide-narrator agent that reliably:

- Picks the correct built-in pipeline by input type (topic / raw text / GitHub repo URL)
- Calls `helmdeck__pipeline-run` with the right pipeline ID and input map
- Reports the pipeline's typed outputs (`mp4_artifact_key`, `srt_artifact_key`, `engagement_artifact_key`, plus the inline `engagement` object) without paraphrasing
- Targets the YouTube tutorial sweet spot (~8–12 minute runtime ≈ 80–120 words of speaker notes per slide across 20–25 slides) by trusting the pipeline's outline-to-narration mapping

It does NOT replace the underlying `slides.outline` / `slides.narrate` packs — it's the opinionated worked example of getting a Tier C model to delegate to a pipeline instead of trying to author Marp markdown itself.

## Worked example — Maya, security researcher

This recipe uses **Maya**, a hypothetical security researcher publishing technical explainers on YouTube about kernel observability, eBPF, and supply-chain attestation. Maya is sanitized — no real operator's identity, employer, or platform list. Adapt the persona to your own context.

## Pre-flight

- [ ] OpenRouter API key set; `openai/gpt-oss-120b:free` confirmed reachable
- [ ] Helmdeck packs / tools available: `helmdeck__pipeline-run`, `helmdeck__artifact-get`
- [ ] Built-in pipelines seeded (auto-seeded at control-plane startup per ADR-041): `builtin.research-narrate`, `builtin.grounded-narrate`, `builtin.repo-presentation`. Verify via `helmdeck__pipeline-list` if unsure.
- [ ] ElevenLabs API key configured for narration (otherwise pass `allow_silent_output: true` in the pipeline inputs for the grounded / repo variants)
- [ ] Per-model profile YAML reviewed: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml). Particular sections: `prompting_style`, `chain_call_reliability` (this recipe is the canonical *short* chain — 1 call — and exists to prove short chains are reliable for video work).

## Step 1 — Create the workspace

In OpenClaw, create a new agent workspace (e.g., `~/.openclaw/workspace-maya-narrator/`). Add the canonical OpenClaw files: `SOUL.md`, `IDENTITY.md`, `USER.md`, `AGENTS.md`. The persona files are yours to define; the recipe below focuses on `AGENTS.md`.

## Step 2 — Configure the model route

In OpenClaw's per-agent model config, set:

```
provider: openrouter
model: openai/gpt-oss-120b:free
sampling:
  temperature: 0.7
  top_p: 0.95
reasoning_effort: low
```

Why these values: the slide-narrator chain is one pack call plus a typed-output report. That's mostly delegation, not reasoning — `low` is the right effort level. The model profile's `reasoning_effort_defaults` puts "formatting" and "summarization" tasks at `low`, and this agent is a delegation task that maps onto that band. Bumping to `medium` is unnecessary overhead.

## Step 3 — AGENTS.md template

Copy the template below to `~/.openclaw/workspace-maya-narrator/AGENTS.md`. The template uses `gpt-oss-120b`'s preferred style — single OBJECTIVE, explicit CONSTRAINTS, machine-checkable SUCCESS CRITERIA framed as INVALIDATION RULES (per the model profile's `prompting_style: objectives_constraints_success_criteria` setting):

````markdown
# AGENTS.md — Maya's slide narrator on openai/gpt-oss-120b:free

This workspace turns one input (a topic, raw text, or a GitHub repo URL)
into a narrated slide-presentation MP4. The chain is exactly ONE pack call
plus a typed-output report. Per the helmdeck profile
models/openai-gpt-oss-120b-free.yaml, short chains (1–2 calls) are the
HIGH-reliability band — the whole agent is designed around staying there.

# OBJECTIVE

Convert the operator's input into a narrated 20–25 slide presentation MP4
with SRT captions and YouTube engagement metadata. Target runtime: 8–12
minutes (the YouTube tutorial sweet spot).

# SOURCE PRIORITY

1. The operator's most recent message (the input).
2. Prior turns in this conversation (for follow-up regenerations of the
   same input).
3. General knowledge (only for picking the appropriate persona /
   audience / angle hints when the operator hasn't specified them).

# CONSTRAINTS

- Do not author Marp markdown yourself. The built-in pipelines own the
  outline → narration → render chain.
- Select the pipeline by input type:
  - A topic / question / subject → `builtin.research-narrate`
  - Raw prose, notes, draft text → `builtin.grounded-narrate`
  - A GitHub repository URL → `builtin.repo-presentation`
- Call `helmdeck__pipeline-run` exactly ONCE. Do not chain
  `research.deep` / `content.ground` / `repo.fetch` / `slides.outline` /
  `slides.narrate` yourself — that's the pipeline's job.
- Pass the operator's input through unchanged. Do not paraphrase or
  re-summarize the input before handing it to the pipeline.
- If the input lacks a topic / angle / persona hint and the operator
  hasn't asked for help filling those in, omit them. The pipelines have
  sensible defaults.
- Word-count math (for the operator's mental model only — the pipeline
  enforces it): ElevenLabs runs at ~150 wpm, so 1 word of speaker notes
  ≈ 0.4 seconds of video. 20–25 slides × 80–120 words of notes each =
  ~8–12 minute target.

# SUCCESS CRITERIA (Invalidation Rules — applied strictly)

The response is INVALID and must NOT be reported as success when:

- `helmdeck__pipeline-run` was not called.
- The `id` passed to `pipeline-run` was not one of `builtin.research-narrate`,
  `builtin.grounded-narrate`, `builtin.repo-presentation`.
- The pipeline ID mismatches the input type (e.g., `builtin.research-narrate`
  with raw prose as the input, or `builtin.repo-presentation` without a
  repo URL).
- The response claims a final MP4 without showing the pipeline's typed
  output fields — at minimum `mp4_artifact_key` (or `video_artifact_key`)
  and `engagement_artifact_key`.
- Any pack result is paraphrased or invented as text instead of cited
  from the actual `pipeline-run` return.
- `slides.narrate` or any other pack inside the pipeline is called
  directly bypassing `pipeline-run`.

# NOTE ON engagement metadata

The pipeline returns BOTH an inline `engagement` object (with `title`,
`chapters`, `hashtags`, `tags`, `hook_30s`) AND an `engagement_artifact_key`
pointing at a JSON sidecar with the same data.

- For short summaries (a YouTube title + a line of hashtags), use the
  inline `engagement` object.
- For the full structured payload (the chapters array with timestamps,
  the full hashtag list, the hook), fetch the sidecar via
  `helmdeck__artifact-get` with the `engagement_artifact_key`.

# OUTPUT FORMAT

When the pipeline completes, report:

- The pipeline ID used and why (one line).
- The `mp4_artifact_key` (or `video_artifact_key`) of the rendered video.
- The `srt_artifact_key` of the captions sidecar.
- The proposed YouTube `title` and `chapters` summary from the inline
  `engagement` object.
- The `engagement_artifact_key` if the operator wants the full JSON.

Do not include any URL the operator did not see in a tool result.
````

## Step 4 — Test prompts

After bootstrapping the agent, run one prompt of each input type to verify pipeline selection:

**Topic input** (expects `builtin.research-narrate`):

```
Narrate a slide presentation on: How eBPF tracepoint observability
is changing kernel-rootkit detection in 2026.
```

**Raw text input** (expects `builtin.grounded-narrate`):

```
Narrate a slide presentation from this draft I wrote:

<paste 800-1200 words of prose>
```

**GitHub repo input** (expects `builtin.repo-presentation`):

```
Narrate a slide presentation explaining the architecture and design
choices of this repository: https://github.com/example/observability-tool
```

**Expected behavior**: each prompt produces exactly one `helmdeck__pipeline-run` call with the correct pipeline ID and an `inputs` map matching the input shape (`query` for the topic, `markdown` for the raw text, `repo_url` for the repo). The response reports the pipeline's typed outputs verbatim.

If the model:

- selects the wrong pipeline (e.g., `builtin.grounded-narrate` for the repo URL),
- skips the pipeline and tries to call `slides.narrate` directly,
- paraphrases a pipeline output instead of citing it,
- or fabricates a `mp4_artifact_key` that doesn't appear in the tool result,

that's a `gpt-oss-120b`-specific finding worth capturing in the [profile YAML's](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml) `community_traces[]` — see [`docs/howto/add-free-models.md` §7](../add-free-models.md) for the contribution path.

## Capture an empirical trace

After running the prompts (one per input type, or all three across a single session) against the agent, extract a community trace via the `helmdeck-trace` CLI:

```bash
./scripts/helmdeck-trace/helmdeck-trace extract \
  --session ~/.openclaw/agents/<workspace-name>/sessions/<session-id>.jsonl \
  --use-case slide-narration-agent \
  --contributor <your-github-handle> \
  --decision <profile-works|profile-helps-partially|profile-not-enough> \
  --url 'https://github.com/tosin2013/helmdeck/issues/496' \
  --output trace-slide-narrator.yaml
```

The CLI emits a schema-compliant `community_traces[]` entry ready to paste into `models/openai-gpt-oss-120b-free.yaml`. Open a follow-on PR with the appended entry.

## What to capture for the empirical trace

For the YAML's `community_traces[]` entry:

| Metric | Notes |
|---|---|
| `real_pack_calls` | Total real pack invocations. Expected: 1 (`helmdeck__pipeline-run`); higher means the model went off-script |
| `verify_manifest_called` | Likely `false` — the pipeline includes its own `av.validate` post-step on the narrated video, so explicit audit-callback is unnecessary unless the operator wants belt-and-braces |
| `all_present` | If `verify_manifest` was called, its result. Otherwise inferred from the pipeline's typed outputs being non-empty |
| `hallucination_count` | Fake or paraphrased pipeline-output claims |
| `simplification_observed` | Boolean — did the model take a shortcut? E.g., claiming a `mp4_artifact_key` without calling `pipeline-run`. (Expected: `true` in the sense of "correctly delegated to pipeline" — the success case looks like simplification because the model didn't try to write Marp itself.) |
| `pipeline_selection_correctness` | "all 3 correct" / "1 wrong" / etc. — qualitative |

Aim for `decision: profile-works` when the model selected the right pipeline per input type AND reported the typed outputs without paraphrasing.

## Why this shape

The Tier C reliability literature (per the model profile YAML's `chain_call_reliability` table) is consistent: short chains (1–2 calls) are the HIGH-reliability band. This recipe lives there by design. Where the [concept-animator companion](./gpt-oss-120b-concept-animator.md) trades higher reliability per call for end-to-end control over a 3-call chain, this recipe trades that control for a 1-call delegation pattern.

Framing the pipeline-selection logic as part of the invalidation rules — not as a "if/then/else" procedural decision tree — is the gpt-oss-specific lever. The model selects the pipeline by matching the input against the constraint set, then reports the result. Two model behaviors: pick + report.

## Related

- Per-model profile: [`models/openai-gpt-oss-120b-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/openai-gpt-oss-120b-free.yaml)
- Companion recipe: [`gpt-oss-120b-concept-animator.md`](./gpt-oss-120b-concept-animator.md) — same model, 3-call pack chain instead of single pipeline call
- Tracking issue: [#496](https://github.com/tosin2013/helmdeck/issues/496)
- Pipeline references: `builtin.research-narrate` / `builtin.grounded-narrate` / `builtin.repo-presentation` are defined in `internal/pipelines/seed.go:Builtins()`. See [`docs/reference/prompt-templates/pipelines.md`](../../reference/prompt-templates/pipelines.md) for the documented prompt templates.
- Pack references: [`slides.outline`](../../reference/packs/slides/outline.md), [`slides.narrate`](../../reference/packs/slides/narrate.md) (these are what the pipelines wrap)
- 150 wpm / 0.4s per word math: [`docs/integrations/SKILLS.md`](../../integrations/SKILLS.md) §slides
- ADR-041 (pipelines as a first-class resource): [`docs/adrs/041-pipelines-as-first-class-resource.md`](../../adrs/041-pipelines-as-first-class-resource.md)
- ADR-052 (`av.validate` Phase 3 default-on integration): [`docs/adrs/052-av-output-validation-post-step.md`](../../adrs/052-av-output-validation-post-step.md)
- Free-model recipe: [`docs/howto/add-free-models.md`](../add-free-models.md)
