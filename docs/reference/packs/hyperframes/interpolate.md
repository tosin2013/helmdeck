---
title: hyperframes.interpolate
description: Rewrite the visible text content (titles, stats, caption transcript) in a hyperframes scaffold to fit the user's topic. Second link in the scaffold-video pipeline.
keywords: [helmdeck, hyperframes, interpolate, video, mp4, transcript, scaffolding, MCP]
---

# `hyperframes.interpolate`

Take a `project_artifact_key` from [`hyperframes.scaffold`](./scaffold.md), run LLM passes over each `compositions/*.html` file to rewrite the visible text content so it fits the user's topic, and re-upload the modified project as a new `project_artifact_key`.

Second link in the four-pack scaffold-video chain:

```
hyperframes.scaffold     → scaffolded project (upstream's generic placeholder content)
       ↓
hyperframes.interpolate  → THIS PACK: visible text now matches the user's topic
       ↓
hyperframes.attach_asset → A-roll image / video spliced into #short_mag_cut_frame
       ↓
hyperframes.render       → MP4
```

## How content is detected

Each `compositions/*.html` file is classified by content pattern (one LLM pass per file):

| Detected pattern | Strategy | Example slots rewritten |
|---|---|---|
| `<h1>`, `<h2>`, `<h3>`, `<div class="stat-value">`, `<div class="stat-label">` | **html_text_slots** — extract each slot's inner text, ask the LLM to rewrite each on-topic in a numbered format, splice back. | `intro.html` title + subtitle. `graphics.html` stat values + labels. |
| `const TRANSCRIPT = [{text, start, end}, ...];` | **js_transcript** — ask the LLM to generate a fresh word-level transcript timed against `duration_seconds`, replace the array literal wholesale. | `captions.html` caption word array. |
| neither pattern present | **unknown_shape** — pass through unchanged. Reported in `files_skipped`. | exotic upstream examples that use different content shapes. |

If NO file in the scaffold matches a recognized shape, the pack rejects as `invalid_input` with the message "no files in the scaffold matched a recognized content shape — try a different upstream example."

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `project_artifact_key` | `string` | yes | — | Key from `hyperframes.scaffold`'s output (or any project tarball satisfying the scaffold contract). |
| `description` | `string` | yes | — | The video topic. The LLM rewrites visible text to fit this. |
| `model` | `string` | yes | — | Provider/model id. Tier C (gpt-oss-120b:free, gemma) gets a verbose constraint-heavy prompt; Tier A/B gets a lean one. |
| `duration_seconds` | `number` | no | `8.0` | Used to pace the caption transcript at ~150 wpm. Pair with `podcast.generate`'s `duration_s` output for a narrated video. Capped at 720s. |
| `audio_note` | `string` | no | `""` | Extra context for the LLM (e.g. "narration is calm and educational" — surfaces in both prompts). |
| `max_tokens` | `number` | no | `4096` | Per-LLM-call token budget. Clamped to [1024, 8192]. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `project_artifact_key` | `string` | NEW key — the modified project tarball. Feeds `hyperframes.attach_asset` or `hyperframes.render`. |
| `original_project_artifact_key` | `string` | Echo of the input key. |
| `files_rewritten` | `array` | Each entry: `{path, kind, original_size, new_size}`. The interpolate manifest. |
| `files_skipped` | `array` | Paths the pack skipped, with reason (`unknown_shape`, `rewrite error: ...`, `file cap`). |
| `model_used` | `string` | Echo of the input model. |

## Behavior

### Soft-degrade on per-file LLM failure
If the LLM fails on one file (returns no choices, dispatcher error, parse error), that file is added to `files_skipped` and processing continues on the next file. The pack only fails the whole call when ZERO files got rewritten.

### Caption-transcript timing is heuristic, not whisper-aligned
The LLM generates a TRANSCRIPT array timed to `duration_seconds` at a ~150 wpm cadence. This produces caption text that's *approximately* aligned to the audio narration but is NOT word-by-word synced to actual speech. For broadcast-quality captioning, a separate whisper pass would be needed after the audio is finalized. The heuristic is fine for explainer/social videos where rough alignment is acceptable.

### Token-budget per file
Each `compositions/*.html` file gets its own LLM call. For a typical 3-composition scaffold (intro + graphics + captions) that's three calls. The pack does NOT batch them into one prompt — the format requirements per file (numbered slots vs JSON transcript) are different enough that batched prompting confuses Tier C reliably.

## Validation & errors

| Failure | Code | Notes |
|---|---|---|
| Missing `project_artifact_key` / `description` / `model` | `invalid_input` | Specific message per field. |
| `project_artifact_key` not in store | `invalid_input` | Surfaces store error. |
| Empty artifact content | `invalid_input` | Catches truncated upload. |
| Malformed tarball (not gzip / not tar) | `invalid_input` | gzip decompress / tar reader errors are surfaced. |
| No files matched any recognized shape | `invalid_input` | Try a different upstream example. |
| Single-file LLM failure | (logged, not fatal) | File appears in `files_skipped`; other files still rewrite. |
| All-files LLM failure | `invalid_input` | Same path as "no files matched" — `rewritten` is empty. |
| Artifact upload failure | `artifact_failed` | Backend issue. |

## Chaining example

Hand-chained:

```sh
# After hyperframes.scaffold returned KEY_SCAFFOLD.
KEY_INTERP=$(curl -X POST .../packs/hyperframes.interpolate/v1/execute \
  -d "{
    \"project_artifact_key\":\"$KEY_SCAFFOLD\",
    \"description\":\"eBPF tracepoint observability for kernel rootkit detection\",
    \"model\":\"openrouter/openai/gpt-oss-120b:free\",
    \"duration_seconds\":15
  }" | jq -r .output.project_artifact_key)

# Continue chain: hyperframes.attach_asset, then hyperframes.render.
```

Pipeline-orchestrated (recommended): the `builtin.scaffolded-narrated-video` pipeline (PR 7) wires this in automatically — operators pass `description`, `model`, and `example`, the pipeline handles the rest.

## See also

- [`hyperframes.scaffold`](./scaffold.md) — produces the input `project_artifact_key`.
- [`hyperframes.attach_asset`](./attach_asset.md) — splices an A-roll image into the modified project.
- [`hyperframes.render`](./render.md) — consumes the final project tarball, produces an MP4.
- Issue [#503](https://github.com/tosin2013/helmdeck/issues/503) — architectural decision and pipeline split rationale.
