#!/usr/bin/env bash
# capture-batch.sh — drive multiple OpenClaw chat-UI captures in one run.
#
# Reads a prompts file in `<page>::<prompt>` format (one per line), runs
# each through capture-oc.sh (which mints a fresh --session-id per call
# so the model can't recall from prior turns), and writes one rendered
# transcript per prompt to <out>/<page-slugified>.md.
#
# Replaces the per-cluster /tmp/capture-pr{a,b,c}-batch.sh wrappers
# that PR-A/B/C used; promoted into scripts/oc-capture/ so any
# maintainer running their own captures gets the same pipeline.
#
# Usage:
#   capture-batch.sh --prompts <file> --out <dir> [--start <n>] [--end <n>]
#
#   --prompts  Prompts file, one `<page>::<prompt>` per line.
#              Page is the relative path the transcript injects into
#              under docs/reference/packs/<page>.md (e.g. "github/list-issues").
#   --out      Output directory for the rendered transcripts (created
#              if missing). Each prompt produces <out>/<page-slugified>.md
#              where slugification replaces "/" with "_".
#   --start    1-indexed start prompt (inclusive). Default: 1.
#   --end      1-indexed end prompt (inclusive). Default: last line.
#
# Examples:
#   # Run every prompt in the easy cluster:
#   capture-batch.sh \
#     --prompts scripts/oc-capture/prompts/easy-cluster.txt \
#     --out /tmp/captures/oc-transcripts
#
#   # Re-run just the 5th prompt (e.g. fix a bad capture):
#   capture-batch.sh \
#     --prompts scripts/oc-capture/prompts/medium-cluster.txt \
#     --out /tmp/captures/oc-transcripts \
#     --start 5 --end 5
#
# Cost ballpark per pack family (observed during PR-A/B/C):
#   - http/fs/git/cmd/language packs: ~$0.001-$0.005 per capture
#   - github/web packs:               ~$0.005-$0.015 per capture
#   - vision packs (with Haiku):       ~$0.05-$0.15 per capture
#   - slides.narrate (with ElevenLabs): ~$0.05 per capture (TTS-bound)
#   - research.deep / content.ground:  ~$0.01-$0.03 per capture
#
# Prerequisites:
#   - helmdeck stack running (control plane on localhost:3000)
#   - OpenClaw installed; configure-openclaw.sh has been run
#   - capture-oc.sh in the same directory as this script
set -uo pipefail

PROMPTS=""
OUT=""
START=1
END=0  # 0 = no upper bound; resolved to len(prompts) below

while (( "$#" )); do
  case "$1" in
    --prompts) PROMPTS="$2"; shift 2 ;;
    --out)     OUT="$2";     shift 2 ;;
    --start)   START="$2";   shift 2 ;;
    --end)     END="$2";     shift 2 ;;
    -h|--help)
      sed -n '2,/^set -uo pipefail/p' "$0" | head -n -2 | sed 's/^# \?//'
      exit 0
      ;;
    *)
      echo "ERROR: unknown flag: $1" >&2
      echo "Usage: $0 --prompts <file> --out <dir> [--start N] [--end N]" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$PROMPTS" || -z "$OUT" ]]; then
  echo "ERROR: --prompts and --out are required" >&2
  echo "Usage: $0 --prompts <file> --out <dir> [--start N] [--end N]" >&2
  exit 2
fi
if [[ ! -f "$PROMPTS" ]]; then
  echo "ERROR: prompts file not found: $PROMPTS" >&2
  exit 2
fi

CAPTURE="$(dirname "$(readlink -f "$0")")/capture-oc.sh"
if [[ ! -x "$CAPTURE" ]]; then
  echo "ERROR: capture-oc.sh not found or not executable at $CAPTURE" >&2
  exit 2
fi

mkdir -p "$OUT"

TOTAL=$(grep -cv '^[[:space:]]*$' "$PROMPTS")
if (( END == 0 )); then
  END=$TOTAL
fi
echo "→ batch: $TOTAL prompts in $PROMPTS, running [$START..$END]" >&2

idx=0
ok=0
fail=0
while IFS='::' read -r LHS REST; do
  [[ -z "$LHS" ]] && continue
  idx=$((idx+1))
  if (( idx < START )) || (( idx > END )); then continue; fi

  PAGE=$(echo "$LHS" | tr '/' '_')
  PROMPT="${REST#:}"
  printf '\n=== [%d/%d] %s ===\n' "$idx" "$TOTAL" "$PAGE" >&2

  bash "$CAPTURE" "$PROMPT" </dev/null > "$OUT/$PAGE.md.tmp" 2>&1
  EXIT=$?
  if (( EXIT == 0 )); then
    mv "$OUT/$PAGE.md.tmp" "$OUT/$PAGE.md"
    SIZE=$(wc -c < "$OUT/$PAGE.md")
    echo "  [OK] $SIZE bytes → $OUT/$PAGE.md" >&2
    ok=$((ok+1))
  else
    echo "  [FAIL exit=$EXIT]" >&2
    head -10 "$OUT/$PAGE.md.tmp" >&2
    fail=$((fail+1))
  fi
done < "$PROMPTS"

echo "" >&2
echo "→ batch complete: $ok ok, $fail failed (of $((END - START + 1)) attempted)" >&2
if (( fail > 0 )); then
  exit 1
fi
