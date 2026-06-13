---
description: "Paste-ready GitHub issue draft for `openclaw/openclaw` documenting the SSE-transport 401 bug. File at github.com/openclaw/openclaw/issues/new with bug+mcp+sse labels."
---

# Draft GitHub issue for openclaw/openclaw

> **Instructions:** This is a paste-ready issue draft. File at
> https://github.com/openclaw/openclaw/issues/new — title goes in the
> title field, body goes in the body field. Tag with `bug`, `mcp`,
> `sse-transport` if those labels exist.

---

## Title

`bundle-mcp` SSE transport: case-distinct duplicate `authorization` headers cause 401 against compliant MCP servers

## Body

### Summary

When OpenClaw connects to a remote MCP server via the SSE transport, the
helper `buildSseEventSourceFetch` in `src/.../content-blocks-k-DyCOGS.js`
merges user-supplied `headers` over the SDK-supplied headers using a plain
JavaScript object spread:

```js
return fetchWithUndici(url, {
  ...init,
  headers: { ...sdkHeaders, ...headers }
});
```

`sdkHeaders` is built by iterating a `Headers` instance returned by
`SSEClientTransport._commonHeaders()`. Per the Fetch / WHATWG spec, iterating
a `Headers` instance yields **lowercase** keys, so `sdkHeaders` ends up with
`authorization` as the key.

If the user's `mcp.servers[name].headers` config uses the conventional capital-A
spelling `Authorization`, the spread produces an object with **two distinct
keys** that point to the same bearer value:

```js
{
  accept: "text/event-stream",
  authorization: "Bearer eyJ...",   // from sdkHeaders (lowercased)
  Authorization: "Bearer eyJ..."    // from user config (capital)
}
```

When this plain object is then passed to `undici.fetch()` as a `HeadersInit`,
undici constructs a `Headers` list using `append` semantics — and per the Fetch
spec, `Headers.append` **comma-joins** duplicate values rather than replacing
them. The wire ends up looking like:

```
Authorization: Bearer eyJ..., Bearer eyJ...
```

Standards-compliant bearer-token parsers (including Go's
`net/http` middleware ecosystem) reject this as malformed and return 401, even
though the underlying token is valid.

### Reproduction

1. Run any MCP server that requires `Authorization: Bearer <jwt>` (we hit this
   against our own server `helmdeck` at `/api/v1/mcp/sse`, but any compliant
   server should reproduce — Heptabase, Linear, etc).
2. Register it in OpenClaw with the **conventional capital-A header key**:
   ```bash
   openclaw mcp set example '{
     "url": "https://example.com/mcp/sse",
     "headers": {"Authorization": "Bearer <jwt>"}
   }'
   ```
3. Drive an agent prompt that requires the MCP server. OpenClaw logs:
   ```
   [bundle-mcp] failed to start server "example": Error: SSE error: Non-200 status code (401)
   ```
4. Server-side logs show a single GET to the SSE endpoint with status 401,
   despite the bearer being correct.

### Root cause

`buildSseEventSourceFetch` in `content-blocks-k-DyCOGS.js`:

```js
function buildSseEventSourceFetch(headers) {
  return (url, init) => {
    const sdkHeaders = {};
    if (init?.headers) {
      if (init.headers instanceof Headers) {
        init.headers.forEach((value, key) => { sdkHeaders[key] = value; });
        // ↑ key is always lowercase per Headers spec
      } else {
        Object.assign(sdkHeaders, init.headers);
      }
    }
    return fetchWithUndici(url, {
      ...init,
      headers: { ...sdkHeaders, ...headers }   // ← case-distinct duplicates survive
    });
  };
}
```

Plain JS objects are case-sensitive on keys; `Headers` is case-insensitive.
Mixing them via spread without normalization preserves both spellings, then
delegates the dedup decision to undici, which (correctly per spec) appends
rather than replaces.

### Workaround

Use **lowercase** `authorization` as the key in the user config:

```bash
openclaw mcp set example '{
  "url": "https://example.com/mcp/sse",
  "headers": {"authorization": "Bearer <jwt>"}
}'
```

This makes the spread collapse to a single `authorization` entry. Confirmed
working against helmdeck v0.6.0 — `tools/list` succeeds and returns the full
catalog.

### Proposed fix

Construct a `Headers` instance and use `.set()` (case-insensitive replace)
instead of plain-object spread:

```js
function buildSseEventSourceFetch(headers) {
  return (url, init) => {
    const merged = new Headers(init?.headers ?? {});
    for (const [k, v] of Object.entries(headers)) merged.set(k, v);
    return fetchWithUndici(url, { ...init, headers: merged });
  };
}
```

`Headers.set()` is case-insensitive and replaces, so this works regardless of
whether the user wrote `Authorization`, `authorization`, or `AUTHORIZATION`.
Same fix should apply to any other place in `bundle-mcp` that merges headers
this way (e.g. the streamable-http path if it has the same pattern).

### Versions

- OpenClaw: `2026.4.10` (CLI banner version, image tag `openclaw:local` built
  via `./scripts/docker/setup.sh`)
- `@modelcontextprotocol/sdk`: `1.29.0` (path:
  `/app/node_modules/@modelcontextprotocol/sdk`)
- `eventsource`: `3.0.7` (path: `/app/node_modules/eventsource`)
- `undici`: bundled at whatever version Node 24 ships
- Node: `24.x` (per OpenClaw's image)

### Related upstream issue

The MCP TypeScript SDK has a separate but related bug that drops
`requestInit.headers` from the SSE GET handshake — see
[modelcontextprotocol/typescript-sdk#436](https://github.com/modelcontextprotocol/typescript-sdk/issues/436).
OpenClaw's `buildSseEventSourceFetch` is the documented workaround for #436;
this issue is a bug *in* the workaround.

### Attribution

Discovered while validating helmdeck (https://github.com/tosin2013/helmdeck)
as a sidecar MCP server for OpenClaw. Full investigation log:
https://github.com/tosin2013/helmdeck/blob/main/docs/integrations/openclaw-sidecar-research.md
