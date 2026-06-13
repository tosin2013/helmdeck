---
description: "ADR-052: AV Output Validation as a Default-On Post-Encode Step — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 52. AV Output Validation as a Default-On Post-Encode Step

**Status**: Accepted (Phases 1–3 shipped in PRs [#428](https://github.com/tosin2013/helmdeck/pull/428), [#430](https://github.com/tosin2013/helmdeck/pull/430), [#431](https://github.com/tosin2013/helmdeck/pull/431), [#432](https://github.com/tosin2013/helmdeck/pull/432); this ADR is Phase 4 — the architecture record.)
**Date**: 2026-06-05
**Domain**: packs, avenc, observability, agent-integrations

## Context

Every "the video has issues" diagnostic before this arc shipped looked the same: authenticate, fetch the artifact from the store, pull it locally, run a multi-command ffprobe sweep, sample RMS at evenly-spaced points, check faststart with a Python atom walk, verify packet contiguity, eyeball duration parity, synthesize findings, write up the diagnosis. The single instance that motivated the arc — `slides.narrate/888de7b23142ba81-video.mp4` showing a 27.9-second audio/video duration mismatch — cost ~3,000 tokens to discover. The finding itself was trivially expressible as a JSON field on the producing pack's output: `{"consistency:audio_video_duration": {"pass": false, "detail": "delta=27.9s"}}`.

The forcing function was a research note the user surfaced about the broader MP4/MP3 validation tool landscape — MP4Box, Bento4, MediaConch, QCTools, mp3val, mp3check, untrunc. Most of that toolset doesn't apply to helmdeck, but the load-bearing observation does: **we generate the artifacts ourselves and we have a stable encoder pipeline.** That changes what validation needs to do. Instead of forensics on untrusted uploads, we need pass-or-fail per check with a typed structured output the agent can read in constant tokens. The cost ceiling matters more than the breadth of forensic coverage.

The arc shipped as four phases — the executable spec, the pack wrapping, the default-on integration, the architecture record — so each layer's value was provable against real artifacts before the next layer was built. Phase 1 caught the duration-mismatch bug it was looking for and revealed a deeper finding (the [PR #431](https://github.com/tosin2013/helmdeck/pull/431) `PadAudioToMin` apad swap, which fixed the underlying encoder bug; the consistency check that surfaced it now runs at its natural `fail` severity again). Phase 2's pack handler centralized the JSON parsing + known-issue demotion logic. Phase 3 made validation the default behavior, which is the only configuration where the token-savings payoff actually compounds.

## Decision

**Validation is a default-on post-encode step on `slides.narrate` and `podcast.generate`.** Every successful run produces a structured `validation` field in the pack output that the agent reads instead of re-deriving an ffprobe diagnostic. The validation result is data; the artifact is the value.

Five sub-decisions follow from this:

### 1. Tool selection: ffprobe + libavfilter only

The arc deliberately rejects the broader validation tool ecosystem the research surfaced. The rationale is per-tool, not blanket:

| Tool | Rejected because |
|---|---|
| **MP4Box / GPAC** | CVE risk (CVE-2026-9572, CVE-2026-7135, CVE-2025-70116 active during the arc) + functional redundancy with ffprobe for our use case. We don't need atom-level surgery; we need pass/fail per check. The GPAC mitigations matter for *future* operator-uploaded artifacts (out of scope here — noted under §"Scope boundary" below). |
| **Bento4 mp4dump** | Same: deep atom inspection isn't where our bugs live. Our encoder bugs land in audio packet timeline (mismatched duration metadata), bitstream-level corruption, container layout (faststart). All three are detectable with ffprobe + a 20-line Python `moov`-vs-`mdat` byte scan. |
| **mp3val / mp3check** | We control encoding to libmp3lame at 192 kbps via a fixed-arg ffmpeg invocation. Garbage MP3 frames aren't a realistic failure mode given the input is well-formed PCM. The class of bugs mp3val catches (encoder VBR header drift, broken frame sync from upstream concatenation of MP3s with different parameters) doesn't apply to a single-codec single-bitrate pipeline. |
| **QCTools / qcli** | Built for analog-tape forensics — vectorscope, VHS-degradation, U-Matic TBC analytics. Useful for media archives digitizing physical sources. Not useful for a slide-deck pipeline. |
| **MediaConch** | Policy-driven archival compliance for institutional archives. Operators can express their own policy in the pack's `skip_checks` input today; a full policy DSL is YAGNI for v0. |
| **untrunc** | Repairs truncated MP4s. We don't produce them; if we did, we'd fix the encoder. Matches the project norm "fix root causes, not symptoms" — exactly the lesson from PR #431's `PadAudioToMin` fix, where the temptation was to demote the consistency check forever and the right answer was the upstream apad swap. |
| **VFR detection (`vfrdet`)** | Our per-segment encode forces CFR via the libx264 default + `-shortest`/`-t`. No VFR risk in our pipeline. |

What we use: **ffprobe** for stream + packet enumeration, ffmpeg's existing **libavfilter detectors** (`silencedetect`, `blackdetect`, `freezedetect`, `ebur128`), the **null-muxer decode pass** (`-f null -` with `-xerror -err_detect crccheck+bitstream+buffer` per the research note's §"Deep Bitstream Decoding"), and **pure-Python byte scanning** for MP4 atom layout. No new toolchain deps beyond the existing sidecar ffmpeg + libavfilter + python3.

### 2. Severity model: `pass` / `warn` / `fail`

Each check has a single severity assigned at the script layer:

- **`fail`** is reserved for checks that match a **shipped bug fix**. Faststart (PR #422), codec pin + sample-rate (PR #421), packet contiguity (PR #404), RMS sample floor (the silent-fallback regression class), audio/video duration parity ([#429](https://github.com/tosin2013/helmdeck/issues/429) → [PR #431](https://github.com/tosin2013/helmdeck/pull/431)), SRT first-cue anchor + comma separator, captions coverage. The semantics are deliberate: a `fail`-severity check failing means a regression we have institutional evidence we don't want.
- **`warn`** is for soft heuristic findings — loudness LUFS out-of-window, silence runs ≥ 2 s, black-frame runs. Useful diagnostic signal; not load-bearing for the artifact's correctness.
- **`pass`** for everything that ran clean.

`all_passed:true` requires zero `fail`-severity failures. Warnings don't affect `all_passed`. Pipelines branch on `all_passed`; humans inspect `warnings` when they're curious.

This severity axis is **separate from the typed-error-code vocabulary in [ADR 008](008-typed-error-codes-for-weak-model-reliability.md).** A failed check is a quality finding — the script ran, the validation produced a structured report, the artifact still exists. A typed error is for "the operation couldn't proceed." They live on different axes and are surfaced through different fields. [ADR 008](008-typed-error-codes-for-weak-model-reliability.md) documents that distinction explicitly in its amendment paragraph below.

### 3. Known-issue demotion lifecycle

When a `fail`-severity check has a known underlying bug with an open tracking ticket, the pack handler (`internal/packs/builtin/av_validate.go`) maintains a `knownIssueDemotions` map keyed by check name → issue reference. A failing check whose name is in the map gets demoted to `warn` at the pack layer, with the issue reference appended to the `detail` string.

The mechanism's lifecycle has three rules to keep it honest:

1. **File the issue first.** A demotion entry without a corresponding tracking ticket is "we know it's broken and we're pretending it's not." That's the failure mode this whole arc was built to prevent.
2. **Same-PR coupling.** The demotion entry MUST be removed in the same PR that ships the upstream fix. This means the regression guard (the test that asserts the check now runs at `fail` severity) lands with the fix. If a future revert breaks the fix without reverting the test, the test catches it.
3. **No demotions for `warn`-severity checks.** Demotions only target `fail`-severity checks (the load-bearing ones). Demoting an already-soft check would be theatrical.

The map starts empty post-PR #431 and the test `TestAVValidate_NoDemotionsInForce` asserts that. New demotions reset that assertion temporarily — the PR adding a demotion entry necessarily changes the test, which forces a reviewer to consider whether the lifecycle is being honored.

### 4. Soft-surface contract

`av.validate` returns success by default even when checks fail. The pack's output IS the report; failing a pack over a `silence_runs` advisory would defeat the surface. The handler's strict-mode opt-in (`strict:true`) surfaces `fail`-severity failures as `CodeArtifactFailed` for CI publish gates and downstream consumers that can't tolerate processing structurally-invalid artifacts.

Default-on integration on `slides.narrate` / `podcast.generate` is soft-surface always — no strict mode escape hatch from those packs. The reasoning: the integration's load-bearing payoff is the agent reading the structured field. Failing the pack hides the field from the run record and forces the agent to re-derive what we already wrote down. Operators wanting fail-fast call `av.validate` standalone with `strict:true`.

### 5. Scope boundary

This ADR scopes validation to **helmdeck-generated artifacts only.**

The validation arc's encoder bug fixes (PR #422 faststart, PR #421 codec pin, PR #431 apad swap) all addressed bugs in **our** encoder pipeline. The check set was sized to catch those classes. Operator-uploaded artifacts have a different threat model:

- **Untrusted bitstreams** — bytes from an external source need adversarial parsing, not pass/fail. The GPAC CVE mitigations the research surfaced (CVE-2026-9572, CVE-2026-7135, CVE-2025-70116) apply here. Specifically: a sandboxed parser, a memory budget, an explicit format whitelist. None of those are required for our internal output.
- **Diverse codec profiles** — an upload could use AV1, Opus, MJPEG, or any of a hundred other valid combinations. Our `mp4:codec_pin` check rejects anything that isn't h264+aac@44100, which is correct for our output and wrong for a general upload validator.
- **VBR + non-CFR variants** — operator uploads from a phone or screencap tool can be VFR. Our pipeline can't be.

When operator-uploaded validation lands (the issue tracking it is filed separately), it gets a sibling pack — likely `av.validate_upload` — with its own check set, sandboxing posture, and CVE mitigations. It does NOT extend `av.validate`'s check set, because the load-bearing assumption (we know the encoder) doesn't carry over.

## Consequences

**Positive.** The token cost of "the video has issues" diagnostics drops from ~3,000 tokens per incident to ~200 tokens — the agent reads `validation.checks[]` from the run record. Across the avbench monthly cadence + ad-hoc operator queries, the saved budget compounds. Pipelines that need a publish gate get one via `strict:true`. Encoder bugs caught by `fail`-severity regressions surface immediately in the run record rather than being discovered weeks later when an operator notices uploads stopped importing captions. Per-PR same-PR coupling on demotions means the system can't silently regress without a test catching it.

**Positive (architectural).** The check set is the executable spec. New encoder bugs add new checks at their natural severity, and existing checks document their motivating PR/incident in their natural severity comment. The script is a junior engineer's reading material for "what kind of bugs do we ship in encoders."

**Negative.** A ~5–15-second null-muxer decode pass adds to every `slides.narrate` / `podcast.generate` run. Acceptable on top of the existing 60–180-second encode budget, but visible on tight benchmark cycles; the `validate:false` pointer-bool gives operators an opt-out. Memory peaks ~600 MB during the decode pass — handled by the existing `SessionSpec.MemoryLimit: 1g` policy ([ADR 045](045-pack-resource-sizing.md)'s amendment captures the bump).

**Negative.** The `knownIssueDemotions` mechanism is a foot-gun if the lifecycle rules drift. A future maintainer who adds a demotion without filing the tracking issue, or who removes a demotion before the fix lands, breaks the same-PR coupling that makes this honest. The mitigation is the lifecycle documentation here + the `TestAVValidate_NoDemotionsInForce` test that fails loud when the map changes shape.

**Negative.** Severity policy ossifies the project's encoder-bug taxonomy in the script. The `fail`-vs-`warn` distinction lives in `scripts/av-validate.sh` only; there's no centralized severity registry. A reviewer adding a check has to consciously pick severity. The mitigation is the documentation here naming the rule ("`fail` is reserved for shipped-bug-fix checks") and the comment-per-check in the script citing the motivating PR.

**Out of scope.** Operator-uploaded artifact validation (separate pack with sandboxing posture). Auto-publish based on validation result (publish is a separate decision; the artifact + the validation are the inputs to that decision, not the decision itself). A severity-promotion path for repeat-offender `warn` checks (we don't have evidence that any current `warn` check should be `fail`; revisit if we accumulate field data). Embeddings-based check selection ("run only relevant checks per artifact type") — the full check set runs in <15 s and the cost is dominated by the null-muxer pass, which isn't optional.

## See also

- [ADR 008](008-typed-error-codes-for-weak-model-reliability.md) — typed error vocabulary; amended paragraph explains the severity-vs-error-code axis.
- [ADR 015](015-pack-slides-video.md) — `slides.narrate`'s pack contract; amended to include the validation post-step.
- [ADR 045](045-pack-resource-sizing.md) — `SessionSpec.MemoryLimit` guidance; amended to capture the null-muxer-pass memory peak.
- [ADR 051](051-failure-mode-aware-dispatch.md) — `FailureClass` routing; amended to confirm validation findings are soft warnings, not routed via `FailureClass`.
- `scripts/av-validate.sh` — the executable spec.
- `internal/packs/builtin/av_validate.go` — the pack handler + `knownIssueDemotions` map.
- `internal/packs/builtin/slides_narrate.go` step 9b — the default-on integration site.
- `internal/packs/builtin/podcast_generate.go` step 9.5 — same.
- PRs #428 (Phase 1 script), #430 (Phase 2 pack), #431 (`PadAudioToMin` apad fix), #432 (Phase 3 integration). This PR is Phase 4.
