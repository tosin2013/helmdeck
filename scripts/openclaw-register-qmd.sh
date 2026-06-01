#!/usr/bin/env bash
# scripts/openclaw-register-qmd.sh — wire MCPorter to dial helmdeck's
# QMD memory-corpus endpoint (ADR 048 PR #3).
#
# What this does:
#   1. Reads the helmdeck JWT OpenClaw already stores at
#      /home/node/.openclaw/openclaw.json:mcp.servers.helmdeck.headers
#      (the same JWT it uses for the main /api/v1/mcp/sse connection).
#   2. Writes an mcporter config entry naming "helmdeck" with the
#      QMD-bridge URL, transport=sse, and that JWT as the
#      Authorization header.
#   3. Reuses the OpenClaw container's bundled `npx mcporter` so no
#      extra binaries need to be installed on the host.
#
# Idempotent. Safe to re-run; updates the JWT each time so a
# rotated token doesn't break the bridge.
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

# ── do the work inside the container ───────────────────────────────
# The JWT is read from OpenClaw's config file inside the container
# and threaded into a Python script via a heredoc — never appears in
# argv or shell history. mcporter config add bootstraps the entry if
# it doesn't exist; the Python step updates the auth header so a
# rotated JWT propagates.
docker exec "$CONTAINER" bash -s <<EOF
set -e

# Extract the helmdeck JWT OpenClaw uses for /api/v1/mcp/sse.
JWT=\$(python3 - <<PYEOF
import json, sys
try:
    with open("/home/node/.openclaw/openclaw.json") as f:
        cfg = json.load(f)
    print(cfg["mcp"]["servers"]["helmdeck"]["headers"]["authorization"])
except (FileNotFoundError, KeyError) as e:
    print(f"missing: {e}", file=sys.stderr)
    sys.exit(2)
PYEOF
)
if [[ -z "\$JWT" ]]; then
  echo "JWT not found in /home/node/.openclaw/openclaw.json" >&2
  exit 2
fi

# Ensure the mcporter entry exists. config add is idempotent — it
# overwrites the URL/transport/description but preserves nothing else
# we'd want to keep.
if ! /usr/local/bin/npx --yes mcporter config add "$SERVER_NAME" \\
    --url "$HELMDECK_URL" \\
    --transport sse \\
    --description "helmdeck QMD memory-corpus bridge (ADR 048 PR #3)" \\
    --scope home >/dev/null 2>&1; then
  echo "mcporter config add failed" >&2
  exit 3
fi

# Patch the Authorization header in-place — covers both fresh and
# rotated-token cases without losing the rest of the entry.
python3 - <<PYEOF
import json, sys
path = "/home/node/.mcporter/mcporter.json"
with open(path) as f:
    cfg = json.load(f)
cfg.setdefault("mcpServers", {}).setdefault("$SERVER_NAME", {})
cfg["mcpServers"]["$SERVER_NAME"].setdefault("headers", {})
cfg["mcpServers"]["$SERVER_NAME"]["headers"]["Authorization"] = "\$JWT"
with open(path, "w") as f:
    json.dump(cfg, f, indent=2)
print("config updated")
PYEOF
EOF

ok "MCPorter wired to $HELMDECK_URL"
dim "Verify with: docker exec $CONTAINER npx mcporter list"
dim "Smoke test:  docker exec $CONTAINER npx mcporter call $SERVER_NAME.query searches='[{\"type\":\"lex\",\"query\":\"test\"}]'"
