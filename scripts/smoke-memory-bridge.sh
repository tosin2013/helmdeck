#!/usr/bin/env bash
# scripts/smoke-memory-bridge.sh — deterministic end-to-end test of the
# ADR 048 memory bridge (PR #2 write surface + PR #3 QMD endpoint).
#
# No LLM in the loop. Writes a unique fact via POST /api/v1/memory/store
# under OpenClaw's helmdeck JWT, then dials it back through MCPorter's
# helmdeck.query tool inside openclaw-gateway. Asserts the unique marker
# surfaces in the QMD response. Cleans up afterward.
#
# This probe is the canonical "did helmdeck's memory plumbing work?"
# answer. The bigger smoke-integration.sh agent probes depend on the
# LLM correctly picking the right tool; this one doesn't, so a failure
# here means the bridge is broken regardless of which model is configured.
#
# Usage:
#   ./scripts/smoke-memory-bridge.sh
#
# Exit codes:
#   0   success — the marker round-tripped through the QMD bridge
#   1   prerequisite missing (helmdeck not reachable, openclaw not running)
#   2   write surface broken (POST /api/v1/memory/store didn't 200)
#   3   QMD bridge broken (mcporter ran but didn't surface the marker)

set -euo pipefail

HELMDECK_URL="${HELMDECK_URL:-http://localhost:3000}"
OPENCLAW_CONTAINER="${OPENCLAW_CONTAINER:-openclaw-openclaw-gateway-1}"

# ── pretty output ──────────────────────────────────────────────────
if [[ -t 1 ]]; then
  C_RESET=$'\033[0m' C_OK=$'\033[32m' C_ERR=$'\033[31m' C_DIM=$'\033[2m'
else
  C_RESET="" C_OK="" C_ERR="" C_DIM=""
fi
ok()   { printf '%s✓%s %s\n' "$C_OK" "$C_RESET" "$1"; }
fail() { printf '%s✗%s %s\n' "$C_ERR" "$C_RESET" "$1" >&2; }
dim()  { printf '%s%s%s\n' "$C_DIM" "$1" "$C_RESET"; }

# ── prerequisites ──────────────────────────────────────────────────
if ! curl -fsS "$HELMDECK_URL/healthz" >/dev/null 2>&1; then
  fail "helmdeck not reachable at $HELMDECK_URL"
  exit 1
fi
if ! docker inspect "$OPENCLAW_CONTAINER" >/dev/null 2>&1; then
  fail "$OPENCLAW_CONTAINER not running"
  exit 1
fi

# ── extract OpenClaw's helmdeck JWT (caller-isolation needs SAME caller for write+read) ─
JWT=$(docker exec "$OPENCLAW_CONTAINER" python3 -c '
import json
with open("/home/node/.openclaw/openclaw.json") as f:
    cfg = json.load(f)
print(cfg["mcp"]["servers"]["helmdeck"]["headers"]["authorization"].replace("Bearer ", ""))
')
if [[ -z "$JWT" ]]; then
  fail "could not extract helmdeck JWT from OpenClaw config"
  exit 1
fi

# ── do the test ────────────────────────────────────────────────────
MARKER="smoke-$(date -u +%Y%m%dT%H%M%S)-$RANDOM"
KEY="smoke/$MARKER"
echo "─ smoke-memory-bridge marker: $MARKER"

# 1. write via REST under OpenClaw's caller namespace
WRITE_STATUS=$(curl -s -o /tmp/smoke-mem-write.json -w "%{http_code}" \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  "$HELMDECK_URL/api/v1/memory/store" \
  -d "{\"key\":\"$KEY\",\"value\":\"smoke test fact $MARKER — deploy via Konflux\",\"category\":\"smoke_test\",\"ttl_seconds\":3600}")
if [[ "$WRITE_STATUS" != "200" ]]; then
  fail "POST /api/v1/memory/store returned $WRITE_STATUS"
  cat /tmp/smoke-mem-write.json | sed 's/^/    /'
  exit 2
fi
ok "wrote fact via REST (key=$KEY)"

# Ensure helmdeck is registered with mcporter. The mcporter config
# lives in the container's writable layer, NOT a named volume, so
# any `compose run` / recreate wipes it. Re-register defensively so
# the probe is robust against that.
if ! docker exec "$OPENCLAW_CONTAINER" /usr/local/bin/npx mcporter list 2>/dev/null | grep -q '^- helmdeck '; then
  dim "  mcporter has no helmdeck entry; running openclaw-register-qmd.sh"
  "$(dirname "$0")/openclaw-register-qmd.sh" >/dev/null
fi

# 2. dial helmdeck.query through mcporter inside openclaw-gateway
#    mcporter may exit non-zero when the SSE round-trip takes longer
#    than its internal timeout even though a payload was returned.
#    Tolerate the exit code and rely on the marker-in-output assertion.
set +e
MCP_OUT=$(docker exec "$OPENCLAW_CONTAINER" /usr/local/bin/npx mcporter call helmdeck.query \
  "searches=[{\"type\":\"lex\",\"query\":\"$MARKER\"}]" limit=5 2>&1)
MCP_RC=$?
set -e
if [[ -z "$MCP_OUT" ]]; then
  fail "mcporter returned nothing (exit code $MCP_RC)"
  exit 3
fi

# 3. assert marker surfaces in the response
if echo "$MCP_OUT" | grep -q "$MARKER"; then
  ok "marker round-tripped through QMD bridge"
  echo "$MCP_OUT" | head -10 | sed 's/^/    /'
else
  fail "marker $MARKER NOT in mcporter helmdeck.query response"
  fail "  (write succeeded, but bridge didn't surface it — projection/auth/caller issue)"
  echo "$MCP_OUT" | head -15 | sed 's/^/    /' >&2

  # cleanup before exiting so re-runs aren't polluted
  curl -fsS -o /dev/null \
    -H "Authorization: Bearer $JWT" \
    -H "Content-Type: application/json" \
    "$HELMDECK_URL/api/v1/memory/forget" \
    -d "{\"scope\":\"key:$KEY\"}" || true
  exit 3
fi

# ── cleanup ────────────────────────────────────────────────────────
curl -fsS -o /dev/null \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  "$HELMDECK_URL/api/v1/memory/forget" \
  -d "{\"scope\":\"key:$KEY\"}" || true
dim "(smoke marker cleaned up via /api/v1/memory/forget)"

echo
ok "smoke-memory-bridge PASSED"
