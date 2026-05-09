---
title: slides.narrate
description: Marp markdown → narrated 1080p MP4 with per-slide ElevenLabs TTS, optional YouTube metadata. Async; degrades to silent video when the ElevenLabs key is missing.
keywords: [helmdeck, slides, narrate, marp, elevenlabs, tts, video, youtube, MCP]
---

# `slides.narrate`

The "deck-to-narrated-video" pack. Caller hands in a Marp deck where each slide carries `<!-- speaker:notes -->` HTML comments. The pipeline runs entirely server-side:

1. **Marp render** — each slide becomes a 1920×1080 PNG.
2. **ElevenLabs TTS** — each slide's speaker notes become an MP3 using a vault-stored ElevenLabs key + a chosen voice.
3. **ffmpeg encode** — per-slide PNG + per-slide MP3 → per-slide MP4 segment, with optional cross-slide fade.
4. **ffmpeg concat** — all segments stitched into one final MP4.
5. **(Optional) LLM metadata synthesis** — if `metadata_model` is set, a frozen system prompt asks the model to generate a YouTube title, description with timestamps, tags, category, and language code, written as a separate JSON artifact.

The pack is **async** by default — calling `tools/call` returns a SEP-1686 task envelope immediately; the work runs in the background. SDK clients that speak SEP-1686 surface the eventual result transparently. Otherwise use `pack.start` / `pack.status` / `pack.result` or pass `webhook_url` + `webhook_secret`.

## Setup prerequisite

The pack runs without the ElevenLabs key (degrades to silent video, `has_narration: false`), but the typical case wants narration. Add via the *Vault* panel:

| Field | Value |
|---|---|
| **Name** | `elevenlabs-key` (exact string) |
| **Type** | `api_key` |
| **Host pattern** | `api.elevenlabs.io` |
| **Value** | Your ElevenLabs API key (`sk_…`) |

Get a key from <https://elevenlabs.io/app/settings/api-keys>. Free tier is 10,000 chars/month — plenty to validate a few decks end-to-end.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `markdown` | `string` | yes | — | Marp deck. **Must preserve `---` slide delimiters and `<!-- speaker:notes -->` HTML comments exactly** — agent prompts that escape or reformat the markdown will produce broken output. The frontmatter must start `---\nmarp: true\n---`. **Custom design** (themes, CSS) goes in the markdown's frontmatter — see [`slides.render` §"Custom design"](./render.md#custom-design-themes--css) for the syntax; the same Marp render is used internally here. |
| `voice_id` | `string` | no | random from top 5 popular voices | ElevenLabs voice ID. The pack queries `/v1/voices` and picks if unset; falls back to `EXAVITQu4vr4xnSDxMaL` (Rachel) on listing failure. |
| `model_id` | `string` | no | `"eleven_multilingual_v2"` | ElevenLabs model. `eleven_turbo_v2_5` is faster/cheaper; `eleven_multilingual_v2` handles non-English. |
| `resolution` | `string` | no | `"1920x1080"` | Video resolution. Smaller = lower memory (try `1280x720` if you OOM at 4K). |
| `fade_ms` | `number` | no | `0` | Cross-slide fade duration in ms. `300`–`500` looks polished. |
| `default_slide_duration` | `number` | no | `5.0` | Seconds of silence for slides without speaker notes. |
| `metadata_model` | `string` | no | — | Provider/model for YouTube metadata (e.g., `openrouter/openai/gpt-4o-mini`). When unset, no `metadata_artifact_key` is returned. |
| `webhook_url` | `string` | no | — | Push the result to this URL on completion (sync alternative to polling). |
| `webhook_secret` | `string` | no | — | HMAC signature secret for the webhook callback. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `video_artifact_key` | `string` | `slides.narrate/<rand>-deck.mp4`. Resolve via `/api/v1/artifacts/<key>`. |
| `video_size` | `number` | Bytes. Capped at 256 MiB. |
| `slide_count` | `number` | Number of slides rendered. |
| `total_duration_s` | `number` | Cumulative video length, post-TTS — the authoritative timing after ElevenLabs has actually synthesized. |
| `has_narration` | `boolean` | `true` if TTS succeeded; `false` if the ElevenLabs key was missing or the API errored on every slide. |
| `voice_used` | `string` | Voice ID that narrated. Empty when `has_narration: false`. |
| `metadata_artifact_key` | `string` | Present only when `metadata_model` was set. JSON file with the YouTube metadata. |
| `metadata` | `object` | Same content as `metadata_artifact_key`'s JSON, inline for convenience: `{title, description, tags, category, language}`. |

## Vault credentials needed

**`elevenlabs-key`** — type `api_key`, host pattern `api.elevenlabs.io`. **Optional** — without it the pack still ships an MP4, just silent.

## Use it from your agent (OpenClaw chat-UI worked example)

> 📌 **The transcript below shows the narrated path** (`has_narration: true`) — the `elevenlabs-key` is in the vault, ElevenLabs synthesized 2 slides of speech, and ffmpeg encoded them into a 199 KB MP4. The same prompt without the key in the vault produces a silent 47 KB MP4 (`has_narration: false`); the silent-fallback transcript was the original capture for this page. The transcript is also a clean reference for the **async polling pattern** (`pack.start → pack.status × N → pack.result`).

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Use helmdeck__slides-narrate with this 2-slide deck: "---\nmarp: true\n---\n# Helmdeck\n<!-- speaker:notes Welcome to a quick demo of the slides.narrate pack. -->\n\n---\n\n# Thanks\n<!-- speaker:notes See you next time. -->" and model_id=eleven_turbo_v2_5. Tell me the video_artifact_key, slide_count, total_duration_s, and whether has_narration is true.

**Tool call** (26 calls, no failures):

```json
{
  "name": "helmdeck__slides-narrate",
  "arguments": {
    "markdown": "---\nmarp: true\n---\n# Helmdeck\n<!-- speaker:notes Welcome to a quick demo of the slides.narrate pack. -->\n\n---\n\n# Thanks\n<!-- speaker:notes See you next time. -->",
    "model_id": "eleven_turbo_v2_5"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":0,"started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":0,"started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":5,"message":"parsed 2 slides","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":10,"message":"audio 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":10,"message":"audio 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":50,"message":"encoding segment 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":50,"message":"encoding segment 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-result",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"error":"not_ready","message":"pack.result: job 834267c19314e628263ab04b93313506 still working — keep polling pack.status"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":50,"message":"encoding segment 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":50,"message":"encoding segment 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":50,"message":"encoding segment 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":50,"message":"encoding segment 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":50,"message":"encoding segment 1/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":70,"message":"encoding segment 2/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":70,"message":"encoding segment 2/2","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"working","progress":90,"message":"concatenating final video","started_at":"2026-05-09T00:58:38Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"834267c19314e628263ab04b93313506","pack":"slides.narrate","state":"completed","progress":100,"message":"uploading video artifact","started_at":"2026-05-09T00:58:38Z","ended_at":"2026-05-09T00:59:19Z"}
```

```json
{
  "name": "helmdeck__pack-result",
  "arguments": {
    "job_id": "834267c19314e628263ab04b93313506"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"slides.narrate","version":"v1","output":{"has_narration":true,"metadata_artifact_key":"","slide_count":2,"total_duration_s":7.810612,"video_artifact_key":"slides.narrate/487371c8459e24ad-video.mp4","video_size":198916,"voice_used":"21m00Tcm4TlvDq8ikWAM"},"artifacts":[{"key":"slides.narrate/487371c8459e24ad-video.mp4","url":"http://garage:3900/helmdeck-artifacts/slides.narrate/487371c8459e24ad-video.mp4?X-Amz-Algorithm=AWS4-HMAC-SHA256\u0026X-Amz-Credential=GKaef177ebe9fd814dc103b84b%2F20260509%2Fgarage%2Fs3%2Faws4_request\u0026X-Amz-Date=20260509T005919Z\u0026X-Amz-Expires=900\u0026X-Amz-SignedHeaders=host\u0026X-Amz-Signature=dfc433b8c42e9bc554b9709da1182d58578335b4a93a89301e010446bf431cad","size":198916,"content_type":"video/mp4","created_at":"2026-05-09T00:59:19.304111422Z","pa
  ...[truncated]
```

**Agent's final reply**:

> video_artifact_key: `slides.narrate/487371c8459e24ad-video.mp4`  
> slide_count: 2  
> total_duration_s: 7.81 seconds  
> has_narration: true

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0187).*
## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/slides.narrate \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "markdown": "---\nmarp: true\n---\n# Helmdeck\n<!-- speaker:notes Welcome to a quick demo of the slides.narrate pack. -->\n\n---\n\n# Thanks\n<!-- speaker:notes See you next time. -->",
    "model_id": "eleven_turbo_v2_5"
  }'
```

Because the pack is `Async: true`, this returns a SEP-1686 task envelope immediately:

```json
{
  "_meta": {
    "modelcontextprotocol.io/related-task": {
      "taskId": "task-abc123"
    }
  },
  "content": [{"type": "text", "text": "task started"}]
}
```

Then poll `pack.status` (until `state == "completed"`) and call `pack.result` for the full output:

```json
{
  "pack": "slides.narrate",
  "version": "v1",
  "output": {
    "video_artifact_key": "slides.narrate/xyz789-deck.mp4",
    "video_size":         3915264,
    "slide_count":        2,
    "total_duration_s":   12.4,
    "has_narration":      true,
    "voice_used":         "EXAVITQu4vr4xnSDxMaL"
  }
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `markdown` empty | `markdown is required` |
| `invalid_input` | Marp render exit non-zero (malformed deck) | `marp exit N: <stderr>` |
| `handler_failed` | ElevenLabs API rejected the key (401) | Pack still ships silent video; `has_narration: false`. **Not** an error. |
| `handler_failed` | ffmpeg encoding failed (resolution OOM, missing codec) | `ffmpeg exit 137: …` (137 = SIGKILL, usually OOM — drop resolution) |
| `handler_failed` | Final video exceeds 256 MiB cap | `final video N bytes exceeds 256 MiB cap` |
| `timeout` | Pack-internal timeout (30 min default) | `pack timed out after 30 minutes` |

## Session chaining

**Required (creates if absent).** Each `slides.narrate` call gets a fresh session by default — high memory ceiling (3 GiB) for ffmpeg encoding. Stateless from the agent's perspective; the input is the deck.

## Async behavior

**`Async: true`.** Wall-clock scales with slide count: roughly **30–60 seconds per slide** at 1080p (TTS dominates, then per-segment ffmpeg). A 20-slide deck is typically 10–20 minutes end-to-end. Plan accordingly:

- **Path 1 (recommended on SDK clients)**: just call the pack normally; SEP-1686-aware SDKs auto-poll `tasks/get` and surface the result transparently when it lands. OpenClaw 2026.5+ supports this.
- **Path 2 (universal fallback)**: manual `pack.start` / `pack.status` / `pack.result` polling.
- **Path 3 (no polling)**: pass `webhook_url` + `webhook_secret`. The pack returns a task envelope immediately and POSTs the result to the webhook on completion.

See [SKILLS.md §"Long-running packs"](/integrations/SKILLS#long-running-packs--three-paths-in-priority-order) for the full decision table.

## YouTube optimization tips

`slides.narrate` is designed to produce videos in the **YouTube monetization sweet spot** (8–12 minutes — long enough for mid-roll ads at ≥8 min, short enough for retention). Each slide's on-screen time = the length of its TTS audio at ~150–160 wpm. Targets for a 20–25 slide deck:

| Words per slide (in `<!-- speaker:notes -->`) | Resulting video length |
|---|---|
| <30 | <4 min (too short for YouTube; feels thin) |
| 30–60 | 4–7 min (short-form) |
| **80–120** | **8–12 min (sweet spot)** |
| 150–200 | 15–20 min (long-form, viable for tutorials) |
| 250+ | 25+ min (risky on retention) |

When the user asks "make me a 10-minute video from N slides" without specifying word counts, target ~`1500/N` words per slide.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `slides.narrate`.
- Source: [`internal/packs/builtin/slides_narrate.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/slides_narrate.go).
- Companion packs: [`slides.render`](./render.md) (just the deck), [`pack.start`](/PACKS) / [`pack.status`](/PACKS) / [`pack.result`](/PACKS) (manual async polling).
- Vault setup: [`tutorials/install-ui-walkthrough.md`](/tutorials/install-ui-walkthrough#add-an-elevenlabs-key-elevenlabs-key).
