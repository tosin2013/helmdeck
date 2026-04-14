# helmdeck → OpenClaw webhook bridge

Tiny Node service (~150 LOC, no dependencies) that accepts helmdeck's outbound webhooks for completed async packs (slides.narrate, research.deep, content.ground) and forwards them into OpenClaw's chat as a system message — so the LLM gets the result as a fresh turn instead of having to poll.

This is the concrete realization of the push-to-LLM pattern documented at [`docs/integrations/webhooks.md`](../../docs/integrations/webhooks.md).

## Why

The MCP spec [forbids server-initiated `sampling/createMessage`](https://modelcontextprotocol.io/specification/2025-06-18/client/sampling), so a true "push from helmdeck triggers the LLM's next turn" pattern can't live inside MCP. This bridge is the missing piece: helmdeck POSTs the result here, this service signs-verifies + reformats + injects into OpenClaw, and the LLM sees a normal-looking system message that triggers its next turn.

## Run with docker-compose

Add to your existing helmdeck docker-compose stack:

```yaml
services:
  helmdeck-callback:
    build: ./examples/webhook-openclaw
    container_name: helmdeck-callback
    environment:
      OPENCLAW_INJECT_URL: http://openclaw-openclaw-gateway-1:3210/api/chat/inject
      WEBHOOK_SECRET: ${HELMDECK_WEBHOOK_SECRET}
      HELMDECK_BASE_URL: http://localhost:3000
    ports:
      - "8080:8080"
    networks:
      - helmdeck_default     # so helmdeck can POST to us
      - openclaw_default     # so we can POST to OpenClaw
    restart: unless-stopped
networks:
  helmdeck_default:
    external: true
  openclaw_default:
    external: true
```

Pick a strong shared secret (must match the `webhook_secret` value the LLM passes to helmdeck):

```bash
echo "HELMDECK_WEBHOOK_SECRET=$(openssl rand -hex 32)" >> .env.local
docker compose up -d helmdeck-callback
```

## Test it end-to-end

In OpenClaw chat, send the LLM a prompt that includes the webhook URL + secret:

```
Render this Marp deck as a narrated video using helmdeck slides narrate.
Set webhook_url to http://helmdeck-callback:8080/done and webhook_secret to <YOUR_SECRET>.
Don't poll — I'll get notified when the result is ready.

---
marp: true
---

# Quarterly Update

<!-- Welcome to the Q1 update. We had a strong quarter. -->

Revenue, headcount, and customer satisfaction are all up.

---

# What's Next

<!-- Looking ahead to Q2, our priorities are shipping the new product line and expanding into Europe. -->

Three priorities for Q2.
```

What you should see in OpenClaw chat:
1. Within ~1s: the LLM reports back something like *"Started slides.narrate, task ID `pack_abc...`. I'll wait for the webhook."*
2. ~60-180s later: a fresh system message appears in chat:
   ```
   [helmdeck] Pack `slides.narrate` completed.
     video_artifact_key: http://localhost:3000/artifacts/slides.narrate/<key>/video.mp4
     metadata_artifact_key: http://localhost:3000/artifacts/slides.narrate/<key>/metadata.json
     job_id: 88182b84209ff78a5569dafdf42c57a9
   ```
3. The LLM responds in its next turn — usually summarizing the artifacts and offering follow-up actions.

## Configuration

| Env var | Default | Notes |
|---|---|---|
| `PORT` | `8080` | Listen port |
| `WEBHOOK_SECRET` | (empty — verification disabled) | Strongly recommend setting this to anything OpenSSL gives you |
| `OPENCLAW_INJECT_URL` | `http://openclaw-openclaw-gateway-1:3210/api/chat/inject` | OpenClaw's chat-injection endpoint; adjust if your container/path differs |
| `HELMDECK_BASE_URL` | `http://localhost:3000` | Used to build clickable artifact links in the injected message |

## Customizing the injected message

The `formatMessage()` function in `server.js` builds the chat text. By default it surfaces:
- pack name + state
- artifact keys with full URLs (video, metadata, grounded.md, etc.)
- `synthesis` field (for `research.deep`)
- `grounded_text` length (for `content.ground`)

If you want richer formatting — e.g. inline base64 thumbnails, structured embeds — modify `formatMessage()` and (optionally) extend the `body` shape sent to OpenClaw's chat-injection endpoint.

## Observability

The service logs every webhook delivery:
```
[2026-04-14T01:32:14Z] event=pack.complete pack=slides.narrate job=88182b84209ff78a5569dafdf42c57a9
  -> openclaw inject status=200
```

Rejected signatures, OpenClaw injection failures (5xx), and inbound parse errors all surface in the same log. `GET /healthz` returns `200 ok` for liveness probes.

## Adapting for non-OpenClaw clients

If your agent runtime isn't OpenClaw, adapt this template:
- `formatMessage()` — formatting is generic, probably keep
- `injectIntoOpenClaw()` — replace with the POST shape your runtime expects (Slack `chat.postMessage`, Discord webhook, A2A `TaskUpdate`, custom HTTP endpoint, etc.)
- `verifySig()` — generic, keep

The wire contract from helmdeck (signature scheme, headers, body schema) is the same regardless of the receiver — only the *outbound* leg changes.
