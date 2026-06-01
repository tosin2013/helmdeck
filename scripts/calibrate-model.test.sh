#!/usr/bin/env bash
# helmdeck — calibrate-model.test.sh — ADR 051 PR #5 self-test.
#
# Smoke-tests scripts/calibrate-model.sh against two anchor models we
# already know the tier classification of: a known-Tier-C model
# (openrouter/openrouter/free) and a known-Tier-A model id pattern
# (the prefix that maps to Tier A in budgets.go). Asserts the
# calibrator's recommendation matches the expected tier — catches
# regressions in the heuristic logic.
#
# Run BEFORE merging changes to scripts/calibrate-model.sh:
#
#   scripts/calibrate-model.test.sh
#
# Exit codes:
#   0 — all assertions pass
#   1 — at least one anchor model received an unexpected tier
#   2 — the calibrator itself failed to run (control-plane down etc.)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CALIBRATOR="${REPO_ROOT}/scripts/calibrate-model.sh"

if [[ ! -x "${CALIBRATOR}" ]]; then
  echo "calibrate-test: calibrator not executable at ${CALIBRATOR}" >&2
  exit 2
fi

# --- Helper -----------------------------------------------------------

assert_tier() {
  local model="$1" expected_tiers="$2"   # space-separated acceptable tiers
  local actual_tier

  echo "=== anchor: ${model} (expecting one of: ${expected_tiers}) ==="

  actual_tier="$("${CALIBRATOR}" --json --skip-paste-heavy "${model}" 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["recommended_tier"])')" || {
    echo "  FAIL: calibrator did not produce parseable JSON output" >&2
    return 2
  }

  # Match the actual against any of the expected tiers.
  local matched=0
  for tier in ${expected_tiers}; do
    if [[ "${actual_tier}" == "${tier}" ]]; then
      matched=1
      break
    fi
  done

  if [[ ${matched} -eq 1 ]]; then
    echo "  PASS: got Tier ${actual_tier}"
    return 0
  else
    echo "  FAIL: got Tier ${actual_tier}, expected one of: ${expected_tiers}" >&2
    return 1
  fi
}

# --- Anchors ----------------------------------------------------------
#
# Two anchors chosen to span the calibration decision tree:
#
#   1. openrouter/openrouter/free — free-tier auto-routing. Empirically
#      this is Tier C (lexical truncation fires on multi-action prompts)
#      but free-route reliability is noisy enough that a single run might
#      see Tier C-unstable or even "unsupported". Accept any of those.
#
#   2. openrouter/anthropic/claude-haiku-4-5 — Anthropic flagship class.
#      Should ALWAYS calibrate as Tier A. If we get anything else, either
#      the heuristic broke or the model is genuinely degraded.

FAIL_COUNT=0

assert_tier "openrouter/openrouter/free" "C C-unstable unsupported" || FAIL_COUNT=$((FAIL_COUNT + 1))
echo
assert_tier "openrouter/anthropic/claude-haiku-4-5" "A" || FAIL_COUNT=$((FAIL_COUNT + 1))

# --- Report -----------------------------------------------------------

echo
echo "===================================="
if [[ ${FAIL_COUNT} -eq 0 ]]; then
  echo "calibrate-model self-test: ALL PASSED"
  exit 0
else
  echo "calibrate-model self-test: ${FAIL_COUNT} assertion(s) failed"
  exit 1
fi
