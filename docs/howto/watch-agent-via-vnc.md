---
title: Watch the agent live via noVNC
description: Open a browser tab into a running helmdeck sidecar and watch what the agent sees. Useful for debugging vision packs, verifying desktop packs, and recording agent runs. Covers the three reachability paths (baas-net, port-forward, public base) and the security caveats.
keywords: [helmdeck, noVNC, VNC, sidecar, debugging, vision packs, desktop mode, ADR 028]
---

# Watch the agent live via noVNC

Helmdeck sidecars can run in **desktop mode**, which boots Xvfb + a minimal XFCE4 + noVNC alongside the headless Chromium. You can open a browser tab and watch what the agent is seeing in real time — the cursor moves, windows focus, screenshots get captured, all live.

## When to use this

- **Debugging vision packs** — `vision.click_anywhere` and `vision.fill_form_by_label` are notoriously hard to debug from logs alone (see [#112](https://github.com/tosin2013/helmdeck/issues/112)). Watching the loop live makes it obvious whether the click landed where the model thought it did.
- **Verifying desktop packs** — `desktop.run-app-and-screenshot`, `slides.narrate`, and the visual portions of `podcast.generate` all run inside the desktop session. The VNC view is the fastest way to confirm they work end-to-end.
- **Recording agent runs** — capture a session for training data, demo videos, or incident postmortems. Combine with browser-side screen recording for the simplest path.
- **Confirming the sidecar image is healthy** — if your install fails the smoke test and you don't know why, opening VNC and seeing whether Chromium even boots is the fastest diagnostic.

If you want to verify the *outputs* of an agent run (artifacts, audit log) rather than watch it happen, you don't need VNC — point at `/api/v1/artifacts/<key>` and the **Audit Logs** UI panel.

## Quick start

You need: a running helmdeck stack, a JWT, and a session created in **desktop mode**.

```bash
JWT="<your helmdeck JWT>"

# 1. Create a session in desktop mode (note: HELMDECK_MODE=desktop)
SESSION_ID=$(curl -fsS -X POST http://localhost:3000/api/v1/sessions \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{
    "spec": {
      "image": "ghcr.io/tosin2013/helmdeck-sidecar:0.10.0",
      "env": { "HELMDECK_MODE": "desktop" },
      "wallclock_timeout_s": 600
    }
  }' | jq -r .id)

# 2. Get the noVNC URL
curl -fsS -H "Authorization: Bearer $JWT" \
  "http://localhost:3000/api/v1/desktop/vnc-url?session_id=$SESSION_ID" | jq .
```

Sample response:

```json
{
  "session_id": "abc123…",
  "host": "172.18.0.7",
  "port": "6080",
  "path": "/vnc.html",
  "url": "http://172.18.0.7:6080/vnc.html?autoconnect=true&resize=remote",
  "expires_at": "2026-05-09T15:35:00Z",
  "agent_status": "",
  "notes": "noVNC URL is reachable from inside baas-net only. …"
}
```

That URL is reachable **only from inside the `baas-net` Docker network** by default. Pick one of the three reachability paths below.

## Three reachability paths

### Path 1 — `docker run` from a container on baas-net (zero config, ad-hoc)

If you just want to peek at the running session quickly and you're on the helmdeck host:

```bash
# Open the noVNC URL from inside baas-net using a throwaway curl container.
# Replace <vnc-url> with the url field from the response above.
docker run --rm --network baas-net -it curlimages/curl <vnc-url> -o /dev/null

# To open it in a browser, you need an interactive container instead:
docker run --rm --network baas-net -p 16080:16080 alpine sh -c \
  'apk add --no-cache socat && socat TCP-LISTEN:16080,fork,reuseaddr TCP:172.18.0.7:6080'
# then open http://localhost:16080/vnc.html in your browser

```

Useful for one-off inspection. Not great for sustained operator workflows.

### Path 2 — Port-forward port 6080 to the host (recommended for dev/staging)

Add a port mapping for the sidecar's noVNC port. Since sidecars are spawned dynamically, the cleanest approach is to set a fixed port mapping on the helmdeck-sidecar service in your compose override file:

```yaml
# deploy/compose/compose.override.yaml — created locally, don't commit
services:
  control-plane:
    environment:
      # The sidecar runtime will publish 6080 from each new desktop-mode
      # sidecar to the next free host port (6080, 6081, …) automatically
      # when you set this. Leave unset for the default (baas-net only).
      HELMDECK_PUBLISH_VNC_PORTS: "true"
```

Then `make install` again to pick up the override. The control plane will publish each desktop-mode sidecar's noVNC port to the host. You can hit `http://localhost:6080/vnc.html` from the same machine helmdeck runs on.

If you want a *single fixed* host:port (useful when one sidecar at a time is your common case), set `HELMDECK_VNC_PUBLIC_BASE` instead — see Path 3.

### Path 3 — Set `HELMDECK_VNC_PUBLIC_BASE` (production-shaped)

If you have a reverse proxy (nginx, Caddy, Traefik) in front of helmdeck, terminate TLS there and forward `/vnc/*` to the sidecar IP on port 6080:

```nginx
# /etc/nginx/sites-available/helmdeck-vnc
server {
    listen 443 ssl http2;
    server_name vnc.helmdeck.example.com;

    # Authenticate at the proxy layer — the sidecar itself has no auth.
    auth_basic "helmdeck operator access";
    auth_basic_user_file /etc/nginx/.htpasswd;

    location / {
        # The sidecar IP changes per session. Use the helmdeck API to
        # resolve it dynamically, or pin a specific session for ops use.
        proxy_pass http://172.18.0.7:6080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

Then on the control plane, set:

```bash
# In .env or compose env block
HELMDECK_VNC_PUBLIC_BASE=https://vnc.helmdeck.example.com
```

The `/api/v1/desktop/vnc-url` endpoint will rewrite the host portion of returned URLs to point at your reverse proxy. Operators who get the URL can hit it directly from any browser.

## Agent status overlay

Vision packs (`vision.click_anywhere`, `vision.fill_form_by_label`) post per-step status updates to the control plane:

```bash
# Internal — the pack handler does this for you
curl -X POST http://localhost:3000/api/v1/desktop/agent_status \
  -H "Authorization: Bearer $JWT" \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"abc123","status":"claude-haiku-4.5 · step 3/10 · clicking Sign In"}'
```

The latest status is returned in the `agent_status` field of `/api/v1/desktop/vnc-url`. The Management UI's "View Desktop" tile will overlay this banner on the noVNC viewer (T603, in progress). Until the UI lands, you can poll the endpoint and display the status however you want.

## Security caveats

The noVNC stack inside the sidecar is **deliberately unauthenticated**. `x11vnc -nopw` runs without a password, websockify passes traffic verbatim. This is safe **only** if:

- The sidecar is reachable only from inside `baas-net` (the default), OR
- You've put authentication at the reverse proxy layer (Path 3), OR
- The host is single-tenant and you trust everyone with shell access

**Do not expose port 6080 directly to the internet.** A cron-style "let's just `docker compose port` it to 0.0.0.0:6080" is the wrong shape — that gives anyone who finds the IP full unauthenticated access to the live desktop, including any credentials the agent has typed in.

The 5-minute URL TTL (`expires_at` in the response) is informational, not enforced. The URL itself doesn't carry a signed token — it's the URL of a network-reachable noVNC server. If you forward port 6080, the URL is valid as long as the sidecar lives. Tear down the sidecar (`DELETE /api/v1/sessions/<id>`) when you're done.

## Known limitations and open work

This how-to documents the **v0.x baseline** per [ADR 028 (WebRTC live session streaming)](../adrs/028-webrtc-live-session-streaming.md). The endpoint is intentionally thin — most of the operator-experience improvements live in tracked issues that haven't shipped yet. Be aware of these gaps before you build a workflow on top of the noVNC path:

### Vision packs converge visually but rarely emit `done` (the most likely reason you're here)

If you opened this page to debug `vision.click_anywhere` or `vision.fill_form_by_label` — what you're seeing on the VNC viewer is real and consistent with what the model sees. The loop fix from [#105](https://github.com/tosin2013/helmdeck/pull/105) made screenshots progress between iterations (no more identical bytes) — that part works. The remaining gap is **model-side**: even when the click lands sensibly, vision models like `claude-haiku-4.5` rarely emit `done` on real GUI tasks.

This isn't a sidecar bug, a noVNC config issue, or anything you can fix by tuning the session spec. It's tracked at [#112](https://github.com/tosin2013/helmdeck/issues/112) as an open-research thread, with five spin-out projects ([#115–#119](https://github.com/tosin2013/helmdeck/issues/115)) covering the directions any one of which would close the gap. Both vision packs are flagged as **experimental for production** in v0.10.0; for deterministic browser automation today, prefer [`web.test`](../reference/packs/web/test.md) (Playwright MCP). VNC is still the right tool for *watching* what's happening — just don't expect tuning the noVNC layer to fix vision-pack convergence.

### No in-control-plane proxy (yet)

The `/api/v1/desktop/vnc-url` endpoint hands you a URL pointing at a sidecar IP on baas-net. It doesn't proxy the WebSocket through the control plane, so reachability is operator-managed (the three paths above). The Management UI's "View Desktop" tile (T603, in progress) and the WebRTC replacement (#23) both fix this — until either lands, plan on either Path 2 (port-forward) or Path 3 (reverse proxy) for any sustained operator use.

### No built-in authentication on the noVNC server itself

x11vnc runs with `-nopw` and websockify passes traffic verbatim — by design. Helmdeck assumes the noVNC port is reachable only from inside baas-net, or that you've put authentication at the reverse-proxy layer (Path 3). There's no `HELMDECK_VNC_PASSWORD` env var, and we don't plan to add one — proxy-layer auth is the right shape, and the WebRTC replacement (#23) will use signed per-session tokens instead.

### No audio capture on the noVNC path

Today's noVNC streams the video display only. If a pack plays audio (e.g. `podcast.generate` previews), you won't hear it. Audio capture is part of [#24 (PulseAudio → WebRTC)](https://github.com/tosin2013/helmdeck/issues/24) in Phase 8, alongside the WebRTC replacement.

### Sidecar version drift can break the noVNC layer

If you upgraded the control plane but didn't run `make sidecars` (or if a future upgrade changes the noVNC stack), the desktop layer can break in confusing ways — websockify present, x11vnc missing, that kind of thing. [#109 (sidecar image version pinning)](https://github.com/tosin2013/helmdeck/issues/109) tracks the long-term fix; until then, run `make sidecars` after every helmdeck upgrade as covered in [Upgrade helmdeck](./upgrade-helmdeck.md).

### What's coming — WebRTC live viewer (Phase 8)

The eventual replacement for the entire noVNC path:

- **Proxied through the control plane** — no operator-managed port forwarding required
- **Per-session signed token** — short-lived, JWT-bound to a single session, no broadcast risk
- **WebRTC instead of VNC** — lower latency, hardware acceleration, multi-viewer support
- **Recording built-in** — write the WebRTC stream to S3 alongside the session artifacts
- **Audio included** — see [#24](https://github.com/tosin2013/helmdeck/issues/24)

Tracked at [#23](https://github.com/tosin2013/helmdeck/issues/23). Until that ships, this how-to is the canonical operator path.

## Troubleshooting

### `not_desktop_mode` error from `/api/v1/desktop/vnc-url`

The session was created in headless mode (the default). VNC is only available for desktop-mode sessions. Recreate the session with `"env": { "HELMDECK_MODE": "desktop" }` in the spec.

### URL works from a baas-net container but not from my host browser

Default reachability is baas-net only. Use Path 2 (port-forward) or Path 3 (reverse proxy with `HELMDECK_VNC_PUBLIC_BASE`).

### `vnc.html` loads but the canvas stays blank

The websockify side is up but x11vnc isn't bridged to the Xvfb display. Check the sidecar logs:

```bash
docker logs <sidecar-container-id> | grep -E "x11vnc|websockify|Xvfb"
```

You should see `x11vnc` listening on 5900 and websockify proxying 6080→localhost:5900. If x11vnc isn't running, the sidecar may have been built without the desktop layer (check the sidecar Dockerfile or rebuild with `make sidecars`).

### Cursor moves but clicks don't land where I expect

Common in vision packs — the model emits coordinates in the screenshot's coordinate system, but Xvfb's resolution may differ from what the model assumes. The default Xvfb display is 1280×720; pin a specific resolution by setting `HELMDECK_DISPLAY_GEOMETRY=1920x1080` on session creation if your model expects a larger viewport.

### Multiple operators want to watch the same session

Today's noVNC setup supports it via x11vnc's `-shared` flag (on by default). Multiple browser tabs pointing at the same VNC URL all see the same desktop concurrently. The WebRTC replacement (Phase 8) will support this more cleanly with per-viewer auth.

## Related

- [ADR 028 — WebRTC live session streaming](../adrs/028-webrtc-live-session-streaming.md) — the long-term replacement for noVNC
- [Issue #112](https://github.com/tosin2013/helmdeck/issues/112) — vision-pack convergence research; VNC is the primary debugging tool for this work
- [Architecture overview §4](../reference/architecture.md#4-trust-boundaries) — where VNC fits in helmdeck's trust model
- [Sidecar extending](../SIDECAR-EXTENDING.md) — desktop mode is one of the existing sidecar layers; you can extend it
