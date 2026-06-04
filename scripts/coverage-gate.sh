#!/usr/bin/env bash
# coverage-gate.sh — enforce per-package coverage floors.
#
# Reads a Go coverage profile (default `coverage.txt`) and verifies
# every tracked package is at or above its declared floor. Exits 0 on
# success, 1 on the first package below floor (after reporting every
# tracked package so a maintainer sees the full picture, not just the
# first failure).
#
# The coverage we compute is STATEMENT-WEIGHTED, parsed directly from
# the coverage profile lines (format: `<file>:<range> <stmts> <count>`).
# Function-averaging via `go tool cover -func` double-weights tiny
# functions and would let a single big untested function hide behind
# many small tested ones. Statement weighting is the same metric
# `go tool cover -func` prints for the `total:` row at the bottom.
#
# Adding a new tracked package: add a row to the FLOORS associative
# array below. Picking the floor:
#  - 90 for critical reliability packages (LLM-facing path)
#  - 80 for infrastructure packages (REST handlers, pipelines, mcp)
#  - Excluded: cmd/* entry points (os.Exit/signal handling),
#    generated code, _test files (already excluded by go cover)
#
# Exclusions are by NOT listing the package; the script doesn't fail
# on packages it doesn't track.
#
# See docs/howto/maintain-coverage-gate.md for the rationale and the
# release-plan-driven ratcheting cadence (PRs A through D bump the
# floors progressively, ending at 90/80 by PR D).

set -euo pipefail

COVERAGE_FILE="${1:-coverage.txt}"
MODULE_PREFIX="github.com/tosin2013/helmdeck"

if [[ ! -f "$COVERAGE_FILE" ]]; then
  echo "coverage-gate: profile $COVERAGE_FILE not found" >&2
  echo "  Run: go test -coverprofile=$COVERAGE_FILE ./..." >&2
  exit 2
fi

# Per-package floors. PRs A–D of the v0.24.0 reliability arc ratcheted
# from baseline. PR D locks the floors at the empirical realistic level:
#
#   Critical (LLM-facing reliability) — 88-90% floors:
#     - avenc (99.3 actual)        — codec/byte-floor guards are
#                                     load-bearing for slides.narrate;
#                                     a regression here breaks every
#                                     ffmpeg-using pack at once.
#     - llmcontext (92.1 actual)   — budget compaction; ADR 050.
#     - gateway (88.1 actual)      — provider dispatch + fallback,
#                                     bumped 85→88.
#
#   Infrastructure — 80% floors:
#     - packs/builtin (80.5 actual) — original plan's 90 target proved
#                                     aspirational; 80 is the floor the
#                                     test surface supports without
#                                     significant new mock infra.
#     - api (80.1 actual)          — REST + MCP adapter surface.
#     - pipelines (84.0 actual)    — NEW track in PR D; ADR 041 store +
#                                     runner. 80 floor with ~4pp slack.
#
# Not yet tracked:
#   - internal/mcp (69.5 actual) — below the 80% infra floor. Adding it
#     now would fail the gate. Tracking + ratcheting is a v0.25.0 task
#     scoped separately so PR D doesn't bundle a forced cleanup.
#   - cmd/* — entry points with os.Exit/signal handling; integration-only.
#   - *_fixtures.go / generated code — excluded by not listing here.
declare -A FLOORS=(
  ["internal/avenc"]=90
  ["internal/llmcontext"]=90
  ["internal/gateway"]=88
  ["internal/packs/builtin"]=80
  ["internal/packs"]=87
  ["internal/api"]=80
  ["internal/pipelines"]=80
  ["internal/mcp"]=81
)

# Compute statement-weighted coverage for one package prefix.
# Returns "N.N" via stdout, or "0.0" if no matching lines.
pkg_coverage() {
  local prefix="$1"
  awk -v pfx="$MODULE_PREFIX/$prefix" '
    # Skip the "mode:" header line.
    /^mode:/ { next }
    # Profile lines look like:
    #   <file>:<startline>.<col>,<endline>.<col> <stmts> <count>
    # We match files whose path starts with the package prefix AND
    # whose next path component is a .go file (not a deeper subpackage).
    {
      # Extract the file path (everything up to the first colon).
      colon = index($1, ":")
      if (colon == 0) next
      file = substr($1, 1, colon - 1)

      # Only the immediate package, not deeper sub-packages. Compare
      # the directory of `file` to `pfx`. If file = "<pfx>/foo.go",
      # dir = "<pfx>". If file = "<pfx>/sub/foo.go", dir = "<pfx>/sub"
      # which is a different package.
      slash = match(file, /\/[^\/]+\.go$/)
      if (slash == 0) next
      dir = substr(file, 1, slash - 1)
      if (dir != pfx) next

      stmts = $2 + 0
      count = $3 + 0
      total += stmts
      if (count > 0) covered += stmts
    }
    END {
      if (total > 0) printf "%.1f", (covered / total) * 100.0
      else print "0.0"
    }
  ' "$COVERAGE_FILE"
}

# Stable iteration order. bash assoc arrays are unordered; sort keys
# so the report is deterministic across runs.
mapfile -t SORTED_KEYS < <(printf '%s\n' "${!FLOORS[@]}" | sort)

fail=0
echo "Per-package coverage gate (statement-weighted)"
echo "=============================================="
printf "%-32s %8s %8s   %s\n" "PACKAGE" "ACTUAL" "FLOOR" "STATUS"
echo "----------------------------------------------------------------"
for prefix in "${SORTED_KEYS[@]}"; do
  floor="${FLOORS[$prefix]}"
  pct="$(pkg_coverage "$prefix")"
  # Compare with a tiny bit of slack (0.05) so we don't flake on
  # rounding — a function-coverage 90.0% reported as 89.95 won't trip.
  status="ok"
  below="$(awk -v p="$pct" -v f="$floor" 'BEGIN { print (p + 0.05 < f) ? 1 : 0 }')"
  if [[ "$below" == "1" ]]; then
    status="BELOW FLOOR"
    fail=1
  fi
  printf "%-32s %7s%% %7s%%   %s\n" "$prefix" "$pct" "$floor" "$status"
done
echo "----------------------------------------------------------------"

if [[ "$fail" == "1" ]]; then
  echo "coverage gate: FAIL"
  echo
  echo "One or more packages dropped below their floor. Options:"
  echo "  1. Add tests for the uncovered code (run \`go tool cover -func=$COVERAGE_FILE | grep <pkg>\` to find functions at 0%)"
  echo "  2. If the drop is justified (e.g. new infrastructure code that's pending tests), update the floor in scripts/coverage-gate.sh AND explain why in the PR description"
  echo "  3. If the package shouldn't be tracked at all, remove its row from FLOORS"
  exit 1
fi

echo "coverage gate: PASS"
