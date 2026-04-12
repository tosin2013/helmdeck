#!/bin/bash
# helmdeck sidecar entrypoint — selects headless or desktop mode.
#
# Modes:
#   HELMDECK_MODE=headless (default)
#       Launches Chromium with --remote-debugging-port=$CHROMIUM_PORT and
#       binds to 0.0.0.0 so the control plane can reach CDP over the
#       internal bridge network.
#   HELMDECK_MODE=desktop
#       Brings up Xvfb on :99, starts XFCE4, exposes the display via
#       noVNC on :6080, and *also* launches Chromium attached to the
#       virtual display (so vision packs see the same browser the
#       operator views in the live viewer).
#
# Both modes use --no-sandbox because the container runs unprivileged
# (UID 1000 + dropped caps + no-new-privileges). The kernel-level sandbox
# is provided by Docker / gVisor / Firecracker (ADR 011).

set -euo pipefail

CHROMIUM_PORT="${CHROMIUM_PORT:-9222}"
HELMDECK_MODE="${HELMDECK_MODE:-headless}"
PLAYWRIGHT_MCP_PORT="${PLAYWRIGHT_MCP_PORT:-8931}"
HELMDECK_PLAYWRIGHT_MCP_ENABLED="${HELMDECK_PLAYWRIGHT_MCP_ENABLED:-true}"
PLAYWRIGHT_MCP_PID=""

CHROME_FLAGS=(
    --no-sandbox
    --disable-dev-shm-usage   # /dev/shm is sized via runtime.Spec.SHMSize
    --disable-gpu
    --no-first-run
    --no-default-browser-check
    --disable-extensions
    --disable-background-networking
    --disable-sync
    --metrics-recording-only
    --remote-debugging-port="${CHROMIUM_PORT}"
    --remote-allow-origins=*
    --user-data-dir=/home/helmdeck/.config/chromium
)

# Chromium 122+ silently ignores --remote-debugging-address and binds CDP
# to 127.0.0.1 only. We launch socat alongside Chromium, bound to the
# container's primary non-loopback interface, so the control plane can
# reach CDP over the internal Docker network without relaxing the bind
# address inside the browser process.
start_cdp_forwarder() {
    local container_ip
    container_ip="$(ip -4 -o addr show scope global 2>/dev/null | awk 'NR==1 {print $4}' | cut -d/ -f1)"
    if [ -z "${container_ip}" ]; then
        echo "helmdeck-entrypoint: no non-loopback IPv4; skipping CDP forwarder" >&2
        return 0
    fi
    echo "helmdeck-entrypoint: CDP forwarder ${container_ip}:${CHROMIUM_PORT} -> 127.0.0.1:${CHROMIUM_PORT}" >&2
    socat "TCP-LISTEN:${CHROMIUM_PORT},fork,reuseaddr,bind=${container_ip}" \
          "TCP4:127.0.0.1:${CHROMIUM_PORT}" &
}

# Playwright MCP (T807a / ADR 035) — attach to the Chromium process this
# entrypoint just started, via CDP on 127.0.0.1:9222, and expose the MCP
# accessibility-tree surface on 0.0.0.0:${PLAYWRIGHT_MCP_PORT}. The control
# plane reaches this endpoint over the internal baas-net bridge; the MCP
# HTTP server binds 0.0.0.0 itself so we do NOT need a second socat hop.
#
# --cdp-endpoint makes Playwright *attach* instead of launching its own
# browser, so there is exactly one Chromium process in the container —
# the one this entrypoint manages. That keeps RAM flat and keeps cookies,
# storage, and any pack-driven browser state consistent across both the
# chromedp-based packs (`browser.*`) and the Playwright MCP tools.
#
# Fire-and-forget: if Playwright MCP fails to start we log a warning and
# keep the sidecar up, because the existing chromedp packs still work
# without it. Set HELMDECK_PLAYWRIGHT_MCP_ENABLED=false on tiny VMs to
# skip it entirely.
start_playwright_mcp() {
    if [ "${HELMDECK_PLAYWRIGHT_MCP_ENABLED}" != "true" ]; then
        echo "helmdeck-entrypoint: Playwright MCP disabled via HELMDECK_PLAYWRIGHT_MCP_ENABLED" >&2
        return 0
    fi
    if ! command -v npx >/dev/null 2>&1; then
        echo "helmdeck-entrypoint: npx not on PATH; Playwright MCP unavailable" >&2
        return 0
    fi
    echo "helmdeck-entrypoint: starting Playwright MCP on 0.0.0.0:${PLAYWRIGHT_MCP_PORT} attached to CDP 127.0.0.1:${CHROMIUM_PORT}" >&2
    npx --yes @playwright/mcp@latest \
        --cdp-endpoint "http://127.0.0.1:${CHROMIUM_PORT}" \
        --host 0.0.0.0 \
        --port "${PLAYWRIGHT_MCP_PORT}" \
        --headless \
        --no-sandbox \
        >/tmp/playwright-mcp.log 2>&1 &
    PLAYWRIGHT_MCP_PID=$!
}

case "${HELMDECK_MODE}" in
  headless)
    CHROME_FLAGS+=(--headless=new)
    chromium "${CHROME_FLAGS[@]}" >/tmp/chromium.log 2>&1 &
    CHROME_PID=$!
    # Wait for Chromium to bind /json/version on localhost before starting
    # the forwarder, otherwise socat happily forwards to a closed port.
    for _ in $(seq 1 30); do
      if curl -fsS "http://127.0.0.1:${CHROMIUM_PORT}/json/version" >/dev/null 2>&1; then
        break
      fi
      sleep 0.2
    done
    start_cdp_forwarder
    start_playwright_mcp
    trap 'kill -TERM ${CHROME_PID} ${PLAYWRIGHT_MCP_PID:-} 2>/dev/null || true' INT TERM
    wait "${CHROME_PID}"
    ;;

  desktop)
    : "${DISPLAY:=:99}"
    export DISPLAY
    Xvfb "${DISPLAY}" -screen 0 1920x1080x24 -nolisten tcp &
    XVFB_PID=$!

    # Give Xvfb a moment to come up before we start the desktop session.
    for _ in $(seq 1 20); do
      if xdpyinfo -display "${DISPLAY}" >/dev/null 2>&1; then break; fi
      sleep 0.1
    done

    dbus-launch --exit-with-session startxfce4 >/tmp/xfce.log 2>&1 &
    XFCE_PID=$!

    websockify --web=/usr/share/novnc/ 6080 localhost:5900 >/tmp/novnc.log 2>&1 &

    chromium "${CHROME_FLAGS[@]}" >/tmp/chromium.log 2>&1 &
    CHROME_PID=$!
    for _ in $(seq 1 30); do
      if curl -fsS "http://127.0.0.1:${CHROMIUM_PORT}/json/version" >/dev/null 2>&1; then
        break
      fi
      sleep 0.2
    done
    start_cdp_forwarder
    start_playwright_mcp

    # Forward signals so the runtime watchdog gets a clean shutdown.
    trap 'kill -TERM ${CHROME_PID} ${XFCE_PID} ${XVFB_PID} ${PLAYWRIGHT_MCP_PID:-} 2>/dev/null || true' INT TERM
    wait -n
    ;;

  *)
    echo "helmdeck-entrypoint: unknown HELMDECK_MODE='${HELMDECK_MODE}'" >&2
    exit 64
    ;;
esac
