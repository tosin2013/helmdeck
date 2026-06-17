#!/usr/bin/env bash
# scripts/hyperframes-bare-baseline.sh — minimal end-to-end MP3-to-MP4
# reproducer that bypasses helmdeck, OpenClaw, and every LLM in the chain.
# Renders an upstream `hyperframes init --example=<x>` scaffold against
# a user-supplied audio file with matched root+child data-duration, then
# samples frames to detect the slot-lifetime bug (blank-canvas-after-
# child-timeline) and the empty-driver-timeline issue (no animation
# despite the composition having rich content).
#
# Why this exists:
#
# When a production helmdeck pipeline produces a video that "looks wrong,"
# four layers can be at fault: (1) upstream hyperframes itself, (2) the
# helmdeck Go packs (scaffold/interpolate/attach_audio/render), (3) the
# pipeline orchestration that chains them, (4) the LLM that authored
# whatever content was interpolated. Without a baseline, every diagnosis
# starts as "is this our bug or theirs?"
#
# This script is the baseline. It exercises the upstream rendering layer
# directly:
#   - upstream's `hyperframes init` for the scaffold (no helmdeck scaffold pack)
#   - hand-rewrites data-duration to match the user's audio (replicates
#     PR #546's child-composition-stretch logic outside the Go pack)
#   - hand-injects the `<audio>` element (replicates attach_audio's splice)
#   - upstream's `hyperframes render` for the MP4
#   - ffmpeg + md5 frame-sampling at known-interesting timestamps
#
# If THIS script produces meaningful animation, the bug is in helmdeck's
# pack orchestration. If THIS script also produces only static frames,
# the issue is upstream — either the scaffold itself lacks driver-
# timeline wiring, or the renderer doesn't auto-sequence sub-composition
# timelines into the host.
#
# Either way, the diagnostic is binary: same output as production →
# upstream layer is the culprit; different output → integration layer.
#
# Frame-sampling md5 contract:
#   - md5 `9c95fca010bc4b6747f415d2c1efad74` (8,680 bytes) = pure blank
#     canvas signature observed when the slot-lifetime bug fires
#   - any frame md5 NOT in the blank set = something rendered (could be
#     content, could be background-only)
#
# Usage:
#   hyperframes-bare-baseline.sh \
#       --audio=<path-to-mp3> \
#       [--example=<name>]      # default: kinetic-type (render-deterministic)
#       [--output=<path>]        # default: /tmp/hf-baseline-out/baseline.mp4
#       [--workdir=<path>]       # default: /tmp/hf-baseline-out
#       [--fps=<n>]              # default: 8 (draft quality)
#       [--max-duration=<n>]     # default: 720 (12 min — hyperframes pack cap)
#       [--lint=true|false]      # default: true (run `hyperframes lint` upfront)
#       [--no-lint]              # shorthand for --lint=false
#       [--image=<docker-image>] # default: helmdeck-sidecar-hyperframes:latest
#
# --example selection. The default kinetic-type is empirically render-
# deterministic (10 distinct frames over 10 samples in a 15s draft
# render). The earlier default decision-tree is render-hostile: even
# rendered unmodified from `npx hyperframes init`, it produces only 2
# distinct frames over 15 seconds (preview animates fine; render does
# not — see heygen-com/hyperframes#1540 + the "render ≠ preview" bug
# class upstream tracks at #1437). Other examples known to render
# deterministically: swiss-grid, warm-grain. To reproduce the v0.29.2/
# v0.29.3 blank-canvas regression for slot-lifetime testing, pass
# --example=decision-tree explicitly.
#
# Audio longer than --max-duration is clamped: only the first N seconds
# are used to size the composition and inject `<audio data-duration>`,
# matching the cap enforced by helmdeck's hyperframes.{compose,render,
# interpolate} packs (hyperframesComposeMaxDuration = 720). Set lower
# (e.g. 60) to speed up iteration during development.
#
# Output:
#   - Rendered MP4 at <output>
#   - JSON diagnostic at <workdir>/diagnostic.json:
#     {
#       "audio_seconds": <float>,
#       "rendered_seconds": <float>,
#       "rendered_frames": <int>,
#       "distinct_frames_sampled": <int>,
#       "blank_canvas_detected": <bool>,   # true iff any frame md5 = 9c95fca0...
#       "frames": [
#         { "t": 2,  "md5": "...", "size": 17897, "is_blank": false },
#         { "t": 10, "md5": "...", "size": 20816, "is_blank": false },
#         ...
#       ]
#     }
#   - Human-readable summary on stdout
#
# Exit codes:
#   0  baseline render completed; check diagnostic.json for findings
#   2  invalid arguments
#   3  audio file missing or unreadable
#   4  ffprobe couldn't read audio duration
#   5  sidecar image missing locally (run `docker pull <image>`)
#   6  render failed inside the sidecar
#   7  ffmpeg/ffprobe missing on host
#
# Companion to the v0.29.3 work tracked at:
#   - helmdeck PR #546 (the attach_audio fix that this script replicates inline)
#   - heygen-com/hyperframes#1540 (slot-lifetime upstream bug)
#   - helmdeck #547 (watch issue)
#   - blog: /blog/child-composition-slot-lifetime
#
# Principle: "upstream CLI takes precedence over custom Go" — this script
# is the upstream-only reference; if it works, our Go is the problem.

set -euo pipefail

#---------------------------------------------------------------------
# Arg parsing
#---------------------------------------------------------------------

AUDIO=""
# Default example: kinetic-type. Verified empirically (2026-06-17) to
# render with real animation (10 distinct frames over 10 samples in a
# 15s render at fps=12). decision-tree was the v0.13.0 default but is
# render-hostile per upstream's own `hyperframes lint`: it uses manual
# __timelines registration that conflicts with the runtime's auto-
# discovery, plus dynamic SVG drawing that only works in preview, not
# in render. Pass --example=decision-tree explicitly to reproduce the
# v0.29.2/v0.29.3 blank-canvas regression test bed.
EXAMPLE="kinetic-type"
OUTPUT=""
WORKDIR="/tmp/hf-baseline-out"
FPS="8"
MAX_DURATION="720"
LINT="true"
IMAGE="helmdeck-sidecar-hyperframes:latest"

for arg in "$@"; do
  case "$arg" in
    --audio=*)        AUDIO="${arg#*=}" ;;
    --example=*)      EXAMPLE="${arg#*=}" ;;
    --output=*)       OUTPUT="${arg#*=}" ;;
    --workdir=*)      WORKDIR="${arg#*=}" ;;
    --fps=*)          FPS="${arg#*=}" ;;
    --max-duration=*) MAX_DURATION="${arg#*=}" ;;
    --lint=*)         LINT="${arg#*=}" ;;
    --no-lint)        LINT="false" ;;
    --image=*)        IMAGE="${arg#*=}" ;;
    -h|--help)
      sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//; /^set -euo/d'
      exit 0
      ;;
    *)
      echo "unknown arg: $arg (try --help)" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$AUDIO" ]]; then
  echo "error: --audio=<path> is required" >&2
  exit 2
fi
if [[ ! -r "$AUDIO" ]]; then
  echo "error: audio file not readable: $AUDIO" >&2
  exit 3
fi

[[ -z "$OUTPUT" ]] && OUTPUT="$WORKDIR/baseline.mp4"

# Host-side dependencies
command -v ffprobe >/dev/null 2>&1 || { echo "error: ffprobe missing on host (install ffmpeg)" >&2; exit 7; }
command -v ffmpeg  >/dev/null 2>&1 || { echo "error: ffmpeg missing on host" >&2; exit 7; }
command -v docker  >/dev/null 2>&1 || { echo "error: docker missing on host" >&2; exit 7; }
command -v python3 >/dev/null 2>&1 || { echo "error: python3 missing on host" >&2; exit 7; }

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "error: sidecar image not present locally: $IMAGE" >&2
  if [[ "$IMAGE" == *"/"* ]]; then
    echo "       run: docker pull $IMAGE" >&2
  else
    echo "       run: docker pull ghcr.io/tosin2013/$IMAGE" >&2
  fi
  exit 5
fi

#---------------------------------------------------------------------
# Step 1 — read audio duration
#---------------------------------------------------------------------

AUDIO_SECONDS_RAW=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$AUDIO" 2>/dev/null || true)
if [[ -z "$AUDIO_SECONDS_RAW" || "$AUDIO_SECONDS_RAW" == "N/A" ]]; then
  echo "error: ffprobe could not determine audio duration" >&2
  exit 4
fi

# Clamp to --max-duration. Matches the cap enforced by helmdeck's
# hyperframes.{compose,render,interpolate} packs (720s default).
AUDIO_SECONDS=$(python3 -c "
raw = float('$AUDIO_SECONDS_RAW')
cap = float('$MAX_DURATION')
print(f'{min(raw, cap):.6f}')
")
CLAMPED=$(python3 -c "
raw = float('$AUDIO_SECONDS_RAW')
cap = float('$MAX_DURATION')
print('True' if raw > cap else 'False')
")
if [[ "$CLAMPED" == "True" ]]; then
  echo "[baseline] audio: $AUDIO ($AUDIO_SECONDS_RAW s, clamped to $AUDIO_SECONDS s by --max-duration=$MAX_DURATION)"
else
  echo "[baseline] audio: $AUDIO ($AUDIO_SECONDS seconds)"
fi

#---------------------------------------------------------------------
# Step 2 — prepare workdir + stage audio
#---------------------------------------------------------------------

mkdir -p "$WORKDIR"
chmod 777 "$WORKDIR"
PROJDIR="$WORKDIR/project"
rm -rf "$PROJDIR"
mkdir -p "$PROJDIR/assets"
AUDIO_EXT="${AUDIO##*.}"
AUDIO_FILE_IN_PROJECT="assets/aroll-audio.${AUDIO_EXT}"

# If clamped, trim the audio file to AUDIO_SECONDS so the final MP4's
# audio track ends at the same point as the rendered video timeline.
# Otherwise just copy.
if [[ "$CLAMPED" == "True" ]]; then
  echo "[baseline] trimming audio to first $AUDIO_SECONDS s via ffmpeg"
  ffmpeg -y -loglevel error -i "$AUDIO" -t "$AUDIO_SECONDS" -c copy \
    "$PROJDIR/$AUDIO_FILE_IN_PROJECT" 2>&1 | tail -3
else
  cp "$AUDIO" "$PROJDIR/$AUDIO_FILE_IN_PROJECT"
fi
chmod -R 777 "$WORKDIR"

#---------------------------------------------------------------------
# Step 3 — `hyperframes init` inside the sidecar
#
# Why inside the sidecar: the sidecar image has Node 22 + hyperframes
# globally installed + the bundled headless Chromium. The host might
# not have any of that. Mount workdir so the scaffold lands on disk.
#---------------------------------------------------------------------

echo "[baseline] scaffolding $EXAMPLE via 'hyperframes init' inside sidecar"
docker run --rm --entrypoint /bin/bash \
  -v "$WORKDIR:/work" \
  -w /work \
  "$IMAGE" \
  -lc "rm -rf project-tmp && hyperframes init --example=$EXAMPLE project-tmp >/dev/null 2>&1 && cp -r project-tmp/. project/ && rm -rf project-tmp" || {
    echo "error: scaffold failed" >&2
    exit 6
}

# Re-stage audio after init may have overwritten assets/
if [[ "$CLAMPED" == "True" ]]; then
  ffmpeg -y -loglevel error -i "$AUDIO" -t "$AUDIO_SECONDS" -c copy \
    "$PROJDIR/$AUDIO_FILE_IN_PROJECT" 2>&1 | tail -3
else
  cp "$AUDIO" "$PROJDIR/$AUDIO_FILE_IN_PROJECT"
fi
chmod -R 777 "$WORKDIR"

#---------------------------------------------------------------------
# Step 4 — rewrite root + child data-duration, inject <audio>
#
# This is what helmdeck's hyperframes.attach_audio pack does, written
# inline so the script has no helmdeck Go dependency. Uses python for
# robust HTML editing — sed regex over multi-line HTML is fragile.
#---------------------------------------------------------------------

echo "[baseline] rewriting data-duration to $AUDIO_SECONDS + injecting <audio>"
python3 - <<PYEOF
import re, pathlib
idx = pathlib.Path("$PROJDIR/index.html")
src = idx.read_text(encoding="utf-8")
# Find root's original data-duration (the one on the div with
# data-composition-id="main"). Conservative: only stretch children
# whose data-duration matched root's original (PR #546 heuristic).
root_match = re.search(r'<div[^>]*data-composition-id\s*=\s*"main"[^>]*>', src)
if not root_match:
    raise SystemExit("no root composition div found in index.html")
root_tag = root_match.group(0)
orig_dur_m = re.search(r'data-duration\s*=\s*"([^"]+)"', root_tag)
if not orig_dur_m:
    raise SystemExit("root has no data-duration")
orig_dur = orig_dur_m.group(1)
new_dur = "$AUDIO_SECONDS"
# Rewrite EVERY data-duration on a div carrying data-composition-id
# whose current value equals orig_dur. Covers root + any child that
# was span-aligned with it.
def rewrite_div(m):
    tag = m.group(0)
    if 'data-composition-id' not in tag:
        return tag
    return re.sub(r'(data-duration\s*=\s*")' + re.escape(orig_dur) + r'(")',
                  r'\g<1>' + new_dur + r'\g<2>', tag)
src = re.sub(r'<div[^>]*data-composition-id\s*=\s*"[^"]+"[^>]*>', rewrite_div, src)
# Inject audio as first child of the (now-rewritten) root div
audio_tag = (f'<audio src="$AUDIO_FILE_IN_PROJECT" data-start="0" '
             f'data-duration="$AUDIO_SECONDS" data-volume="1" '
             f'data-track-index="9"></audio>')
src = re.sub(r'(<div[^>]*data-composition-id\s*=\s*"main"[^>]*>)',
             r'\1' + audio_tag, src, count=1)
idx.write_text(src, encoding="utf-8")
print("[baseline] index.html rewritten")
PYEOF

#---------------------------------------------------------------------
# Step 4b — `hyperframes lint --json` (pre-render gate)
#
# Catches render-killing issues BEFORE burning the render budget. Most
# important: `media_missing_id` (audio without id is silent in renders)
# and `gsap_studio_edit_blocked` (manual __timelines registration that
# conflicts with the runtime's auto-discovery). Soft surface: lint
# findings are reported but don't gate the render unless --no-lint is
# omitted AND there are error-severity findings.
#---------------------------------------------------------------------

LINT_ERRORS=0
LINT_WARNINGS=0
LINT_OK="true"
if [[ "$LINT" == "true" ]]; then
  echo "[baseline] running hyperframes lint (pre-render gate)"
  LINT_JSON=$(docker run --rm --entrypoint /bin/bash \
    -v "$WORKDIR:/work" \
    -w /work/project \
    "$IMAGE" \
    -lc "hyperframes lint --json 2>/dev/null | sed -n '/^{/,$p'" 2>/dev/null || echo '{}')
  echo "$LINT_JSON" > "$WORKDIR/lint.json"
  LINT_ERRORS=$(python3 -c "import json,sys; d=json.loads(open('$WORKDIR/lint.json').read() or '{}'); print(d.get('errorCount',0))")
  LINT_WARNINGS=$(python3 -c "import json,sys; d=json.loads(open('$WORKDIR/lint.json').read() or '{}'); print(d.get('warningCount',0))")
  LINT_OK=$(python3 -c "import json; d=json.loads(open('$WORKDIR/lint.json').read() or '{}'); print('true' if d.get('ok', True) else 'false')")
  echo "[baseline] lint: errors=$LINT_ERRORS warnings=$LINT_WARNINGS ok=$LINT_OK"
  if (( LINT_ERRORS > 0 )); then
    echo "[baseline] lint errors detected (rendering anyway for diagnostic):"
    python3 -c "
import json
d = json.loads(open('$WORKDIR/lint.json').read() or '{}')
for f in d.get('findings', []):
    if f.get('severity') == 'error':
        print(f'  [{f[\"code\"]}] {f[\"message\"][:140]}')
        if f.get('fixHint'):
            print(f'    fix: {f[\"fixHint\"][:140]}')
"
  fi
fi

#---------------------------------------------------------------------
# Step 5 — render via `hyperframes render` in the sidecar
#---------------------------------------------------------------------

echo "[baseline] rendering at fps=$FPS (draft quality)"
docker run --rm --entrypoint /bin/bash \
  -v "$WORKDIR:/work" \
  -w /work/project \
  "$IMAGE" \
  -lc "hyperframes render --fps $FPS --quality draft --output baseline.mp4 2>&1 | tail -3" || {
    echo "error: render failed" >&2
    exit 6
}

cp "$PROJDIR/baseline.mp4" "$OUTPUT"

#---------------------------------------------------------------------
# Step 6 — diagnostic: ffprobe + frame md5 sampling
#---------------------------------------------------------------------

# Sample at key timestamps. 14s is just past the canonical 15s scaffold
# child-timeline boundary; 100s/200s/300s catch any blank-canvas state
# that would hold for the rest of the audio.
read RENDERED_SECONDS RENDERED_FRAMES <<< $(ffprobe -v error -select_streams v:0 \
  -show_entries stream=duration,nb_frames \
  -of csv=p=0 "$OUTPUT" | tr ',' ' ')

BLANK_MD5="9c95fca010bc4b6747f415d2c1efad74"
SAMPLE_TIMESTAMPS=(2 7 14 17 22 45 70)
# Add timestamps within the rendered duration only
if (( $(printf '%.0f\n' "$RENDERED_SECONDS") > 100 )); then
  SAMPLE_TIMESTAMPS+=(100)
fi
if (( $(printf '%.0f\n' "$RENDERED_SECONDS") > 200 )); then
  SAMPLE_TIMESTAMPS+=(200)
fi
LAST_T=$(printf '%.0f\n' "$RENDERED_SECONDS")
LAST_T=$((LAST_T - 1))
SAMPLE_TIMESTAMPS+=($LAST_T)

declare -a HASHES SIZES
BLANK_DETECTED=False
DISTINCT_HASHES=""
for t in "${SAMPLE_TIMESTAMPS[@]}"; do
  frame_png="$WORKDIR/frame_t${t}.png"
  ffmpeg -y -loglevel quiet -ss "$t" -i "$OUTPUT" -frames:v 1 -update 1 "$frame_png" 2>/dev/null
  if [[ ! -f "$frame_png" ]]; then continue; fi
  hash=$(md5sum "$frame_png" | awk '{print $1}')
  size=$(stat -c%s "$frame_png")
  HASHES+=("$hash")
  SIZES+=("$size")
  if [[ "$hash" == "$BLANK_MD5" ]]; then BLANK_DETECTED=True; fi
  if [[ "$DISTINCT_HASHES" != *"$hash"* ]]; then
    DISTINCT_HASHES="$DISTINCT_HASHES $hash"
  fi
done
DISTINCT_COUNT=$(echo "$DISTINCT_HASHES" | wc -w)

#---------------------------------------------------------------------
# Step 7 — emit diagnostic.json + human summary
#---------------------------------------------------------------------

python3 - <<PYEOF > "$WORKDIR/diagnostic.json"
import json
timestamps = ${SAMPLE_TIMESTAMPS[@]@Q}.split()
hashes = ${HASHES[@]@Q}.split()
sizes  = ${SIZES[@]@Q}.split()
blank_md5 = "$BLANK_MD5"
frames = [
    {"t": int(t), "md5": h, "size": int(s), "is_blank": h == blank_md5}
    for t, h, s in zip(timestamps, hashes, sizes)
]
out = {
    "example": "$EXAMPLE",
    "lint": ({
        "ran": True,
        "ok": "$LINT_OK" == "true",
        "error_count": $LINT_ERRORS,
        "warning_count": $LINT_WARNINGS,
    } if "$LINT" == "true" else {"ran": False}),
    "audio_seconds_raw": float("$AUDIO_SECONDS_RAW"),
    "audio_seconds": float("$AUDIO_SECONDS"),
    "max_duration_cap": float("$MAX_DURATION"),
    "clamped": $CLAMPED,
    "rendered_seconds": float("$RENDERED_SECONDS"),
    "rendered_frames": int("$RENDERED_FRAMES"),
    "distinct_frames_sampled": $DISTINCT_COUNT,
    "blank_canvas_detected": $BLANK_DETECTED,
    "frames": frames,
}
print(json.dumps(out, indent=2))
PYEOF

echo
echo "[baseline] === diagnostic ==="
echo "  example: $EXAMPLE"
echo "  audio: $AUDIO_SECONDS s"
echo "  rendered: $RENDERED_SECONDS s, $RENDERED_FRAMES frames"
echo "  distinct frames sampled: $DISTINCT_COUNT"
echo "  blank canvas detected: $BLANK_DETECTED"
if [[ "$LINT" == "true" ]]; then
  echo "  lint: errors=$LINT_ERRORS warnings=$LINT_WARNINGS ok=$LINT_OK"
fi
echo "  output: $OUTPUT"
echo "  diagnostic.json: $WORKDIR/diagnostic.json"
echo
if [[ "$BLANK_DETECTED" == "True" ]]; then
  echo "  VERDICT: slot-lifetime bug fired — blank canvas after child timeline."
  echo "           Either the rewrite didn't catch all child data-durations,"
  echo "           or upstream is producing a NEW blank-frame signature."
elif (( DISTINCT_COUNT < 3 )); then
  echo "  VERDICT: rendered but only $DISTINCT_COUNT distinct frame(s) across $(printf '%.0f' "$RENDERED_SECONDS") seconds."
  echo "           Slot stays alive (good — PR #546 logic works), but the"
  echo "           composition isn't animating. Likely an empty driver"
  echo "           timeline in index.html — sub-composition's own GSAP"
  echo "           timeline never gets sequenced. This is the bug to chase next."
else
  echo "  VERDICT: rendered with $DISTINCT_COUNT distinct frames — composition IS animating."
  echo "           If production helmdeck pipelines see fewer distinct frames,"
  echo "           the bug is in the integration layer, not upstream."
fi
