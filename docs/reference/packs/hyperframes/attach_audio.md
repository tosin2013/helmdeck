---
title: hyperframes.attach_audio
description: Splice a narration audio track into a hyperframes scaffold project. Fourth (deterministic) step in the scaffolded-narrated-video pipeline. Closes the silent-video failure mode where upstream `hyperframes init --audio` is silently ignored by some examples.
keywords: [helmdeck, hyperframes, audio, narration, podcast, mp3, MCP]
---

# `hyperframes.attach_audio`

Take a project tarball (from [`hyperframes.scaffold`](./scaffold.md), [`hyperframes.interpolate`](./interpolate.md), or [`hyperframes.attach_asset`](./attach_asset.md)) plus an audio artifact key (typically from [`podcast.generate`](../podcast/generate.md)'s `audio_artifact_key` output), embed the audio bytes into the project's `assets/` directory, inject an `<audio>` element into the root composition div, optionally rewrite the root's `data-duration` to match the audio length, and re-upload the modified project. Fourth (deterministic) step in the chain:

```
hyperframes.scaffold     → scaffolded project (no audio yet)
       ↓
hyperframes.interpolate  → topic-specific text content
       ↓
hyperframes.attach_asset → (optional) A-roll image / video
       ↓
hyperframes.attach_audio → THIS PACK: narration spliced in
       ↓
hyperframes.render       → narrated MP4
```

## Why this pack exists — issue [#521](https://github.com/tosin2013/helmdeck/issues/521)

Upstream `hyperframes init` accepts an `--audio=<path>` flag, but **at least the `decision-tree` example silently ignores it** (and possibly others — empirical verification per upstream example). The flag is accepted (no error), but no `<audio>` element is written to `index.html`, no `assets/` directory is created, and `data-duration` stays at the example's intrinsic 15s. The rendered MP4 has no audio stream at all.

`hyperframes.scaffold`'s `audio_url` input passes this flag through, so when an unreliable example is picked, the pipeline produces a silent video despite `podcast.generate` successfully producing real narration. This pack is the deterministic alternative: pure-Go in-process tarball transform that works regardless of upstream example behavior.

The `builtin.scaffolded-narrated-video` pipeline now chains through this pack instead of relying on `hyperframes.scaffold`'s `audio_url` pass-through.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `project_artifact_key` | `string` | yes | — | A `hyperframes.*.tar.gz` artifact (gzipped tarball with `index.html` at root). |
| `audio_artifact_key` | `string` | yes | — | Audio bytes in the artifact store. Typically `podcast.generate`'s `audio_artifact_key` output. URL fetch is not supported in v1. |
| `duration_seconds` | `number` | yes | — | Authoritative audio duration. Caller is responsible (e.g. `podcast.generate`'s `duration_s` output); the pack does NOT probe the audio. |
| `volume` | `number` | no | `1.0` | Audio volume `[0.0, 1.0]`. Per upstream's `AUDIO VOLUME IS IMMUTABLE` rule, this is fixed at scaffold time and cannot be GSAP-tweened later. |
| `track_index` | `number` | no | `9` | Upstream's audio-track convention (tracks 0-1 = visual, 9+ = audio). Override only if your scaffold uses a different track scheme. |
| `update_root_duration` | `boolean` | no | `true` | When `true`, rewrites the root composition div's `data-duration` to `duration_seconds`. Set `false` only when `hyperframes.interpolate` already set it deliberately. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `project_artifact_key` | `string` | New project tarball with audio embedded. Pass to `hyperframes.render` (or further transforms). |
| `original_project_artifact_key` | `string` | Echo of the input. |
| `audio_filename` | `string` | Content-addressed filename: `aroll-audio-<sha256-prefix>.<ext>`. Same bytes → same name (dedup). |
| `audio_size` | `number` | Bytes embedded in the tarball. |
| `duration_seconds_used` | `number` | Echo of the input. |
| `root_duration_updated` | `boolean` | `true` when the root div's `data-duration` was rewritten. `false` when `update_root_duration:false` was passed OR the root div didn't have a `data-duration` attribute. |
| `track_index_used` | `number` | Resolved track index (input or default `9`). |
| `volume_used` | `number` | Resolved volume (input or default `1.0`). |

## Vault credentials needed

**None.** Pure-Go in-process — no dispatcher, no session executor, no external API calls. Just the artifact store.

## How the splice works

The pack:
1. Downloads the audio bytes from the artifact store
2. Computes a content-addressed filename: `aroll-audio-<sha256-prefix>.<ext>`
3. Downloads the project tarball + extracts in memory
4. Finds the root composition div in `index.html` by matching `data-composition-id="main"` (the canonical hyperframes scaffold convention)
5. Inserts `<audio src="assets/aroll-audio-...mp3" data-start="0" data-duration="<duration_seconds>" data-volume="<volume>" data-track-index="<track_index>"></audio>` as the first child of the root div
6. If `update_root_duration:true` (default), rewrites the root div's `data-duration` attribute to `duration_seconds` so the renderer plays the full audio (instead of truncating to the example's intrinsic duration)
7. Appends the audio file to the tarball under `assets/<filename>`
8. Re-packs + uploads

The root-div regex is anchored to `data-composition-id="main"` rather than `id="root"` because upstream examples vary in their id attribute but the composition-id is mandatory per upstream's contract.

## Supported audio content types

| MIME | Extension | Source |
|---|---|---|
| `audio/mpeg` | `.mp3` | ElevenLabs default (`mp3_44100_192`) |
| `audio/mp3` | `.mp3` | Alternate ElevenLabs / podcast pipelines |
| `audio/mp4` | `.m4a` | AAC-LC encoded audio |
| `audio/aac` | `.aac` | Bare AAC stream |
| `audio/wav` / `audio/x-wav` | `.wav` | Raw PCM |

## Error codes

| Code | Triggers |
|---|---|
| `invalid_input` | Missing/empty `project_artifact_key`, `audio_artifact_key`, or `duration_seconds <= 0`. Audio or project not in artifact store. Empty audio bytes. Audio exceeds 50 MiB cap. Unsupported content type. Root composition div not found in `index.html`. `index.html` missing from the tarball. |
| `internal` | No `Artifacts` wired into the ExecutionContext. |
| `handler_failed` | Tarball repackaging fails (rare; typically OOM). |
| `artifact_failed` | Artifact store `Put` fails on the modified tarball. |

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/hyperframes.attach_audio \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "project_artifact_key": "hyperframes.interpolate/abc...-interpolated.tar.gz",
    "audio_artifact_key":   "podcast.generate/def...-podcast.mp3",
    "duration_seconds":     96.339592
  }'
```

## See also

- Catalog row: [`PACKS.md`](/PACKS).
- Source: [`internal/packs/builtin/hyperframes_attach_audio.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/hyperframes_attach_audio.go).
- Companion packs in the four-pack scaffold chain: [`hyperframes.scaffold`](./scaffold.md), [`hyperframes.interpolate`](./interpolate.md), [`hyperframes.attach_asset`](./attach_asset.md), [`hyperframes.render`](./render.md).
- Pipeline: [`builtin.scaffolded-narrated-video`](/docs/reference/pipelines/scaffolded-narrated-video.md) — automated chain that uses this pack.
- Related issue: [#521 — hyperframes init --audio silently ignored](https://github.com/tosin2013/helmdeck/issues/521).
