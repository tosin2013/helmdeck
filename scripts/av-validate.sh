#!/usr/bin/env bash
# scripts/av-validate.sh — AV-artifact validator for slides.narrate /
# podcast.generate output. Runs a focused check set (the bugs we've
# actually shipped fixes for + a few cheap forensic heuristics) and
# emits either a colored human report or a structured JSON document.
#
# This is the executable spec for the `av.validate` pack that Phase 2
# will wrap. Tested against real artifacts before being lifted into
# pack form — same posture as PR #423's pipelines-smoke.sh mp4:av
# checks, which were prototyped here before being formalized.
#
# Motivation: every manual "the video has issues" diagnostic burns
# ~3000 tokens of bash output + analysis. A structured pass/fail per
# check, runnable on any path, collapses that to 100-500 tokens once
# the pack-form lands.
#
# Usage:
#   ./scripts/av-validate.sh --video PATH [--captions PATH] [--json]
#   ./scripts/av-validate.sh --audio PATH                  [--json]
#   ./scripts/av-validate.sh -h | --help
#
# Exit code:
#   0 — every applicable `fail`-severity check passed
#   1 — at least one `fail`-severity check failed
#   2 — usage / missing dependency
#
# Severity model:
#   fail — matches a shipped bug fix (faststart, codec, packet
#          contiguity, audio/video duration). Non-zero exit.
#   warn — soft heuristic (silence runs, freeze runs, black runs,
#          loudness). Doesn't fail the run; surfaces for review.
#   pass — check held.

set -uo pipefail

# ── config / defaults ───────────────────────────────────────────────
VIDEO=""
AUDIO=""
CAPTIONS=""
JSON_OUT=0
EBUR128_TARGET="${EBUR128_TARGET:--14}"   # YouTube default; -23 for broadcast
# Comma-separated check names to skip. slides.narrate output should
# pass "video:freeze_runs" — slide-deck videos hold a static image per
# slide so the freezedetect filter false-positives on every slide.
# Talking-head pipelines (if helmdeck ever ships one) should leave it on.
SKIP_CHECKS="${SKIP_CHECKS:-video:freeze_runs}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) sed -n '2,30p' "$0" | sed 's|^# \?||'; exit 0 ;;
    --video) VIDEO="$2"; shift 2 ;;
    --audio) AUDIO="$2"; shift 2 ;;
    --captions) CAPTIONS="$2"; shift 2 ;;
    --json) JSON_OUT=1; shift ;;
    --ebur128-target) EBUR128_TARGET="$2"; shift 2 ;;
    --skip-checks) SKIP_CHECKS="$2"; shift 2 ;;
    --no-skip) SKIP_CHECKS=""; shift ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

# is_skipped <check_name> — returns 0 (true) if the check should be
# skipped per SKIP_CHECKS.
is_skipped() {
  local needle="$1"
  case ",${SKIP_CHECKS}," in
    *,"$needle",*) return 0 ;;
    *) return 1 ;;
  esac
}

if [[ -z "$VIDEO" && -z "$AUDIO" ]]; then
  echo "must supply --video PATH or --audio PATH" >&2; exit 2
fi
for f in "$VIDEO" "$AUDIO" "$CAPTIONS"; do
  if [[ -n "$f" && ! -f "$f" ]]; then
    echo "file not found: $f" >&2; exit 2
  fi
done

# ── output helpers ──────────────────────────────────────────────────
RESULTS=()    # one entry per check: "name|severity|pass|detail"

record() {
  # record <name> <severity:fail|warn> <pass:0|1> <detail>
  local name="$1" sev="$2" pass="$3" detail="$4"
  RESULTS+=("$name|$sev|$pass|$detail")
}

red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }

# ── dependency checks ───────────────────────────────────────────────
need_ffprobe=0
for f in "$VIDEO" "$AUDIO"; do
  [[ -n "$f" ]] && need_ffprobe=1
done

if (( need_ffprobe )); then
  if ! command -v ffprobe >/dev/null || ! command -v ffmpeg >/dev/null; then
    echo "ffprobe/ffmpeg required for av-validate" >&2; exit 2
  fi
fi

# ═══════════════════════════════════════════════════════════════════
# MP4 (video) checks
# ═══════════════════════════════════════════════════════════════════

check_mp4_faststart() {
  # moov atom must appear BEFORE mdat for streaming players to begin
  # playback without downloading the full file. PR #422 regression.
  # Pure-Python so it runs even when ffprobe is absent.
  local detail
  detail=$(python3 - "$VIDEO" <<'PY'
import sys
data = open(sys.argv[1], "rb").read()
moov = data.find(b"moov")
mdat = data.find(b"mdat")
ok = 0 < moov < mdat
print(f"moov@{moov} mdat@{mdat} faststart_ok={ok}")
sys.exit(0 if ok else 1)
PY
)
  local rc=$?
  record "mp4:faststart" "fail" $((1-rc)) "$detail"
}

check_mp4_codec_pin() {
  # Codec + sample-rate pin per PR #421 + PR #422. h264 video, aac LC
  # audio, 44.1 kHz audio sample rate. Drift here surfaces encoder
  # regressions silently — exact same bug class as the v0.21 ar=48000
  # default that nobody noticed for weeks.
  local vcodec acodec aprof arate
  vcodec=$(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name \
           -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  acodec=$(ffprobe -v error -select_streams a:0 -show_entries stream=codec_name \
           -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  aprof=$(ffprobe -v error -select_streams a:0 -show_entries stream=profile \
          -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  arate=$(ffprobe -v error -select_streams a:0 -show_entries stream=sample_rate \
          -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  local pass=1 detail="vcodec=$vcodec acodec=$acodec acodec_profile=$aprof asample_rate=$arate"
  if [[ "$vcodec" != "h264" || "$acodec" != "aac" || "$arate" != "44100" ]]; then
    pass=0
  fi
  record "mp4:codec_pin" "fail" "$pass" "$detail"
}

check_mp4_bitstream_decode() {
  # Null muxer decode pass with -xerror — research §"Deep Bitstream
  # Decoding". The decoder traverses every frame; any corruption that
  # survived the muxer surfaces as a non-zero exit. Cheap (decode-only,
  # no re-encode, no disk writes) and catches macroblock corruption.
  local stderr
  stderr=$(ffmpeg -v error -xerror -err_detect crccheck+bitstream+buffer \
           -i "$VIDEO" -f null - 2>&1)
  local rc=$?
  if (( rc == 0 )); then
    record "mp4:bitstream_decode" "fail" 1 "null-muxer decode pass clean"
  else
    record "mp4:bitstream_decode" "fail" 0 "decoder rejected: $(echo "$stderr" | head -3 | tr '\n' '|')"
  fi
}

check_audio_packet_contiguity() {
  # Packet pts gap > 0.5s indicates mid-stream truncation (the
  # ElevenLabs partial-response cascade we saw in 2025-11).
  # Lifted from pipelines-smoke.sh:audio_packets_contiguous.
  local out
  out=$(ffprobe -v error -select_streams a -show_packets -of csv=p=0 "$VIDEO" 2>/dev/null \
    | awk -F',' '
        NR==1 { prev=$4+$8; first=1; next }
        { if ($4 - prev > 0.5) { printf "GAP@%.3fs delta=%.3fs after packet %d\n", $4, $4-prev, NR-1; bad=1; exit }
          prev = $4+$8 }
        END { if (!bad) print "OK no gaps" }
      ')
  if [[ "$out" == OK* ]]; then
    record "audio:packet_contiguity" "fail" 1 "no gaps > 0.5s"
  else
    record "audio:packet_contiguity" "fail" 0 "$out"
  fi
}

check_audio_rms_sweep() {
  # 5-point RMS sweep across the file. -45 dB floor catches silent-
  # fallback regressions. Lifted from pipelines-smoke.sh:audio_rms_above.
  local dur
  dur=$(ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  if [[ -z "$dur" ]] || awk -v d="$dur" 'BEGIN{exit (d<5)?0:1}'; then
    record "audio:rms_sweep" "warn" 1 "audio too short to sample meaningfully"
    return
  fi
  local i t rms minrms="0" minat=""
  for i in 1 2 3 4 5; do
    t=$(awk -v d="$dur" -v i="$i" 'BEGIN{printf "%.2f", d*i/6}')
    rms=$(ffmpeg -ss "$t" -t 2 -i "$VIDEO" -af volumedetect -vn -sn -dn -f null - 2>&1 \
           | awk '/mean_volume:/ {print $5}')
    [[ -z "$rms" ]] && continue
    if [[ -z "$minat" ]] || awk -v a="$rms" -v b="$minrms" 'BEGIN{exit (a<b)?0:1}'; then
      minrms="$rms"; minat="$t"
    fi
  done
  if awk -v rms="$minrms" 'BEGIN{exit (rms<-45)?0:1}'; then
    record "audio:rms_sweep" "fail" 0 "min mean RMS $minrms dB at t=${minat}s — below -45 floor (silent-fallback regression?)"
  else
    record "audio:rms_sweep" "fail" 1 "min mean RMS $minrms dB at t=${minat}s — above floor"
  fi
}

check_audio_loudness_lufs() {
  # EBU R128 integrated loudness (LUFS). YouTube target -14 ± 2.
  # Drift here means operators ship out-of-spec audio that platforms
  # may normalize aggressively. Surfaced as warn — not a hard fail.
  local lufs
  lufs=$(ffmpeg -i "$VIDEO" -af ebur128=peak=none -vn -f null - 2>&1 \
         | awk '/Integrated loudness:/{flag=1;next} flag && /^[ \t]*I:/{print $(NF-1); flag=0}')
  if [[ -z "$lufs" ]]; then
    record "audio:loudness_lufs" "warn" 1 "ebur128 did not return a reading (audio too short?)"
    return
  fi
  local lo hi
  lo=$(awk -v t="$EBUR128_TARGET" 'BEGIN{print t-2}')
  hi=$(awk -v t="$EBUR128_TARGET" 'BEGIN{print t+2}')
  if awk -v v="$lufs" -v lo="$lo" -v hi="$hi" 'BEGIN{exit (v>=lo && v<=hi)?0:1}'; then
    record "audio:loudness_lufs" "warn" 1 "$lufs LUFS within $EBUR128_TARGET±2"
  else
    record "audio:loudness_lufs" "warn" 0 "$lufs LUFS outside $EBUR128_TARGET±2 target window"
  fi
}

check_audio_silence_runs() {
  # silencedetect at -50dB, ≥2s. Catches dead-mic / dropout segments
  # that the per-point RMS sweep might miss between sample points.
  # Surfaced as warn — between-slide pauses can legitimately exceed
  # 2s and we don't want to fail the run over them.
  local runs
  runs=$(ffmpeg -i "$VIDEO" -af silencedetect=noise=-50dB:d=2 -vn -f null - 2>&1 \
         | grep -c silence_start)
  if (( runs == 0 )); then
    record "audio:silence_runs" "warn" 1 "no silence runs ≥ 2s at -50dB"
  else
    record "audio:silence_runs" "warn" 1 "$runs silence run(s) ≥ 2s (review if unexpected)"
  fi
}

check_video_freeze_runs() {
  # freezedetect — research §"Frozen Frame Detection". Catches encoder-
  # hang frame repeats. Surfaced as warn; we haven't seen this in our
  # pipeline yet but it's cheap and forensically valuable.
  local runs
  runs=$(ffmpeg -i "$VIDEO" -vf freezedetect=n=-60dB:d=2 -an -f null - 2>&1 \
         | grep -c freeze_start)
  if (( runs == 0 )); then
    record "video:freeze_runs" "warn" 1 "no freeze runs ≥ 2s"
  else
    record "video:freeze_runs" "warn" 0 "$runs freeze run(s) detected — possible encoder hang"
  fi
}

check_video_black_runs() {
  # blackdetect — accidental long black inserts (marp render failure
  # class). Tolerates short cinematic fades.
  local runs
  runs=$(ffmpeg -i "$VIDEO" -vf blackdetect=d=2.0:pix_th=0.10 -an -f null - 2>&1 \
         | grep -c black_start)
  if (( runs == 0 )); then
    record "video:black_runs" "warn" 1 "no black runs ≥ 2s"
  else
    record "video:black_runs" "warn" 0 "$runs black run(s) ≥ 2s — possible render failure"
  fi
}

check_consistency_audio_video_duration() {
  # THE bug we found on 888de7b... — audio stream ends but video keeps
  # playing. Compute audio_content_duration from packet count × frame
  # size / sample rate (canonical truth) and compare to container
  # duration. Tolerance: 1s.
  local arate aframes acount cdur audur delta abs
  arate=$(ffprobe -v error -select_streams a:0 -show_entries stream=sample_rate \
          -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  aframes=$(ffprobe -v error -select_streams a:0 -count_packets \
            -show_entries stream=nb_read_packets -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  cdur=$(ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  if [[ -z "$arate" || -z "$aframes" || -z "$cdur" ]]; then
    record "consistency:audio_video_duration" "fail" 0 "could not probe (arate=$arate aframes=$aframes cdur=$cdur)"
    return
  fi
  # AAC frame = 1024 samples. audio_content = aframes * 1024 / arate.
  audur=$(awk -v f="$aframes" -v r="$arate" 'BEGIN{printf "%.3f", f*1024/r}')
  delta=$(awk -v c="$cdur" -v a="$audur" 'BEGIN{printf "%.3f", c-a}')
  abs=$(awk -v d="$delta" 'BEGIN{printf "%.3f", (d<0)?-d:d}')
  if awk -v a="$abs" 'BEGIN{exit (a>1)?0:1}'; then
    record "consistency:audio_video_duration" "fail" 0 \
      "container=${cdur}s audio_content=${audur}s delta=${delta}s exceeds 1s tolerance"
  else
    record "consistency:audio_video_duration" "fail" 1 \
      "container=${cdur}s audio_content=${audur}s delta=${delta}s within tolerance"
  fi
}

# ═══════════════════════════════════════════════════════════════════
# SRT (captions) checks — only when --captions provided
# ═══════════════════════════════════════════════════════════════════

check_srt_first_cue_anchor() {
  local first
  first=$(awk 'NR==2{print; exit}' "$CAPTIONS")
  if [[ "$first" == "00:00:00,000 --> "* ]]; then
    record "srt:first_cue_anchor" "fail" 1 "first cue starts at 00:00:00,000"
  else
    record "srt:first_cue_anchor" "fail" 0 "first cue starts at '$first' — YouTube CC rejects non-zero anchor"
  fi
}

check_srt_comma_separator() {
  # SRT spec mandates comma decimal in timestamps. Period silently
  # parses as hours in some libass builds (7-hour-offset captions).
  if grep -qE '[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}' "$CAPTIONS"; then
    record "srt:comma_separator" "fail" 0 "found period decimal in timestamps — libass/YouTube parsers expect comma"
  else
    record "srt:comma_separator" "fail" 1 "all timestamps use comma decimal"
  fi
}

check_consistency_captions_coverage() {
  # Last SRT cue end should land within 2s of audio_content_duration.
  # If captions end well before the audio (the 27s gap we saw on
  # 888de7b... if the SRT were aligned to audio — actually it's
  # aligned, the bug there is between audio and video), the operator
  # has unaccounted narration past the captions.
  local last_end aframes arate audur
  last_end=$(grep -oE '[0-9]{2}:[0-9]{2}:[0-9]{2},[0-9]{3} --> [0-9]{2}:[0-9]{2}:[0-9]{2},[0-9]{3}' "$CAPTIONS" \
             | tail -1 | awk '{print $3}')
  if [[ -z "$last_end" ]]; then
    record "consistency:captions_coverage" "fail" 0 "could not parse last cue end from SRT"
    return
  fi
  # Convert HH:MM:SS,mmm to seconds.
  local h m s ms last_sec
  IFS=':,' read -r h m s ms <<<"$last_end"
  last_sec=$(awk -v h="$h" -v m="$m" -v s="$s" -v ms="$ms" 'BEGIN{printf "%.3f", h*3600+m*60+s+ms/1000}')

  # audio_content_duration via the same method as
  # check_consistency_audio_video_duration.
  arate=$(ffprobe -v error -select_streams a:0 -show_entries stream=sample_rate \
          -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  aframes=$(ffprobe -v error -select_streams a:0 -count_packets \
            -show_entries stream=nb_read_packets -of default=nw=1:nk=1 "$VIDEO" 2>/dev/null)
  if [[ -z "$arate" || -z "$aframes" ]]; then
    record "consistency:captions_coverage" "fail" 0 "could not probe audio stream for content duration"
    return
  fi
  audur=$(awk -v f="$aframes" -v r="$arate" 'BEGIN{printf "%.3f", f*1024/r}')
  local delta abs
  delta=$(awk -v a="$audur" -v c="$last_sec" 'BEGIN{printf "%.3f", a-c}')
  abs=$(awk -v d="$delta" 'BEGIN{printf "%.3f", (d<0)?-d:d}')
  if awk -v a="$abs" 'BEGIN{exit (a>2)?0:1}'; then
    record "consistency:captions_coverage" "fail" 0 \
      "audio_content=${audur}s last_cue_end=${last_sec}s delta=${delta}s exceeds 2s tolerance"
  else
    record "consistency:captions_coverage" "fail" 1 \
      "audio_content=${audur}s last_cue_end=${last_sec}s delta=${delta}s within tolerance"
  fi
}

# ═══════════════════════════════════════════════════════════════════
# Execution
# ═══════════════════════════════════════════════════════════════════

# run_check <check-name> <fn> — invokes the function unless the check
# is in SKIP_CHECKS. Skipped checks don't appear in RESULTS at all so
# downstream counters / JSON consumers can't misinterpret a skip as
# a pass.
run_check() {
  is_skipped "$1" && return 0
  "$2"
}

if [[ -n "$VIDEO" ]]; then
  run_check "mp4:faststart"                    check_mp4_faststart
  run_check "mp4:codec_pin"                    check_mp4_codec_pin
  run_check "mp4:bitstream_decode"             check_mp4_bitstream_decode
  run_check "audio:packet_contiguity"          check_audio_packet_contiguity
  run_check "audio:rms_sweep"                  check_audio_rms_sweep
  run_check "audio:loudness_lufs"              check_audio_loudness_lufs
  run_check "audio:silence_runs"               check_audio_silence_runs
  run_check "video:freeze_runs"                check_video_freeze_runs
  run_check "video:black_runs"                 check_video_black_runs
  run_check "consistency:audio_video_duration" check_consistency_audio_video_duration
fi
if [[ -n "$AUDIO" ]]; then
  # For audio-only artifacts (podcast.generate output), run the
  # audio-side checks against the audio path. The MP4 wrapper checks
  # don't apply. We reuse the same helpers by pointing VIDEO at the
  # audio file — ffprobe handles MP3 transparently. The codec_pin
  # check expects h264+aac+44100; skip when --audio rather than
  # --video was passed.
  VIDEO="$AUDIO"
  run_check "audio:packet_contiguity" check_audio_packet_contiguity
  run_check "audio:rms_sweep"         check_audio_rms_sweep
  run_check "audio:loudness_lufs"     check_audio_loudness_lufs
  run_check "audio:silence_runs"      check_audio_silence_runs
fi
if [[ -n "$CAPTIONS" ]]; then
  run_check "srt:first_cue_anchor" check_srt_first_cue_anchor
  run_check "srt:comma_separator"  check_srt_comma_separator
  if [[ -n "$VIDEO" ]]; then
    run_check "consistency:captions_coverage" check_consistency_captions_coverage
  fi
fi

# ── emit results ────────────────────────────────────────────────────
passed=0; failed=0; warned=0
for r in "${RESULTS[@]}"; do
  IFS='|' read -r name sev pass detail <<<"$r"
  case "$sev:$pass" in
    fail:1|warn:1) passed=$((passed+1)) ;;
    fail:0) failed=$((failed+1)) ;;
    warn:0) warned=$((warned+1)) ;;
  esac
done

if (( JSON_OUT )); then
  python3 - "$VIDEO" "$AUDIO" "$CAPTIONS" "$passed" "$failed" "$warned" <<PY "${RESULTS[@]}"
import json, sys
video, audio, captions, p, f, w = sys.argv[1:7]
checks = []
for r in sys.argv[7:]:
    name, sev, pass_, detail = r.split("|", 3)
    checks.append({"name": name, "severity": sev, "pass": bool(int(pass_)), "detail": detail})
out = {
    "video_path": video or None,
    "audio_path": audio or None,
    "captions_path": captions or None,
    "checks": checks,
    "passed": int(p), "failed": int(f), "warnings": int(w),
    "all_passed": int(f) == 0,
}
print(json.dumps(out, indent=2))
PY
else
  echo
  echo "── av-validate ────────────────────────────────────────────────────"
  for r in "${RESULTS[@]}"; do
    IFS='|' read -r name sev pass detail <<<"$r"
    case "$sev:$pass" in
      fail:1) green "  ✓ $name: $detail" ;;
      warn:1) green "  ✓ $name: $detail" ;;
      fail:0) red   "  ✗ $name (fail): $detail" ;;
      warn:0) yellow "  ! $name (warn): $detail" ;;
    esac
  done
  echo
  if (( failed > 0 )); then
    red "── result: FAIL — $passed passed, $warned warned, $failed failed"
  elif (( warned > 0 )); then
    yellow "── result: PASS WITH WARNINGS — $passed passed, $warned warned"
  else
    green "── result: PASS — $passed checks held"
  fi
fi

# Exit non-zero on any fail-severity failure (warns are advisory).
exit $(( failed > 0 ? 1 : 0 ))
