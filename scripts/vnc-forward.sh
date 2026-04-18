#!/usr/bin/env bash
# scripts/vnc-forward.sh — expose the currently-active desktop-mode
# session's noVNC port (6080) on the helmdeck host's 127.0.0.1:6080
# so operators SSH-forwarding port 6080 to their laptop can watch
# agent runs through a browser.
#
# Usage:
#   # one-shot: start, Ctrl-C to stop
#   ./scripts/vnc-forward.sh
#
#   # point at a specific session
#   ./scripts/vnc-forward.sh --session <id>
#
#   # publish on a different host port (e.g. 8080) and bind to all
#   # interfaces instead of loopback
#   ./scripts/vnc-forward.sh --bind 0.0.0.0 --port 8080
#
# Why this exists: the sidecar exposes noVNC on container port 6080
# but docker run doesn't publish it, so the only in-tree access path
# is baas-net-internal. This script socat-proxies host:6080 → the
# active session's container IP:6080. Replaces the manual
# per-session `docker inspect … NetworkSettings.Networks.baas-net.
# IPAddress` + SSH -L dance the ADR 028 comment describes. The
# proper server-side websocket proxy in the Management UI is T603;
# the full WebRTC live viewer is T804, post-GA.
#
# Note: this tracks exactly one session. If you spawn a new desktop
# session while the forwarder is running, re-run the script (the
# old socat will exit when its target container is reaped).

set -euo pipefail

BIND="127.0.0.1"
PORT=6080
SESSION_ID=""
NETWORK="${HELMDECK_NETWORK:-baas-net}"

while [[ $# -gt 0 ]]; do
	case "$1" in
		--bind)    BIND="$2"; shift 2 ;;
		--port)    PORT="$2"; shift 2 ;;
		--session) SESSION_ID="$2"; shift 2 ;;
		-h|--help) sed -n '2,20p' "$0" | sed 's/^# //;s/^#$//'; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

command -v socat >/dev/null \
	|| { echo "socat not installed (apt-get install -y socat)" >&2; exit 1; }

# find_session — locate a running helmdeck session container in
# desktop mode. Preference order:
#   1. exact match on --session <id> (short or full uuid)
#   2. most-recently-started container with Xvfb in its process list
resolve_session() {
	if [[ -n "$SESSION_ID" ]]; then
		local match
		match=$(docker ps --format '{{.Names}}' \
			| grep -E "helmdeck-session-${SESSION_ID}" | head -1)
		if [[ -z "$match" ]]; then
			echo "no running session matched '$SESSION_ID'" >&2
			return 1
		fi
		echo "$match"
		return 0
	fi
	# Probe each session container for Xvfb. Keep this quick — a few
	# dozen ms per container is fine for an interactive script.
	local picked="" picked_started=""
	for s in $(docker ps --format '{{.Names}}' | grep '^helmdeck-session-'); do
		if docker exec "$s" pgrep -a Xvfb >/dev/null 2>&1; then
			local started
			started=$(docker inspect "$s" --format '{{.State.StartedAt}}')
			if [[ -z "$picked_started" || "$started" > "$picked_started" ]]; then
				picked="$s"
				picked_started="$started"
			fi
		fi
	done
	if [[ -z "$picked" ]]; then
		echo "no desktop-mode session found (need one with Xvfb running)" >&2
		echo "start one via: POST /api/v1/packs/desktop.run_app_and_screenshot" >&2
		return 1
	fi
	echo "$picked"
}

SESSION=$(resolve_session) || exit 1

# Read the session's baas-net IP.
IP=$(docker inspect "$SESSION" \
	--format "{{(index .NetworkSettings.Networks \"$NETWORK\").IPAddress}}")
if [[ -z "$IP" ]]; then
	echo "could not read $NETWORK IP from $SESSION" >&2
	exit 1
fi

echo "forwarding ${BIND}:${PORT} → ${SESSION}:6080 (${IP})"
echo "browse http://${BIND}:${PORT}/vnc.html?autoconnect=true&resize=remote"
echo "Ctrl-C to stop"
echo

# -v on socat prints each connection to stderr so the operator can
# see activity. fork spawns a handler per connection so multiple
# tabs / reconnects don't block each other.
exec socat -v "TCP-LISTEN:${PORT},bind=${BIND},reuseaddr,fork" "TCP:${IP}:6080"
