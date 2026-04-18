#!/usr/bin/env bash
# scripts/validate-phase-6-5.sh — end-to-end validation of every
# Phase 6.5 pack and feature against a running helmdeck stack.
#
# Unlike validate-openclaw.sh (which routes through an LLM agent),
# this script drives packs DIRECTLY via the REST API so results are
# deterministic and don't depend on model availability for most
# probes. LLM-dependent probes (web.test, research.deep,
# content.ground, vision.click_anywhere native) are skipped cleanly
# when no AI provider key is configured.
#
# Prerequisites:
#   - Helmdeck stack running: ./scripts/install.sh
#   - For Firecrawl probes: compose.firecrawl.yml overlay up
#   - For Docling probes: compose.docling.yml overlay up
#   - For LLM probes: at least one provider key in the keystore
#
# Usage:
#   ./scripts/validate-phase-6-5.sh              # full run
#   ./scripts/validate-phase-6-5.sh --test T807a # single probe
#   ./scripts/validate-phase-6-5.sh --skip-llm   # skip LLM probes
#
# Exits 0 if every run test passed, 1 if any failed.

set -euo pipefail

HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
HELMDECK_USER="${HELMDECK_USER:-admin}"
HELMDECK_PASS="${HELMDECK_PASS:-}"

ONE_TEST=""
SKIP_LLM=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --test) ONE_TEST="$2"; shift 2 ;;
    --skip-llm) SKIP_LLM=true; shift ;;
    -h|--help)
      sed -n '2,24p' "$0" | sed 's|^# \?||'
      exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 64 ;;
  esac
done

# ── helpers ───────────────────────────────────────────────────────

red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
blue()   { printf '\033[34m%s\033[0m\n' "$*"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { red "missing prerequisite: $1"; exit 2; }
}

read_admin_password() {
  if [[ -n "$HELMDECK_PASS" ]]; then return; fi
  local env_local="/root/helmdeck/deploy/compose/.env.local"
  if [[ -f "$env_local" ]]; then
    HELMDECK_PASS=$(grep '^HELMDECK_ADMIN_PASSWORD=' "$env_local" | cut -d= -f2-)
  fi
  if [[ -z "$HELMDECK_PASS" ]]; then
    red "HELMDECK_PASS not set and could not read from .env.local"
    exit 2
  fi
}

mint_jwt() {
  curl -fsS -X POST "$HELMDECK_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$HELMDECK_USER\",\"password\":\"$HELMDECK_PASS\"}" \
  | jq -r '.token'
}

# api_post fires a pack call or REST endpoint and prints the body.
# Returns the HTTP status code via $?.
api_post() {
  local path="$1" body="$2"
  curl -fsS -w '\n__status=%{http_code}' \
    -H "Authorization: Bearer $JWT" \
    -H "Content-Type: application/json" \
    -d "$body" \
    "$HELMDECK_URL$path" 2>/dev/null || true
}

api_get() {
  local path="$1"
  curl -fsS -w '\n__status=%{http_code}' \
    -H "Authorization: Bearer $JWT" \
    "$HELMDECK_URL$path" 2>/dev/null || true
}

# run_pack calls POST /api/v1/packs/<name> and returns the JSON
# output. Writes the HTTP status to $LAST_STATUS.
LAST_STATUS=0
run_pack() {
  local pack="$1" body="$2"
  local raw
  raw=$(api_post "/api/v1/packs/$pack" "$body")
  LAST_STATUS=$(echo "$raw" | grep -oP '__status=\K\d+' || echo 0)
  echo "$raw" | grep -v __status
}

# Counters
PASS=0
FAIL=0
SKIP=0
FAILED=()

# run_test wraps a test function with pass/fail/skip reporting.
run_test() {
  local id="$1" name="$2"
  if [[ -n "$ONE_TEST" && "$ONE_TEST" != "$id" ]]; then return; fi
  blue "── $id: $name"
  if "$3"; then
    green "  PASS"
    PASS=$((PASS + 1))
  else
    red "  FAIL"
    FAIL=$((FAIL + 1))
    FAILED+=("$id")
  fi
  echo
}

skip_test() {
  local id="$1" reason="$2"
  if [[ -n "$ONE_TEST" && "$ONE_TEST" != "$id" ]]; then return; fi
  yellow "── $id: SKIP — $reason"
  SKIP=$((SKIP + 1))
  echo
}

# ── pre-flight ────────────────────────────────────────────────────

require_cmd curl
require_cmd jq

read_admin_password

if ! curl -fsS "$HELMDECK_URL/healthz" >/dev/null 2>&1; then
  red "helmdeck not reachable at $HELMDECK_URL/healthz"
  exit 2
fi

blue "minting JWT"
JWT=$(mint_jwt)
if [[ -z "$JWT" || "$JWT" == "null" ]]; then
  red "failed to mint JWT (check HELMDECK_ADMIN_PASSWORD)"
  exit 2
fi
green "  JWT minted"
echo

# ── detect optional services ─────────────────────────────────────

FIRECRAWL_UP=false
DOCLING_UP=false
LLM_AVAILABLE=false

# Check Firecrawl by probing web.scrape. When HELMDECK_FIRECRAWL_ENABLED
# is not set, the pack returns {"error":"invalid_input",...} — we detect
# success by checking for a non-null, non-empty .markdown field in the
# response. The probe must capture the full response (not pipe through
# curl -f which swallows the body on 4xx).
fc_probe=$(run_pack "web.scrape" '{"url":"https://example.com"}' 2>/dev/null || true)
fc_md=$(echo "$fc_probe" | jq -r '.output.markdown // .markdown // empty' 2>/dev/null || true)
if [[ -n "$fc_md" ]]; then
  FIRECRAWL_UP=true
  green "  Firecrawl overlay: UP"
else
  yellow "  Firecrawl overlay: not detected (web.scrape probes will skip)"
fi

# Check Docling similarly.
docling_probe=$(run_pack "doc.parse" '{"source_url":"https://example.com","formats":["md"]}' 2>/dev/null || true)
dc_md=$(echo "$docling_probe" | jq -r '.output.markdown // .markdown // empty' 2>/dev/null || true)
if [[ -n "$dc_md" ]]; then
  DOCLING_UP=true
  green "  Docling overlay: UP"
else
  yellow "  Docling overlay: not detected (doc.parse probes will skip)"
fi

# Check LLM availability via /v1/models. Strip whitespace from jq
# output so bash arithmetic doesn't choke on trailing newlines.
models_resp=$(api_get "/v1/models" 2>/dev/null | grep -v __status || true)
model_count=$(echo "$models_resp" | jq -r '.data | length' 2>/dev/null | tr -d '[:space:]' || echo 0)
if [[ -z "$model_count" ]]; then model_count=0; fi
if [[ "$model_count" -gt 0 ]] && [[ "$SKIP_LLM" != true ]]; then
  LLM_AVAILABLE=true
  green "  LLM providers: $model_count model(s) available"
else
  if [[ "$SKIP_LLM" == true ]]; then
    yellow "  LLM providers: skipped (--skip-llm)"
  else
    yellow "  LLM providers: none configured (LLM-dependent probes will skip)"
  fi
fi
echo

# ── T807a: Playwright MCP bundled in sidecar ─────────────────────

test_T807a() {
  # Create a session and verify playwright_mcp_endpoint is populated.
  local resp
  resp=$(api_post "/api/v1/sessions" '{}')
  local status
  status=$(echo "$resp" | grep -oP '__status=\K\d+' || echo 0)
  resp=$(echo "$resp" | grep -v __status)

  if [[ "$status" != "201" && "$status" != "200" ]]; then
    red "  session create returned $status"
    return 1
  fi

  local session_id pw_endpoint
  session_id=$(echo "$resp" | jq -r '.id')
  pw_endpoint=$(echo "$resp" | jq -r '.playwright_mcp_endpoint // empty')

  if [[ -z "$pw_endpoint" ]]; then
    red "  playwright_mcp_endpoint is empty — sidecar may not have T807a"
    # Clean up
    api_post "/api/v1/sessions/$session_id/terminate" '{}' >/dev/null 2>&1 || true
    return 1
  fi

  echo "  session=$session_id playwright_mcp_endpoint=$pw_endpoint"
  # Terminate
  api_post "/api/v1/sessions/$session_id/terminate" '{}' >/dev/null 2>&1 || true
  return 0
}

# ── T807b: web.scrape (Firecrawl) ────────────────────────────────

test_T807b() {
  local resp
  resp=$(run_pack "web.scrape" '{"url":"https://example.com"}')
  local md
  md=$(echo "$resp" | jq -r '.output.markdown // .markdown // empty')
  if [[ -z "$md" ]]; then
    red "  markdown is empty"
    echo "  response: $(echo "$resp" | head -c 500)"
    return 1
  fi
  echo "  markdown length: ${#md} chars"
  if ! echo "$md" | grep -qi "example"; then
    red "  markdown does not contain 'example'"
    return 1
  fi
  return 0
}

# ── T807c: doc.parse (Docling) ───────────────────────────────────

test_T807c() {
  local resp
  resp=$(run_pack "doc.parse" '{"source_url":"https://example.com","formats":["md"]}')
  local md status
  md=$(echo "$resp" | jq -r '.output.markdown // .markdown // empty')
  status=$(echo "$resp" | jq -r '.output.status // .status // empty')
  if [[ -z "$md" ]]; then
    red "  markdown is empty, status=$status"
    echo "  response: $(echo "$resp" | head -c 500)"
    return 1
  fi
  echo "  markdown length: ${#md} chars, status=$status"
  return 0
}

# ── T807e: web.test (Playwright MCP NL testing) ──────────────────

test_T807e() {
  # Pick the first available model.
  local model
  model=$(echo "$models_resp" | jq -r '.data[0].id // empty')
  if [[ -z "$model" ]]; then
    red "  no model available"
    return 1
  fi
  echo "  using model: $model"
  local resp
  resp=$(run_pack "web.test" "{\"url\":\"https://example.com\",\"instruction\":\"Confirm the page has the heading Example Domain\",\"model\":\"$model\",\"max_steps\":3,\"assertions\":[\"Example Domain\"]}")
  local completed
  completed=$(echo "$resp" | jq -r '.output.completed // .completed // empty')
  echo "  completed=$completed"
  if [[ "$completed" != "true" ]]; then
    local reason
    reason=$(echo "$resp" | jq -r '.output.reason // .reason // empty')
    red "  web.test did not complete: $reason"
    return 1
  fi
  return 0
}

# ── T622: research.deep ──────────────────────────────────────────

test_T622() {
  local model
  model=$(echo "$models_resp" | jq -r '.data[0].id // empty')
  if [[ -z "$model" ]]; then
    red "  no model available"
    return 1
  fi
  echo "  using model: $model"
  local resp
  resp=$(run_pack "research.deep" "{\"query\":\"helmdeck browser automation\",\"model\":\"$model\",\"limit\":2}")
  local src_count synthesis
  src_count=$(echo "$resp" | jq -r '.output.sources | length' 2>/dev/null || echo 0)
  synthesis=$(echo "$resp" | jq -r '.output.synthesis // .synthesis // empty')
  echo "  sources=$src_count synthesis_length=${#synthesis}"
  if [[ "$src_count" -lt 1 ]]; then
    red "  no sources returned"
    return 1
  fi
  if [[ -z "$synthesis" ]]; then
    red "  synthesis is empty"
    return 1
  fi
  return 0
}

# ── T807f: gateway tool-use plumbing (structural check) ──────────

test_T807f_gateway() {
  # Verify the /v1/chat/completions endpoint accepts a request with
  # tools[] without erroring — even if the model is a stub. We don't
  # need an actual LLM response; we just need the gateway to not 400
  # on the new fields.
  local model
  model=$(echo "$models_resp" | jq -r '.data[0].id // empty')
  if [[ -z "$model" ]]; then
    red "  no model available for structural check"
    return 1
  fi
  local resp raw_status
  resp=$(curl -fsS -w '\n__status=%{http_code}' \
    -H "Authorization: Bearer $JWT" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"say hi\"}],\"tools\":[{\"name\":\"test_tool\",\"description\":\"test\",\"input_schema\":{\"type\":\"object\"}}]}" \
    "$HELMDECK_URL/v1/chat/completions" 2>/dev/null || true)
  raw_status=$(echo "$resp" | grep -oP '__status=\K\d+' || echo 0)
  echo "  /v1/chat/completions with tools[]: HTTP $raw_status"
  if [[ "$raw_status" -ge 400 && "$raw_status" -lt 500 ]]; then
    red "  gateway rejected tools[] field (HTTP $raw_status)"
    return 1
  fi
  # Any 2xx or 5xx (upstream provider error) is fine — the point is
  # the gateway didn't 400 on the schema.
  return 0
}

# ── T807f: desktop primitives (structural check) ─────────────────

test_T807f_desktop() {
  # Create a desktop-mode session, then call the new endpoints.
  # We can't verify xdotool actually ran (no display), but we can
  # verify the endpoints exist and accept the right shapes.
  echo "  (structural check: verifying new endpoints are registered)"

  # Check that the endpoints at least exist by sending a request
  # with a missing session_id — should get 400 not 404.
  local endpoints=(
    "/api/v1/desktop/double_click"
    "/api/v1/desktop/triple_click"
    "/api/v1/desktop/drag"
    "/api/v1/desktop/scroll"
    "/api/v1/desktop/modifier_click"
    "/api/v1/desktop/mouse_move"
    "/api/v1/desktop/wait"
    "/api/v1/desktop/zoom"
    "/api/v1/desktop/agent_status"
  )
  local all_ok=true
  for ep in "${endpoints[@]}"; do
    local resp raw_status
    resp=$(curl -fsS -w '\n__status=%{http_code}' \
      -H "Authorization: Bearer $JWT" \
      -H "Content-Type: application/json" \
      -d '{}' \
      "$HELMDECK_URL$ep" 2>/dev/null || true)
    raw_status=$(echo "$resp" | grep -oP '__status=\K\d+' || echo 0)
    # 400 (bad request) or 503 (no executor) means the endpoint is
    # registered. 404 means it's missing.
    if [[ "$raw_status" == "404" ]]; then
      red "  $ep returned 404 — endpoint not registered"
      all_ok=false
    else
      echo "  $ep → $raw_status (ok, endpoint exists)"
    fi
  done
  $all_ok
}

# ── T622a: repo.fetch context envelope ───────────────────────────
#
# Closes the 2026-04-14 OpenClaw "empty repo" false-positive. The
# workshop repo uses README.adoc, not README.md — an envelope-less
# repo.fetch response forced agents to guess at discovery. The
# envelope now surfaces the README inline + a signals block so
# the agent can branch deterministically.

test_T622a_envelope() {
  local resp
  resp=$(run_pack "repo.fetch" \
    '{"url":"https://github.com/tosin2013/low-latency-performance-workshop.git","depth":1}')

  # Use the JSON shape as the pass signal — run_pack's LAST_STATUS
  # is set in a subshell and is unreliable for callers (pre-existing
  # idiom in this script; see test_T807b which checks .output.markdown
  # directly rather than HTTP status).
  if [[ -z "$(echo "$resp" | jq -r '.output.clone_path // empty')" ]]; then
    red "  repo.fetch did not return clone_path"
    echo "  response: $(echo "$resp" | head -c 300)"
    return 1
  fi

  # All envelope fields must be present.
  local readme_path readme_truncated signals_has_readme sparse has_docs_dir code_count
  readme_path=$(echo "$resp" | jq -r '.output.readme.path // empty')
  readme_truncated=$(echo "$resp" | jq -r '.output.readme.truncated // empty')
  signals_has_readme=$(echo "$resp" | jq -r '.output.signals.has_readme // empty')
  sparse=$(echo "$resp" | jq -r '.output.signals.sparse // empty')
  has_docs_dir=$(echo "$resp" | jq -r '.output.signals.has_docs_dir // empty')
  code_count=$(echo "$resp" | jq -r '.output.signals.code_file_count // empty')

  local ok=true
  if [[ "$readme_path" != "README.adoc" ]]; then
    red "  readme.path = '$readme_path', want 'README.adoc' (glob-detect is broken)"
    ok=false
  fi
  if [[ "$signals_has_readme" != "true" ]]; then
    red "  signals.has_readme = '$signals_has_readme', want true"
    ok=false
  fi
  if [[ "$has_docs_dir" != "true" ]]; then
    red "  signals.has_docs_dir = '$has_docs_dir', want true (repo has content/, docs/, blog-posts/)"
    ok=false
  fi
  if [[ "$sparse" == "true" ]]; then
    red "  signals.sparse = true on a 190-file repo — threshold is busted"
    ok=false
  fi
  # tree should be an array with ≥10 entries for a real repo
  local tree_len
  tree_len=$(echo "$resp" | jq -r '.output.tree | length')
  if [[ "$tree_len" -lt 10 ]]; then
    red "  tree length = $tree_len, want ≥10"
    ok=false
  fi

  echo "  readme=$readme_path truncated=$readme_truncated tree_len=$tree_len code=$code_count"
  $ok
}

# ── T622a: repo.map ──────────────────────────────────────────────
#
# Aider-style structural symbol map. Runs the full ctags + python3
# reducer pipeline inside the sidecar against helmdeck's own Go
# source, verifies budget enforcement and --languages filtering.

test_T622a_map() {
  # Clone helmdeck to exercise real Go code.
  local fetch
  fetch=$(run_pack "repo.fetch" \
    '{"url":"https://github.com/tosin2013/helmdeck.git","depth":1}')
  local session_id clone_path
  session_id=$(echo "$fetch" | jq -r '.session_id // empty')
  clone_path=$(echo "$fetch" | jq -r '.output.clone_path // empty')
  if [[ -z "$session_id" || -z "$clone_path" ]]; then
    red "  bootstrap clone did not return session_id + clone_path"
    echo "  response: $(echo "$fetch" | head -c 300)"
    return 1
  fi

  # Map with a 1500-token budget restricted to Go, which is what
  # agents naturally reach for when asking "where is X defined."
  local map_resp
  map_resp=$(run_pack "repo.map" \
    "{\"_session_id\":\"$session_id\",\"clone_path\":\"$clone_path\",\"token_budget\":1500,\"languages\":[\"Go\"]}")
  if [[ -z "$(echo "$map_resp" | jq -r '.output.map // empty' 2>/dev/null)" ]]; then
    red "  repo.map did not return a map field"
    echo "  response: $(echo "$map_resp" | head -c 300)"
    return 1
  fi

  local covered total tokens map_text
  covered=$(echo "$map_resp" | jq -r '.output.files_covered')
  total=$(echo "$map_resp" | jq -r '.output.files_total')
  tokens=$(echo "$map_resp" | jq -r '.output.tokens_estimated')
  map_text=$(echo "$map_resp" | jq -r '.output.map')

  local ok=true
  if [[ "$covered" -lt 1 ]]; then
    red "  files_covered = $covered, want ≥1 (ctags found nothing in a Go repo?)"
    ok=false
  fi
  if [[ "$tokens" -gt 2000 ]]; then
    red "  tokens_estimated = $tokens, expected ≤1500 + small slack (budget ignored)"
    ok=false
  fi
  if [[ "$covered" -ge "$total" && "$total" -gt 10 ]]; then
    red "  covered=$covered >= total=$total on a big repo — budget should have truncated"
    ok=false
  fi
  # The default excludes should keep package-lock.json and node_modules out.
  if grep -qE 'package-lock\.json|node_modules/' <<<"$map_text"; then
    red "  junk files leaked into the map (defaultExcludes not applied)"
    ok=false
  fi

  echo "  files_covered=$covered/$total tokens=$tokens"
  $ok
}

# ── run all tests ─────────────────────────────────────────────────

run_test "T807a" "Playwright MCP endpoint on session" test_T807a

run_test "T622a-envelope" "repo.fetch context envelope (readme auto-detect + signals)" test_T622a_envelope
run_test "T622a-map"      "repo.map structural symbol map (budget + language filter + junk exclude)" test_T622a_map

if [[ "$FIRECRAWL_UP" == true ]]; then
  run_test "T807b" "web.scrape via Firecrawl" test_T807b
else
  skip_test "T807b" "Firecrawl overlay not running (docker compose -f compose.firecrawl.yml up)"
fi

if [[ "$DOCLING_UP" == true ]]; then
  run_test "T807c" "doc.parse via Docling" test_T807c
else
  skip_test "T807c" "Docling overlay not running (docker compose -f compose.docling.yml up)"
fi

if [[ "$LLM_AVAILABLE" == true ]] && [[ "$FIRECRAWL_UP" == true ]]; then
  run_test "T807e" "web.test NL browser testing" test_T807e
else
  skip_test "T807e" "requires LLM + Firecrawl (web.test needs Playwright MCP + model)"
fi

if [[ "$LLM_AVAILABLE" == true ]] && [[ "$FIRECRAWL_UP" == true ]]; then
  run_test "T622" "research.deep Firecrawl search + synthesis" test_T622
else
  skip_test "T622" "requires LLM + Firecrawl"
fi

# T623 (content.ground) needs a session with a file — skip in the
# automated script; it's covered by unit tests with 12 subtests.
skip_test "T623" "content.ground requires a pre-cloned repo session (covered by unit tests)"

if [[ "$LLM_AVAILABLE" == true ]]; then
  run_test "T807f-gateway" "gateway tool-use plumbing (accepts tools[] field)" test_T807f_gateway
else
  skip_test "T807f-gateway" "requires at least one LLM provider"
fi

run_test "T807f-desktop" "desktop primitives (8 new endpoints registered)" test_T807f_desktop

# T807f-vision.* native tool-use requires a desktop-mode session
# with a live display — structural tests are in Go (30 vision tests).
skip_test "T807f-vision" "native tool-use routing (covered by 30 Go unit tests)"

# T807f-D/E (audit + noVNC) are verified structurally via the
# desktop endpoint check above (agent_status is one of the endpoints).
# Full E2E requires a live noVNC viewer.
skip_test "T807f-D+E" "audit recording + noVNC witness mode (covered by Go tests + agent_status endpoint check)"

# ── summary ───────────────────────────────────────────────────────

echo
blue "══ Phase 6.5 Validation Summary ══"
green "  PASSED: $PASS"
if [[ "$FAIL" -gt 0 ]]; then
  red "  FAILED: $FAIL (${FAILED[*]})"
fi
yellow "  SKIPPED: $SKIP"
echo
echo "  Tests that require services not currently running are skipped"
echo "  cleanly — bring up the overlay and re-run to validate them."
echo

[[ "$FAIL" -eq 0 ]]
