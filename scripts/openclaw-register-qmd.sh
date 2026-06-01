#!/usr/bin/env bash
# scripts/openclaw-register-qmd.sh — wire MCPorter to dial helmdeck's
# QMD memory-corpus endpoint (ADR 048 PR #3).
#
# What this does:
#   1. Bootstraps an mcporter config entry naming "helmdeck" with the
#      QMD-bridge URL + transport=sse.
#   2. Reads the helmdeck JWT OpenClaw already stores at
#      /home/node/.openclaw/openclaw.json:mcp.servers.helmdeck.headers
#      (the same JWT it uses for the main /api/v1/mcp/sse connection)
#      and patches it into the mcporter entry's Authorization header.
#   3. Uses the bundled `npx mcporter` so no extra host binaries.
#
# Idempotent. Safe to re-run; updates the JWT each time so a rotated
# token doesn't break the bridge. Re-run after any
# `docker compose ... up -d --force-recreate openclaw-gateway` —
# the mcporter config lives in the container's writable layer, not a
# named volume, so a recreate wipes it.
#
# Why this is a separate script, not baked into the compose overlay:
#   The helmdeck JWT is materialized only after OpenClaw boots and
#   reads the helmdeck server config. A compose-level init container
#   would have to race that boot. Running this script AFTER the stack
#   is healthy is simpler and survives token rotation.
#
# Usage:
#   scripts/openclaw-register-qmd.sh
#
# Exit codes:
#   0   success
#   1   prerequisite missing (docker, openclaw-gateway container)
#   2   helmdeck JWT not found in OpenClaw config
#   3   mcporter config write failed

set -euo pipefail

CONTAINER="${OPENCLAW_CONTAINER:-openclaw-openclaw-gateway-1}"
HELMDECK_URL="${HELMDECK_QMD_URL:-http://helmdeck-control-plane:3000/api/v1/mcp/qmd/sse}"
SERVER_NAME="${MCPORTER_SERVER_NAME:-helmdeck}"

# ── pretty output ──────────────────────────────────────────────────
if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'
  C_OK=$'\033[32m'
  C_ERR=$'\033[31m'
  C_DIM=$'\033[2m'
else
  C_RESET="" C_OK="" C_ERR="" C_DIM=""
fi

info()  { printf '%s\n' "$1"; }
ok()    { printf '%s✓%s %s\n' "$C_OK" "$C_RESET" "$1"; }
fail()  { printf '%s✗%s %s\n' "$C_ERR" "$C_RESET" "$1" >&2; }
dim()   { printf '%s%s%s\n' "$C_DIM" "$1" "$C_RESET"; }

# ── prerequisite checks ────────────────────────────────────────────
if ! command -v docker >/dev/null 2>&1; then
  fail "docker not found on PATH"
  exit 1
fi
if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  fail "container '$CONTAINER' not running"
  dim "Bring the OpenClaw sidecar up first:"
  dim "  docker compose -f /root/openclaw/docker-compose.yml \\"
  dim "    -f deploy/compose/compose.openclaw-sidecar.yml up -d"
  exit 1
fi

info "Registering helmdeck QMD bridge with MCPorter inside $CONTAINER..."

# ── step 1: bootstrap the mcporter entry ───────────────────────────
# `mcporter config add` is idempotent — overwrites url/transport/
# description but doesn't touch headers if the entry already exists.
# We capture stderr separately so a real failure (vs a benign warning)
# fails the script.
if ! docker exec "$CONTAINER" /usr/local/bin/npx --yes mcporter config add "$SERVER_NAME" \
    --url "$HELMDECK_URL" \
    --transport sse \
    --description "helmdeck QMD memory-corpus bridge (ADR 048 PR #3)" \
    --scope home >/dev/null 2>&1; then
  fail "mcporter config add failed — re-run with the docker exec above unsilenced to see the error"
  exit 3
fi

# ── step 2: patch the Authorization header ─────────────────────────
# The JWT is read from OpenClaw's config inside the container and
# never appears in this script's argv or shell history — Python
# reads it from one JSON file and writes it into another.
# We pipe the Python source via stdin (docker exec -i) instead of
# inlining it in a heredoc because bash heredoc nesting through
# `bash -s` doesn't preserve inner heredocs cleanly.
PATCH_PY=$(cat <<'PYEOF'
import json, sys
try:
    with open("/home/node/.openclaw/openclaw.json") as f:
        oc = json.load(f)
    jwt = oc["mcp"]["servers"]["helmdeck"]["headers"]["authorization"]
except (FileNotFoundError, KeyError) as e:
    print(f"helmdeck JWT not found in /home/node/.openclaw/openclaw.json: {e}", file=sys.stderr)
    sys.exit(2)
mp_path = "/home/node/.mcporter/mcporter.json"
try:
    with open(mp_path) as f:
        mp = json.load(f)
except FileNotFoundError:
    print(f"mcporter config not found at {mp_path} (step 1 should have created it)", file=sys.stderr)
    sys.exit(3)
mp.setdefault("mcpServers", {}).setdefault("helmdeck", {})
mp["mcpServers"]["helmdeck"].setdefault("headers", {})
mp["mcpServers"]["helmdeck"]["headers"]["Authorization"] = jwt
with open(mp_path, "w") as f:
    json.dump(mp, f, indent=2)
print("patched")
PYEOF
)

if ! echo "$PATCH_PY" | docker exec -i "$CONTAINER" python3 - >/dev/null; then
  fail "Authorization header patch failed"
  exit 3
fi

ok "MCPorter wired to $HELMDECK_URL"
dim "Verify with: docker exec $CONTAINER npx mcporter list"
dim "Smoke test:  docker exec $CONTAINER npx mcporter call $SERVER_NAME.query searches='[{\"type\":\"lex\",\"query\":\"test\"}]'"
