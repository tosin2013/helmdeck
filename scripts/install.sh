#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The helmdeck contributors
#
# install.sh — one-command bootstrap for a fresh helmdeck install.
#
# Takes a clean Linux/macOS box from `git clone` to "logged in at
# http://localhost:3000" with no manual env-var fiddling. The script:
#
#   1. Verifies prerequisites (docker, node ≥20, go ≥1.26, make,
#      openssl, curl) and prints platform-specific install hints
#      when something is missing.
#   2. Generates fresh secrets (HELMDECK_JWT_SECRET, HELMDECK_VAULT_KEY,
#      HELMDECK_KEYSTORE_KEY, HELMDECK_ADMIN_PASSWORD) into
#      deploy/compose/.env.local — or reuses an existing file when
#      one is already present.
#   3. Builds the Management UI bundle, the Go binaries, and the
#      browser sidecar image. Skips npm install when web/node_modules
#      already exists so re-runs are fast.
#   4. Brings the Compose stack up and waits for healthchecks.
#   5. Prints the URL, admin credentials, and the most useful
#      next-step commands.
#
# Flags:
#   --reset      Tear down the compose stack, remove .env.local,
#                regenerate secrets, then re-run from step 1.
#   --no-build   Skip the build steps. Useful for re-running after
#                a config change without recompiling.
#   --help       Print this help text.
#
# Exit codes:
#   0   success
#   1   user error (bad flag, missing flag value)
#   2   prerequisite missing
#   3   build failed
#   4   compose up failed / healthcheck never passed

set -euo pipefail

# ────────────────────────────────────────────────────────────────────────
# config
# ────────────────────────────────────────────────────────────────────────

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose/compose.yaml"
ENV_FILE="${REPO_ROOT}/deploy/compose/.env.local"
URL="http://localhost:3000"

# Minimum tool versions. The script's check_tool function compares
# the parsed version against these. Bump alongside go.mod / package.json.
MIN_GO_MAJOR=1
MIN_GO_MINOR=26
MIN_NODE_MAJOR=20

# ────────────────────────────────────────────────────────────────────────
# pretty output
# ────────────────────────────────────────────────────────────────────────

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'
  C_BOLD=$'\033[1m'
  C_DIM=$'\033[2m'
  C_RED=$'\033[31m'
  C_GREEN=$'\033[32m'
  C_YELLOW=$'\033[33m'
  C_BLUE=$'\033[34m'
  C_CYAN=$'\033[36m'
else
  C_RESET="" C_BOLD="" C_DIM="" C_RED="" C_GREEN="" C_YELLOW="" C_BLUE="" C_CYAN=""
fi

step()  { printf "%s==>%s %s%s%s\n" "${C_BLUE}${C_BOLD}" "${C_RESET}" "${C_BOLD}" "$*" "${C_RESET}"; }
ok()    { printf "    %s✓%s %s\n" "${C_GREEN}" "${C_RESET}" "$*"; }
warn()  { printf "    %s!%s %s\n" "${C_YELLOW}" "${C_RESET}" "$*" >&2; }
fail()  { printf "%sERROR:%s %s\n" "${C_RED}${C_BOLD}" "${C_RESET}" "$*" >&2; }
info()  { printf "    %s%s%s\n" "${C_DIM}" "$*" "${C_RESET}"; }

# ────────────────────────────────────────────────────────────────────────
# usage / flag parsing
# ────────────────────────────────────────────────────────────────────────

usage() {
  cat <<EOF
Usage: scripts/install.sh [--reset] [--no-build] [--help]

Bootstraps a fresh helmdeck install on the current host.

Options:
  --reset      Tear down the running stack and start over from scratch.
               Removes deploy/compose/.env.local and regenerates secrets.
  --no-build   Skip the build steps (web bundle, Go binaries, sidecar
               image). Useful for re-running after a config change.
  --help       Print this help and exit.

Examples:
  scripts/install.sh                # Fresh install
  scripts/install.sh --no-build     # Bring up without rebuilding
  scripts/install.sh --reset        # Wipe + reinstall

After install, open ${URL} in your browser. The admin password is
generated on first run and printed to stdout — save it then.
EOF
}

DO_RESET=0
DO_BUILD=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --reset) DO_RESET=1 ;;
    --no-build) DO_BUILD=0 ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown flag: $1"; usage; exit 1 ;;
  esac
  shift
done

# ────────────────────────────────────────────────────────────────────────
# platform detection (used by per-tool install hints)
# ────────────────────────────────────────────────────────────────────────

detect_platform() {
  case "$(uname -s)" in
    Linux)
      if [[ -f /etc/debian_version ]]; then
        echo "debian"
      elif [[ -f /etc/redhat-release ]]; then
        echo "rhel"
      elif [[ -f /etc/alpine-release ]]; then
        echo "alpine"
      else
        echo "linux"
      fi
      ;;
    Darwin)  echo "macos" ;;
    *)       echo "unknown" ;;
  esac
}
PLATFORM="$(detect_platform)"

install_hint() {
  local tool="$1"
  case "${PLATFORM}:${tool}" in
    macos:docker)   echo "  brew install --cask docker" ;;
    macos:node)     echo "  brew install node" ;;
    macos:go)       echo "  brew install go" ;;
    macos:make)     echo "  xcode-select --install" ;;
    macos:openssl)  echo "  brew install openssl" ;;
    macos:curl)     echo "  brew install curl" ;;

    debian:docker)  echo "  curl -fsSL https://get.docker.com | sudo sh" ;;
    debian:node)    echo "  curl -fsSL https://deb.nodesource.com/setup_20.x | sudo bash - && sudo apt-get install -y nodejs" ;;
    debian:go)      echo "  See https://go.dev/doc/install — apt's golang-go is usually too old" ;;
    debian:make)    echo "  sudo apt-get install -y build-essential" ;;
    debian:openssl) echo "  sudo apt-get install -y openssl" ;;
    debian:curl)    echo "  sudo apt-get install -y curl" ;;

    rhel:docker)    echo "  sudo dnf install -y docker && sudo systemctl enable --now docker" ;;
    rhel:node)      echo "  curl -fsSL https://rpm.nodesource.com/setup_20.x | sudo bash - && sudo dnf install -y nodejs" ;;
    rhel:go)        echo "  See https://go.dev/doc/install" ;;
    rhel:make)      echo "  sudo dnf groupinstall -y 'Development Tools'" ;;
    rhel:openssl)   echo "  sudo dnf install -y openssl" ;;
    rhel:curl)      echo "  sudo dnf install -y curl" ;;

    *)              echo "  See https://github.com/tosin2013/helmdeck/blob/main/README.md for ${tool} install instructions" ;;
  esac
}

# ────────────────────────────────────────────────────────────────────────
# prerequisite checks
# ────────────────────────────────────────────────────────────────────────

check_tool() {
  local tool="$1"
  if ! command -v "${tool}" >/dev/null 2>&1; then
    fail "${tool} is required but not installed"
    info "install with:"
    info "$(install_hint "${tool}")"
    return 1
  fi
  return 0
}

check_node_version() {
  if ! command -v node >/dev/null 2>&1; then
    fail "node is required but not installed"
    info "install with:"
    info "$(install_hint node)"
    return 1
  fi
  local v
  v="$(node --version | sed 's/^v//' | cut -d. -f1)"
  if [[ "${v}" -lt "${MIN_NODE_MAJOR}" ]]; then
    fail "node ${MIN_NODE_MAJOR}+ required (found $(node --version))"
    info "upgrade with:"
    info "$(install_hint node)"
    return 1
  fi
  return 0
}

check_go_version() {
  if ! command -v go >/dev/null 2>&1; then
    fail "go is required but not installed"
    info "install with:"
    info "$(install_hint go)"
    return 1
  fi
  local v major minor
  v="$(go version | awk '{print $3}' | sed 's/^go//')"
  major="$(echo "${v}" | cut -d. -f1)"
  minor="$(echo "${v}" | cut -d. -f2)"
  if [[ "${major}" -lt "${MIN_GO_MAJOR}" ]] ||
     [[ "${major}" -eq "${MIN_GO_MAJOR}" && "${minor}" -lt "${MIN_GO_MINOR}" ]]; then
    fail "go ${MIN_GO_MAJOR}.${MIN_GO_MINOR}+ required (found go${v})"
    info "upgrade with:"
    info "$(install_hint go)"
    return 1
  fi
  return 0
}

check_docker_running() {
  if ! docker info >/dev/null 2>&1; then
    fail "docker is installed but the daemon is not reachable"
    info "is the docker daemon running and is your user in the docker group?"
    case "${PLATFORM}" in
      debian|rhel) info "  sudo systemctl start docker && sudo usermod -aG docker \$USER  (then re-login)" ;;
      macos)       info "  open -a Docker  (and wait for the whale icon to settle)" ;;
    esac
    return 1
  fi
  return 0
}

preflight() {
  step "Pre-flight checks"
  local failed=0

  for tool in make openssl curl; do
    check_tool "${tool}" || failed=1
  done
  check_tool docker || failed=1
  check_node_version  || failed=1
  check_go_version    || failed=1

  # Docker running check is separate because the binary can be
  # installed without the daemon running (common on macOS Docker
  # Desktop right after install).
  if [[ "${failed}" -eq 0 ]]; then
    check_docker_running || failed=1
  fi

  if [[ "${failed}" -ne 0 ]]; then
    fail "one or more prerequisites are missing — fix the issues above and re-run"
    exit 2
  fi

  ok "all prerequisites present (${PLATFORM})"
}

# ────────────────────────────────────────────────────────────────────────
# secret generation
# ────────────────────────────────────────────────────────────────────────

generate_hex() {
  # 32 raw bytes → 64 hex chars. The exact format every helmdeck
  # secret env var expects.
  openssl rand -hex 32
}

generate_password() {
  # 24 random bytes → ~32 url-safe base64 chars. Long enough that
  # nobody guesses it, short enough to copy-paste once.
  openssl rand -base64 24 | tr -d '/+=' | head -c 32
}

write_env_file() {
  local jwt vault keystore password docker_gid
  jwt="$(generate_hex)"
  vault="$(generate_hex)"
  keystore="$(generate_hex)"
  password="$(generate_password)"
  # On Linux the host's docker group GID needs to be passed into the
  # control-plane container so the nonroot user can read the docker
  # socket. macOS Docker Desktop runs in a VM and doesn't need this
  # but a default of 999 (Debian/Ubuntu's standard) keeps the file
  # parseable on every platform.
  docker_gid="$(stat -c '%g' /var/run/docker.sock 2>/dev/null || echo 999)"

  umask 077  # rw------- on the env file
  cat > "${ENV_FILE}" <<EOF
# helmdeck — local install secrets
#
# Generated by scripts/install.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ).
# Re-run scripts/install.sh --reset to regenerate.
#
# Treat every value below as a secret. The file is chmod 600 by
# default. Do NOT check it into version control — it's already in
# .gitignore but double-check before any commit.

# JWT signing secret. The control plane signs every minted token
# with this. 32-byte hex.
HELMDECK_JWT_SECRET=${jwt}

# Credential vault encryption key. Separate from the keystore key
# so a leak of one domain doesn't compromise the other (ADR 007).
# 32-byte hex.
HELMDECK_VAULT_KEY=${vault}

# AI provider keystore encryption key. Used to encrypt the
# Anthropic / OpenAI / Gemini / Ollama / Deepseek API keys
# operators add via the AI Providers panel. 32-byte hex.
HELMDECK_KEYSTORE_KEY=${keystore}

# Management UI admin password. The login form at http://localhost:3000
# accepts (admin, this value) and mints a 12-hour JWT.
HELMDECK_ADMIN_PASSWORD=${password}
HELMDECK_ADMIN_USERNAME=admin

# Host docker group id — the control plane needs this to read
# /var/run/docker.sock from inside its non-root container. The
# install script auto-detects this from your host. Override here
# if you've remapped the docker group.
HELMDECK_DOCKER_GID=${docker_gid}

# Sidecar image. The local 'make sidecar-build' target produces
# 'helmdeck-sidecar:dev'; the published ghcr.io image is the fallback
# the control plane uses when this var is unset. Pin to the locally
# built tag so a fresh install doesn't fail with "no such image"
# when ghcr is unreachable or the image hasn't been published yet.
HELMDECK_SIDECAR_IMAGE=helmdeck-sidecar:dev
EOF
  chmod 600 "${ENV_FILE}"
}

read_password_from_env_file() {
  grep '^HELMDECK_ADMIN_PASSWORD=' "${ENV_FILE}" | cut -d= -f2-
}

ensure_env_file() {
  step "Secrets"
  if [[ -f "${ENV_FILE}" ]]; then
    ok "reusing existing ${ENV_FILE}"
    info "(use --reset to regenerate)"
    return 0
  fi
  write_env_file
  ok "wrote ${ENV_FILE} (chmod 600)"
}

# ────────────────────────────────────────────────────────────────────────
# build pipeline
# ────────────────────────────────────────────────────────────────────────

run_build() {
  step "Building helmdeck"

  if [[ -d "${REPO_ROOT}/web/node_modules" ]]; then
    ok "web/node_modules present — skipping npm install"
  else
    info "installing npm dependencies (this takes ~30s the first time)..."
    (cd "${REPO_ROOT}" && make web-deps) || { fail "npm install failed"; exit 3; }
    ok "web dependencies installed"
  fi

  info "building Management UI bundle..."
  (cd "${REPO_ROOT}" && make web-build) || { fail "web-build failed"; exit 3; }
  ok "Management UI bundle built"

  info "building Go binaries (control-plane + helmdeck-mcp)..."
  (cd "${REPO_ROOT}" && rm -f bin/control-plane bin/helmdeck-mcp && make build) \
    || { fail "go build failed"; exit 3; }
  ok "Go binaries built"

  info "building browser sidecar image (this takes ~3-5 min the first time)..."
  (cd "${REPO_ROOT}" && make sidecar-build) || { fail "sidecar build failed"; exit 3; }
  ok "browser sidecar image built"
}

# ────────────────────────────────────────────────────────────────────────
# compose up
# ────────────────────────────────────────────────────────────────────────

compose_pull() {
  # Pre-pull all images that aren't built locally, so `compose up` doesn't
  # block on an unattended download (or worse: come up healthy while the
  # sidecar image is still pulling in the background, leaving the first
  # session hanging on a 30 s timeout). `--ignore-buildable` skips images
  # whose service has a `build:` clause — those are produced by
  # run_build / make sidecar-build above.
  step "Pre-pulling published images"
  info "this fetches images like dxflrs/garage and helmdeck-sidecar before stack-up..."
  if ! docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" pull --ignore-buildable; then
    fail "image pull failed — check network reachability to docker.io and ghcr.io"
    fail "if behind a corporate proxy, set HTTPS_PROXY before re-running"
    exit 4
  fi
  ok "images ready"
}

compose_up() {
  step "Starting Compose stack"
  info "this brings up the control plane, the Garage object store, and the garage-init bootstrap..."
  # Note: --wait omitted intentionally. The sidecar-warm service is a
  # one-shot pull-warmer that exits 0 by design; `compose up --wait`
  # treats Exited as failure even when restart: "no". Our own
  # wait_for_health() below polls /healthz directly and is the real
  # readiness gate.
  if ! docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" up -d --build; then
    fail "compose up failed — dumping control-plane logs:"
    docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" logs control-plane 2>&1 | tail -40 >&2 || true
    exit 4
  fi
  ok "stack is up"
}

# ────────────────────────────────────────────────────────────────────────
# health check
# ────────────────────────────────────────────────────────────────────────

wait_for_health() {
  step "Health check"
  for i in $(seq 1 30); do
    if curl -fsS "${URL}/healthz" >/dev/null 2>&1; then
      ok "control plane responding at ${URL}/healthz"
      return 0
    fi
    sleep 1
  done
  fail "control plane never reported healthy after 30s"
  docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" logs control-plane 2>&1 | tail -40 >&2 || true
  exit 4
}

# ────────────────────────────────────────────────────────────────────────
# reset
# ────────────────────────────────────────────────────────────────────────

do_reset() {
  step "Resetting helmdeck"
  if [[ -f "${COMPOSE_FILE}" ]]; then
    info "tearing down compose stack..."
    docker compose -f "${COMPOSE_FILE}" --env-file "${ENV_FILE}" down -v --remove-orphans \
      >/dev/null 2>&1 || true
    ok "compose stack down"
  fi
  if [[ -f "${ENV_FILE}" ]]; then
    rm -f "${ENV_FILE}"
    ok "removed ${ENV_FILE}"
  fi
  ok "reset complete — re-running install"
  echo
}

# ────────────────────────────────────────────────────────────────────────
# post-install summary
# ────────────────────────────────────────────────────────────────────────

print_summary() {
  local password
  password="$(read_password_from_env_file)"

  echo
  printf "%s%s helmdeck is up %s\n" "${C_GREEN}${C_BOLD}" "✓" "${C_RESET}"
  echo
  printf "  %sURL:%s       %s%s%s\n" "${C_DIM}" "${C_RESET}" "${C_CYAN}" "${URL}" "${C_RESET}"
  printf "  %sUsername:%s  %s%s%s\n" "${C_DIM}" "${C_RESET}" "${C_BOLD}" "admin" "${C_RESET}"
  printf "  %sPassword:%s  %s%s%s\n" "${C_DIM}" "${C_RESET}" "${C_BOLD}${C_YELLOW}" "${password}" "${C_RESET}"
  echo
  printf "  %s(save the password now — it's only printed here once.%s\n" "${C_DIM}" "${C_RESET}"
  printf "  %s the same value lives in %s, mode 0600.)%s\n" "${C_DIM}" "${ENV_FILE}" "${C_RESET}"
  echo
  printf "  %sNext steps in the UI:%s\n" "${C_BOLD}" "${C_RESET}"
  printf "    Add an AI provider key:  %s%s/admin/ai-providers%s\n" "${C_CYAN}" "${URL}" "${C_RESET}"
  printf "    Connect an MCP client:   %s%s/admin/connect%s\n" "${C_CYAN}" "${URL}" "${C_RESET}"
  printf "    Add a vault credential:  %s%s/admin/vault%s\n" "${C_CYAN}" "${URL}" "${C_RESET}"
  echo
  printf "  %sConnect an MCP client (next required step for actual use):%s\n" "${C_BOLD}" "${C_RESET}"
  printf "    OpenClaw (validated end-to-end): %sdocs/integrations/openclaw.md%s\n" "${C_DIM}" "${C_RESET}"
  printf "    Claude Code:                     %sdocs/integrations/claude-code.md%s\n" "${C_DIM}" "${C_RESET}"
  printf "    Claude Desktop:                  %sdocs/integrations/claude-desktop.md%s\n" "${C_DIM}" "${C_RESET}"
  printf "    Gemini CLI:                      %sdocs/integrations/gemini-cli.md%s\n" "${C_DIM}" "${C_RESET}"
  printf "    Hermes Agent:                    %sdocs/integrations/hermes-agent.md%s\n" "${C_DIM}" "${C_RESET}"
  printf "    Pack demo prompts:               %sdocs/integrations/pack-demo-playbook.md%s\n" "${C_DIM}" "${C_RESET}"
  echo
  printf "  %sLoad the agent skills (required for the model to know which packs to call):%s\n" "${C_BOLD}" "${C_RESET}"
  printf "    Skill bundle (catalog + contracts): %sdocs/integrations/SKILLS.md%s\n" "${C_DIM}" "${C_RESET}"
  printf "    OpenClaw users: already stamped by %sscripts/configure-openclaw.sh%s\n" "${C_DIM}" "${C_RESET}"
  printf "    Other clients:  per-integration doc above has a %sLoad the agent skills%s subsection\n" "${C_DIM}" "${C_RESET}"
  echo
  printf "  %sFirst session note:%s\n" "${C_BOLD}" "${C_RESET}"
  printf "    The browser sidecar image was just built. Your first session create call\n"
  printf "    will be quick. If you ever see a 502 on first session, the sidecar image\n"
  printf "    is still warming — wait ~30s and retry. See:\n"
  printf "      %sdocs/howto/troubleshoot-install.md%s\n" "${C_DIM}" "${C_RESET}"
  echo
  printf "  %sUseful commands:%s\n" "${C_BOLD}" "${C_RESET}"
  printf "    Tail logs:   %sdocker compose -f %s logs -f control-plane%s\n" "${C_DIM}" "${COMPOSE_FILE}" "${C_RESET}"
  printf "    Tear down:   %sdocker compose -f %s down -v%s\n" "${C_DIM}" "${COMPOSE_FILE}" "${C_RESET}"
  printf "    Reinstall:   %sscripts/install.sh --reset%s\n" "${C_DIM}" "${C_RESET}"
  echo
}

# ────────────────────────────────────────────────────────────────────────
# main
# ────────────────────────────────────────────────────────────────────────

main() {
  cd "${REPO_ROOT}"

  if [[ "${DO_RESET}" -eq 1 ]]; then
    do_reset
  fi

  preflight
  ensure_env_file

  if [[ "${DO_BUILD}" -eq 1 ]]; then
    run_build
  else
    info "skipping build (--no-build)"
  fi

  compose_pull
  compose_up
  wait_for_health
  setup_github_token
  print_summary
}

# ────────────────────────────────────────────────────────────────────────
# optional GitHub PAT setup
# ────────────────────────────────────────────────────────────────────────

setup_github_token() {
  # Skip if non-interactive (piped stdin) or if HELMDECK_SKIP_GITHUB=1.
  if [[ "${HELMDECK_SKIP_GITHUB:-}" == "1" ]] || [[ ! -t 0 ]]; then
    return 0
  fi

  echo
  printf "  %sOptional: add a GitHub Personal Access Token for private repo access.%s\n" "${C_BOLD}" "${C_RESET}"
  printf "  %sThis lets repo.fetch / repo.push clone and push to private repos%s\n" "${C_DIM}" "${C_RESET}"
  printf "  %sover HTTPS without SSH keys. The token is encrypted in helmdeck's%s\n" "${C_DIM}" "${C_RESET}"
  printf "  %svault (AES-256-GCM) and never leaves the control plane.%s\n" "${C_DIM}" "${C_RESET}"
  printf "  %sCreate one at: https://github.com/settings/tokens%s\n" "${C_DIM}" "${C_RESET}"
  printf "  %sRequired scopes: repo (full control of private repos)%s\n" "${C_DIM}" "${C_RESET}"
  echo
  printf "  Enter your GitHub PAT (or press Enter to skip): "
  read -r github_token
  if [[ -z "${github_token}" ]]; then
    info "skipping GitHub token setup"
    return 0
  fi

  # Mint a JWT for the vault API call.
  local password jwt
  password="$(read_password_from_env_file)"
  jwt=$(curl -fsS -X POST "${URL}/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"${password}\"}" \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])' 2>/dev/null) || true

  if [[ -z "${jwt}" ]]; then
    warn "could not mint JWT — skipping GitHub token. Add it manually in the Vault panel."
    return 0
  fi

  # Add the credential to the vault.
  local resp http_code
  local b64
  b64=$(printf '%s' "${github_token}" | base64 -w0)
  local cred_resp
  cred_resp=$(curl -s -X POST "${URL}/api/v1/vault/credentials" \
    -H "Authorization: Bearer ${jwt}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"github-token\",\"type\":\"api_key\",\"host_pattern\":\"github.com\",\"plaintext_b64\":\"${b64}\"}")
  resp=$(echo "${cred_resp}" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('id',''))" 2>/dev/null)
  local http_status
  if [[ -n "${resp}" && "${resp}" != "None" ]]; then
    http_status="201"
    # Auto-grant wildcard access so packs can use the credential
    curl -s -X POST "${URL}/api/v1/vault/credentials/${resp}/grants" \
      -H "Authorization: Bearer ${jwt}" \
      -H 'Content-Type: application/json' \
      -d '{"actor_subject":"*"}' >/dev/null 2>&1
  else
    http_status=$(echo "${cred_resp}" | python3 -c "import sys,json;d=json.load(sys.stdin);print('409' if 'duplicate' in d.get('error','') else '400')" 2>/dev/null || echo "400")
  fi
  resp="${http_status}"

  if [[ "${resp}" == "201" || "${resp}" == "200" ]]; then
    ok "GitHub token stored in vault as 'github-token'"
    printf "  %sUsage: repo.fetch with {\"credential\":\"github-token\"} for private repos%s\n" "${C_DIM}" "${C_RESET}"
  elif [[ "${resp}" == "409" ]]; then
    info "vault credential 'github-token' already exists — skipping (rotate via the Vault panel)"
  else
    warn "failed to store GitHub token (HTTP ${resp}). Add it manually in the Vault panel."
  fi
}

main "$@"
