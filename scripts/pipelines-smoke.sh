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
    always)   return 0 ;;
    firecrawl) integration_enabled HELMDECK_FIRECRAWL_ENABLED ;;
    docling)   integration_enabled HELMDECK_DOCLING_ENABLED ;;
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

PASS=0; FAIL=0; SKIP=0; FAILED=()
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT

# assert_artifact <name> <assert-spec> <run-json-file>
#   assert-spec: pdf | pdf:N | mp4 | mp3 | blog
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
    mp3)
      if [[ "$size" -lt 1000 ]]; then red "  ✗ $name: MP3 too small (size=$size)"; return 1; fi
      green "  ✓ $name: MP3 ok (size=$size)"; return 0 ;;
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

# ── results ───────────────────────────────────────────────────────
echo
blue "── results: passed=$PASS skipped=$SKIP failed=$FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  red "pipelines-smoke FAILED: ${FAILED[*]}"; exit 1
fi
green "=== pipelines-smoke PASSED ($PASS ran, $SKIP skipped) ==="
