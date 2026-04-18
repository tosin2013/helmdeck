#!/usr/bin/env bash
# scripts/configure-openclaw.sh — one-shot setup so an OpenClaw
# agent can consume helmdeck's MCP catalog without surprises.
#
# Codifies every manual step from docs/integrations/openclaw-upgrade-runbook.md:
# network bridge, JWT mint with iss=helmdeck, MCP config with
# lowercase authorization (issue #1 workaround), native OpenClaw
# skill install (skills/helmdeck/SKILL.md), tool-capable model pin,
# optional identity seed so the BOOTSTRAP.md loop doesn't hijack
# the agent. Idempotent — re-runs are safe.
#
# Release sync: the skill carries a `helmdeckVersion` stamp in its
# frontmatter (derived from git HEAD short-hash at install time).
# After any helmdeck release, re-run this script to refresh the
# stamped skill so agents see new packs and updated decision tables.
#
# Usage:
#   ./scripts/configure-openclaw.sh                           # configure agents.defaults
#   ./scripts/configure-openclaw.sh --agent coder             # configure agents.coder only
#   ./scripts/configure-openclaw.sh --seed-identity           # also seed IDENTITY/USER/SOUL
#   ./scripts/configure-openclaw.sh --rotate-jwt              # force fresh JWT
#   ./scripts/configure-openclaw.sh --model <id>              # pin a different primary model
#   ./scripts/configure-openclaw.sh --fallbacks a,b,c         # comma-separated fallback chain
#   ./scripts/configure-openclaw.sh --skip-mcp --skip-skills  # only refresh identity
#
# Exits 0 on success. Prints the verification probe commands at
# the end so the operator can sanity-check the outcome.

set -euo pipefail

# --- defaults --------------------------------------------------------------

AGENT="defaults"
# Model refs are parsed by splitting on the first '/' (see OpenClaw
# /app/docs/cli/models.md). Prefix with `openrouter/` to route through
# the OpenRouter auth profile instead of the direct Anthropic/MiniMax
# APIs, which require their own per-provider keys we typically don't
# have configured. Override with --model / --fallbacks for a different
# environment (e.g. a Bedrock-routed stack).
MODEL="openrouter/anthropic/claude-sonnet-4.6"
FALLBACKS_CSV="openrouter/minimax/minimax-m2.7"
SEED_IDENTITY="false"
ROTATE_JWT="false"
SKIP_MCP="false"
SKIP_SKILLS="false"

HELMDECK_ROOT="${HELMDECK_ROOT:-/root/helmdeck}"
HELMDECK_ENV_FILE="${HELMDECK_ENV_FILE:-${HELMDECK_ROOT}/deploy/compose/.env.local}"
HELMDECK_CONTAINER="${HELMDECK_CONTAINER:-helmdeck-control-plane}"
HELMDECK_NETWORK="${HELMDECK_NETWORK:-baas-net}"
HELMDECK_URL="${HELMDECK_URL:-http://${HELMDECK_CONTAINER}:3000/api/v1/mcp/sse}"

OPENCLAW_CONTAINER="${OPENCLAW_CONTAINER:-openclaw-openclaw-gateway-1}"
JWT_CACHE="${JWT_CACHE:-/tmp/helmdeck-jwt.txt}"
JWT_TTL_DAYS="${JWT_TTL_DAYS:-7}"
JWT_REFRESH_WINDOW_HOURS="${JWT_REFRESH_WINDOW_HOURS:-24}"

SKILLS_FILE="${SKILLS_FILE:-${HELMDECK_ROOT}/docs/integrations/SKILLS.md}"
SKILL_FILE="${SKILL_FILE:-${HELMDECK_ROOT}/skills/helmdeck/SKILL.md}"
SKILL_NAME="${SKILL_NAME:-helmdeck}"
# Path inside the container where OpenClaw scans for machine-managed
# skills (see /app/docs/tools/skills.md load precedence — managed
# local skills live at ~/.openclaw/skills and are visible to every
# agent on the machine).
OPENCLAW_SKILL_ROOT="${OPENCLAW_SKILL_ROOT:-/home/node/.openclaw/skills}"

# --- arg parse -------------------------------------------------------------

usage() {
	sed -n '2,20p' "$0" | sed 's/^# //;s/^#$//'
	exit 0
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--agent)          AGENT="$2"; shift 2 ;;
		--model)          MODEL="$2"; shift 2 ;;
		--fallbacks)      FALLBACKS_CSV="$2"; shift 2 ;;
		--seed-identity)  SEED_IDENTITY="true"; shift ;;
		--rotate-jwt)     ROTATE_JWT="true"; shift ;;
		--skip-mcp)       SKIP_MCP="true"; shift ;;
		--skip-skills)    SKIP_SKILLS="true"; shift ;;
		-h|--help)        usage ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

# --- helpers ---------------------------------------------------------------

log()  { printf '\033[1;34m[configure-openclaw]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[configure-openclaw]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[configure-openclaw]\033[0m %s\n' "$*" >&2; exit 1; }

require() {
	command -v "$1" >/dev/null 2>&1 || die "missing dependency: $1"
}

# --- 0. preflight ----------------------------------------------------------

require docker
require python3

[[ -f "$HELMDECK_ENV_FILE" ]] \
	|| die "env file not found: $HELMDECK_ENV_FILE (override with HELMDECK_ENV_FILE)"

docker ps --format '{{.Names}}' | grep -qx "$HELMDECK_CONTAINER" \
	|| die "$HELMDECK_CONTAINER is not running (start with 'make compose-up')"

docker ps --format '{{.Names}}' | grep -qx "$OPENCLAW_CONTAINER" \
	|| die "$OPENCLAW_CONTAINER is not running (start with 'docker compose -f /root/openclaw/docker-compose.yml up -d openclaw-gateway')"

log "preflight ok"

# --- 1. bridge baas-net into the openclaw container -----------------------

if docker inspect "$OPENCLAW_CONTAINER" \
	 --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
	 | grep -qw "$HELMDECK_NETWORK"; then
	log "network: $HELMDECK_NETWORK already attached to $OPENCLAW_CONTAINER"
else
	log "network: attaching $HELMDECK_NETWORK to $OPENCLAW_CONTAINER"
	docker network connect "$HELMDECK_NETWORK" "$OPENCLAW_CONTAINER"
fi

# Sanity-probe DNS before we do anything JWT-shaped.
docker exec "$OPENCLAW_CONTAINER" getent hosts "$HELMDECK_CONTAINER" >/dev/null \
	|| die "DNS: $OPENCLAW_CONTAINER cannot resolve $HELMDECK_CONTAINER"
log "network: DNS resolution ok"

# --- 2. JWT (mint or reuse) -----------------------------------------------

jwt_needs_refresh() {
	[[ "$ROTATE_JWT" == "true" ]] && return 0
	[[ ! -f "$JWT_CACHE" ]] && return 0
	# Inspect the cached token's exp claim. If <JWT_REFRESH_WINDOW_HOURS
	# from now, refresh. Avoids churning a valid JWT on every run while
	# still guaranteeing the configured token is usable for the day.
	python3 - "$JWT_CACHE" "$JWT_REFRESH_WINDOW_HOURS" <<'PY' 2>/dev/null || return 0
import json, base64, sys, time
path, window_h = sys.argv[1], int(sys.argv[2])
tok = open(path).read().strip()
parts = tok.split(".")
if len(parts) < 2: sys.exit(0)  # malformed; refresh
pad = parts[1] + "=" * (4 - len(parts[1]) % 4)
claims = json.loads(base64.urlsafe_b64decode(pad))
remaining = claims.get("exp", 0) - int(time.time())
sys.exit(0 if remaining < window_h * 3600 else 1)
PY
}

secret="$(grep ^HELMDECK_JWT_SECRET "$HELMDECK_ENV_FILE" | cut -d= -f2)"
[[ -n "$secret" ]] || die "HELMDECK_JWT_SECRET not found in $HELMDECK_ENV_FILE"

if jwt_needs_refresh; then
	log "jwt: minting fresh ${JWT_TTL_DAYS}-day token"
	HELMDECK_JWT_SECRET="$secret" JWT_TTL_DAYS="$JWT_TTL_DAYS" \
		python3 - > "$JWT_CACHE" <<'PY'
import jwt, time, os
now = int(time.time())
ttl = int(os.environ["JWT_TTL_DAYS"]) * 86400
print(jwt.encode({
	"sub":   "openclaw-configure",
	"name":  "openclaw-configure",
	"client":"openclaw",
	"scopes":["admin"],
	"iss":   "helmdeck",
	"iat":   now,
	"nbf":   now - 60,
	"exp":   now + ttl,
}, os.environ["HELMDECK_JWT_SECRET"], algorithm="HS256"))
PY
else
	log "jwt: reusing cached token ($(python3 -c '
import json,base64,sys,datetime
t=open("'"$JWT_CACHE"'").read().strip().split(".")[1]
p=t+"="*(4-len(t)%4)
c=json.loads(base64.urlsafe_b64decode(p))
print("exp:",datetime.datetime.utcfromtimestamp(c["exp"]).isoformat()+"Z")
'))"
fi

token="$(cat "$JWT_CACHE")"

# --- 3. MCP config (issue #1 workaround: lowercase authorization) --------

if [[ "$SKIP_MCP" == "true" ]]; then
	log "mcp: --skip-mcp set, leaving config untouched"
else
	log "mcp: writing mcp.servers.helmdeck (lowercase 'authorization' — issue #1)"
	mcp_json="$(HELMDECK_URL="$HELMDECK_URL" TOKEN="$token" python3 -c "
import json, os
print(json.dumps({
	'url':     os.environ['HELMDECK_URL'],
	'headers': {'authorization': 'Bearer ' + os.environ['TOKEN']},
	'timeoutMs': 300000,
}))
")"
	docker exec "$OPENCLAW_CONTAINER" openclaw mcp set helmdeck "$mcp_json" >/dev/null
fi

# --- 4. install helmdeck as a native OpenClaw Skill ----------------------

if [[ "$SKIP_SKILLS" == "true" ]]; then
	log "skills: --skip-skills set, leaving skill and systemPromptOverride untouched"
else
	[[ -f "$SKILL_FILE" ]] || die "SKILL.md not found at $SKILL_FILE — run from a helmdeck checkout"
	log "skills: installing $SKILL_NAME skill from $SKILL_FILE ($(wc -c < "$SKILL_FILE") bytes)"

	# Re-stamp the helmdeckVersion frontmatter with the current git
	# HEAD short-hash before copying, so the installed skill points
	# at whatever the operator has checked out — NOT whatever hash
	# was baked in at commit time (which is always one commit stale
	# for the SKILL.md file itself). Fall back to the committed
	# stamp if we're not in a git checkout or git isn't installed.
	stamp_tmp="$(mktemp -t helmdeck-skill.XXXXXX.md)"
	# shellcheck disable=SC2064
	trap "rm -f '$stamp_tmp'" EXIT
	if git_hash="$(cd "$HELMDECK_ROOT" && git rev-parse --short HEAD 2>/dev/null)"; then
		sed -E 's/(helmdeckVersion: *")[^"]*(")/\1'"$git_hash"'\2/' \
			"$SKILL_FILE" > "$stamp_tmp"
	else
		cp "$SKILL_FILE" "$stamp_tmp"
	fi

	# Copy SKILL.md into the managed-skill root so OpenClaw's loader
	# finds it via the documented precedence. We provision the
	# directory first so docker cp has a target (cp won't create
	# intermediate dirs).
	docker exec "$OPENCLAW_CONTAINER" mkdir -p "${OPENCLAW_SKILL_ROOT}/${SKILL_NAME}"
	docker cp "$stamp_tmp" \
		"${OPENCLAW_CONTAINER}:${OPENCLAW_SKILL_ROOT}/${SKILL_NAME}/SKILL.md"

	# Mark the skill enabled in skills.entries so an explicit
	# allowlist later doesn't accidentally hide it. Empty object
	# means "enabled with default settings."
	docker exec "$OPENCLAW_CONTAINER" \
		openclaw config set "skills.entries.${SKILL_NAME}" '{"enabled":true}' >/dev/null

	# Clear any stale systemPromptOverride left over from the
	# pre-skill era of this script. Two copies of the same prompt
	# (one in systemPromptOverride, one loaded via the skill) would
	# double the token bill and confuse the agent on conflicts.
	# Using config unset so the key is removed, not set-to-empty.
	if docker exec "$OPENCLAW_CONTAINER" \
		 openclaw config get "agents.${AGENT}.systemPromptOverride" 2>/dev/null \
		 | grep -q 'helmdeck'; then
		docker exec "$OPENCLAW_CONTAINER" \
			openclaw config unset "agents.${AGENT}.systemPromptOverride" >/dev/null 2>&1 || true
		log "skills: cleared stale agents.${AGENT}.systemPromptOverride (migrated to skill)"
	fi

	stamp="$(grep -oE 'helmdeckVersion: *"[^"]+"' "$stamp_tmp" | head -1 | sed 's/.*"\([^"]*\)".*/\1/')"
	[[ -n "$stamp" ]] && log "skills: stamped helmdeck version ${stamp}"
fi

# --- 5. pin a tool-capable model + fallback chain ------------------------

log "model: pinning agents.${AGENT}.model.primary = ${MODEL}"
docker exec "$OPENCLAW_CONTAINER" \
	openclaw config set "agents.${AGENT}.model.primary" "$MODEL" >/dev/null

# Fallback chain — comma-separated to JSON array. Empty string clears it.
if [[ -n "$FALLBACKS_CSV" ]]; then
	fallbacks_json="$(FALLBACKS_CSV="$FALLBACKS_CSV" python3 -c "
import json, os
items = [s.strip() for s in os.environ['FALLBACKS_CSV'].split(',') if s.strip()]
print(json.dumps(items))
")"
	log "model: pinning agents.${AGENT}.model.fallbacks = ${fallbacks_json}"
	docker exec "$OPENCLAW_CONTAINER" \
		openclaw config set "agents.${AGENT}.model.fallbacks" "$fallbacks_json" >/dev/null
fi

# --- 6. optional: seed identity so BOOTSTRAP.md doesn't loop -------------

if [[ "$SEED_IDENTITY" == "true" ]]; then
	log "identity: seeding IDENTITY/USER/SOUL.md in workspace so bootstrap completes"
	# The workspace is per-agent when the agent has its own, otherwise
	# the shared 'main' workspace. Ask the container which exists.
	workspace="$(docker exec "$OPENCLAW_CONTAINER" sh -c "
		if [ -d /home/node/.openclaw/agents/${AGENT}/workspace ]; then
			echo /home/node/.openclaw/agents/${AGENT}/workspace
		else
			echo /home/node/.openclaw/workspace
		fi" 2>/dev/null || echo "/home/node/.openclaw/workspace")"

	# Write the three seed files to a host tmpdir first so the heredoc
	# content is never nested inside a quoted docker-exec payload —
	# safer than sh-c stacking heredocs over a ssh-like channel.
	seed_tmp="$(mktemp -d -t helmdeck-identity.XXXXXX)"
	# shellcheck disable=SC2064  # want $seed_tmp expanded now, not on signal
	trap "rm -rf '$seed_tmp'" EXIT

	cat > "$seed_tmp/IDENTITY.md" <<'EOF'
# Identity

Name: helmdeck-agent
Role: Capability-pack operator paired with helmdeck (self-hosted browser + AI tooling platform)
Surface: OpenClaw agent in the main session channel, MCP-backed tools prefixed `helmdeck__*`
EOF

	cat > "$seed_tmp/USER.md" <<'EOF'
# User

The operator of this agent is a helmdeck developer. Expect technical prompts around:
- Building and testing Capability Packs (see SKILLS.md for the full list)
- Repo orientation via `helmdeck__repo_fetch` and `helmdeck__repo_map`
- Browser automation, web scraping, slide rendering, document parsing
Assume the operator is comfortable reading audit logs and JSON tool-call transcripts.
EOF

	cat > "$seed_tmp/SOUL.md" <<'EOF'
# Soul

Default temperament: terse, precise, action-oriented. Use helmdeck tools by their
full MCP name (e.g. `helmdeck__repo_fetch`) and trust the JSON envelopes they
return — do not hallucinate fields or paper over errors. When a tool call fails,
surface the exact error code and message rather than summarising.

The SKILLS.md system prompt describes every pack's input/output shape and error
semantics. Follow its decision tables (especially the `repo.fetch` `signals`
branch for orientation) before falling back to generic shell exec.
EOF

	docker exec "$OPENCLAW_CONTAINER" mkdir -p "$workspace"
	for f in IDENTITY.md USER.md SOUL.md; do
		docker cp "$seed_tmp/$f" "${OPENCLAW_CONTAINER}:${workspace}/$f"
	done
	log "identity: seeded $workspace/{IDENTITY,USER,SOUL}.md"

	# BOOTSTRAP.md is a presence-based gate — its own final line says
	# "Delete this file. You don't need a bootstrap script anymore —
	# you're you now." Since we've just filled in IDENTITY/USER/SOUL,
	# remove it so the agent doesn't loop on the bootstrap preamble
	# on every startup (startup-context-B0ypI-Q1.js in the OpenClaw
	# bundle keys off its mere existence).
	if docker exec "$OPENCLAW_CONTAINER" test -f "$workspace/BOOTSTRAP.md"; then
		docker exec "$OPENCLAW_CONTAINER" rm "$workspace/BOOTSTRAP.md"
		log "identity: removed $workspace/BOOTSTRAP.md (bootstrap complete)"
	fi
fi

# --- 7. restart gateway so new config is loaded --------------------------

log "restart: $OPENCLAW_CONTAINER"
docker restart "$OPENCLAW_CONTAINER" >/dev/null

# Wait for the gateway to come back (up to ~10s).
for _ in 1 2 3 4 5 6 7 8 9 10; do
	if docker exec "$OPENCLAW_CONTAINER" openclaw --version >/dev/null 2>&1; then
		break
	fi
	sleep 1
done

# --- 8. verification hints ------------------------------------------------

log ""
log "done. verify with:"
cat <<EOF

  # Tool catalog (expect ~36 helmdeck__* tools alongside built-ins)
  docker exec $OPENCLAW_CONTAINER openclaw config get agents.${AGENT}.model.primary

  # Live tool visibility via the chat UI
  open http://localhost:18789
  # (paste: "list every tool whose name starts with helmdeck__")

  # Server-side proof of handshake
  docker logs -f $HELMDECK_CONTAINER 2>&1 | grep -E '/api/v1/mcp/sse'

  # Token claims (decode the one we just wrote)
  python3 -c "import json,base64; t=open('$JWT_CACHE').read().strip().split('.')[1]; \\
    p=t+'='*(4-len(t)%4); print(json.dumps(json.loads(base64.urlsafe_b64decode(p)),indent=2))"
EOF
