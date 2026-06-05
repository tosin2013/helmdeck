---
id: av-validate
title: av.validate
---

# `av.validate`

Structured validation for `slides.narrate` / `podcast.generate` AV artifacts. Runs a focused check set (faststart, codec pin, packet contiguity, RMS sweep, loudness LUFS, audio/video duration parity, SRT format compliance, etc.) and returns a typed `validation` object the agent can read in ~200 tokens rather than re-deriving a ~3,000-token ffprobe diagnostic each time.

This pack is **Phase 2 of a 4-phase validation arc**, now complete. Phase 1 shipped the standalone [`scripts/av-validate.sh`](https://github.com/tosin2013/helmdeck/blob/main/scripts/av-validate.sh) in [PR #428](https://github.com/tosin2013/helmdeck/pull/428). Phase 3 ([PR #432](https://github.com/tosin2013/helmdeck/pull/432)) wired this pack as a default-on post-step on `slides.narrate` and `podcast.generate`. Phase 4 captured the architecture in [ADR 052](../../adrs/052-av-output-validation-post-step.md), including the per-tool rationale for what we chose (ffprobe + libavfilter) and what we rejected (GPAC, Bento4, QCTools, MediaConch, mp3val, untrunc).

## When to use

- **Operator-triggered validation** — pass an artifact key, get back a structured report.
- **CI publish gate** — pass `strict:true`; fail-severity check failures surface as a typed `CodeArtifactFailed` error.
- **Phase 3 integration** — `slides.narrate` / `podcast.generate` will call this internally with `video_path` (file already in session `/tmp`); the validation result lands as a `validation` field on the producing pack's output.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `video_artifact_key` | string | one of v/a | — | Fetched from artifact store; written to `/tmp/av-validate-video.mp4`. |
| `audio_artifact_key` | string | one of v/a | — | Fetched + written to `/tmp/av-validate-audio.mp3`. |
| `captions_artifact_key` | string | no | — | Fetched + written to `/tmp/av-validate-captions.srt`. |
| `video_path` | string | one of v/a | — | Direct path (skips fetch). Wins over `video_artifact_key`. |
| `audio_path` | string | one of v/a | — | Direct path. |
| `captions_path` | string | no | — | Direct path. |
| `ebur128_target` | number | no | `-14` (YouTube) | EBU R128 target. `-23` for broadcast. Out-of-window readings surface as `warn`. |
| `skip_checks` | string | no | `video:freeze_runs` | Comma-separated check names to skip. `video:freeze_runs` is default-skipped because slide-deck videos hold a static image per slide. |
| `strict` | boolean | no | `false` | When `true`, any `fail`-severity check failure surfaces as a typed `CodeArtifactFailed` error. Default `false` (soft surface — findings ARE the output). |

At least one of `video_artifact_key` / `video_path` / `audio_artifact_key` / `audio_path` must be supplied. Captions alone aren't enough — the SRT checks need an audio stream to compare against for coverage.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `validation` | object | The structured report. See shape below. |
| `validation_artifact_key` | string | `av.validate/<rand>-validation.json`. Persisted sidecar mirroring `engagement.json` / `captions.srt` patterns. |

### `validation` shape

```json
{
  "video_path": "/tmp/probe-video.mp4",
  "audio_path": null,
  "captions_path": "/tmp/probe-captions.srt",
  "checks": [
    {"name": "mp4:faststart", "severity": "fail", "pass": true, "detail": "moov@36 mdat@568069 faststart_ok=True"},
    {"name": "consistency:audio_video_duration", "severity": "warn", "pass": false,
     "detail": "container=693.344s audio_content=665.414s delta=27.930s exceeds 1s tolerance (known issue, tracked in #429)"},
    ...
  ],
  "passed": 11,
  "failed": 0,
  "warnings": 1,
  "all_passed": true
}
```

`all_passed` is `true` when no `fail`-severity check failed. Warnings don't affect `all_passed`.

## Severity policy + known-issue demotion

The script reports each check at its natural severity:

- **`fail`** — matches a shipped bug fix. Faststart, codec pin, packet contiguity, RMS floor, audio/video duration parity, SRT first-cue anchor, SRT comma separator, captions coverage.
- **`warn`** — soft heuristic. Loudness LUFS, silence runs, black frame runs.

The pack **overrides** the script's severity for checks listed in the internal `knownIssueDemotions` map. When a `fail`-severity check is in the map, the pack demotes it to `warn` and appends the tracking-issue reference to the detail string.

The demotion is coupled to the tracking issue, not to a release calendar. When the fix lands, the **same PR** removes the entry from `knownIssueDemotions` — bumping severity back to `fail` together with the underlying fix. The same-PR coupling makes the regression guard impossible to silently leave behind.

**Currently no demotions are in force.** The `consistency:audio_video_duration` demotion was removed in the same PR that landed the apad-swap fix in `encodeSegment` (issue [#429](https://github.com/tosin2013/helmdeck/issues/429) closed). Fresh `slides.narrate` outputs now produce content-accurate audio stream durations; the check runs at its natural `fail` severity again. New demotion entries added in the future follow the same lifecycle: file the tracking issue first, add the entry with the issue reference, remove it in the same PR that ships the upstream fix.

## Strict mode (`strict: true`)

When `strict:true`:

- Any `fail`-severity check failure (after known-issue demotion) surfaces as `CodeArtifactFailed` with the failing check names in the message
- The pipeline run shows as failed
- The agent's typed-error recovery path per [ADR 008](../../adrs/008-typed-error-codes-for-weak-model-reliability.md) decides whether to retry, escalate, or report

Use `strict:true` when:

- A CI publish gate must reject broken builds
- A downstream consumer can't tolerate processing a structurally-invalid artifact
- Operator policy demands fail-fast behavior

Use default (`strict:false`) when:

- The findings should surface but the pipeline must keep going
- An LLM agent should read the structured `validation` field and reason about it
- Phase 3 wired this pack as a post-step on a content-producing pipeline

## Runtime-error vs check-finding distinction

These are different things and produce different outputs:

| Situation | Result |
|---|---|
| Validation ran; some checks failed | Returns success with `validation.failed > 0`. Caller reads the field. |
| Validation ran; same as above but `strict:true` | Returns `CodeArtifactFailed` with failing check names. |
| Script exited 2 (missing dependency / usage error) | Returns `CodeHandlerFailed`. The validation DIDN'T RUN — distinct from "ran and reported issues." |
| `ec.Exec` returned a transport error | Returns `CodeHandlerFailed` with the underlying transport error. |
| `video_artifact_key` couldn't be fetched | Returns `CodeArtifactFailed` with the fetch error. |
| No video/audio inputs supplied | Returns `CodeInvalidInput`. |

## Verification

The standalone script's [acceptance test](https://github.com/tosin2013/helmdeck/pull/428) — running against `slides.narrate/888de7b23142ba81-video.mp4` — IS the canonical functional test for the validator's check set. The pack's unit tests cover the JSON parsing, known-issue demotion, strict mode, and runtime-error surface (`internal/packs/builtin/av_validate_test.go`, 7 tests).

For end-to-end verification against the running stack, invoke via REST:

```bash
curl -X POST http://localhost:3000/api/v1/packs/av.validate/run \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"video_artifact_key":"slides.narrate/<key>-video.mp4"}'
```

The response carries the `validation` field. `validation.all_passed: true` for a healthy artifact (or one whose only failure is a known-issue demotion); `false` when a real `fail`-severity check broke.

## Related

- [PR #428](https://github.com/tosin2013/helmdeck/pull/428) — Phase 1 standalone script
- [PR #430](https://github.com/tosin2013/helmdeck/pull/430) — Phase 2 (this pack)
- [PR #431](https://github.com/tosin2013/helmdeck/pull/431) — `PadAudioToMin` apad swap (closed #429; promoted `consistency:audio_video_duration` back to `fail` severity)
- [PR #432](https://github.com/tosin2013/helmdeck/pull/432) — Phase 3 default-on integration on `slides.narrate` / `podcast.generate`
- [ADR 052](../../adrs/052-av-output-validation-post-step.md) — Phase 4 architecture record (tool selection rationale, severity model, demotion lifecycle, soft-surface contract, scope boundary)
