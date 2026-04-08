#!/usr/bin/env bash
# Four-client MCP bridge smoke matrix (T308).
#
# Boots the same Compose stack make smoke uses, then for each
# supported client (claude-code, claude-desktop, openclaw, gemini-cli):
#
#   1. Fetches the connect snippet from /api/v1/connect/{client} and
#      asserts the JSON parses with the expected shape.
#   2. Spawns the freshly-built helmdeck-mcp binary with HELMDECK_URL
#      and HELMDECK_TOKEN — the same env every supported client sets.
#   3. Pipes a JSON-RPC initialize -> tools/list -> tools/call sequence
#      for browser.screenshot_url through stdio and asserts the
#      resulting base64 payload starts with the PNG magic bytes.
#
# Per ADR 025, the wire contract every client uses is identical at the
# stdio MCP layer; the only per-client variation is the config file
# shape, which the snippet generator (T309) covers and which step (1)
# verifies here. That makes one bridge invocation per client both
# necessary and sufficient for the exit-gate assertion.
#
# Exits 0 on success, non-zero on the first failed leg.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose/compose.yaml"
ENV_FILE="${REPO_ROOT}/deploy/compose/.env.clients-smoke"
SIDECAR_IMAGE="${SIDECAR_IMAGE:-helmdeck-sidecar:dev}"
PORT="${PORT:-3000}"
BRIDGE_BIN="${BRIDGE_BIN:-${REPO_ROOT}/bin/helmdeck-mcp}"

CLIENTS=(claude-code claude-desktop openclaw gemini-cli)

cleanup() {
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "--- clients-smoke FAILED, dumping control-plane logs ---" >&2
    docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" logs control-plane 2>&1 | tail -80 >&2 || true
  fi
  docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" down -v --remove-orphans >/dev/null 2>&1 || true
  rm -f "${ENV_FILE}"
  exit $rc
}
trap cleanup EXIT

require() { command -v "$1" >/dev/null 2>&1 || { echo "clients-smoke: missing tool: $1" >&2; exit 2; }; }
require docker
require curl
require jq

if [[ ! -x "${BRIDGE_BIN}" ]]; then
  echo "clients-smoke: building helmdeck-mcp at ${BRIDGE_BIN}"
  mkdir -p "$(dirname "${BRIDGE_BIN}")"
  (cd "${REPO_ROOT}" && go build -o "${BRIDGE_BIN}" ./cmd/helmdeck-mcp)
fi

# Reuse the same env shape make smoke writes so the compose stack
# comes up identically.
SECRET="$(openssl rand -hex 32)"
cat > "${ENV_FILE}" <<EOF
HELMDECK_JWT_SECRET=${SECRET}
HELMDECK_PORT=${PORT}
SIDECAR_IMAGE=${SIDECAR_IMAGE}
EOF

echo "--- booting compose stack ---"
docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" up -d --wait

URL="http://localhost:${PORT}"
TOKEN="$(go run "${REPO_ROOT}/scripts/mint-token" --secret "${SECRET}" --subject clients-smoke 2>/dev/null \
  || curl -fsS -X POST "${URL}/api/v1/dev/token" -d "{\"subject\":\"clients-smoke\"}" | jq -r .token)"
if [[ -z "${TOKEN}" ]]; then
  echo "clients-smoke: could not mint a JWT" >&2
  exit 1
fi

call_bridge() {
  # Stream three JSON-RPC frames into helmdeck-mcp on stdin and read
  # responses from stdout. The bridge proxies them verbatim to the
  # platform's WebSocket MCP endpoint. We rely on line-delimited JSON
  # in both directions (the bridge wire format).
  local payload
  payload="$(jq -nc \
    '{jsonrpc:"2.0",id:1,method:"initialize",params:{protocolVersion:"2024-11-05",capabilities:{},clientInfo:{name:"clients-smoke",version:"0.0.0"}}},
     {jsonrpc:"2.0",id:2,method:"tools/list"},
     {jsonrpc:"2.0",id:3,method:"tools/call",params:{name:"browser.screenshot_url",arguments:{url:"https://example.com"}}}')"
  HELMDECK_URL="${URL}" HELMDECK_TOKEN="${TOKEN}" \
    timeout 60 "${BRIDGE_BIN}" <<<"${payload}"
}

for client in "${CLIENTS[@]}"; do
  echo "--- ${client}: fetching connect snippet ---"
  snippet="$(curl -fsS -H "Authorization: Bearer ${TOKEN}" "${URL}/api/v1/connect/${client}")"
  echo "${snippet}" | jq -e '.client and .install_path and .config' >/dev/null \
    || { echo "clients-smoke: ${client} snippet malformed" >&2; exit 1; }

  echo "--- ${client}: invoking browser.screenshot_url via bridge ---"
  out="$(call_bridge)"
  # Find the tools/call response (id=3) and pull out the first base64
  # image content block, then check the magic bytes.
  png_b64="$(echo "${out}" | jq -rs '
    map(select(.id == 3)) | .[0].result.content
    | map(select(.type == "image")) | .[0].data // empty
  ')"
  if [[ -z "${png_b64}" ]]; then
    echo "clients-smoke: ${client} got no image content" >&2
    echo "${out}" >&2
    exit 1
  fi
  magic="$(echo "${png_b64}" | base64 -d 2>/dev/null | head -c 8 | xxd -p || true)"
  if [[ "${magic}" != "89504e470d0a1a0a" ]]; then
    echo "clients-smoke: ${client} payload not a PNG (magic=${magic})" >&2
    exit 1
  fi
  echo "--- ${client}: OK ---"
done

echo "--- clients-smoke: all four legs green ---"
