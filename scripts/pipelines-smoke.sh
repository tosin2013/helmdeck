#!/usr/bin/env bash
# scripts/pipelines-smoke.sh — live, opt-in smoke test for the built-in
# PIPELINES. Runs real pipelines against an ALREADY-RUNNING stack via the
# REST API and asserts on the ACTUAL artifact each one produces — the
# layer the hermetic Go tests (internal/pipelines/builtins_run_test.go)
# can't cover because they stub the packs.
#
# It exists because builtin.grounded-deck once "ran successfully" while
# silently dropping the back half of the deck (a 2048-token rewrite cap).
# A run-status of "succeeded" is NOT enough — we fetch the artifact and
# check its shape: a 5-slide deck must render 5 PDF pages.
#
# Like scripts/smoke-integration.sh this is NON-destructive (never
# compose up/down) and INTEGRATION-AWARE: pipelines whose packs need
# Firecrawl/Docling are skipped (not failed) when those overlays are off.
# Only builtin.html-video is fully offline and always runs.
#
# Usage:
#   ./scripts/pipelines-smoke.sh            # run every gate-satisfied case
#   HELMDECK_URL=... HELMDECK_PASS=... ./scripts/pipelines-smoke.sh
#   ./scripts/pipelines-smoke.sh -h
#
# Exits 0 if every gate-satisfied case passed, non-zero on the first
# failure with the run record + artifact diagnosis.

set -euo pipefail

# ── config (override via env) ─────────────────────────────────────
HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
HELMDECK_USER="${HELMDECK_USER:-admin}"
HELMDECK_PASS="${HELMDECK_PASS:-}"
HELMDECK_ENV_FILE="${HELMDECK_ENV_FILE:-/root/helmdeck/deploy/compose/.env.local}"
HELMDECK_CONTAINER="${HELMDECK_CONTAINER:-helmdeck-control-plane}"
RUN_TIMEOUT_SECS="${RUN_TIMEOUT_SECS:-180}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) sed -n '2,33p' "$0" | sed 's|^# \?||'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 64 ;;
  esac
done

red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
blue()   { printf '\033[34m%s\033[0m\n' "$*"; }

require_cmd() { command -v "$1" >/dev/null 2>&1 || { red "missing prerequisite: $1"; exit 2; }; }

read_admin_password() {
  if [[ -n "$HELMDECK_PASS" ]]; then return; fi
  if [[ -f "$HELMDECK_ENV_FILE" ]]; then
    HELMDECK_PASS=$(grep '^HELMDECK_ADMIN_PASSWORD=' "$HELMDECK_ENV_FILE" | cut -d= -f2-)
  fi
  [[ -n "$HELMDECK_PASS" ]] || { red "HELMDECK_PASS not set and not in $HELMDECK_ENV_FILE"; exit 2; }
}

mint_jwt() {
  curl -fsS -X POST "$HELMDECK_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$HELMDECK_USER\",\"password\":\"$HELMDECK_PASS\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])'
}

# Read an integration flag from the live container env first
# (authoritative), falling back to .env.local. Same discipline as
# scripts/smoke-integration.sh.
integration_enabled() {
  local var="$1" val=""
  val=$(docker exec "$HELMDECK_CONTAINER" sh -c "printf '%s' \"\${$var:-}\"" 2>/dev/null || true)
  if [[ -z "$val" && -f "$HELMDECK_ENV_FILE" ]]; then
    val=$(grep "^${var}=" "$HELMDECK_ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)
  fi
  [[ "$val" == "true" || "$val" == "1" ]]
}

gate_ok() {
  case "$1" in
    always)     return 0 ;;
    firecrawl)  integration_enabled HELMDECK_FIRECRAWL_ENABLED ;;
    docling)    integration_enabled HELMDECK_DOCLING_ENABLED ;;
    elevenlabs) elevenlabs_enabled ;;
    *) return 1 ;;
  esac
}

# Start a pipeline run; echo the run_id.
start_run() {
  local id="$1" inputs="$2"
  curl -fsS -X POST "$HELMDECK_URL/api/v1/pipelines/$id/run" \
    -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
    -d "{\"inputs\":$inputs}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["run_id"])'
}

# Poll a run to a terminal state; echo the final run JSON.
poll_run() {
  local id="$1" run_id="$2" deadline=$((SECONDS + RUN_TIMEOUT_SECS)) body status
  while (( SECONDS < deadline )); do
    body=$(curl -fsS -H "Authorization: Bearer $JWT" \
      "$HELMDECK_URL/api/v1/pipelines/$id/runs/$run_id") || true
    status=$(printf '%s' "$body" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("status",""))' 2>/dev/null || echo "")
    case "$status" in
      succeeded|failed) printf '%s' "$body"; return 0 ;;
    esac
    sleep 3
  done
  printf '%s' "${body:-{\}}"
  return 1
}

# Extract the terminal step's artifact key from a run JSON (prefers the
# recorded artifacts[] surfaced by the runner, falls back to the step
# output's artifact_key).
terminal_artifact_key() {
  python3 -c '
import sys, json
run = json.load(sys.stdin)
steps = run.get("steps", [])
if not steps:
    sys.exit(0)
last = steps[-1]
arts = last.get("artifacts") or []
if arts:
    print(arts[0].get("key","")); sys.exit(0)
out = last.get("output") or {}
if isinstance(out, str):
    try: out = json.loads(out)
    except Exception: out = {}
print(out.get("artifact_key") or out.get("video_artifact_key") or "")
'
}

run_status() { python3 -c 'import sys,json;print(json.load(sys.stdin).get("status",""))'; }
run_error()  { python3 -c 'import sys,json;print(json.load(sys.stdin).get("error",""))'; }

fetch_artifact() {
  local key="$1" out="$2"
  curl -fsS -H "Authorization: Bearer $JWT" \
    "$HELMDECK_URL/api/v1/artifacts/download/$key" -o "$out"
}

# Page count of a PDF: pdfinfo when present (reliable), else best-effort.
pdf_pages() {
  local f="$1"
  if command -v pdfinfo >/dev/null 2>&1; then
    pdfinfo "$f" 2>/dev/null | awk '/^Pages:/{print $2}'
  else
    echo ""  # caller treats empty as "unknown — skip strict count"
  fi
}

has_magic() {  # file, hex-or-ascii prefix matcher
  local f="$1" want="$2"
  head -c 8 "$f" | grep -qa "$want"
}

# Integration gate for ElevenLabs-dependent cases. Returns 0 if a key
# is reachable to the control-plane via either env-var fallback or the
# vault entry the packs read at handler entry. False otherwise.
elevenlabs_enabled() {
  local val=""
  val=$(docker exec "$HELMDECK_CONTAINER" sh -c "printf '%s' \"\${HELMDECK_ELEVENLABS_API_KEY:-}\"" 2>/dev/null || true)
  if [[ -z "$val" && -f "$HELMDECK_ENV_FILE" ]]; then
    val=$(grep '^HELMDECK_ELEVENLABS_API_KEY=' "$HELMDECK_ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)
  fi
  # Vault path: a successful test call from the running stack proves the
  # vault-stored key works; skip the credential probe and let the
  # pipeline run surface "no key" failures through its existing error path.
  [[ -n "$val" ]]
}

# ── ffprobe-based av asserts (mp4:av + mp3:av) ────────────────────
# Catches container layout regressions (moov-at-end / no-faststart),
# silent-region drift (TTS silent-fallback hitting unexpectedly),
# packet-truncation cascades (ElevenLabs 200-OK-no-audio), and codec
# / sample-rate / bitrate drift.
#
# All checks degrade to a yellow note (and SKIP that specific check)
# when ffprobe isn't present on the host — same posture pdf_pages
# takes for pdfinfo. The faststart check is pure Python (no ffprobe)
# so it always runs.

ffprobe_present() { command -v ffprobe >/dev/null 2>&1 && command -v ffmpeg >/dev/null 2>&1; }

# mp4_faststart_ok <file>
# Reads the first ~64 KiB looking for the moov atom; faststart requires
# moov to appear BEFORE mdat in the byte stream so streaming players
# can begin playback before download completes. Pure Python — no
# external dep; runs even when ffprobe is absent.
mp4_faststart_ok() {
  python3 - "$1" <<'PY'
import sys
data = open(sys.argv[1], "rb").read()
moov = data.find(b"moov")
mdat = data.find(b"mdat")
sys.exit(0 if (0 < moov < mdat) else 1)
PY
}

# audio_packets_contiguous <file>
# Walks the audio packet timeline; fails if any consecutive packet
# gap > 0.5s. The 0.5s threshold lets normal between-slide pauses
# through but catches mid-segment dropouts where packets simply stop.
audio_packets_contiguous() {
  ffprobe -v error -select_streams a -show_packets -of csv=p=0 "$1" 2>/dev/null \
    | awk -F',' '
        NR==1 { prev=$4+$8; next }
        { if ($4 - prev > 0.5) exit 2; prev = $4+$8 }
      '
}

# audio_rms_above <file> <floor-db>
# Samples 5 evenly-spaced 2-second windows across the audio duration;
# fails if any window's mean RMS is below floor-db (default -45 dB —
# threshold that lets normal pauses through but catches a silent
# slide). Detects the "TTS silent-fallback fired for slide N" failure
# mode that the existing unit tests can't see.
audio_rms_above() {
  local f="$1" floor="$2" dur t rms
  dur=$(ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 "$f" 2>/dev/null)
  if [[ -z "$dur" ]] || awk -v d="$dur" 'BEGIN{exit (d<5)?0:1}'; then
    return 2  # too short to sample meaningfully — skip
  fi
  for i in 1 2 3 4 5; do
    t=$(awk -v d="$dur" -v i="$i" 'BEGIN{printf "%.2f", d*i/6}')
    rms=$(ffmpeg -ss "$t" -t 2 -i "$f" -af volumedetect -vn -sn -dn -f null - 2>&1 \
           | awk '/mean_volume:/ {print $5}')
    if [[ -z "$rms" ]]; then return 3; fi
    if awk -v rms="$rms" -v floor="$floor" 'BEGIN{exit (rms<floor)?0:1}'; then
      printf 'FAIL_AT t=%s rms=%s floor=%s\n' "$t" "$rms" "$floor"
      return 1
    fi
  done
}

# audio_codec_params_ok <file> <expected_codec> <expected_rate>
# Verifies the audio codec and sample rate match. Catches encoder
# drift (codec swap, sample-rate not pinned).
audio_codec_params_ok() {
  local f="$1" want_codec="$2" want_rate="$3" codec rate
  codec=$(ffprobe -v error -select_streams a -show_entries stream=codec_name -of default=nw=1:nk=1 "$f" 2>/dev/null)
  rate=$(ffprobe -v error -select_streams a -show_entries stream=sample_rate -of default=nw=1:nk=1 "$f" 2>/dev/null)
  [[ "$codec" == "$want_codec" && "$rate" == "$want_rate" ]]
}

# ── engagement-metadata asserts (mp4:av + mp3:av structural checks) ──
# The engagement object lives in the run-record's terminal-step
# output (slides.narrate / podcast.generate emit it inline). Each
# pack's structural rules (chapter floor, 0:00 / startTime=0 anchor,
# CTA placement) are baked into the pack's prompt, but the prompt is
# advisory to the LLM — these asserts are the regression-impossibility
# layer that catches prompt drift over time.
#
# Asserts run only when engagement IS emitted (presence is gated on
# metadata_model). When absent, the pack-level test still passed but
# the engagement layer doesn't gate the run.

# engagement_assert_youtube <runfile> <duration_s>
# Asserts the YouTube-shaped engagement object on a slides.narrate
# pipeline run: title <= 60 chars, chapters[0].timestamp == "0:00",
# at least 3 chapters when duration > 7 minutes.
engagement_assert_youtube() {
  python3 - "$1" "$2" <<'PY'
import sys, json
run = json.load(open(sys.argv[1]))
dur = float(sys.argv[2] or 0)
steps = run.get("steps") or []
if not steps:
    sys.exit(0)
out = steps[-1].get("output") or {}
if isinstance(out, str):
    try: out = json.loads(out)
    except Exception: out = {}
eng = out.get("engagement") or {}
if not eng:
    print("ENGAGEMENT_ABSENT")
    sys.exit(0)
errs = []
title = eng.get("title", "")
if len(title) > 60:
    errs.append(f"title {len(title)} chars > 60 cap")
chapters = eng.get("chapters") or []
if not chapters:
    errs.append("chapters empty")
elif chapters[0].get("timestamp") != "0:00":
    errs.append(f"chapters[0].timestamp = {chapters[0].get('timestamp')!r}, must be '0:00'")
if dur > 7 * 60 and len(chapters) < 3:
    errs.append(f"duration {dur:.0f}s > 7min but only {len(chapters)} chapters (YouTube guidance: ≥3)")
if errs:
    print("FAIL: " + "; ".join(errs))
    sys.exit(1)
print(f"OK: title={len(title)}c, chapters={len(chapters)}")
PY
}

# engagement_assert_podcast <runfile> <duration_s>
# Asserts the Podcasting-2.0-shaped engagement object on a
# podcast.generate pipeline run: chapters[0].startTime == 0, ≥3
# chapters when duration > 10 minutes, cta.placement == "mid-roll".
engagement_assert_podcast() {
  python3 - "$1" "$2" <<'PY'
import sys, json
run = json.load(open(sys.argv[1]))
dur = float(sys.argv[2] or 0)
steps = run.get("steps") or []
if not steps:
    sys.exit(0)
out = steps[-1].get("output") or {}
if isinstance(out, str):
    try: out = json.loads(out)
    except Exception: out = {}
eng = out.get("engagement") or {}
if not eng:
    print("ENGAGEMENT_ABSENT")
    sys.exit(0)
errs = []
chapters = eng.get("chapters") or []
if not chapters:
    errs.append("chapters empty")
elif chapters[0].get("startTime") != 0:
    errs.append(f"chapters[0].startTime = {chapters[0].get('startTime')!r}, must be 0")
if dur > 10 * 60 and len(chapters) < 3:
    errs.append(f"duration {dur:.0f}s > 10min but only {len(chapters)} chapters")
cta = eng.get("cta") or {}
if cta.get("placement") != "mid-roll":
    errs.append(f"cta.placement = {cta.get('placement')!r}, must be 'mid-roll' (defensive override)")
if errs:
    print("FAIL: " + "; ".join(errs))
    sys.exit(1)
print(f"OK: chapters={len(chapters)}, cta=mid-roll")
PY
}

# captions_assert <runfile>
# Asserts the slides.narrate captions sidecar exists, is non-trivially
# sized, and looks like an SRT (first cue at 00:00:00,000 with -->
# separator). Returns ENGAGEMENT_ABSENT-style soft-skip when the
# captions_artifact_key is empty (operator disabled the sidecar via
# captions_sidecar:false).
#
# Catches: the sidecar artifact-store Put silently failing (key
# empty but no log); SRT format drift (wrong time format would
# silently break YouTube CC import — operators would notice weeks
# later when uploads stop importing captions).
captions_assert() {
  local runfile="$1"
  local key
  key=$(python3 -c '
import sys, json
run = json.load(sys.stdin)
steps = run.get("steps") or []
if not steps: sys.exit(0)
out = steps[-1].get("output") or {}
if isinstance(out, str):
    try: out = json.loads(out)
    except Exception: out = {}
print(out.get("captions_artifact_key") or "")
' <"$runfile")
  if [[ -z "$key" ]]; then
    echo "CAPTIONS_ABSENT"
    return 0
  fi
  local srt="$WORK/captions.srt"
  if ! fetch_artifact "$key" "$srt"; then
    echo "FAIL: captions artifact $key not downloadable"
    return 1
  fi
  local size; size=$(wc -c <"$srt" | tr -d ' ')
  if [[ "$size" -lt 30 ]]; then
    echo "FAIL: captions.srt suspiciously small (size=$size)"
    return 1
  fi
  # YouTube acceptance signature: must contain "-->" and an
  # 00:00:00,000 first stamp (comma decimal, NOT period).
  if ! grep -q -- '-->' "$srt"; then
    echo "FAIL: captions.srt missing '-->' cue separator"
    return 1
  fi
  if ! grep -q '00:00:00,000' "$srt"; then
    echo "FAIL: captions.srt first cue not at 00:00:00,000 (YouTube spec)"
    return 1
  fi
  echo "OK: size=$size, SRT format valid"
}

# terminal_step_duration_s <runfile> — extracts total_duration_s
# (slides.narrate) or duration_s (podcast.generate) from the terminal
# step's output. Empty string when absent.
terminal_step_duration_s() {
  python3 -c '
import sys, json
run = json.load(sys.stdin)
steps = run.get("steps") or []
if not steps: sys.exit(0)
out = steps[-1].get("output") or {}
if isinstance(out, str):
    try: out = json.loads(out)
    except Exception: out = {}
print(out.get("total_duration_s") or out.get("duration_s") or "")
' <"$1"
}

PASS=0; FAIL=0; SKIP=0; FAILED=()
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT

# assert_artifact <name> <assert-spec> <run-json-file>
#   assert-spec: pdf | pdf:N | mp4 | mp4:av | mp3 | mp3:av | blog
#
# The :av variants add ffprobe-based deep checks on top of the basic
# shape/magic verification: faststart container layout, audio packet
# contiguity, RMS sanity at evenly-spaced sample points, and codec /
# sample-rate verification. These are the checks that would have
# caught the faststart bug (#422) which silently shipped with every
# helmdeck-produced MP4 from #379 onward — the basic mp4 spec missed
# it because the ffmpeg argv looked fine and the file size was sane.
assert_artifact() {
  local name="$1" spec="$2" runfile="$3"
  local key art; key=$(terminal_artifact_key <"$runfile")
  if [[ -z "$key" ]]; then
    red "  ✗ $name: run succeeded but no artifact key in the run record"; return 1
  fi
  art="$WORK/$name.bin"
  if ! fetch_artifact "$key" "$art"; then
    red "  ✗ $name: artifact $key not downloadable"; return 1
  fi
  local size; size=$(wc -c <"$art" | tr -d ' ')
  case "$spec" in
    pdf|pdf:*)
      if ! has_magic "$art" '%PDF'; then red "  ✗ $name: not a PDF (size=$size)"; return 1; fi
      if [[ "$spec" == pdf:* ]]; then
        local want pages; want="${spec#pdf:}"; pages=$(pdf_pages "$art")
        if [[ -z "$pages" ]]; then
          yellow "  ! $name: PDF ok (size=$size) — pdfinfo absent, page-count ($want) NOT verified"
        elif [[ "$pages" != "$want" ]]; then
          red "  ✗ $name: expected $want PDF pages, got $pages (the truncation regression)"; return 1
        else
          green "  ✓ $name: PDF with $pages pages (size=$size)"; return 0
        fi
      fi
      green "  ✓ $name: PDF ok (size=$size)"; return 0 ;;
    mp4)
      if ! has_magic "$art" 'ftyp' && [[ "$size" -lt 1000 ]]; then red "  ✗ $name: not a plausible MP4 (size=$size)"; return 1; fi
      green "  ✓ $name: MP4 ok (size=$size)"; return 0 ;;
    mp4:av)
      if ! has_magic "$art" 'ftyp' && [[ "$size" -lt 1000 ]]; then red "  ✗ $name: not a plausible MP4 (size=$size)"; return 1; fi
      # Faststart: pure-Python check, always runs.
      if ! mp4_faststart_ok "$art"; then
        red "  ✗ $name: MP4 moov atom AFTER mdat — streaming players cannot begin playback before full download (#422 regression)"; return 1
      fi
      if ! ffprobe_present; then
        yellow "  ! $name: MP4 faststart ok (size=$size) — ffprobe absent, audio packet / RMS / codec NOT verified"
        return 0
      fi
      if ! audio_packets_contiguous "$art"; then
        red "  ✗ $name: audio packet timeline has a gap > 0.5s (possible TTS truncation cascading into the concat)"; return 1
      fi
      local rms_out
      rms_out=$(audio_rms_above "$art" -45)
      case $? in
        0) ;;  # all sample points above -45 dB
        1) red "  ✗ $name: silent region detected — $rms_out (TTS silent-fallback fired unexpectedly?)"; return 1 ;;
        2) yellow "  ! $name: too short to sample RMS — skipping" ;;
        3) red "  ✗ $name: ffmpeg RMS probe failed unexpectedly"; return 1 ;;
      esac
      if ! audio_codec_params_ok "$art" aac 44100; then
        red "  ✗ $name: audio codec/rate drifted from aac+44100 (encoder regression)"; return 1
      fi
      # Engagement object structural check (YouTube-shaped).
      local engdur eng_out
      engdur=$(terminal_step_duration_s "$runfile")
      eng_out=$(engagement_assert_youtube "$runfile" "$engdur")
      case $? in
        0)
          case "$eng_out" in
            ENGAGEMENT_ABSENT)
              yellow "  ! $name: engagement object absent — pipeline didn't request it (metadata_model unset)" ;;
            OK:*)
              ;;  # passed; captions check below decides whether to green-success
          esac ;;
        1) red "  ✗ $name: engagement object structurally invalid — $eng_out"; return 1 ;;
      esac
      # Captions sidecar (slides.narrate v0.26.0+).
      local cap_out
      cap_out=$(captions_assert "$runfile")
      case $? in
        0)
          case "$cap_out" in
            CAPTIONS_ABSENT)
              yellow "  ! $name: captions sidecar absent — operator disabled via captions_sidecar:false" ;;
            OK:*)
              green "  ✓ $name: MP4 ok (size=$size, faststart, audio contiguous, RMS ≥ -45dB, aac@44100, engagement $eng_out, captions $cap_out)"
              return 0 ;;
          esac ;;
        1) red "  ✗ $name: captions sidecar invalid — $cap_out"; return 1 ;;
      esac
      green "  ✓ $name: MP4 ok (size=$size, faststart, audio contiguous, RMS ≥ -45dB, aac@44100)"; return 0 ;;
    mp3)
      if [[ "$size" -lt 1000 ]]; then red "  ✗ $name: MP3 too small (size=$size)"; return 1; fi
      green "  ✓ $name: MP3 ok (size=$size)"; return 0 ;;
    mp3:av)
      if [[ "$size" -lt 1000 ]]; then red "  ✗ $name: MP3 too small (size=$size)"; return 1; fi
      if ! ffprobe_present; then
        yellow "  ! $name: MP3 size ok (size=$size) — ffprobe absent, audio packet / RMS / codec NOT verified"
        return 0
      fi
      if ! audio_packets_contiguous "$art"; then
        red "  ✗ $name: audio packet timeline has a gap > 0.5s (possible TTS truncation cascading into the concat)"; return 1
      fi
      local rms_out_mp3
      rms_out_mp3=$(audio_rms_above "$art" -45)
      case $? in
        0) ;;
        1) red "  ✗ $name: silent region detected — $rms_out_mp3 (TTS silent-fallback fired unexpectedly?)"; return 1 ;;
        2) yellow "  ! $name: too short to sample RMS — skipping" ;;
        3) red "  ✗ $name: ffmpeg RMS probe failed unexpectedly"; return 1 ;;
      esac
      if ! audio_codec_params_ok "$art" mp3 44100; then
        red "  ✗ $name: audio codec/rate drifted from mp3+44100 (encoder regression)"; return 1
      fi
      # Engagement object structural check (Podcasting-2.0-shaped).
      local engdur_mp3 eng_out_mp3
      engdur_mp3=$(terminal_step_duration_s "$runfile")
      eng_out_mp3=$(engagement_assert_podcast "$runfile" "$engdur_mp3")
      case $? in
        0)
          case "$eng_out_mp3" in
            ENGAGEMENT_ABSENT)
              yellow "  ! $name: engagement object absent — pipeline didn't generate it" ;;
            OK:*)
              green "  ✓ $name: MP3 ok (size=$size, audio contiguous, RMS ≥ -45dB, mp3@44100, engagement $eng_out_mp3)"
              return 0 ;;
          esac ;;
        1) red "  ✗ $name: engagement object structurally invalid — $eng_out_mp3"; return 1 ;;
      esac
      green "  ✓ $name: MP3 ok (size=$size, audio contiguous, RMS ≥ -45dB, mp3@44100)"; return 0 ;;
    blog)
      if ! grep -qa '\[source\](' "$art" && [[ "$size" -lt 20 ]]; then
        red "  ✗ $name: blog body empty / no citations (size=$size)"; return 1
      fi
      green "  ✓ $name: blog body ok (size=$size)"; return 0 ;;
    *) red "  ✗ $name: unknown assert spec $spec"; return 1 ;;
  esac
}

# case: <name> <pipeline-id> <gate> <assert-spec> <inputs-json>
run_case() {
  local name="$1" id="$2" gate="$3" spec="$4" inputs="$5"
  if ! gate_ok "$gate"; then
    yellow "── skip: $name ($id) — gate '$gate' not satisfied"; SKIP=$((SKIP+1)); return 0
  fi
  blue "── case: $name ($id, assert=$spec)"
  local run_id runfile
  if ! run_id=$(start_run "$id" "$inputs"); then
    red "  ✗ $name: failed to start run"; FAIL=$((FAIL+1)); FAILED+=("$name"); return 1
  fi
  runfile="$WORK/$name.run.json"
  if ! poll_run "$id" "$run_id" >"$runfile"; then
    red "  ✗ $name: run $run_id did not finish within ${RUN_TIMEOUT_SECS}s"; FAIL=$((FAIL+1)); FAILED+=("$name"); return 1
  fi
  local status; status=$(run_status <"$runfile")
  if [[ "$status" != "succeeded" ]]; then
    red "  ✗ $name: run status=$status err=$(run_error <"$runfile")"; FAIL=$((FAIL+1)); FAILED+=("$name"); return 1
  fi
  if assert_artifact "$name" "$spec" "$runfile"; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); FAILED+=("$name"); fi
}

# ── pre-flight ────────────────────────────────────────────────────
require_cmd curl; require_cmd python3; require_cmd docker
read_admin_password
if ! curl -fsS "$HELMDECK_URL/healthz" >/dev/null 2>&1; then
  red "helmdeck not reachable at $HELMDECK_URL/healthz — start the stack first (./scripts/install.sh)"; exit 2
fi
JWT=$(mint_jwt)

# A 5-slide Marp deck (4 '---' separators). content.ground (rewrite:false)
# only adds citations, so slides.render must emit exactly 5 pages — the
# direct regression check for the truncation bug.
DECK5='# One\n\n---\n\n## Two\n\n---\n\n## Three\n\n---\n\n## Four\n\n---\n\n## Five'

# ── cases ─────────────────────────────────────────────────────────
# Only html-video is fully offline; the rest are gated on their packs'
# integrations and skipped (not failed) when those are off.
run_case "html-video"     "builtin.html-video"     "always"    "mp4"   "{\"composition_html\":\"<html><body style='background:#111'><h1>hi</h1></body></html>\"}"
run_case "grounded-deck"  "builtin.grounded-deck"  "firecrawl" "pdf:5" "{\"markdown\":\"$DECK5\"}"
run_case "scrape-deck"    "builtin.scrape-deck"    "firecrawl" "pdf"   "{\"url\":\"https://example.com\"}"
run_case "grounded-blog"  "builtin.grounded-blog"  "firecrawl" "blog"  "{\"markdown\":\"# Post\n\nWebAssembly is fast.\",\"title\":\"Smoke\"}"
run_case "doc-ground-blog" "builtin.doc-ground-blog" "docling"  "blog"  "{\"source_url\":\"https://example.com\",\"title\":\"Smoke\"}"

# AV-bench cases — gated on ElevenLabs because TTS is the load-
# bearing seam these exercise. Each produces a ~1–2 minute artifact
# whose container layout, audio packet continuity, RMS levels, and
# codec params are deep-verified by the mp4:av / mp3:av asserts above.
# The bug class these were added to catch: every helmdeck-produced
# MP4 from #379 through #422 shipped with the moov atom at the file
# tail, breaking streaming playback in ways unit tests can't see.
# We use a tiny public repo as the input so runtime stays predictable;
# the avbench.yml workflow can override REPO_URL via env if a more
# realistic input is wanted.
AVBENCH_REPO_URL="${AVBENCH_REPO_URL:-https://github.com/tosin2013/helmdeck}"
run_case "repo-presentation" "builtin.repo-presentation" "elevenlabs" "mp4:av" "{\"repo_url\":\"$AVBENCH_REPO_URL\"}"
run_case "repo-readme-podcast" "builtin.repo-readme-podcast" "elevenlabs" "mp3:av" "{\"repo_url\":\"$AVBENCH_REPO_URL\"}"

# ── results ───────────────────────────────────────────────────────
echo
blue "── results: passed=$PASS skipped=$SKIP failed=$FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  red "pipelines-smoke FAILED: ${FAILED[*]}"; exit 1
fi
green "=== pipelines-smoke PASSED ($PASS ran, $SKIP skipped) ==="
