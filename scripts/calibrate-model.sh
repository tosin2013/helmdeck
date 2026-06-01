#!/usr/bin/env bash
# helmdeck — calibrate-model.sh — ADR 051 PR #5.
#
# Runs a fixed suite of helmdeck-specific prompts against a model via
# the live REST /api/v1/packs/helmdeck.plan endpoint, measures outcome
# per prompt, and emits a recommended tier classification + a draft
# `budgets.go` entry the operator can paste into a branch.
#
# Use it when:
#   - A new model lands on OpenRouter (or another configured gateway)
#     and you want to know which tier it belongs in.
#   - You suspect an existing tier classification is wrong because
#     the model is failing where it shouldn't (or succeeding where
#     it shouldn't) in production.
#   - You're cutting a release and want to refresh the table per the
#     RELEASES.md §"Agent sync checklist" quarterly review.
#
# Requires:
#   - helmdeck-control-plane running locally on $PORT (default 3000)
#   - The model id you want to calibrate is reachable through the
#     gateway (check `helmdeck://models` or your provider config)
#   - jq + curl + python3 in $PATH
#
# Usage:
#   scripts/calibrate-model.sh openrouter/deepseek/deepseek-v4-pro
#   scripts/calibrate-model.sh --json openrouter/anthropic/claude-haiku-4-5
#   scripts/calibrate-model.sh --skip-paste-heavy openrouter/openrouter/free
#
# Exit codes:
#   0 — calibration complete; recommendation printed
#   1 — control-plane unreachable or model dispatch failure on every prompt
#   2 — invalid arguments
#
# See docs/howto/calibrate-model-tiers.md for the methodology and the
# rules for the recommended-tier output.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${PORT:-3000}"
BASE_URL="${BASE_URL:-http://127.0.0.1:${PORT}}"
TIMEOUT_S="${TIMEOUT_S:-180}"
OUTPUT_FORMAT="human"     # human | json
SKIP_PASTE_HEAVY=0

print_usage() {
  cat >&2 <<'EOF'
Usage: calibrate-model.sh [--json] [--skip-paste-heavy] <model-id>

Runs the helmdeck calibration prompt suite against <model-id> and
emits a recommended tier classification + draft budgets.go entry.

Options:
  --json                 Emit machine-readable JSON instead of human text
  --skip-paste-heavy     Skip the paste-heavy prompt (saves ~60s on weak models)
  -h, --help             Print this help

Environment:
  PORT          Control plane port (default 3000)
  BASE_URL      Full URL override (default http://127.0.0.1:$PORT)
  TIMEOUT_S    Per-prompt timeout in seconds (default 180)
EOF
}

# --- Argument parsing ------------------------------------------------

MODEL=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --json) OUTPUT_FORMAT="json"; shift ;;
    --skip-paste-heavy) SKIP_PASTE_HEAVY=1; shift ;;
    -h|--help) print_usage; exit 0 ;;
    --*) echo "calibrate-model: unknown flag: $1" >&2; print_usage; exit 2 ;;
    *) MODEL="$1"; shift ;;
  esac
done

if [[ -z "${MODEL}" ]]; then
  echo "calibrate-model: missing model id" >&2
  print_usage
  exit 2
fi

# --- Dependency checks -----------------------------------------------

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "calibrate-model: missing required tool: $1" >&2; exit 2; }
}
require curl
require jq
require python3
require docker

# --- Control-plane discovery + token mint ----------------------------

if ! curl -fsS -m 3 "${BASE_URL}/health" >/dev/null 2>&1; then
  echo "calibrate-model: control-plane at ${BASE_URL} unreachable" >&2
  echo "  Bring it up: docker compose -f deploy/compose/compose.yaml ... up -d control-plane" >&2
  exit 1
fi

if ! docker ps --format '{{.Names}}' | grep -q '^helmdeck-control-plane$'; then
  echo "calibrate-model: helmdeck-control-plane container not running" >&2
  exit 1
fi

TOKEN="$(docker exec helmdeck-control-plane /usr/local/bin/control-plane \
  -mint-token=calibrate -mint-token-scopes=admin 2>/dev/null | tail -1)"
if [[ -z "${TOKEN}" ]]; then
  echo "calibrate-model: failed to mint admin token" >&2
  exit 1
fi

# --- Prompt suite ----------------------------------------------------
#
# Three prompts representing the failure-mode classes ADR 050 + ADR 051
# were designed around:
#
#   1. Trivial single-action — baseline latency + "does this model
#      respond at all"
#   2. Multi-action — tests structured-output reliability when the
#      model has to decompose into 3+ tool calls
#   3. Paste-heavy + multi-action — tests the worst case (the MiniMax
#      M3 motivating prompt) that exposed the empty-completion failure
#      mode

PROMPT_TRIVIAL='take a screenshot of github.com'
PROMPT_MULTI_ACTION='remember this fact, then write a blog about it, then generate an image'
PROMPT_PASTE_HEAVY='remember this MiniMax M3 launch announcement as a durable fact, then write a 300-word technical blog post about it, then generate an illustration for the post. Source: MiniMax announces M3, a frontier-class open-weights LLM with 235B parameters and 22B activated, trained on 15T high-quality tokens. The release includes both base and instruction-tuned variants under permissive licensing.'

# --- Per-prompt runner -----------------------------------------------
#
# Sends one prompt to helmdeck.plan, captures: HTTP status, wall-clock
# duration, the parsed output (or error), and the cascade trace from
# the `compaction.dropped` field. Echoes a structured single-line JSON
# result to stdout for collection.

run_prompt() {
  local label="$1" intent="$2"
  local start_s end_s body http_code

  start_s="$(date +%s.%N)"
  body="$(curl -sS -w "\n%{http_code}" -m "${TIMEOUT_S}" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -X POST "${BASE_URL}/api/v1/packs/helmdeck.plan" \
    -d "$(jq -nc --arg intent "${intent}" --arg model "${MODEL}" \
      '{user_intent: $intent, model: $model, max_tokens: 1500}')" \
    2>/dev/null || echo $'\n000')"
  end_s="$(date +%s.%N)"

  http_code="$(printf '%s\n' "${body}" | tail -n1)"
  body="$(printf '%s\n' "${body}" | sed '$d')"

  python3 - "${label}" "${start_s}" "${end_s}" "${http_code}" <<'PY' "${body}"
import json, sys, time

label, start_s, end_s, http_code = sys.argv[1:5]
raw = sys.argv[5] if len(sys.argv) >= 6 else ""
duration_s = round(float(end_s) - float(start_s), 2)

result = {
    "label": label,
    "duration_s": duration_s,
    "http_code": http_code,
    "outcome": "unknown",   # success | empty | error | timeout
    "compaction": None,
    "error_message": "",
    "steps_count": 0,
    "complexity": "",
}

if http_code == "000":
    result["outcome"] = "timeout"
    result["error_message"] = f"no HTTP response within {sys.argv[3]}s"
else:
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        parsed = {}

    if http_code == "200" and "output" in parsed:
        out = parsed.get("output", {})
        result["outcome"] = "success"
        result["compaction"] = out.get("compaction")
        result["steps_count"] = len(out.get("steps", []))
        result["complexity"] = out.get("complexity", "")
    elif "error" in parsed:
        msg = parsed.get("message", "")
        result["error_message"] = msg
        # Classify "empty plan response" as a distinct outcome — it's
        # the headline failure mode ADR 051 exists to address.
        if "empty" in msg.lower() and "response" in msg.lower():
            result["outcome"] = "empty"
        else:
            result["outcome"] = "error"
    else:
        result["outcome"] = "error"
        result["error_message"] = raw[:200]

print(json.dumps(result))
PY
}

# --- Execute the suite -----------------------------------------------

if [[ "${OUTPUT_FORMAT}" == "human" ]]; then
  echo "calibrate-model: ${MODEL}"
  echo "  control plane: ${BASE_URL}"
  echo "  per-prompt timeout: ${TIMEOUT_S}s"
  echo
  echo "Running prompt suite..."
fi

R_TRIVIAL="$(run_prompt trivial "${PROMPT_TRIVIAL}")"
R_MULTI="$(run_prompt multi-action "${PROMPT_MULTI_ACTION}")"

if [[ ${SKIP_PASTE_HEAVY} -eq 1 ]]; then
  R_PASTE="$(jq -nc '{label:"paste-heavy", outcome:"skipped", duration_s:0, compaction:null}')"
else
  R_PASTE="$(run_prompt paste-heavy "${PROMPT_PASTE_HEAVY}")"
fi

# --- Tier recommendation ---------------------------------------------
#
# Decision tree:
#
#   All three succeed AND no compaction fired AND latencies < 20s
#     → Tier A (frontier, full catalog passes through)
#
#   All three succeed AND compaction shows metadata trim only
#     (no `lexical.top_n` in dropped[])
#     → Tier B (mid-tier, metadata trim sufficient)
#
#   All three succeed but compaction shows `lexical.top_n` or
#     `llm_filter(...)`
#     → Tier C (weak/free, retrieval cascade required)
#
#   Multi-action OR paste-heavy fails (empty/error/timeout)
#     but trivial succeeds → Tier C/unstable (works for simple cases
#     only; production use depends on whether the operator's intents
#     match the model's reliability band)
#
#   Trivial fails → not viable; flag as "unsupported"
#
# Latency > 20s on trivial intent ALSO suggests hybrid reasoning
# (model emits <think> blocks before responding); flag as such in
# the recommendation so the operator sets IsHybridReasoning when
# adding to the budgets table.

RECOMMENDATION="$(R_TRIVIAL="${R_TRIVIAL}" R_MULTI="${R_MULTI}" R_PASTE="${R_PASTE}" MODEL_ID="${MODEL}" python3 - <<'PY'
import json, os

trivial = json.loads(os.environ["R_TRIVIAL"])
multi   = json.loads(os.environ["R_MULTI"])
paste   = json.loads(os.environ["R_PASTE"])
model_id = os.environ["MODEL_ID"]

results = [trivial, multi, paste]
non_skipped = [r for r in results if r["outcome"] != "skipped"]

# Categorize outcomes
def succeeded(r): return r["outcome"] == "success"
def failed(r):    return r["outcome"] in ("empty", "error", "timeout")

trivial_ok = succeeded(trivial)
multi_ok   = succeeded(multi)
paste_ok   = succeeded(paste) or paste["outcome"] == "skipped"

# Inspect compaction.dropped on successful runs to determine which
# cascade stages fired (signals the actual budget the model needed).
def fired(r, marker):
    c = r.get("compaction") or {}
    return any(marker in d for d in c.get("dropped", []))

any_lexical = any(succeeded(r) and fired(r, "lexical.top_n") for r in results)
any_filter  = any(succeeded(r) and fired(r, "llm_filter") for r in results)
any_meta    = any(succeeded(r) and (r.get("compaction") or {}).get("dropped") for r in results)

# Hybrid-reasoning heuristic: extended latency on a trivial prompt.
slow_trivial = trivial.get("duration_s", 0) > 20 if trivial_ok else False

# Classification
if not trivial_ok:
    tier = "unsupported"
    reasoning = "model could not produce a valid plan for the simplest prompt"
elif trivial_ok and not multi_ok:
    tier = "C-unstable"
    reasoning = "trivial works but multi-action fails — usable for routing-only workloads"
elif trivial_ok and multi_ok and not paste_ok:
    tier = "C"
    reasoning = "simple multi-action works; paste-heavy fails — cascade can carry this model but operators should expect the worst-case prompt to error"
elif any_lexical or any_filter:
    tier = "C"
    reasoning = "all prompts succeed but the cascade fired its lexical / filter stages — model needs the full ADR 050 retrieval pipeline"
elif any_meta:
    tier = "B"
    reasoning = "all prompts succeed with metadata compaction only — model handles ~25KB catalog reliably"
else:
    tier = "A"
    reasoning = "all prompts succeed with no compaction needed — full catalog passes through"

flags = []
if slow_trivial:
    flags.append("IsHybridReasoning")
    reasoning += "; trivial-intent latency >20s suggests hybrid reasoning — set IsHybridReasoning=true in the entry"

# Pick MaxCatalogBytes per tier
mcb_by_tier = {"A": 0, "B": 22000, "C": 10000, "C-unstable": 10000, "unsupported": 10000}
itok_by_tier = {"A": 200000, "B": 32000, "C": 16000, "C-unstable": 16000, "unsupported": 16000}
otok_by_tier = {"A": 4000, "B": 2000, "C": 1500, "C-unstable": 1500, "unsupported": 1500}

# Build draft budgets.go entry (model_id already pulled from env above).
# Map our tier string to the Go const name. "unsupported" / "C-unstable"
# need explicit handling — "unsupported" should not produce a pasteable
# entry at all because adding it to the table implies "we support it."
if tier == "unsupported":
    draft = "# DO NOT add this model to internal/llmcontext/budgets.go — calibration shows it cannot produce a valid plan even for the trivial prompt. The unmapped-model TierC fallback already covers this case if an operator passes the id anyway."
else:
    tier_const = {"A": "TierA", "B": "TierB", "C": "TierC", "C-unstable": "TierC"}[tier]
    allows_filter = "true" if tier in ("C", "C-unstable") else "false"
    draft = f'\t{{Model: "{model_id}", InputTokens: {itok_by_tier[tier]}, OutputTokens: {otok_by_tier[tier]}, MaxCatalogBytes: {mcb_by_tier[tier]}, Tier: {tier_const}'
    if allows_filter == "true":
        draft += ", AllowsLLMFilter: true"
    draft += "},"

report = {
    "model": model_id,
    "recommended_tier": tier,
    "reasoning": reasoning,
    "flags_to_set_manually": flags,
    "prompt_results": results,
    "draft_budgets_entry": draft,
}
print(json.dumps(report, indent=2))
PY
)"

# --- Render report ---------------------------------------------------

if [[ "${OUTPUT_FORMAT}" == "json" ]]; then
  printf '%s\n' "${RECOMMENDATION}"
  exit 0
fi

# Human-formatted output
RECOMMENDATION="${RECOMMENDATION}" python3 - <<'PY'
import json, os
r = json.loads(os.environ["RECOMMENDATION"])

print()
print("=" * 72)
print(f"  RECOMMENDATION: Tier {r['recommended_tier']}")
print("=" * 72)
print()
print(f"  Model:      {r['model']}")
print(f"  Reasoning:  {r['reasoning']}")
if r['flags_to_set_manually']:
    print()
    print("  Manual flags suggested (set in your budgets.go entry):")
    for f in r['flags_to_set_manually']:
        print(f"    - {f} = true")
print()
print("  Per-prompt results:")
for p in r['prompt_results']:
    label = p['label']
    outcome = p['outcome']
    duration = p['duration_s']
    marker = "OK " if outcome == "success" else "X  " if outcome != "skipped" else "-  "
    extra = ""
    if outcome == "success":
        c = (p.get("compaction") or {})
        dropped = c.get("dropped", []) or []
        if any("lexical.top_n" in d for d in dropped): extra += " [lexical]"
        if any("llm_filter" in d for d in dropped):    extra += " [filter]"
        if not dropped and outcome == "success":       extra += " [pass-through]"
    elif outcome != "skipped":
        extra = f" ({p['error_message'][:60]})"
    print(f"    {marker} {label:<14} {duration}s{extra}")
print()
print("  Draft budgets.go entry:")
print(f"    {r['draft_budgets_entry']}")
print()
print("  Next steps:")
print(f"    1. Verify the recommendation against docs/howto/calibrate-model-tiers.md")
print(f"    2. Run scripts/calibrate-model.sh again — calibration should be reproducible")
print(f"    3. If consistent, paste the draft entry into internal/llmcontext/budgets.go")
print(f"    4. Add a trailing comment naming the BFCL score, Aider score, or live observation")
print(f"    5. Open a PR")
print()
PY
