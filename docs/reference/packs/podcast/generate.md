---
title: podcast.generate
description: Multi-speaker (1..N) podcast MP3 via pluggable TTS engine. Three input modes — script, prompt+model, or long-form content (URL/text → LLM converts). Five themes bake in podcast best practices.
keywords: [helmdeck, podcast, tts, elevenlabs, multi-speaker, dialogue, MCP]
---

# `podcast.generate`

The "produce a podcast MP3" pack. Caller supplies a `speakers` map (1..N speaker name → voice ID) and one of three script-source modes:

- **Mode A — `script`**: agent provides the structured turns directly
- **Mode B — `prompt + model`**: pack calls the gateway LLM to generate dialogue from a prompt
- **Mode C — `source_url` or `source_text` + `model`**: pack scrapes long-form content (or accepts inline text) and converts it into speaker-tagged dialogue

The pack iterates each turn through the configured TTS engine, concats the per-turn MP3s with silence padding via ffmpeg, and returns a single MP3 artifact. **Day 1** ships **ElevenLabs** as the only engine; the `engine` input field is reserved so future PRs can add PlayHT, Hume.ai, Resemble.ai, etc. without touching the pack handler.

Five closed-set `theme`s bake podcast best-practices into the LLM system prompt (modes B and C):

| Theme | What you get |
|---|---|
| `interview` | Host + guest format, open-ended questions, guest does ~70% of talking, actionable takeaway |
| `debate` | Two opposing positions, steel-man required, moderator-style closer |
| `news-roundup` | 3–5 fast stories, sponsor-break placeholder at midpoint, "watching this week" closer |
| `deep-dive` | Single topic, narrative arc (problem → exploration → resolution → implication) |
| `solo-essay` | One speaker, monologue, written-for-the-ear pacing, 8–12 min sweet spot |

Distinct from [`tts.synthesize`](https://github.com/tosin2013/helmdeck/issues/70) (single-voice, single-line) and [`video.generate`](https://github.com/tosin2013/helmdeck/issues/69) (talking-head video).

## Setup prerequisite

For day-1 ElevenLabs engine, add the API key to the *Vault* panel:

| Field | Value |
|---|---|
| **Name** | `elevenlabs-key` (exact string — pack default; override with `credential` input) |
| **Type** | `api_key` |
| **Host pattern** | `api.elevenlabs.io` |
| **Value** | Your ElevenLabs API key (`sk_…`) |

Same credential as `slides.narrate`. **Optional** — without it, the pack still ships an MP3 (silent, with `has_narration: false`) so the structure stays intact for testing.

For mode C with `source_url`, the Firecrawl overlay must be enabled (`HELMDECK_FIRECRAWL_ENABLED=true`).

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `engine` | `string` | no | `"elevenlabs"` | Closed set; day 1 only `"elevenlabs"`. |
| `speakers` | `object` | yes | — | `{name: voice_id}` map. Non-empty. Use one entry for solo monologue, two+ for dialogue. |
| `script` | `array` | one-of | — | Mode A. `[{speaker, text}, ...]`. Every `speaker` value must exist in `speakers`. |
| `prompt` | `string` | one-of | — | Mode B. Plain-English description of what the podcast should be. Requires `model`. |
| `source_url` | `string` | one-of | — | Mode C-1. URL to scrape via Firecrawl. Requires `model` + Firecrawl overlay. |
| `source_text` | `string` | one-of | — | Mode C-2. Inline long-form markdown to riff on. Requires `model`. |
| `model` | `string` | with prompt/source_* | — | Provider/model for script generation. e.g. `openrouter/openai/gpt-4o-mini`. |
| `max_tokens` | `number` | no | `4096` | LLM cap for script generation. |
| `model_id` | `string` | no | `"eleven_turbo_v2_5"` | ElevenLabs TTS model. `eleven_turbo_v2_5` is fast/cheap; `eleven_multilingual_v2` for non-English. |
| `theme` | `string` | no | `"deep-dive"` | One of: `interview`, `debate`, `news-roundup`, `deep-dive`, `solo-essay`. Influences modes B/C only. |
| `duration_target_min` | `number` | no | `8` | LLM target length in minutes (modes B/C). At ~150 wpm, an 8-min target asks for ~1200 total words. |
| `silence_between_turns_ms` | `number` | no | `600` | Pause between consecutive turns (ms). 600ms feels conversational; 200ms feels rushed; 1000ms feels formal. |
| `generate_cover_prompt` | `boolean` | no | `false` | When `true`, output includes `cover_image_prompt` — a one-paragraph prompt the agent can pass to a future image-gen pack for cover art. |
| `cover_image` | `boolean` | no | `false` | When `true`, the pack auto-generates the cover via `image.generate` and surfaces `cover_image_artifact_key` in the output. Uses the same prompt as `generate_cover_prompt`. Honored only outside `dry_run`. Added v0.12.0 (#146). |
| `cover_image_model` | `string` | no | `"fal-ai/flux/schnell"` | fal.ai model used when `cover_image:true`. Browse choices via the `helmdeck://image-models` MCP resource. |
| `credential` | `string` | no | `"elevenlabs-key"` | Vault credential name. |

**Validation:**
- Exactly one of `script` / `prompt` / (`source_url` OR `source_text`)
- `prompt` and `source_*` modes require `model`
- Every speaker referenced in `script` (mode A) must exist in `speakers`
- `theme` must be in the closed set
- `engine` must be `"elevenlabs"` (day 1)
- `source_url` requires `HELMDECK_FIRECRAWL_ENABLED=true`

## Outputs

| Field | Type | Notes |
|---|---|---|
| `engine` | `string` | Echo. |
| `audio_artifact_key` | `string` | `podcast.generate/<rand>.mp3`. Resolve via `/api/v1/artifacts/<key>`. |
| `audio_size` | `number` | Bytes. |
| `duration_s` | `number` | Total length (sum of per-turn TTS + silence padding), measured by ffprobe. |
| `speaker_count` | `number` | Unique speakers actually appearing in the final script. |
| `turn_count` | `number` | Total turn count (number of speaker lines synthesized). |
| `script_source` | `string` | `"input"` / `"model"` / `"source_url"` / `"source_text"`. |
| `model_used` | `string` | Only when `script_source != "input"`. |
| `voices_used` | `object` | `{speaker: voice_id}` for speakers that appeared. |
| `has_narration` | `boolean` | `false` when the vault key was missing — MP3 contains silence (5s per turn). |
| `theme` | `string` | Echo. |
| `cover_image_prompt` | `string` | Only when `generate_cover_prompt: true`. |
| `cover_image_artifact_key` | `string` | Only when `cover_image: true`. Namespaced under `podcast.generate/`. Resolve via `/api/v1/artifacts/<key>`. |
| `cover_image_model_used` | `string` | Only when `cover_image: true`. Echoes the model that actually generated the cover. |

## Vault credentials needed

`elevenlabs-key` for day-1 ElevenLabs engine (same as `slides.narrate`). **Optional** — silent fallback when missing.

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste an OpenClaw chat-UI transcript here.
     Prompt to use: "Use helmdeck__podcast-generate in script mode with two speakers (Alex=21m00Tcm4TlvDq8ikWAM, Jordan=EXAVITQu4vr4xnSDxMaL) and this 4-turn script: Alex: 'Welcome back!'. Jordan: 'Today we discuss WebAssembly'. Alex: 'Why does it matter?'. Jordan: 'Performance and portability'. Theme deep-dive. Tell me the audio_artifact_key, duration_s, and has_narration." -->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

### Mode A — script (no LLM, no Firecrawl)

```bash
ADMIN_PW=$(grep HELMDECK_ADMIN_PASSWORD /root/helmdeck/deploy/compose/.env.local | cut -d= -f2)
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

curl -fsS -X POST http://localhost:3000/api/v1/packs/podcast.generate \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "speakers": {
      "Alex":   "21m00Tcm4TlvDq8ikWAM",
      "Jordan": "EXAVITQu4vr4xnSDxMaL"
    },
    "script": [
      {"speaker":"Alex",   "text":"Welcome back to the show. I'\''m Alex."},
      {"speaker":"Jordan", "text":"And I'\''m Jordan. Today we'\''re diving into WebAssembly."},
      {"speaker":"Alex",   "text":"What makes it interesting in 2026?"},
      {"speaker":"Jordan", "text":"Two things: performance parity with native, and portability across runtimes."}
    ],
    "theme": "deep-dive",
    "silence_between_turns_ms": 600
  }'
```

Response shape (truncated):

```json
{
  "pack": "podcast.generate",
  "version": "v1",
  "output": {
    "engine":             "elevenlabs",
    "audio_artifact_key": "podcast.generate/abc123.mp3",
    "audio_size":         512000,
    "duration_s":         34.2,
    "speaker_count":      2,
    "turn_count":         4,
    "script_source":      "input",
    "voices_used":        {"Alex":"21m00Tcm4TlvDq8ikWAM","Jordan":"EXAVITQu4vr4xnSDxMaL"},
    "has_narration":      true,
    "theme":              "deep-dive"
  }
}
```

### Mode B — prompt + model

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/podcast.generate \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "speakers": {
      "Host":  "21m00Tcm4TlvDq8ikWAM",
      "Guest": "EXAVITQu4vr4xnSDxMaL"
    },
    "prompt":              "Interview with a Rust expert about why Rust is gaining ground in 2026 backend systems.",
    "model":               "openrouter/openai/gpt-4o-mini",
    "theme":               "interview",
    "duration_target_min": 8,
    "generate_cover_prompt": true
  }'
```

The pack calls the gateway LLM with a frozen system prompt that bakes in the `interview` theme + the speaker names + the word target (8 × 150 ≈ 1200 words). The model returns structured JSON `[{speaker, text}, ...]` that the pack then synthesizes turn-by-turn.

### Mode C — long-form content → script

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/podcast.generate \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "speakers": {
      "Reader": "21m00Tcm4TlvDq8ikWAM"
    },
    "source_url":          "https://blog.example.com/long-form-essay",
    "model":               "openrouter/openai/gpt-4o-mini",
    "theme":               "solo-essay",
    "duration_target_min": 10
  }'
```

The pack scrapes the URL via Firecrawl, then asks the LLM to convert the content into a solo-essay-style script with the single speaker `Reader`.

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `speakers` missing or empty | `speakers map is required …` |
| `invalid_input` | None of script/prompt/source_* set | `must provide one of: script \| prompt+model \| source_url/source_text+model` |
| `invalid_input` | Multiple modes set | `must provide exactly one of: …` |
| `invalid_input` | `prompt`/`source_*` without `model` | `model is required when using prompt or source_url/source_text mode` |
| `invalid_input` | `theme` outside closed set | `theme must be one of: interview, debate, news-roundup, deep-dive, solo-essay …` |
| `invalid_input` | `engine` not `"elevenlabs"` | `engine must be "elevenlabs" (got …)` |
| `invalid_input` | Speaker in `script` not in `speakers` | `script[N]: speaker "X" not in speakers map (configured: A, B)` |
| `invalid_input` | `source_url` mode without Firecrawl | `source_url mode requires Firecrawl overlay …` |
| `invalid_input` | Source URL blocked by egress guard | `egress denied: …` |
| `internal` | Prompt/source mode without dispatcher | `podcast.generate prompt mode registered without a gateway dispatcher` |
| `handler_failed` | ElevenLabs API non-2xx (key invalid, rate-limited, voice not found) | `synthesize turn N: elevenlabs 401: …` |
| `handler_failed` | Firecrawl scrape failed | `scrape source_url: …` |
| `handler_failed` | ffmpeg concat failed | `concat: ffmpeg concat: exit N: …` |
| `session_unavailable` | Engine has no session executor | `engine has no session executor …` |
| `artifact_failed` | Object store write failed | `artifact upload failed: …` |

## Session chaining

**Required (creates if absent).** The pack runs ffmpeg in a session sidecar. Stateless from the agent's perspective; the session is implementation detail.

Common chains:

- `research.deep` → `podcast.generate` (`theme: news-roundup`) — turn a search-and-synthesis pass into a news-roundup-style podcast
- `web.scrape` → `podcast.generate` (`source_text` mode + `theme: solo-essay`) — re-narrate a single article as a solo essay
- `podcast.generate` (`generate_cover_prompt: true`) → future `image.generate` (#71) — cover-art pipeline

## Async behavior

**`Async: true`.** Wall-clock scales with turn count: ~2–4s per turn at typical TTS speeds, plus ~5–10s for ffmpeg concat at the end. A 24-turn deep-dive runs ~60–90s end-to-end. The pack reports progress via `ec.Report(pct, message)` so SDK clients can display "synthesizing 12/24 turns".

See [SKILLS.md §"Long-running packs"](/integrations/SKILLS#long-running-packs--three-paths-in-priority-order) for the SEP-1686 task-envelope decision table.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `podcast.generate`.
- Source (pack handler): [`internal/packs/builtin/podcast_generate.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/podcast_generate.go).
- Source (engine interface + ElevenLabs impl + concat): [`internal/podcast/`](https://github.com/tosin2013/helmdeck/tree/main/internal/podcast).
- Companion packs: [`slides.narrate`](../slides/narrate.md) (closely related — same vault credential, same ffmpeg infra), [`research.deep`](../research/deep.md) (canonical upstream for evidence-grounded shows), [`tts.synthesize`](https://github.com/tosin2013/helmdeck/issues/70) (single-voice TTS, future).
- ElevenLabs API docs: <https://elevenlabs.io/docs/api-reference/text-to-speech>
- Future engines: PlayHT (#TBD), Hume.ai (#TBD), Resemble.ai (#TBD) — each ships in its own PR by adding a new file under `internal/podcast/<engine>.go`.
