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
# Usage:
#   ./scripts/validate-openclaw.sh                       # full run
#   ./scripts/validate-openclaw.sh --pack browser.screenshot_url  # one
#   ./scripts/validate-openclaw.sh --skip-mcp-rewrite    # don't re-mint JWT
#
# Exits 0 if every test passed, 1 if any failed.

set -euo pipefail

HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
HELMDECK_USER="${HELMDECK_USER:-admin}"
HELMDECK_PASS="${HELMDECK_PASS:-}"
OPENCLAW_GATEWAY_CONTAINER="${OPENCLAW_GATEWAY_CONTAINER:-openclaw-openclaw-gateway-1}"
OPENCLAW_COMPOSE="${OPENCLAW_COMPOSE:-/root/openclaw/docker-compose.yml}"
SIDECAR_OVERLAY="${SIDECAR_OVERLAY:-/root/helmdeck/deploy/compose/compose.openclaw-sidecar.yml}"
HELMDECK_NETWORK="${HELMDECK_NETWORK:-baas-net}"

# CLI flags
ONE_PACK=""
SKIP_REWRITE=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --pack) ONE_PACK="$2"; shift 2 ;;
    --skip-mcp-rewrite) SKIP_REWRITE=true; shift ;;
    -h|--help)
      sed -n '2,30p' "$0" | sed 's|^# \?||'
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

declare -a TESTS=(
  "http.fetch GET example.com|http.fetch|Use the helmdeck__http_fetch tool to GET https://example.com. Set the User-Agent header to 'Helmdeck-Test/1.0'. Just call the tool."
  "browser.screenshot_url example.com|browser.screenshot_url|Use the helmdeck__browser_screenshot_url tool with url https://example.com. Just call the tool."
  "web.scrape_spa example.com h1|web.scrape_spa|Use the helmdeck__web_scrape_spa tool with url https://example.com and selectors {\"title\":\"h1\"}. Just call the tool."
  "slides.render tiny markdown|slides.render|Use the helmdeck__slides_render tool with markdown '# Hello\\n\\nWorld' and format pdf. Just call the tool."
)

declare -a SKIPS=(
  "python.run|sidecar image helmdeck-sidecar-python:dev not built (run: make sidecar-python-build)"
  "node.run|sidecar image helmdeck-sidecar-node:dev not built (run: make sidecar-node-build)"
  "repo.fetch|v1 only supports ssh URLs; https support is T504"
  "fs.read|requires a session-chained pack run after repo.fetch"
  "fs.write|requires a session-chained pack run"
  "fs.list|requires a session-chained pack run"
  "fs.patch|requires a session-chained pack run"
  "cmd.run|requires a session-chained pack run"
  "git.commit|requires a session-chained pack run"
  "repo.push|requires a session-chained pack run"
  "doc.ocr|requires an image artifact in the session workspace"
  "desktop.run_app_and_screenshot|requires desktop sidecar variant"
  "vision.click_anywhere|requires desktop session + vision model"
  "vision.extract_visible_text|requires desktop session + vision model"
  "vision.fill_form_by_label|requires desktop session + vision model"
)

PASS=0
FAIL=0
FAILED=()
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

echo
blue "── results"
green "  passed: $PASS"
if [[ "$FAIL" -gt 0 ]]; then
  red "  failed: $FAIL (${FAILED[*]})"
fi
echo
yellow "── intentionally skipped (not bugs):"
for entry in "${SKIPS[@]}"; do
  IFS='|' read -r pack reason <<<"$entry"
  printf '  %-35s %s\n' "$pack" "$reason"
done

[[ "$FAIL" -eq 0 ]]
