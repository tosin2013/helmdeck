#!/usr/bin/env bash
# helmdeck end-to-end smoke harness (T111).
#
# Boots the Compose stack, mints a token, creates a real browser session
# against helmdeck-sidecar:dev, runs the navigate -> extract -> execute ->
# screenshot -> terminate flow, asserts on PNG magic bytes, then tears
# the stack back down.
#
# Exits 0 on success, non-zero on the first failed step. Streams the
# control-plane logs on failure for debugging.
#
# Run from the repo root: `make smoke` or `scripts/smoke.sh`.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose/compose.yaml"
ENV_FILE="${REPO_ROOT}/deploy/compose/.env.smoke"
SIDECAR_IMAGE="${SIDECAR_IMAGE:-helmdeck-sidecar:dev}"
PORT="${PORT:-3000}"
SHOT=""

cleanup() {
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "--- smoke FAILED, dumping control-plane logs ---" >&2
    docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" logs control-plane 2>&1 | tail -50 >&2 || true
  fi
  echo "--- tearing down compose stack ---"
  docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" down -v --remove-orphans >/dev/null 2>&1 || true
  rm -f "${ENV_FILE}" "${SHOT}"
  exit $rc
}
trap cleanup EXIT

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "smoke: missing required tool: $1" >&2; exit 2; }
}

require docker
require curl
require python3

echo "--- ensuring sidecar image is present (${SIDECAR_IMAGE})"
if ! docker image inspect "${SIDECAR_IMAGE}" >/dev/null 2>&1; then
  echo "smoke: ${SIDECAR_IMAGE} missing — run \`make sidecar-build\` first" >&2
  exit 2
fi

echo "--- generating ephemeral .env.smoke"
JWT_SECRET=$(head -c 32 /dev/urandom | xxd -p -c 64)
# on macOS, Docker Desktop runs in a Linux VM where the socket is root:root (GID 0).
# stat -c is GNU only and silently fails on macOS, so detect the platform instead.
if [[ "$(uname -s)" == "Darwin" ]]; then
  DOCKER_GID=0
else
  DOCKER_GID=$(stat -c '%g' /var/run/docker.sock 2>/dev/null || echo 999)
fi
{
  echo "HELMDECK_JWT_SECRET=${JWT_SECRET}"
  echo "HELMDECK_DOCKER_GID=${DOCKER_GID}"
} > "${ENV_FILE}"

echo "--- compose build + up"
docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" build control-plane
docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" up -d control-plane

echo "--- waiting for /healthz"
for i in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    echo "control plane up after ${i} tries"
    break
  fi
  sleep 0.5
  if [[ $i -eq 40 ]]; then
    echo "smoke: control-plane never reported healthy" >&2
    exit 1
  fi
done

echo "--- minting smoke token"
TOKEN=$(docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" exec -T control-plane \
  /usr/local/bin/control-plane \
    -mint-token=smoke \
    -mint-token-client=ci \
    -mint-token-scopes=admin \
    -mint-token-ttl=10m | tr -d '\r\n')
if [[ -z "${TOKEN}" ]]; then
  echo "smoke: empty token" >&2; exit 1
fi
echo "token: ${TOKEN:0:24}…"

api() {
  local method=$1 path=$2
  shift 2
  curl -fsS -X "${method}" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    "http://127.0.0.1:${PORT}${path}" "$@"
}

echo "--- creating browser session"
SESSION_JSON=$(api POST /api/v1/sessions -d "{
  \"label\": \"smoke\",
  \"image\": \"${SIDECAR_IMAGE}\",
  \"memory_limit\": \"1g\",
  \"shm_size\": \"1g\",
  \"cpu_limit\": 1,
  \"timeout_seconds\": 120
}")
SESSION_ID=$(echo "${SESSION_JSON}" | grep -o '"id":"[^"]*' | cut -d'"' -f4)
if [[ -z "${SESSION_ID}" ]]; then
  echo "smoke: session create response missing id: ${SESSION_JSON}" >&2; exit 1
fi
echo "session: ${SESSION_ID}"

echo "--- waiting for chromium to settle"
sleep 3

echo "--- navigate"
api POST /api/v1/browser/navigate -d "{
  \"session_id\": \"${SESSION_ID}\",
  \"url\": \"data:text/html,<html><body><h1>helmdeck-smoke</h1></body></html>\"
}" >/dev/null

echo "--- extract h1 text"
EXTRACT=$(api POST /api/v1/browser/extract -d "{
  \"session_id\": \"${SESSION_ID}\",
  \"selector\": \"h1\",
  \"format\": \"text\"
}")
if [[ "${EXTRACT}" != *"helmdeck-smoke"* ]]; then
  echo "smoke: extract assertion failed: ${EXTRACT}" >&2; exit 1
fi
echo "extract OK"

echo "--- execute 1+1"
EXECUTE=$(api POST /api/v1/browser/execute -d "{
  \"session_id\": \"${SESSION_ID}\",
  \"script\": \"1+1\"
}")
if [[ "${EXECUTE}" != *'"result":2'* ]]; then
  echo "smoke: execute assertion failed: ${EXECUTE}" >&2; exit 1
fi
echo "execute OK"

echo "--- screenshot"
SHOT="$(mktemp /tmp/helmdeck-smoke-XXXXXX)"
api POST /api/v1/browser/screenshot -o "${SHOT}" -d "{
  \"session_id\": \"${SESSION_ID}\",
  \"full_page\": false
}"
if ! head -c4 "${SHOT}" | LC_ALL=C grep -q $'\x89PNG'; then
  echo "smoke: screenshot is not a PNG (head: $(head -c8 "${SHOT}" | xxd))" >&2
  exit 1
fi
SIZE=$(stat -c '%s' "${SHOT}" 2>/dev/null || stat -f '%z' "${SHOT}")
if [[ ${SIZE} -lt 500 ]]; then
  echo "smoke: screenshot suspiciously small (${SIZE} bytes)" >&2
  exit 1
fi
echo "screenshot OK (${SIZE} bytes)"

echo "--- pack: browser.screenshot_url (exercises Garage artifact store, T211a)"
PACK_RESP=$(api POST /api/v1/packs/browser.screenshot_url -d '{
  "url": "data:text/html,<html><body><h1>helmdeck-pack-smoke</h1></body></html>",
  "full_page": false
}')
# The engine wraps handler output in {output, artifacts}; the signed
# URL we want is artifacts[0].url. Use python for a robust JSON parse
# (smoke.sh's existing grep-based approach picks up the input data:
# URL by mistake here).
ARTIFACT_URL=$(python3 -c "
import json,sys
d=json.loads(sys.argv[1])
arts=d.get('artifacts') or []
if not arts: sys.exit('no artifacts in response: '+sys.argv[1])
print(arts[0]['url'])
" "${PACK_RESP}")
if [[ -z "${ARTIFACT_URL}" ]]; then
  echo "smoke: pack response missing artifact url: ${PACK_RESP}" >&2; exit 1
fi
echo "artifact url: ${ARTIFACT_URL}"
# Fetch the artifact from inside the compose network (where the
# "garage" hostname resolves) via a throwaway curl container on
# baas-net. The control-plane image is distroless and has no curl.
docker run --rm --network baas-net -v /tmp:/host curlimages/curl:latest \
  -fsS -o /host/pack-shot.png "${ARTIFACT_URL}" >/dev/null
PACK_SHOT_SIZE=$(stat -c '%s' /tmp/pack-shot.png 2>/dev/null || stat -f '%z' /tmp/pack-shot.png)
if [[ ${PACK_SHOT_SIZE} -lt 500 ]]; then
  echo "smoke: pack screenshot suspiciously small (${PACK_SHOT_SIZE} bytes)" >&2
  exit 1
fi
if ! head -c4 /tmp/pack-shot.png | LC_ALL=C grep -q $'\x89PNG'; then
  echo "smoke: pack screenshot is not a PNG" >&2
  exit 1
fi
rm -f /tmp/pack-shot.png
echo "pack screenshot OK (${PACK_SHOT_SIZE} bytes from Garage)"

echo "--- terminate session"
curl -fsS -o /dev/null -X DELETE \
  -H "Authorization: Bearer ${TOKEN}" \
  "http://127.0.0.1:${PORT}/api/v1/sessions/${SESSION_ID}"
echo "terminate OK"

echo
echo "=== smoke PASSED ==="
