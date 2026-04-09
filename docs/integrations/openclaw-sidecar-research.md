# OpenClaw sidecar — research brief (in-progress, blocker on T565)

> **Status:** ⚠️ Investigation paused — OpenClaw's `bundle-mcp` consumer
> rejects helmdeck's SSE endpoint with 401 even though every isolated
> test of the same JWT against the same URL succeeds with 200. This
> doc is the handoff for the next debugging session.
>
> See also: [openclaw.md](openclaw.md) for the (unverified) setup
> recipe and [README.md](README.md) for the matrix.

## Goal

Make helmdeck work as an MCP capability sidecar for OpenClaw via the
SSE transport at `/api/v1/mcp/sse` (T302a). When this lands, an
OpenClaw agent's LLM can call helmdeck packs (browser, fs, repo, …)
without baking the `helmdeck-mcp` stdio bridge into the OpenClaw
image. Closes the validation gate (T565) that flips
[openclaw.md](openclaw.md) from 🟡 to ✅.

## What works

| Component | Result |
|---|---|
| Helmdeck SSE handshake via curl from host | ✅ 200, `event: endpoint` frame |
| Same handshake via curl from inside `openclaw-gateway` container | ✅ 200 |
| Native Node `fetch()` with `Authorization` header from gateway container | ✅ 200 |
| `undici.fetch()` directly (the fetch impl OpenClaw uses) | ✅ 200 |
| Direct `SSEClientTransport` (MCP SDK 1.29.0) instantiation with the same JWT, from inside the gateway container | ✅ `STARTED` |
| OpenClaw agent → LLM (`What is 2+2?`) | ✅ `4` |
| OpenClaw `bundle-mcp` → helmdeck SSE production path | ❌ **`SSE error: Non-200 status code (401)`** |

The JWT is valid (admin scope, 12h expiry from mint time). Helmdeck
accepts it on every direct test. OpenClaw's bundle-mcp rejects it.

## Topology

```
host (this dev box, port-forwarded via SSH tunnel from operator workstation)
│
├── helmdeck stack (compose, baas-net)
│     └── helmdeck-control-plane:3000
│           ├── /api/v1/mcp/sse        (T302a — the surface under test)
│           ├── /api/v1/mcp/ws         (T302 — works, not under test)
│           └── /v1/chat/completions   (T201 — works)
│
└── openclaw stack (compose, openclaw_default + manually-attached baas-net)
      ├── openclaw-gateway:18789
      │     ├── env: OPENROUTER_API_KEY=sk-or-v1-…
      │     └── env: OPENCLAW_LOAD_SHELL_ENV=true
      │
      └── openclaw-cli (network_mode: service:openclaw-gateway)
            └── reads ~/.openclaw/openclaw.json + auth-profiles.json
```

## Files in play

### Helmdeck (this repo)

- `internal/api/mcp_sse.go` — the SSE handler under suspicion. JWT
  enforcement is the standard `IsProtectedPath` middleware in
  `internal/api/router.go`. Both the GET (stream open) and POST
  (`/api/v1/mcp/sse/message`) routes are JWT-protected.
- `internal/api/router.go` — `IsProtectedPath` line 56-66.
- `deploy/compose/compose.openclaw-sidecar.yml` — overlay that joins
  `openclaw-gateway` to `baas-net` AND injects
  `OPENROUTER_API_KEY` + `OPENCLAW_LOAD_SHELL_ENV=true`. Watch out:
  every `docker compose run --rm openclaw-cli ...` recreates the
  gateway container WITHOUT the overlay flags unless both `-f`
  arguments are passed every time. Manual workaround:
  `docker network connect baas-net openclaw-openclaw-gateway-1`.

### OpenClaw (containerized at /root/openclaw, NOT in this repo)

- `/app/dist/content-blocks-k-DyCOGS.js` — the bundle-mcp consumer
  that constructs `SSEClientTransport`. Search functions:
  - `resolveMcpTransport` — top-level dispatcher
  - `resolveMcpTransportConfig` — picks stdio vs http vs sse
  - `resolveHttpTransportConfig` — http-side path
  - `resolveHttpMcpServerLaunchConfig` — header parsing from `openclaw.json`
  - `toMcpStringRecord` — preserves header keys as-is, no case normalization
  - `buildSseEventSourceFetch` — wraps `fetchWithUndici` and merges
    user `headers` over the SDK's `init.headers`

- `/app/node_modules/@modelcontextprotocol/sdk/dist/esm/client/sse.js`
  — MCP SDK 1.29.0 SSE client. Key functions:
  - `_commonHeaders()` — only sets `Authorization` from
    `_authProvider.tokens()`. Headers from `requestInit.headers`
    are merged in via `extraHeaders`.
  - `_startOrAuth()` — creates `EventSource` with
    `eventSourceInit.fetch` set to a closure that calls
    `_commonHeaders()` then the user-supplied fetch.

- `/app/node_modules/eventsource/dist/index.cjs` — eventsource@3.0.7.
  Line 127 sets `_fetch` from `eventSourceInitDict.fetch`. Line 206
  calls `__privateGet(this, _fetch)(url, getRequestOptions_fn(...))`.
  The question is what `getRequestOptions_fn` puts in the request
  options' `headers` map.

### OpenClaw config (manually edited, NOT versioned)

- `/root/.openclaw/openclaw.json` — has `mcp.servers.helmdeck` with
  `url` + `headers.Authorization`
- `/root/.openclaw/agents/main/agent/auth-profiles.json` — has
  `profiles["openrouter:helmdeck"]` of type `api_key`

Both must be re-applied after a `--reset` of the OpenClaw stack.

## Research questions, ranked by likelihood

### 1. Does eventsource@3.0.7 honor `eventSourceInit.fetch` AND propagate headers correctly?

The eventsource library `_fetch` is set from `eventSourceInitDict.fetch`,
and `getRequestOptions_fn` builds the request options. If the library's
`getRequestOptions_fn` populates `headers` (e.g. with `Accept: text/event-stream`,
`Cache-Control: no-cache`, `Last-Event-ID`), then OpenClaw's
`buildSseEventSourceFetch` does:

```js
return fetchWithUndici(url, {
    ...init,                                  // includes library headers
    headers: { ...sdkHeaders, ...headers }    // user headers win on conflict
});
```

That should produce a request with `Authorization` set. **But maybe it
doesn't.** Patch `content-blocks-k-DyCOGS.js` in the running container
to log the actual `merged headers` map and re-run an OpenClaw agent
prompt. Compare against the direct `SSEClientTransport` test in this
brief that returned 200.

```bash
# Patch live in container; revert by rebuilding
docker exec openclaw-openclaw-gateway-1 sh -c '
sed -i "s/fetchWithUndici(url, {/console.error(\"SSE-FETCH\", url.toString(), JSON.stringify({...sdkHeaders, ...headers})); fetchWithUndici(url, {/" /app/dist/content-blocks-k-DyCOGS.js
'
docker compose -f /root/openclaw/docker-compose.yml -f /root/helmdeck/deploy/compose/compose.openclaw-sidecar.yml restart openclaw-gateway
# trigger an agent run, then:
docker logs openclaw-openclaw-gateway-1 2>&1 | grep SSE-FETCH
```

### 2. Is the 401 actually on the POST `/message` endpoint, not the GET stream?

The MCP SDK SSE client's `send()` makes a POST to the paired
`/api/v1/mcp/sse/message?sessionId=…` endpoint with the same
`_commonHeaders`. If the helmdeck-side handler validates JWT on the
GET handshake but not on the POST, OR vice-versa, the message round
trip could fail with 401 even though the GET succeeded.

Check helmdeck logs for the request order:

```bash
docker logs helmdeck-control-plane 2>&1 | grep -E "mcp/sse|message"
```

If the GET shows 200 but the POST shows 401, the bug is in the
POST handler's auth path or in OpenClaw's POST header propagation.

Also instrument helmdeck to log the `Authorization` header on both
endpoints (temporarily, in `internal/api/mcp_sse.go`) so we can see
exactly what reaches the server side.

### 3. Clock skew between containers?

JWT validation checks `nbf` and `exp`. If the helmdeck container
clock drifted, validation fails.

```bash
docker exec helmdeck-control-plane date -u
docker exec openclaw-openclaw-gateway-1 date -u
```

Should be within 30s.

### 4. Header-case quirk in undici → helmdeck?

Go `http.Header` is case-insensitive per RFC, so this *shouldn't*
matter. But it's cheap to verify by adding a debug print of the raw
header map at the SSE handler entry point.

### 5. Bundle-mcp wrapper around `SSEClientTransport`?

There may be code between `resolveMcpTransport` and the SDK that
mutates the transport options. Search for all callers of
`resolveMcpTransport` in `/app/dist/*.js` and check what they do
with the returned `transport` object before connecting.

```bash
docker exec openclaw-openclaw-gateway-1 sh -c '
grep -rn "resolveMcpTransport\|\.transport\s*\\.\\(start\\|connect\\)" /app/dist/*.js | head -20
'
```

### 6. Is OpenClaw reading a different `openclaw.json` at runtime?

`openclaw mcp show helmdeck` returns the right entry from
`~/.openclaw/openclaw.json`. But bundle-mcp may read from a snapshot,
agent-scoped store, or in-memory cache that hasn't been refreshed.
Patch `resolveMcpTransport` to log `rawServer` to confirm bundle-mcp
sees the headers we wrote.

## Direct repro steps

After `ssh -L 18789:localhost:18789 -L 3000:localhost:3000 root@<host>`:

```bash
# 1. Confirm both stacks are up
curl -s localhost:3000/healthz; curl -s localhost:18789/healthz

# 2. Make sure openclaw-gateway is on baas-net (recreates lose this)
docker network connect baas-net openclaw-openclaw-gateway-1 2>/dev/null || true
docker inspect openclaw-openclaw-gateway-1 --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'
# expect: baas-net openclaw_default

# 3. Mint a fresh helmdeck JWT
JWT=$(curl -s -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"<from .env.local>\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

# 4. Update OpenClaw's helmdeck MCP server entry with the new JWT
docker compose -f /root/openclaw/docker-compose.yml run --rm openclaw-cli \
  mcp set helmdeck "{\"url\":\"http://helmdeck-control-plane:3000/api/v1/mcp/sse\",\"headers\":{\"Authorization\":\"Bearer $JWT\"}}"

# Recreating the gateway via compose run drops baas-net — reattach:
docker network connect baas-net openclaw-openclaw-gateway-1 2>/dev/null || true

# 5. Confirm direct curl works (sanity check)
docker exec openclaw-openclaw-gateway-1 curl -sN --max-time 2 \
  -H "Authorization: Bearer $JWT" \
  http://helmdeck-control-plane:3000/api/v1/mcp/sse | head -3
# expect: event: endpoint / data: /api/v1/mcp/sse/message?sessionId=...

# 6. Confirm direct SSEClientTransport works
docker exec openclaw-openclaw-gateway-1 node -e "
const { SSEClientTransport } = require('/app/node_modules/@modelcontextprotocol/sdk/dist/cjs/client/sse.js');
const url = new URL('http://helmdeck-control-plane:3000/api/v1/mcp/sse');
const headers = { Authorization: 'Bearer $JWT' };
const t = new SSEClientTransport(url, {
    requestInit: { headers },
    eventSourceInit: { fetch: async (u, init) => {
        return await fetch(u, { ...init, headers: { ...(init?.headers||{}), ...headers } });
    } }
});
t.onerror = (e) => { console.error('ERROR', e.message); process.exit(1); };
(async () => { await t.start(); console.error('STARTED'); setTimeout(()=>process.exit(0), 1500); })();
"
# expect: STARTED

# 7. Trigger the failing path — OpenClaw agent
docker exec openclaw-openclaw-gateway-1 node /app/dist/index.js agent \
  --message "List the tools available from the helmdeck MCP server." \
  --to "+10000000001"
# observed: bundle-mcp logs "SSE error: Non-200 status code (401)"
```

## What's wired today (committed)

- `2929c6b` T202a — keystore→gateway hydration + OpenRouter env-var fast path
- `99bde7f` T302a — SSE MCP transport at `/api/v1/mcp/sse` (the thing under test)
- `b33b9f9` D3a — sidecar reframe of all six client integration docs
- `975e2e8` D3a fix — corrected OpenClaw schema (`mcp.servers` is top-level)
- (this commit) — adds `OPENCLAW_LOAD_SHELL_ENV=true` to the sidecar
  overlay AND lands this research brief
