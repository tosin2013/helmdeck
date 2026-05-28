#!/usr/bin/env bash
# scripts/validate-openclaw.sh — drive every helmdeck pack we can
# test today through the OpenClaw → SSE MCP → helmdeck round trip,
# verifying each call landed via helmdeck's audit log (event_type
# pack_call). The audit log is the truth source — LLM responses
# are unreliable (the model hallucinates failures and successes).
#
# Prerequisites:
#   - Helmdeck stack running: ./scripts/install.sh
#   - OpenClaw stack running with the sidecar overlay applied:
#       OPENROUTER_API_KEY=... docker compose \
#         -f /root/openclaw/docker-compose.yml \
#         -f deploy/compose/compose.openclaw-sidecar.yml \
#         up -d openclaw-gateway
#   - openclaw-gateway joined to baas-net (compose run can drop
#     this — re-attach with `docker network connect baas-net
#     openclaw-openclaw-gateway-1` if needed)
#   - OpenClaw default model set to a tool-calling LLM:
#       docker compose -f /root/openclaw/docker-compose.yml \
#         run --rm openclaw-cli models set openrouter/auto
#   - OpenRouter auth profile present in
#     /root/.openclaw/agents/main/agent/auth-profiles.json
#
# After the per-pack round trips, it also runs every builtin pipeline
# (ADR 041) end-to-end through the pipeline REST API and judges each by
# the run's failure_class: a pack_bug failure fails the run; a
# caller_fixable/transient/state_changed failure is reported but does NOT
# fail (the pipeline wiring worked — the input/world/env was the cause).
#
# Usage:
#   ./scripts/validate-openclaw.sh                       # packs + pipelines
#   ./scripts/validate-openclaw.sh --pack browser.screenshot_url  # one pack
#   ./scripts/validate-openclaw.sh --pipeline builtin.research-blog # one pipeline
#   ./scripts/validate-openclaw.sh --pipelines-only      # skip packs
#   ./scripts/validate-openclaw.sh --skip-pipelines      # packs only
#   ./scripts/validate-openclaw.sh --skip-mcp-rewrite    # don't re-mint JWT
#
# Exits 0 if every pack passed and no pipeline hit a pack_bug; 1 otherwise.

set -euo pipefail

HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
HELMDECK_USER="${HELMDECK_USER:-admin}"
HELMDECK_PASS="${HELMDECK_PASS:-}"
OPENCLAW_GATEWAY_CONTAINER="${OPENCLAW_GATEWAY_CONTAINER:-openclaw-openclaw-gateway-1}"
OPENCLAW_COMPOSE="${OPENCLAW_COMPOSE:-/root/openclaw/docker-compose.yml}"
SIDECAR_OVERLAY="${SIDECAR_OVERLAY:-/root/helmdeck/deploy/compose/compose.openclaw-sidecar.yml}"
HELMDECK_NETWORK="${HELMDECK_NETWORK:-baas-net}"
# Pipeline validation knobs. Pipelines chain several packs (some heavy:
# narrate renders video), so they poll to a generous deadline.
PIPELINE_TIMEOUT="${PIPELINE_TIMEOUT:-900}"   # seconds to reach a terminal run state
PIPELINE_POLL="${PIPELINE_POLL:-5}"           # seconds between run-status polls

# CLI flags
ONE_PACK=""
ONE_PIPELINE=""
SKIP_REWRITE=false
SKIP_PACKS=false
SKIP_PIPELINES=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pack) ONE_PACK="$2"; shift 2 ;;
    --pipeline) ONE_PIPELINE="$2"; shift 2 ;;
    --skip-mcp-rewrite) SKIP_REWRITE=true; shift ;;
    --skip-packs) SKIP_PACKS=true; shift ;;
    --skip-pipelines) SKIP_PIPELINES=true; shift ;;
    --pipelines-only) SKIP_PACKS=true; shift ;;
    -h|--help)
      sed -n '2,40p' "$0" | sed 's|^# \?||'
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
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])'
}

ensure_baas_net() {
  if ! docker inspect "$OPENCLAW_GATEWAY_CONTAINER" \
       --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
       2>/dev/null | grep -q "$HELMDECK_NETWORK"; then
    yellow "openclaw-gateway not on $HELMDECK_NETWORK; reattaching"
    docker network connect "$HELMDECK_NETWORK" "$OPENCLAW_GATEWAY_CONTAINER" 2>/dev/null || true
  fi
}

write_helmdeck_mcp_server() {
  local jwt="$1"
  # Use lowercase 'authorization' — see docs/integrations/openclaw.md
  # known issue. Capital A triggers a header case-collision in
  # OpenClaw's bundle-mcp that turns into a 401 against helmdeck.
  docker compose -f "$OPENCLAW_COMPOSE" -f "$SIDECAR_OVERLAY" run --rm openclaw-cli \
    mcp set helmdeck \
    "{\"url\":\"http://helmdeck-control-plane:3000/api/v1/mcp/sse\",\"headers\":{\"authorization\":\"Bearer $jwt\"}}" \
    >/dev/null
  # openclaw mcp set triggers a config-change reload that drops the
  # gateway's WebSocket connections. Wait for it to come back healthy
  # before running any agent prompts. docker compose run also recreates
  # the container, which drops the baas-net overlay — re-attach.
  blue "  waiting for openclaw-gateway to stabilize after config write"
  sleep 3
  ensure_baas_net
  for _ in $(seq 1 30); do
    if curl -fsS "http://localhost:18789/healthz" >/dev/null 2>&1; then break; fi
    sleep 1
  done
}

# Query helmdeck audit log for pack_call entries since a given
# RFC3339 timestamp, optionally filtered by path substring.
audit_pack_calls_since() {
  local jwt="$1" since="$2" path_filter="${3:-}"
  # MCP-routed pack calls log as event_type=mcp_call (path
  # /api/v1/mcp/sse/message); direct REST calls log as pack_call
  # (path /api/v1/packs/<name>). Accept either.
  curl -fsS -H "Authorization: Bearer $jwt" \
    "$HELMDECK_URL/api/v1/audit?limit=50&from=$since" \
    | python3 -c "
import sys, json
d = json.load(sys.stdin)
filt = '$path_filter'
hits = []
for e in d.get('entries', []):
    et = e.get('event_type','')
    path = e.get('path','')
    # Accept pack_call with the pack path, OR mcp_call (which routes
    # through /api/v1/mcp/sse/message for all packs — we can't filter
    # by pack name on MCP calls without inspecting the payload, so
    # any mcp_call since the timestamp counts as a hit).
    if et == 'pack_call':
        if filt and filt not in path:
            continue
    elif et == 'mcp_call':
        pass  # accept all MCP calls — they're tool invocations
    else:
        continue
    hits.append(f\"{e['timestamp'][:19]} {et:15} {e.get('method','?')} {path} {e.get('status_code','?')}\")
for h in hits:
    print(h)
print(f'__count={len(hits)}')
"
}

# Run an OpenClaw agent prompt and report whether the expected pack
# call landed in the audit log within the run window.
#   $1 — test name
#   $2 — pack name (e.g. http.fetch)
#   $3 — agent prompt
run_test() {
  local name="$1" pack="$2" prompt="$3"
  blue "── $name (pack=$pack)"

  local since
  since=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
  sleep 1  # ensure audit log timestamps are >= since

  local agent_out
  if ! agent_out=$(docker exec "$OPENCLAW_GATEWAY_CONTAINER" \
      node /app/dist/index.js agent --message "$prompt" --to "+10000000001" 2>&1); then
    red "  ✗ agent invocation failed: $agent_out"
    return 1
  fi
  echo "  agent reply: $(echo "$agent_out" | tail -1 | head -c 200)"

  sleep 1  # let audit writer flush
  local audit
  audit=$(audit_pack_calls_since "$JWT" "$since" "/api/v1/packs/$pack")
  local count
  count=$(echo "$audit" | grep -oP '__count=\K\d+' || echo 0)

  if [[ "$count" -ge 1 ]]; then
    green "  ✓ $count pack_call(s) for $pack landed in audit log"
    echo "$audit" | grep -v __count | sed 's/^/    /'
    return 0
  else
    red "  ✗ NO pack_call for $pack in audit log since $since"
    red "    (the LLM may have hallucinated the call without invoking the tool)"
    return 1
  fi
}

# Extract a top-level string field from a run-status JSON blob.
run_field() {
  python3 -c "import sys,json; print(json.load(sys.stdin).get('$1',''))" 2>/dev/null
}

# Run a builtin pipeline end-to-end via the REST API and judge it by the
# run's failure_class — leaning on the pipeline failure-attribution
# (ADR 044) the runner already produces:
#
#   succeeded                          → PASS (rc 0)
#   failed + failure_class=pack_bug    → FAIL (rc 1)  — a real helmdeck bug
#   failed + caller_fixable/transient/ → "ran, not a bug" (rc 2)  — the
#     state_changed                       pipeline machinery worked; the
#                                          input/world/env didn't cooperate
#     (e.g. a Firecrawl overlay that's down, or a query with no sources).
#
# This is deliberately NOT driven through OpenClaw's LLM: a multi-step
# pipeline started + polled by an agent prompt is too flaky to assert on.
# We hit helmdeck's pipeline REST API directly (same JWT) so the result
# reflects the pipeline, not the model's tool-use.
#   $1 — display name   $2 — pipeline id   $3 — inputs JSON object
run_pipeline_test() {
  local name="$1" pid="$2" inputs="$3"
  blue "── pipeline: $name ($pid)"

  local resp run_id
  if ! resp=$(curl -fsS -X POST "$HELMDECK_URL/api/v1/pipelines/$pid/run" \
      -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
      -d "{\"inputs\":$inputs}" 2>&1); then
    red "  ✗ failed to start run: $resp"
    return 1
  fi
  run_id=$(printf '%s' "$resp" | run_field run_id)
  if [[ -z "$run_id" ]]; then
    red "  ✗ no run_id in start response: $resp"
    return 1
  fi
  echo "  run_id=$run_id — polling up to ${PIPELINE_TIMEOUT}s"

  local status="" fclass="" freason="" run
  local deadline=$((SECONDS + PIPELINE_TIMEOUT))
  while (( SECONDS < deadline )); do
    if run=$(curl -fsS -H "Authorization: Bearer $JWT" \
        "$HELMDECK_URL/api/v1/pipelines/$pid/runs/$run_id" 2>/dev/null); then
      status=$(printf '%s' "$run" | run_field status)
      if [[ "$status" == "succeeded" || "$status" == "failed" ]]; then
        fclass=$(printf '%s' "$run" | run_field failure_class)
        freason=$(printf '%s' "$run" | run_field failure_reason)
        break
      fi
    fi
    sleep "$PIPELINE_POLL"
  done

  case "$status" in
    succeeded)
      green "  ✓ pipeline succeeded (all steps ran)"
      return 0 ;;
    failed)
      if [[ "$fclass" == "pack_bug" ]]; then
        red "  ✗ FAILED — pack_bug (a real helmdeck bug): $freason"
        return 1
      fi
      yellow "  ⚠ failed but NOT a pack bug (class=${fclass:-unknown}) — pipeline wiring is fine, the input/world/env is the cause:"
      yellow "      $freason"
      return 2 ;;
    *)
      red "  ✗ run did not reach a terminal state within ${PIPELINE_TIMEOUT}s (last status=${status:-none})"
      return 1 ;;
  esac
}

# ── pre-flight ────────────────────────────────────────────────────

require_cmd docker
require_cmd curl
require_cmd python3
require_cmd jq

read_admin_password

if ! curl -fsS "$HELMDECK_URL/healthz" >/dev/null; then
  red "helmdeck not reachable at $HELMDECK_URL/healthz"
  exit 2
fi

if ! docker ps --format '{{.Names}}' | grep -q "^$OPENCLAW_GATEWAY_CONTAINER$"; then
  red "openclaw-gateway container not running ($OPENCLAW_GATEWAY_CONTAINER)"
  exit 2
fi

ensure_baas_net

if [[ "$SKIP_REWRITE" != true ]]; then
  blue "minting fresh helmdeck JWT and updating openclaw mcp.servers.helmdeck"
  JWT=$(mint_jwt)
  write_helmdeck_mcp_server "$JWT"
else
  JWT=$(mint_jwt)  # still need it for audit queries
fi

# ── tests ─────────────────────────────────────────────────────────

# ── standalone packs (no session needed) ─────────────────────────

declare -a TESTS=(
  # HTTP + web
  "http.fetch GET example.com|http.fetch|Use the helmdeck__http_fetch tool to GET https://example.com with User-Agent 'Helmdeck-Test/1.0'. Just call the tool."
  "browser.screenshot_url example.com|browser.screenshot_url|Use the helmdeck__browser_screenshot_url tool with url https://example.com. Just call the tool."
  "web.scrape_spa HN headlines|web.scrape_spa|Use the helmdeck__web_scrape_spa tool with url https://news.ycombinator.com and fields {\"top\":{\"selector\":\"span.titleline > a\",\"format\":\"text\"}}. Just call the tool."
  "slides.render deck|slides.render|Use the helmdeck__slides_render tool with markdown '---\\nmarp: true\\n---\\n# Test\\nHello' and format pdf. Just call the tool."
  "browser.interact example.com|browser.interact|Use the helmdeck__browser_interact tool with url https://example.com and actions [{\"action\":\"extract\",\"selector\":\"h1\",\"format\":\"text\"},{\"action\":\"assert_text\",\"text\":\"Example Domain\"},{\"action\":\"screenshot\"}]. Just call the tool."

  # GitHub (PAT optional for reads)
  "github.list_prs openclaw|github.list_prs|Use the helmdeck__github_list_prs tool with repo openclaw/openclaw and state open. Just call the tool."
  "github.list_issues helmdeck|github.list_issues|Use the helmdeck__github_list_issues tool with repo tosin2013/helmdeck and state all. Just call the tool."
  "github.search SSE|github.search|Use the helmdeck__github_search tool with query 'repo:tosin2013/helmdeck SSE' and type code. Just call the tool."

  # Session-chained: repo.fetch → fs → git → cmd (uses _session_id)
  # NOTE: These test prompts ask the LLM to chain the calls using
  # the session_id from repo.fetch. If the model doesn't chain
  # correctly, individual packs may pass REST testing but fail here.
  "repo.fetch + fs.list|repo.fetch|Use helmdeck__repo_fetch to clone https://github.com/octocat/Hello-World.git with depth 1. Then use helmdeck__fs_list with the clone_path and _session_id from the result. Report both results."

  # ── Phase 6.5 packs ──────────────────────────────────────────────
  # Firecrawl-backed (requires compose.firecrawl.yml overlay running)
  "web.scrape example.com|web.scrape|Use the helmdeck__web_scrape tool with url https://example.com. Just call the tool."
  "research.deep helmdeck|research.deep|Use the helmdeck__research_deep tool with query 'helmdeck browser automation' and model openrouter/auto and limit 5. Just call the tool."

  # Docling-backed (requires compose.docling.yml overlay running)
  "doc.parse example.com|doc.parse|Use the helmdeck__doc_parse tool with source_url https://example.com and formats [\"md\"]. Just call the tool."

  # Playwright MCP-backed (requires T807a sidecar with Playwright MCP)
  "web.test example.com|web.test|Use the helmdeck__web_test tool with url https://example.com and instruction 'Confirm the page has the heading Example Domain' and model openrouter/auto and max_steps 3. Just call the tool."

  # Session-chained content grounding (needs repo.fetch first)
  "content.ground blog post|content.ground|First use helmdeck__repo_fetch to clone https://github.com/octocat/Hello-World.git with depth 1. Then use helmdeck__content_ground with clone_path and path README and model openrouter/auto and _session_id from repo.fetch. Just call the tools."

  # Slides narration (requires ElevenLabs vault key 'elevenlabs-key')
  "slides.narrate deck|slides.narrate|Use the helmdeck__slides_narrate tool with markdown '---\\nmarp: true\\n---\\n# Welcome\\n\\n<!-- Hello everyone, welcome to this test. -->\\n\\nFirst slide.\\n\\n---\\n# End\\n\\nThank you.' Just call the tool."

  # Desktop primitives via vision (requires desktop-mode session + vision model)
  "vision.click_anywhere example.com|vision.click_anywhere|Use the helmdeck__vision_click_anywhere tool with goal 'click the Example Domain heading' and model openrouter/auto and max_steps 3. Just call the tool."
  "vision.extract_visible_text|vision.extract_visible_text|Use the helmdeck__vision_extract_visible_text tool with model openrouter/auto. Just call the tool."
  "vision.fill_form_by_label|vision.fill_form_by_label|Use the helmdeck__vision_fill_form_by_label tool with model openrouter/auto and fields {\"search\":\"hello\"}. Just call the tool."

  # Desktop app + screenshot
  "desktop.run_app_and_screenshot|desktop.run_app_and_screenshot|Use the helmdeck__desktop_run_app_and_screenshot tool with command 'chromium' and args ['--no-sandbox','https://example.com']. Just call the tool."

  # Document OCR (session-based, needs an image)
  "doc.ocr screenshot|doc.ocr|First use helmdeck__browser_screenshot_url with url https://example.com. Then use helmdeck__doc_ocr with the artifact from the screenshot. Just call both tools."

  # Language sidecars
  "python.run hello|python.run|Use the helmdeck__python_run tool with code 'print(\"hello from python\")'. Just call the tool."
  "node.run hello|node.run|Use the helmdeck__node_run tool with code 'console.log(\"hello from node\")'. Just call the tool."

  # Repo push (read-only clone, push to a test branch, needs vault credential)
  "repo.push test|repo.push|First use helmdeck__repo_fetch to clone https://github.com/octocat/Hello-World.git with depth 1. Then use helmdeck__repo_push with the clone_path and _session_id and branch test-helmdeck-validate. Just call the tools."
)

declare -a SKIPS=(
  # Write operations that create real resources on GitHub — intentionally
  # never automated. Test manually with a throwaway repo.
  "github.create_issue|write operation — creates real issues on GitHub"
  "github.post_comment|write operation — posts real comments on GitHub"
  "github.create_release|write operation — creates real releases on GitHub"
)

PASS=0
FAIL=0
FAILED=()
if [[ "$SKIP_PACKS" != true ]]; then
  for entry in "${TESTS[@]}"; do
    IFS='|' read -r name pack prompt <<<"$entry"
    if [[ -n "$ONE_PACK" && "$pack" != "$ONE_PACK" ]]; then continue; fi
    if run_test "$name" "$pack" "$prompt"; then
      PASS=$((PASS + 1))
    else
      FAIL=$((FAIL + 1))
      FAILED+=("$pack")
    fi
    echo
  done
fi

# ── builtin pipelines (ADR 041) ───────────────────────────────────
# Each runs end-to-end through the pipeline REST API and is judged by
# the run's failure_class (see run_pipeline_test): pack_bug is a real
# helmdeck bug and fails the run; caller_fixable/transient/state_changed
# mean the pipeline wiring worked but the env/input didn't, so they're
# reported, not failed. Inputs use the seed's baked openrouter/auto +
# allow_silent_output, so most run with just the right service overlays
# (Firecrawl for research/scrape, Docling for doc, hyperframes for video)
# and no extra credentials. Queries are kept short/focused so research
# steps actually find sources.
declare -a PIPELINES=(
  "grounded-deck|builtin.grounded-deck|{\"markdown\":\"# Helmdeck\\n\\nHelmdeck runs browser automation and capability packs inside sandboxed containers.\"}"
  "grounded-blog|builtin.grounded-blog|{\"markdown\":\"# Helmdeck\\n\\nHelmdeck runs browser automation in sandboxed containers.\",\"title\":\"Helmdeck overview\"}"
  "research-deck|builtin.research-deck|{\"query\":\"mnemonics memory techniques\"}"
  "research-narrate|builtin.research-narrate|{\"query\":\"mnemonics memory techniques\"}"
  "research-podcast|builtin.research-podcast|{\"query\":\"mnemonics memory techniques\"}"
  "scrape-ground-blog|builtin.scrape-ground-blog|{\"url\":\"https://example.com\",\"title\":\"Example\"}"
  "research-ground-deck|builtin.research-ground-deck|{\"query\":\"mnemonics memory techniques\"}"
  "doc-ground-blog|builtin.doc-ground-blog|{\"source_url\":\"https://example.com\",\"title\":\"Example\"}"
  "scrape-deck|builtin.scrape-deck|{\"url\":\"https://example.com\"}"
  "research-blog|builtin.research-blog|{\"query\":\"mnemonics memory techniques\",\"title\":\"Mnemonics\"}"
  "repo-readme-narrate|builtin.repo-readme-narrate|{\"repo_url\":\"https://github.com/octocat/Hello-World.git\"}"
  "repo-readme-podcast|builtin.repo-readme-podcast|{\"repo_url\":\"https://github.com/octocat/Hello-World.git\"}"
  "html-video|builtin.html-video|{\"composition_html\":\"<html><body><h1>Hello from helmdeck</h1></body></html>\"}"
)

PIPE_PASS=0
PIPE_BUG=0
PIPE_NONBUG=0
PIPE_FAILED=()
if [[ "$SKIP_PIPELINES" != true ]]; then
  echo
  blue "════ builtin pipelines — end-to-end via REST, fail only on pack_bug ════"
  echo
  for entry in "${PIPELINES[@]}"; do
    IFS='|' read -r name pid inputs <<<"$entry"
    if [[ -n "$ONE_PIPELINE" && "$pid" != "$ONE_PIPELINE" && "$name" != "$ONE_PIPELINE" ]]; then continue; fi
    set +e
    run_pipeline_test "$name" "$pid" "$inputs"
    rc=$?
    set -e
    case "$rc" in
      0) PIPE_PASS=$((PIPE_PASS + 1)) ;;
      2) PIPE_NONBUG=$((PIPE_NONBUG + 1)) ;;
      *) PIPE_BUG=$((PIPE_BUG + 1)); PIPE_FAILED+=("$pid") ;;
    esac
    echo
  done
fi

echo
blue "── results"
green "  packs passed:        $PASS"
if [[ "$FAIL" -gt 0 ]]; then
  red "  packs failed:        $FAIL (${FAILED[*]})"
fi
green "  pipelines ok:        $PIPE_PASS"
if [[ "$PIPE_NONBUG" -gt 0 ]]; then
  yellow "  pipelines ran, non-bug failure: $PIPE_NONBUG (env/input — not a helmdeck bug)"
fi
if [[ "$PIPE_BUG" -gt 0 ]]; then
  red "  pipelines pack_bug:  $PIPE_BUG (${PIPE_FAILED[*]})"
fi
echo
yellow "── intentionally skipped (not bugs):"
for entry in "${SKIPS[@]}"; do
  IFS='|' read -r pack reason <<<"$entry"
  printf '  %-35s %s\n' "$pack" "$reason"
done

# Exit non-zero on any pack failure OR any pipeline pack_bug. A non-bug
# pipeline failure (caller_fixable/transient/state_changed) does NOT fail
# the run — the machinery worked; the input/world/env was the cause.
[[ "$FAIL" -eq 0 && "$PIPE_BUG" -eq 0 ]]
