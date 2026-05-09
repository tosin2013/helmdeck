#!/usr/bin/env bash
# scripts/publish-to-mcp-registry.sh
#
# Publishes .mcp/server.json to the official MCP Registry
# (registry.modelcontextprotocol.io). Run this once per release tag.
#
# Prereqs:
#   - Go ≥ 1.22 on PATH (we build mcp-publisher from source upstream)
#   - GitHub login: the publisher CLI uses GitHub OAuth interactively
#   - Network egress to github.com + registry.modelcontextprotocol.io
#
# What it does, in order:
#   1. Validates .mcp/server.json against the upstream draft schema
#   2. Builds (or reuses) the mcp-publisher binary
#   3. Authenticates via GitHub OAuth (interactive, opens a browser)
#   4. Publishes the metadata
#   5. Prints the live registry URL for verification
#
# Idempotent: re-running is safe. The registry accepts re-publishes of
# the same version (server-side dedup) and overwrites metadata; bumping
# the `version` field in .mcp/server.json is what creates a new entry.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_JSON="${REPO_ROOT}/.mcp/server.json"
WORK_DIR="${HELMDECK_MCP_PUBLISHER_DIR:-/tmp/mcp-registry}"
PUBLISHER_BIN="${WORK_DIR}/bin/mcp-publisher"
SCHEMA_URL="https://raw.githubusercontent.com/modelcontextprotocol/registry/main/docs/reference/server-json/draft/server.schema.json"

red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }

step() { printf '\n\033[36m▸ %s\033[0m\n' "$*"; }

# --- Step 0: Sanity ----------------------------------------------------------
[[ -f "$SERVER_JSON" ]] || { red "Missing: $SERVER_JSON"; exit 1; }
command -v go >/dev/null 2>&1 || { red "Need go on PATH"; exit 1; }
command -v git >/dev/null 2>&1 || { red "Need git on PATH"; exit 1; }
command -v jq >/dev/null 2>&1 || { red "Need jq on PATH"; exit 1; }

NAME="$(jq -r .name "$SERVER_JSON")"
VERSION="$(jq -r .version "$SERVER_JSON")"
green "About to publish: $NAME @ $VERSION"

# --- Step 1: Schema validation ----------------------------------------------
step "Validating $SERVER_JSON against upstream schema"
if command -v node >/dev/null 2>&1; then
  TMP_VALIDATE_DIR="$(mktemp -d)"
  trap 'rm -rf "$TMP_VALIDATE_DIR"' EXIT
  pushd "$TMP_VALIDATE_DIR" >/dev/null
  npm init -y >/dev/null 2>&1
  npm i --silent ajv ajv-formats >/dev/null 2>&1
  curl -fsSL "$SCHEMA_URL" -o schema.json
  node -e "
    const Ajv = require('ajv');
    const addFormats = require('ajv-formats');
    const fs = require('fs');
    const schema = JSON.parse(fs.readFileSync('schema.json'));
    const data   = JSON.parse(fs.readFileSync(process.argv[1]));
    const ajv = new Ajv({ strict: false, allErrors: true });
    addFormats(ajv);
    const validate = ajv.compile(schema);
    if (!validate(data)) {
      console.error('Schema errors:');
      console.error(JSON.stringify(validate.errors, null, 2));
      process.exit(1);
    }
    console.log('VALID ✓');
  " "$SERVER_JSON"
  popd >/dev/null
else
  yellow "node not found — skipping schema validation; the publisher CLI will validate server-side"
fi

# --- Step 2: Build the publisher --------------------------------------------
step "Preparing mcp-publisher in $WORK_DIR"
if [[ ! -x "$PUBLISHER_BIN" ]]; then
  if [[ ! -d "$WORK_DIR/.git" ]]; then
    git clone --depth 1 https://github.com/modelcontextprotocol/registry.git "$WORK_DIR"
  else
    git -C "$WORK_DIR" pull --ff-only
  fi
  pushd "$WORK_DIR" >/dev/null
  if [[ -f Makefile ]] && grep -q '^publisher:' Makefile; then
    make publisher
  else
    mkdir -p bin
    go build -o bin/mcp-publisher ./cmd/publisher
  fi
  popd >/dev/null
fi
green "Publisher ready: $PUBLISHER_BIN"

# --- Step 3: Authenticate ---------------------------------------------------
step "Authenticating via GitHub (interactive — a browser tab will open)"
"$PUBLISHER_BIN" login github

# --- Step 4: Publish --------------------------------------------------------
step "Publishing $NAME @ $VERSION"
"$PUBLISHER_BIN" publish "$SERVER_JSON"

# --- Step 5: Verify ---------------------------------------------------------
step "Verification"
NS_ENCODED="$(printf '%s' "$NAME" | sed 's|/|%2F|g')"
URL="https://registry.modelcontextprotocol.io/v0/servers?search=$NS_ENCODED"
green "Registry API search: $URL"
green "Web view (browse the live entry):"
green "  https://registry.modelcontextprotocol.io/servers/$NAME"
echo
yellow "If the entry doesn't render within ~1 min, check the publisher output above for errors."
yellow "Downstream aggregators (mcp.so, Glama, PulseMCP) typically ingest within 24h."
