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
#   ./scripts/configure-openclaw.sh --skill helmdeck-debug    # install just one skill (default: all)
#   ./scripts/configure-openclaw.sh --seed-canonical-layout   # seed SOUL/IDENTITY/USER/AGENTS skeletons
#   ./scripts/configure-openclaw.sh --seed-identity           # alias for --seed-canonical-layout (kept for backwards compat)
#   ./scripts/configure-openclaw.sh --force-overwrite         # when seeding, .bak existing files and write fresh
#   ./scripts/configure-openclaw.sh --rotate-jwt              # force fresh JWT
#   ./scripts/configure-openclaw.sh --model <id>              # pin a different primary model
#   ./scripts/configure-openclaw.sh --fallbacks a,b,c         # comma-separated fallback chain
#   ./scripts/configure-openclaw.sh --skip-mcp --skip-skills  # only refresh identity
#   ./scripts/configure-openclaw.sh --skip-compose-override   # don't install docker-compose.override.yml
#   ./scripts/configure-openclaw.sh --smoke                   # also run the integration smoke check
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
FORCE_OVERWRITE="false"
ROTATE_JWT="false"
SKIP_MCP="false"
SKIP_SKILLS="false"
DO_SMOKE="false"
SKILL_ONLY=""
INSTALL_COMPOSE_OVERRIDE="true"

HELMDECK_ROOT="${HELMDECK_ROOT:-/root/helmdeck}"
HELMDECK_ENV_FILE="${HELMDECK_ENV_FILE:-${HELMDECK_ROOT}/deploy/compose/.env.local}"
HELMDECK_CONTAINER="${HELMDECK_CONTAINER:-helmdeck-control-plane}"
HELMDECK_NETWORK="${HELMDECK_NETWORK:-baas-net}"
HELMDECK_URL="${HELMDECK_URL:-http://${HELMDECK_CONTAINER}:3000/api/v1/mcp/sse}"

OPENCLAW_CONTAINER="${OPENCLAW_CONTAINER:-openclaw-openclaw-gateway-1}"
# Path to the OpenClaw repo's docker-compose.yml. Default matches the
# canonical /root/openclaw checkout; override for hosts where OpenClaw
# lives elsewhere (e.g. /home/<user>/openclaw). Used by the auth-list
# probe and the "is-the-container-running" hint.
OPENCLAW_COMPOSE_FILE="${OPENCLAW_COMPOSE_FILE:-/root/openclaw/docker-compose.yml}"
JWT_CACHE="${JWT_CACHE:-/tmp/helmdeck-jwt.txt}"
JWT_TTL_DAYS="${JWT_TTL_DAYS:-7}"
JWT_REFRESH_WINDOW_HOURS="${JWT_REFRESH_WINDOW_HOURS:-24}"

SKILLS_FILE="${SKILLS_FILE:-${HELMDECK_ROOT}/docs/integrations/SKILLS.md}"
# Every skills/<name>/SKILL.md under this root is installed (helmdeck +
# helmdeck-debug + any future skill). --skill <name> narrows to one.
SKILLS_ROOT="${SKILLS_ROOT:-${HELMDECK_ROOT}/skills}"
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
		--skill)          SKILL_ONLY="$2"; shift 2 ;;
		--model)          MODEL="$2"; shift 2 ;;
		--fallbacks)      FALLBACKS_CSV="$2"; shift 2 ;;
		--seed-identity)  SEED_IDENTITY="true"; shift ;;
		--seed-canonical-layout) SEED_IDENTITY="true"; shift ;;
		--force-overwrite) FORCE_OVERWRITE="true"; shift ;;
		--rotate-jwt)     ROTATE_JWT="true"; shift ;;
		--skip-mcp)       SKIP_MCP="true"; shift ;;
		--skip-skills)    SKIP_SKILLS="true"; shift ;;
		--smoke)          DO_SMOKE="true"; shift ;;
		--skip-compose-override) INSTALL_COMPOSE_OVERRIDE="false"; shift ;;
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
	|| die "$OPENCLAW_CONTAINER is not running (start with 'docker compose -f $OPENCLAW_COMPOSE_FILE up -d openclaw-gateway')"

# Probe for the LLM-provider auth OpenClaw needs before this script can pin
# a model. Helmdeck's gateway env-var (HELMDECK_OPENROUTER_API_KEY in
# .env.local) is *separate* — that wires the gateway, not OpenClaw itself.
# OpenClaw stores its own auth under ~/.openclaw/. If the provider implied
# by --model isn't authenticated, every chat-UI tool call will 401, and
# the failure mode is visible only at runtime. Catch it here.
provider_from_model() {
	local m="$1"
	# strip "openrouter/" prefix and take everything before the next '/'
	# so "openrouter/anthropic/claude-..." → "openrouter".
	printf '%s' "$m" | awk -F'/' '{print $1}'
}
PROVIDER="$(provider_from_model "$MODEL")"

# Detect the shell-env credential injection path (set in the OpenClaw
# overlay compose.openclaw-sidecar.yml). When OPENCLAW_LOAD_SHELL_ENV=true
# is on the container AND the corresponding <PROVIDER>_API_KEY env var
# is set on the container, OpenClaw routes provider calls fine without
# an `openclaw models auth login` profile — so the auth-list probe
# below is a false positive in that case.
provider_shell_env_ok() {
	local provider_upper
	provider_upper="$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')"
	docker exec "$OPENCLAW_CONTAINER" sh -c \
		"[ \"\${OPENCLAW_LOAD_SHELL_ENV:-}\" = \"true\" ] && [ -n \"\${${provider_upper}_API_KEY:-}\" ]" \
		2>/dev/null
}

if [[ "$PROVIDER" != "openclaw" && "$PROVIDER" != "" ]]; then
	# OpenClaw 2026.5.6 'models auth list' output shape:
	#   Profiles:
	#   - openrouter:default [openrouter/api_key]
	# So the provider line is `- <provider>:<profile-id> [<provider>/<auth-method>]`.
	# Grep for "^- <provider>(:| )" to match the profile entry across older + newer formats.
	#
	# Two-step capture-then-grep instead of a single pipeline. Direct
	# `... | grep -q ...` is SIGPIPE-vulnerable under `set -o pipefail`:
	# grep -q exits immediately after the first match, which closes
	# its stdin and SIGPIPEs the upstream `docker exec` (rc=141 = 128
	# + signal 13). pipefail then propagates that 141 as the pipeline
	# exit, the `if !` inverts it, and we hit the "missing auth" die
	# branch on a deployment that's actually authenticated correctly.
	# Capturing stdout into a variable first lets the docker exec
	# finish cleanly; the in-memory grep against $auth_list never
	# closes its stdin early.
	#
	# Also: use `docker exec` against the running gateway rather than
	# `docker compose run --rm openclaw-cli`. The compose-run path
	# spawns a fresh container (slower, noisier, and exits non-zero
	# under `2>/dev/null` for unrelated reasons). The running gateway
	# has the auth state OpenClaw actually uses; the docker-exec
	# pattern is already used elsewhere in this script.
	auth_list="$(docker exec "$OPENCLAW_CONTAINER" openclaw models auth list 2>/dev/null || true)"
	if ! printf '%s\n' "$auth_list" \
			| grep -qiE "^[-* ]+${PROVIDER}([:[:space:]]|$)"; then
		# Check the shell-env path before we hard-fail. If it's in
		# use the auth-list probe will always come back empty —
		# OpenClaw's `models auth list` only shows entries created by
		# `auth login`, never the OPENCLAW_LOAD_SHELL_ENV passthrough.
		if provider_shell_env_ok "$PROVIDER"; then
			warn "OpenClaw has no '${PROVIDER}' login profile, but"
			warn "  OPENCLAW_LOAD_SHELL_ENV=true and ${PROVIDER^^}_API_KEY is set on"
			warn "  the container — provider calls will route via the shell-env"
			warn "  fallback. Continuing. If routing 401s at runtime, run:"
			warn "    docker compose -f $OPENCLAW_COMPOSE_FILE run --rm -it openclaw-cli \\"
			warn "      models auth login --provider ${PROVIDER}"
		else
			warn "OpenClaw has no '${PROVIDER}' auth configured."
			warn ""
			warn "  Run this once, paste your API key when prompted, then re-run me:"
			warn ""
			warn "    docker compose -f $OPENCLAW_COMPOSE_FILE run --rm -it openclaw-cli \\"
			warn "      models auth login --provider ${PROVIDER}"
			warn ""
			warn "  (OpenClaw 2026.5.6+ requires --provider as a flag, not a positional arg."
			warn "   The -it is required for the interactive TTY prompt.)"
			warn ""
			warn "  (This is OpenClaw's own model auth — separate from helmdeck's"
			warn "  gateway env vars in deploy/compose/.env.local. See"
			warn "  docs/integrations/openclaw.md §5 'Configure OpenClaw's LLM provider'.)"
			warn ""
			warn "  Or set OPENCLAW_LOAD_SHELL_ENV=true on the container and export"
			warn "  ${PROVIDER^^}_API_KEY in the host shell — see the openclaw-sidecar"
			warn "  compose overlay."
			die "missing ${PROVIDER} auth"
		fi
	else
		log "preflight: ${PROVIDER} auth is configured"
	fi
fi

log "preflight ok"

# --- 1. bridge baas-net into the openclaw container -----------------------
#
# Two-layer attachment so the bridge survives container recreation:
#
#   1a. Install a compose override into the OpenClaw compose dir
#       (deploy/openclaw-baas-net.compose.yml). This declares the
#       attachment in OpenClaw's lifecycle so every `docker compose up`
#       re-establishes it automatically.
#
#   1b. Best-effort runtime `docker network connect` for the CURRENT
#       container instance. The override takes effect on the NEXT
#       compose-recreate; until then this step keeps the live
#       container reachable so the rest of this run (JWT mint, MCP
#       config write, skill install) can probe and verify against
#       helmdeck without requiring a restart.
#
# History: the runtime-only attachment was the recurring 24-hour
# breakage source — every rebuild dropped the bridge silently, the
# bundle-mcp probes started 401-ing (stale token) or DNS-failing
# (network gone), and operators re-ran this script. The override
# file moves the load-bearing piece to a place that survives.

if [[ "$INSTALL_COMPOSE_OVERRIDE" == "true" ]]; then
	override_src="${HELMDECK_ROOT}/deploy/openclaw-baas-net.compose.yml"
	# Detect the OpenClaw compose directory from OPENCLAW_COMPOSE_FILE.
	# OpenClaw's main compose is `docker-compose.yml` (legacy naming),
	# so the auto-loaded override is `docker-compose.override.yml`.
	openclaw_compose_dir="$(dirname "$OPENCLAW_COMPOSE_FILE")"
	override_dst="${openclaw_compose_dir}/docker-compose.override.yml"
	if [[ ! -f "$override_src" ]]; then
		warn "compose-override: source missing ($override_src) — skipping; manual fix:"
		warn "  cp deploy/openclaw-baas-net.compose.yml $override_dst"
	elif [[ ! -d "$openclaw_compose_dir" ]]; then
		warn "compose-override: OpenClaw compose dir not found ($openclaw_compose_dir) — skipping"
	elif [[ -f "$override_dst" ]] && ! cmp -s "$override_src" "$override_dst"; then
		# An existing, DIFFERENT override is present. Back it up and
		# replace — but loudly, so the operator can see we touched it.
		backup="${override_dst}.bak.$(date -u +%Y%m%d-%H%M%S)"
		warn "compose-override: existing $override_dst differs; backing up to $backup"
		cp "$override_dst" "$backup"
		cp "$override_src" "$override_dst"
		log "compose-override: installed $override_dst (backup at $backup)"
	elif [[ -f "$override_dst" ]]; then
		log "compose-override: $override_dst already current"
	else
		cp "$override_src" "$override_dst"
		log "compose-override: installed $override_dst"
	fi
fi

if docker inspect "$OPENCLAW_CONTAINER" \
	 --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
	 | grep -qw "$HELMDECK_NETWORK"; then
	log "network: $HELMDECK_NETWORK already attached to $OPENCLAW_CONTAINER"
else
	log "network: attaching $HELMDECK_NETWORK to $OPENCLAW_CONTAINER (runtime — override takes effect on next compose-up)"
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
	log "skills: --skip-skills set, leaving skills and systemPromptOverride untouched"
else
	# install_openclaw_skill re-stamps the helmdeckVersion frontmatter
	# with the current git HEAD short-hash (so the installed skill points
	# at whatever the operator has checked out, not the always-one-commit-
	# stale baked-in stamp), copies SKILL.md into the managed-skill root
	# (OpenClaw's loader finds it via the documented precedence), fixes
	# ownership (docker cp lands as root; openclaw runs as `node`), and
	# marks the skill enabled. The per-skill tmp file is removed at the end
	# of each call so the loop below doesn't leak temp files.
	install_openclaw_skill() {
		local skill_name="$1" skill_file="$2"
		log "skills: installing ${skill_name} from ${skill_file} ($(wc -c < "$skill_file") bytes)"

		local stamp_tmp
		stamp_tmp="$(mktemp -t helmdeck-skill.XXXXXX.md)"
		if git_hash="$(cd "$HELMDECK_ROOT" && git rev-parse --short HEAD 2>/dev/null)"; then
			sed -E 's/(helmdeckVersion: *")[^"]*(")/\1'"$git_hash"'\2/' "$skill_file" > "$stamp_tmp"
		else
			cp "$skill_file" "$stamp_tmp"
		fi

		# Provision the dir first (docker cp won't create intermediate dirs).
		docker exec "$OPENCLAW_CONTAINER" mkdir -p "${OPENCLAW_SKILL_ROOT}/${skill_name}"
		docker cp "$stamp_tmp" \
			"${OPENCLAW_CONTAINER}:${OPENCLAW_SKILL_ROOT}/${skill_name}/SKILL.md"
		# -u root so chown has privilege regardless of the gateway
		# entrypoint's default user.
		docker exec -u root "$OPENCLAW_CONTAINER" sh -c \
			"chown -R node:node '${OPENCLAW_SKILL_ROOT}/${skill_name}' && chmod 644 '${OPENCLAW_SKILL_ROOT}/${skill_name}/SKILL.md'"
		# Empty object means "enabled with default settings" — keeps a later
		# explicit allowlist from accidentally hiding the skill.
		docker exec "$OPENCLAW_CONTAINER" \
			openclaw config set "skills.entries.${skill_name}" '{"enabled":true}' >/dev/null

		local stamp
		stamp="$(grep -oE 'helmdeckVersion: *"[^"]+"' "$stamp_tmp" | head -1 | sed 's/.*"\([^"]*\)".*/\1/' || true)"
		[[ -n "$stamp" ]] && log "skills: ${skill_name} stamped helmdeck version ${stamp}"
		rm -f "$stamp_tmp"
	}

	[[ -d "$SKILLS_ROOT" ]] || die "skills root not found at $SKILLS_ROOT — run from a helmdeck checkout"
	installed_any="false"
	for skill_dir in "$SKILLS_ROOT"/*/; do
		[[ -d "$skill_dir" ]] || continue
		skill_name="$(basename "$skill_dir")"
		skill_file="${skill_dir}SKILL.md"
		[[ -f "$skill_file" ]] || { warn "skills: no SKILL.md in ${skill_dir}, skipping"; continue; }
		if [[ -n "$SKILL_ONLY" && "$SKILL_ONLY" != "$skill_name" ]]; then
			continue
		fi
		install_openclaw_skill "$skill_name" "$skill_file"
		installed_any="true"
	done
	[[ "$installed_any" == "true" ]] || die "no skills installed (looked under ${SKILLS_ROOT}/*/SKILL.md; --skill='${SKILL_ONLY}')"

	# Clear any stale systemPromptOverride left over from the pre-skill era
	# of this script (global, runs once after all skills). Two copies of the
	# same prompt (one in systemPromptOverride, one loaded via the skill)
	# would double the token bill and confuse the agent on conflicts. Using
	# config unset so the key is removed, not set-to-empty.
	if docker exec "$OPENCLAW_CONTAINER" \
		 openclaw config get "agents.${AGENT}.systemPromptOverride" 2>/dev/null \
		 | grep -q 'helmdeck'; then
		docker exec "$OPENCLAW_CONTAINER" \
			openclaw config unset "agents.${AGENT}.systemPromptOverride" >/dev/null 2>&1 || true
		log "skills: cleared stale agents.${AGENT}.systemPromptOverride (migrated to skill)"
	fi
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
	log "identity: seeding canonical SOUL/IDENTITY/USER/AGENTS.md in workspace"
	# The workspace is per-agent when the agent has its own, otherwise
	# the shared 'main' workspace. Ask the container which exists.
	workspace="$(docker exec "$OPENCLAW_CONTAINER" sh -c "
		if [ -d /home/node/.openclaw/agents/${AGENT}/workspace ]; then
			echo /home/node/.openclaw/agents/${AGENT}/workspace
		else
			echo /home/node/.openclaw/workspace
		fi" 2>/dev/null || echo "/home/node/.openclaw/workspace")"

	# Write the four seed files to a host tmpdir first so the heredoc
	# content is never nested inside a quoted docker-exec payload —
	# safer than sh-c stacking heredocs over a ssh-like channel.
	# Each file is intentionally well under OpenClaw's 12,000-char
	# bootstrap injection cap, with operator-tunable TODO comments
	# at the head of each section. Concerns are split per OpenClaw's
	# canonical model (SOUL=voice, IDENTITY=name, USER=operator,
	# AGENTS=operating rules) so editing one doesn't leak into another.
	# See docs/integrations/openclaw.md §5d and
	# docs/howto/personalize-an-openclaw-agent.md for the layering rationale.
	seed_tmp="$(mktemp -d -t helmdeck-identity.XXXXXX)"
	# shellcheck disable=SC2064  # want $seed_tmp expanded now, not on signal
	trap "rm -rf '$seed_tmp'" EXIT

	# SOUL.md — voice, tone, banned phrases (NOT operating rules)
	cat > "$seed_tmp/SOUL.md" <<'EOF'
# Soul

Voice posture for this agent. Edit to match your style — defaults are intentionally generic.

<!-- TODO: tone — architect-voiced? practitioner? academic? casual-but-precise? -->
- First-person, terse, action-oriented
- Skeptical of vendor marketing; favor "I tested this on..." over abstract claims
- One sentence, one idea

<!-- TODO: editorial discipline — adjust to your publishing surface -->
- Sentence case for headings
- Subheading every 2-4 paragraphs (for long-form content)
- Acronyms spelled out on first use ("Model Context Protocol (MCP)" then "MCP")

<!-- TODO: banned phrases — add domain-specific weasel words -->
- No marketing jargon: "game-changer", "10x", "transformative", "synergy", "leverage" (as verb)
- No filler: "great question", "let's dive in", "in conclusion"

See docs/howto/personalize-an-openclaw-agent.md § "Walkthrough — tuning SOUL.md" for the
canonical guidance. Generally don't customize this file heavily; voice rules apply broadly.
EOF

	# IDENTITY.md — name / emoji / one-line theme (target ~150 chars)
	cat > "$seed_tmp/IDENTITY.md" <<'EOF'
# Identity

<!-- TODO: customize for this agent. Keep small (target ~150 chars). -->
- name: helmdeck-agent
- emoji: 🛠️
- theme: Capability-pack operator paired with helmdeck self-hosted MCP server
EOF

	# USER.md — who the operator is (their profile)
	cat > "$seed_tmp/USER.md" <<'EOF'
# User

<!-- TODO: replace with YOUR profile. See docs/howto/personalize-an-openclaw-agent.md
     § "Walkthrough — populating USER.md" for the template. -->

## Who I am
- Name: <your name>
- Role: helmdeck operator (default; replace with your actual role)
- Geographic context: <city / region — informs timezone, regulatory context>

## My domain
- Primary expertise: <one or two sentences>
- Secondary areas I write about: <list>

## My audience
- Where I publish: <list your platforms>
- Who reads me: <reader persona>

## Current focus
- Active projects: <list with one-line descriptions>
- Ongoing themes: <2-4 ideas you keep returning to>

## Editorial preferences
- Tone: <override SOUL.md's defaults if needed>
- Length defaults: <e.g., 800-1300 words for technical-deep-dive>
- Things I avoid: <e.g., listicle clickbait, marketing recap intros>
EOF

	# AGENTS.md — operating rules / workflow shape / tool whitelist
	cat > "$seed_tmp/AGENTS.md" <<'EOF'
# Agents

Operating instructions for this agent. Tune to your workflow shape and model.

<!-- TODO: customize per your use case. See docs/howto/per-model-agents/ for model-specific recipes
     (e.g., gemma-4-iterative-workflow.md) — copy the closest match and adapt. -->

## Tool whitelist

You MAY call helmdeck packs (prefixed `helmdeck__`) per the contracts in
`~/.openclaw/skills/helmdeck/SKILL.md`. Use packs by their full MCP name
(e.g., `helmdeck__repo-fetch`).

<!-- TODO: tighten to a specific allow-list if your workflow doesn't need all packs.
     Empirically (PR #481 + PR #484), explicit whitelists prevent goal drift to
     unauthorized tools. -->

## Workflow shape

<!-- TODO: pick a workflow shape that matches your model's chain-call reliability
     per `models/<provider>-<model>.yaml`. For Tier C models the three-turn
     iterative pattern (outline → draft + ground → deposit + verify) is
     empirically validated. See docs/howto/per-model-agents/ for recipes. -->

## Hard constraints

- Trust the JSON envelopes pack responses return. Do NOT hallucinate fields or
  paper over errors — surface the exact error code and message instead of summarizing.
- Follow the decision tables in `~/.openclaw/skills/helmdeck/SKILL.md` for pack
  routing (especially the `repo.fetch` `signals` branch for orientation) before
  falling back to generic shell exec.
- When a workflow says "exactly N tool calls per turn," honor it. The
  pack-status / pack-result polling pattern for async packs (e.g.,
  content.ground) does NOT count against that budget when the agent waits for
  state:completed before proceeding.

## Etiquette

- Ask ONE clarifying question if a load-bearing decision is ambiguous; otherwise
  state your assumption and proceed.
- When uncertain, say what you tested vs. what you assumed; don't conflate.

<!-- TODO: persona-specific etiquette overrides — e.g., specific handoff lines,
     literal phrasing requirements, response-shape invariants. -->
EOF

	docker exec "$OPENCLAW_CONTAINER" mkdir -p "$workspace"
	wrote_any="false"
	skipped_any="false"
	for f in SOUL.md IDENTITY.md USER.md AGENTS.md; do
		if docker exec "$OPENCLAW_CONTAINER" test -f "${workspace}/$f" 2>/dev/null; then
			if [[ "$FORCE_OVERWRITE" == "true" ]]; then
				ts="$(date +%Y%m%d-%H%M%S)"
				docker exec "$OPENCLAW_CONTAINER" cp "${workspace}/$f" "${workspace}/${f}.bak.${ts}"
				docker cp "$seed_tmp/$f" "${OPENCLAW_CONTAINER}:${workspace}/$f"
				log "identity: overwrote ${workspace}/$f (backup: ${f}.bak.${ts})"
				wrote_any="true"
			else
				log "identity: skipped ${workspace}/$f (already exists; pass --force-overwrite to .bak and write fresh)"
				skipped_any="true"
			fi
		else
			docker cp "$seed_tmp/$f" "${OPENCLAW_CONTAINER}:${workspace}/$f"
			wrote_any="true"
		fi
	done
	if [[ "$wrote_any" == "true" ]]; then
		log "identity: seeded canonical layout in $workspace (SOUL+IDENTITY+USER+AGENTS)"
	fi
	if [[ "$skipped_any" == "true" && "$FORCE_OVERWRITE" != "true" ]]; then
		log "identity: see docs/howto/personalize-an-openclaw-agent.md for guidance on editing existing files"
	fi

	# BOOTSTRAP.md is a presence-based gate — its own final line says
	# "Delete this file. You don't need a bootstrap script anymore —
	# you're you now." Since we've just filled in SOUL/IDENTITY/USER/AGENTS,
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

# --- 9. optional: integration smoke check --------------------------------
# Gated behind --smoke so re-running configure non-interactively (CI,
# release-sync scripts) isn't forced into a full agent round-trip.
if [[ "$DO_SMOKE" == "true" ]]; then
	log ""
	log "running integration smoke check (--smoke) against the live stack"
	bash "${HELMDECK_ROOT}/scripts/smoke-integration.sh" \
		|| warn "integration smoke check failed — see output above (configure itself succeeded)"
fi
