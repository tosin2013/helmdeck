#!/usr/bin/env bash
# scripts/smoke-integration.sh — fast (~30s), NON-destructive
# post-install / post-upgrade confidence check that drives a real
# OpenClaw agent round-trip against the ALREADY-RUNNING stack.
#
# Unlike scripts/smoke.sh (which boots its own ephemeral core stack
# and tears it down) and scripts/validate-openclaw.sh (the full
# 23-pack regression matrix), this script answers ONE question:
#
#   "When I talk to my agent, does a pack actually execute
#    end-to-end?"
#
# It runs the smallest set of probes that proves each layer of the
# agent → MCP bridge → control plane → pack → audit log path is
# alive. It does NOT `compose up`/`down` and never tears anything
# down — it assumes both helmdeck and OpenClaw are already up.
#
# Probes (driven THROUGH the OpenClaw agent):
#   0. Catalog       — agent lists tools starting with `helmdeck__`
#                      (proves bridge + JWT + SSE path + SKILLS.md).
#                      If ZERO are visible we FAIL LOUDLY with the
#                      OpenClaw CLI MCP-load regression diagnosis —
#                      we never report "0 packs ran = healthy".
#   1. http.fetch    — GET example.com (stateless dispatch).
#   2. browser.screenshot_url — session pack + Garage artifact store.
#   3. repo.fetch → fs.list   — session chaining via the same
#                      `_session_id` (the contract from #232).
#
# Integration-aware: if HELMDECK_FIRECRAWL_ENABLED is on, also probe
# `web.scrape`; if HELMDECK_DOCLING_ENABLED is on, also probe
# `doc.parse`. Disabled integrations are SKIPPED, not failed.
#
# Truth source is the audit log (event_type pack_call / mcp_call),
# NOT the LLM's text reply — same discipline as validate-openclaw.sh.
# A model that *says* it ran a pack but produced no audit entry FAILS.
#
# Usage:
#   ./scripts/smoke-integration.sh                 # run all enabled probes
#   ./scripts/smoke-integration.sh --skip-mcp-rewrite   # don't re-mint JWT
#   ./scripts/smoke-integration.sh -h
#
# Exits 0 if every probe passed, non-zero on the first failure with
# the failing probe + an audit-log excerpt.

set -euo pipefail

# ── config (override via env) ─────────────────────────────────────
HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
HELMDECK_USER="${HELMDECK_USER:-admin}"
HELMDECK_PASS="${HELMDECK_PASS:-}"
HELMDECK_ENV_FILE="${HELMDECK_ENV_FILE:-/root/helmdeck/deploy/compose/.env.local}"
HELMDECK_CONTAINER="${HELMDECK_CONTAINER:-helmdeck-control-plane}"
HELMDECK_NETWORK="${HELMDECK_NETWORK:-baas-net}"
OPENCLAW_GATEWAY_CONTAINER="${OPENCLAW_GATEWAY_CONTAINER:-openclaw-openclaw-gateway-1}"
OPENCLAW_COMPOSE="${OPENCLAW_COMPOSE:-/root/openclaw/docker-compose.yml}"
SIDECAR_OVERLAY="${SIDECAR_OVERLAY:-/root/helmdeck/deploy/compose/compose.openclaw-sidecar.yml}"
OPENCLAW_DOCS="docs/integrations/openclaw.md"

# ── CLI flags ─────────────────────────────────────────────────────
SKIP_REWRITE=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-mcp-rewrite) SKIP_REWRITE=true; shift ;;
    -h|--help)
      sed -n '2,49p' "$0" | sed 's|^# \?||'
      exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 64 ;;
  esac
done

# ── color helpers (match validate-openclaw.sh) ───────────────────
red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
blue()   { printf '\033[34m%s\033[0m\n' "$*"; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { red "missing prerequisite: $1"; exit 2; }
}

read_admin_password() {
  if [[ -n "$HELMDECK_PASS" ]]; then return; fi
  if [[ -f "$HELMDECK_ENV_FILE" ]]; then
    HELMDECK_PASS=$(grep '^HELMDECK_ADMIN_PASSWORD=' "$HELMDECK_ENV_FILE" | cut -d= -f2-)
  fi
  if [[ -z "$HELMDECK_PASS" ]]; then
    red "HELMDECK_PASS not set and could not read from $HELMDECK_ENV_FILE"
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

# Detect whether an integration overlay is enabled. We read the flag
# from the live control-plane container env first (authoritative — it
# reflects what's actually running), and fall back to .env.local.
integration_enabled() {
  local var="$1" val=""
  val=$(docker exec "$HELMDECK_CONTAINER" sh -c "printf '%s' \"\${$var:-}\"" 2>/dev/null || true)
  if [[ -z "$val" && -f "$HELMDECK_ENV_FILE" ]]; then
    val=$(grep "^${var}=" "$HELMDECK_ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)
  fi
  [[ "$val" == "true" || "$val" == "1" ]]
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
  # gateway's WebSocket connections, and `compose run` recreates the
  # container (dropping the baas-net overlay). Wait + re-attach.
  blue "  waiting for openclaw-gateway to stabilize after config write"
  sleep 3
  ensure_baas_net
  for _ in $(seq 1 30); do
    if curl -fsS "http://localhost:18789/healthz" >/dev/null 2>&1; then break; fi
    sleep 1
  done
}

# Run an OpenClaw agent prompt and return its raw output. Drives the
# CLI agent surface in the gateway container.
run_agent() {
  local prompt="$1"
  docker exec "$OPENCLAW_GATEWAY_CONTAINER" \
    node /app/dist/index.js agent --message "$prompt" --to "+10000000001" 2>&1
}

# Query helmdeck audit log for pack_call / mcp_call entries since a
# given RFC3339 timestamp, optionally filtered by REST pack path.
# Prints matching lines plus a trailing `__count=<n>` marker.
audit_calls_since() {
  local jwt="$1" since="$2" path_filter="${3:-}"
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
    # pack_call logs the REST pack path; mcp_call routes every tool
    # invocation through /api/v1/mcp/sse/message (can't filter by pack
    # name without the payload, so any mcp_call since 'since' counts).
    if et == 'pack_call':
        if filt and filt not in path:
            continue
    elif et == 'mcp_call':
        pass
    else:
        continue
    hits.append(f\"{e['timestamp'][:19]} {et:10} {e.get('method','?')} {path} {e.get('status_code','?')}\")
for h in hits:
    print(h)
print(f'__count={len(hits)}')
"
}

# Probe: run a prompt, then assert the expected pack landed in the
# audit log within the run window.
#   $1 — probe name   $2 — pack name   $3 — agent prompt
PASS=0
FAIL=0
FAILED=()
probe() {
  local name="$1" pack="$2" prompt="$3"
  blue "── probe: $name (pack=$pack)"

  local since
  since=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
  sleep 1  # ensure audit timestamps are >= since

  local agent_out
  if ! agent_out=$(run_agent "$prompt"); then
    red "  ✗ agent invocation failed: $(echo "$agent_out" | tail -1 | head -c 200)"
    FAIL=$((FAIL + 1)); FAILED+=("$pack"); return 1
  fi
  echo "  agent reply: $(echo "$agent_out" | tail -1 | head -c 160)"

  sleep 1  # let the audit writer flush
  local audit count
  audit=$(audit_calls_since "$JWT" "$since" "/api/v1/packs/$pack")
  count=$(echo "$audit" | grep -oE '__count=[0-9]+' | cut -d= -f2 || echo 0)

  if [[ "${count:-0}" -ge 1 ]]; then
    green "  ✓ $count pack call(s) for $pack landed in audit log"
    echo "$audit" | grep -v __count | sed 's/^/    /'
    PASS=$((PASS + 1)); return 0
  fi

  red "  ✗ NO pack call for $pack in audit log since $since"
  red "    (the LLM may have hallucinated the call without invoking the tool)"
  echo "$audit" | grep -v __count | sed 's/^/    /' || true
  FAIL=$((FAIL + 1)); FAILED+=("$pack"); return 1
}

# ── pre-flight ────────────────────────────────────────────────────
require_cmd docker
require_cmd curl
require_cmd python3

read_admin_password

if ! curl -fsS "$HELMDECK_URL/healthz" >/dev/null 2>&1; then
  red "helmdeck not reachable at $HELMDECK_URL/healthz"
  red "  this check runs against an ALREADY-RUNNING stack — start it first"
  red "  (e.g. ./scripts/install.sh)"
  exit 2
fi

if ! docker ps --format '{{.Names}}' | grep -qx "$OPENCLAW_GATEWAY_CONTAINER"; then
  red "openclaw-gateway container not running ($OPENCLAW_GATEWAY_CONTAINER)"
  red "  bring it up per docs/integrations/openclaw.md §2-3 before smoking"
  exit 2
fi

ensure_baas_net

if [[ "$SKIP_REWRITE" != true ]]; then
  blue "minting fresh helmdeck JWT and updating openclaw mcp.servers.helmdeck"
  JWT=$(mint_jwt)
  write_helmdeck_mcp_server "$JWT"
else
  JWT=$(mint_jwt)  # still needed for audit queries
fi

# ── probe 0: catalog visibility (the regression trap) ─────────────
# This is the probe the whole issue exists to catch. OpenClaw
# 2026.4.18+ does not load bundled MCP tools via the CLI agent path
# (only the 24 built-ins appear; the helmdeck__* packs are missing —
# suspect upstream 0e7a992d). If we see ZERO helmdeck__* tools we
# FAIL LOUDLY with that diagnosis instead of mistaking a dead bridge
# for a healthy "0 packs ran" run.
blue "── probe: catalog visibility (helmdeck__* tools)"
CATALOG_OUT=$(run_agent "List every MCP tool whose name starts with helmdeck__. Just the names, one per line." || true)
CATALOG_COUNT=$(printf '%s\n' "$CATALOG_OUT" | grep -oE 'helmdeck__[A-Za-z0-9_-]+' | sort -u | wc -l | tr -d ' ')

if [[ "${CATALOG_COUNT:-0}" -lt 1 ]]; then
  red "  ✗ ZERO helmdeck__* tools visible to the OpenClaw agent."
  red ""
  red "  This is the OpenClaw CLI MCP-load regression, NOT a healthy"
  red "  '0 packs ran' result. OpenClaw >= 2026.4.18 does not load"
  red "  bundled MCP tools via the CLI agent path (only the 24 built-ins"
  red "  appear; the helmdeck__* packs are missing). Suspect upstream"
  red "  commit 0e7a992d."
  red ""
  red "  See: $OPENCLAW_DOCS  (§ 'Status (CLI path)' and § 5b)."
  red "  The chat UI path at http://localhost:18789 DOES load the catalog —"
  red "  drive the round-trip there, or pin a known-good OpenClaw version,"
  red "  until upstream fixes the CLI path."
  red ""
  red "  agent reply (head):"
  printf '%s\n' "$CATALOG_OUT" | head -10 | sed 's/^/    /'
  exit 1
fi
green "  ✓ $CATALOG_COUNT helmdeck__* tools visible to the agent"

# ── core probes (always run) ──────────────────────────────────────
probe "http.fetch GET example.com" "http.fetch" \
  "Use the helmdeck__http_fetch tool to GET https://example.com with User-Agent 'Helmdeck-Smoke/1.0'. Just call the tool." || true

probe "browser.screenshot_url example.com" "browser.screenshot_url" \
  "Use the helmdeck__browser_screenshot_url tool with url https://example.com. Just call the tool." || true

probe "repo.fetch → fs.list (session chaining)" "repo.fetch" \
  "Use helmdeck__repo_fetch to clone https://github.com/octocat/Hello-World.git with depth 1. Then use helmdeck__fs_list with the clone_path and _session_id from the result. Report both results." || true

# ── integration-aware probes (one representative pack each) ───────
if integration_enabled HELMDECK_FIRECRAWL_ENABLED; then
  probe "web.scrape example.com (Firecrawl)" "web.scrape" \
    "Use the helmdeck__web_scrape tool with url https://example.com. Just call the tool." || true
else
  yellow "── skip: web.scrape (Firecrawl) — HELMDECK_FIRECRAWL_ENABLED not set"
fi

if integration_enabled HELMDECK_DOCLING_ENABLED; then
  probe "doc.parse example.com (Docling)" "doc.parse" \
    "Use the helmdeck__doc_parse tool with source_url https://example.com and formats [\"md\"]. Just call the tool." || true
else
  yellow "── skip: doc.parse (Docling) — HELMDECK_DOCLING_ENABLED not set"
fi

# ── results ───────────────────────────────────────────────────────
echo
blue "── results"
green "  passed: $PASS"
if [[ "$FAIL" -gt 0 ]]; then
  red "  failed: $FAIL (${FAILED[*]})"
  echo
  red "smoke-integration FAILED — see the audit-log excerpts above."
  exit 1
fi
echo
green "=== smoke-integration PASSED ==="
