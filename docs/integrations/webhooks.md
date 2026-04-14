# Helmdeck Webhooks — Push Pack Results to Your Agent Gateway

When a long-running pack (`slides.narrate`, `research.deep`, `content.ground`, etc.) finishes, helmdeck can POST the result to a URL you supply instead of making the LLM poll. The receiver (a tiny gateway service you run) then re-injects the result into your agent's chat as a fresh user/system message — which triggers a new LLM turn.

This is the only way to get true **push-to-LLM** semantics in helmdeck today. The MCP spec [forbids server-initiated `sampling/createMessage`](https://modelcontextprotocol.io/specification/2025-06-18/client/sampling), so push has to live outside MCP. Webhooks are how you bridge it back.

For a working OpenClaw-specific receiver, see [`examples/webhook-openclaw/`](../../examples/webhook-openclaw/) and the [OpenClaw integration walkthrough](./openclaw.md#webhook-callback).

## When to use webhooks vs polling

| You want… | Use |
|---|---|
| The simplest path; client SDK handles everything | **Don't supply a webhook.** Heavy packs return a SEP-1686 task envelope; SDK polls `tasks/get` under the hood |
| The LLM never sees -32001, AND your client SDK doesn't speak SEP-1686 yet | **Webhooks.** Your gateway re-injects the result as a chat message |
| Hours-long jobs (book-writing, large batch ops) | **Webhooks.** Polling at 5-second intervals adds up; one HTTP POST is cheaper |
| You're building a Slack/Discord/email bot off helmdeck | **Webhooks.** This pattern is identical to how chat-bot integrations work |

You can ALWAYS fall back to polling `tasks/get` (or `pack.status`/`pack.result`) even when a webhook is configured — the webhook is additive, not exclusive.

## Wire contract

### Configuring the webhook

Pass `webhook_url` (and optionally `webhook_secret`) in the input arguments of any async-marked pack call. Two equivalent ways:

**Option A — through `pack.start` (works on every MCP client today):**

```json
{
  "name": "pack.start",
  "arguments": {
    "pack": "slides.narrate",
    "input": { "markdown": "---\nmarp: true\n---\n# Hello", "metadata_model": "openrouter/auto" },
    "webhook_url": "https://my-gateway.example.com/helmdeck-callback",
    "webhook_secret": "any-shared-secret-you-pick"
  }
}
```

**Option B — directly on a heavy pack call (when the client speaks the SEP-1686 task envelope):**

```json
{
  "name": "slides.narrate",
  "arguments": {
    "markdown": "---\nmarp: true\n---\n# Hello",
    "metadata_model": "openrouter/auto",
    "webhook_url": "https://my-gateway.example.com/helmdeck-callback",
    "webhook_secret": "any-shared-secret-you-pick"
  }
}
```

In Option B, helmdeck strips `webhook_url` and `webhook_secret` from the input before passing it to the pack handler — your pack never sees the secret.

### Request helmdeck sends to your URL

When the job reaches a terminal state (`completed` or `failed`), helmdeck sends:

```http
POST /your/path HTTP/1.1
Host: my-gateway.example.com
Content-Type: application/json
User-Agent: helmdeck-webhook/1.0
X-Helmdeck-Event: pack.complete
X-Helmdeck-Job-Id: 88182b84209ff78a5569dafdf42c57a9
X-Helmdeck-Task-Id: pack_88182b84209ff78a5569dafdf42c57a9
X-Helmdeck-Delivery-Attempt: 1
X-Helmdeck-Signature: sha256=2c1ee49e8f0a...
Content-Length: 1234

{
  "event_type": "pack.complete",
  "job_id": "88182b84209ff78a5569dafdf42c57a9",
  "task_id": "pack_88182b84209ff78a5569dafdf42c57a9",
  "pack": "slides.narrate",
  "state": "completed",
  "started_at": "2026-04-14T01:30:00Z",
  "ended_at": "2026-04-14T01:32:14Z",
  "result": {
    "content": [
      { "type": "text", "text": "{\"video_artifact_key\":\"slides.narrate/uuid/video.mp4\", ...}" }
    ],
    "isError": false
  },
  "snapshot": {
    "job_id": "88182b84209ff78a5569dafdf42c57a9",
    "pack": "slides.narrate",
    "state": "completed",
    "progress": 100,
    "started_at": "2026-04-14T01:30:00Z",
    "ended_at": "2026-04-14T01:32:14Z"
  }
}
```

On failure, `event_type` is `pack.failed`, `state` is `failed`, and an `error` object replaces `result`:

```json
{
  "event_type": "pack.failed",
  "state": "failed",
  "error": {
    "content": [{ "type": "text", "text": "{\"error\":\"handler_failed\",\"message\":\"ffmpeg exit 1: ...\"}" }],
    "isError": true
  }
}
```

### Headers reference

| Header | Meaning |
|---|---|
| `X-Helmdeck-Event` | `pack.complete` or `pack.failed` |
| `X-Helmdeck-Job-Id` | Internal job identifier (raw hex) |
| `X-Helmdeck-Task-Id` | SEP-1686 task identifier (`pack_<hex>`); same identity, different format |
| `X-Helmdeck-Delivery-Attempt` | `1`, `2`, or `3` — useful for receiver-side dedupe |
| `X-Helmdeck-Signature` | `sha256=<hex>` HMAC-SHA256 of the raw body, keyed by `webhook_secret`; absent when no secret was configured |

### Verifying the signature

The signature scheme matches GitHub / Stripe / Slack. In Node:

```js
const crypto = require("crypto");
function verify(rawBody, headerSig, secret) {
  const expected = "sha256=" + crypto.createHmac("sha256", secret).update(rawBody).digest("hex");
  return crypto.timingSafeEqual(Buffer.from(headerSig), Buffer.from(expected));
}
```

In Python:

```python
import hmac, hashlib
def verify(raw_body: bytes, header_sig: str, secret: str) -> bool:
    expected = "sha256=" + hmac.new(secret.encode(), raw_body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(header_sig, expected)
```

In Go:

```go
import (
  "crypto/hmac"; "crypto/sha256"; "encoding/hex"
)
func verify(rawBody []byte, headerSig, secret string) bool {
  mac := hmac.New(sha256.New, []byte(secret))
  mac.Write(rawBody)
  expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
  return hmac.Equal([]byte(headerSig), []byte(expected))
}
```

**Always use a constant-time comparison** (`crypto.timingSafeEqual` / `hmac.compare_digest` / `hmac.Equal`) to avoid timing attacks.

### Retry policy

Helmdeck retries up to 3 times: immediately, then after 5s, then after 30s. Total worst-case window: ~35 seconds.

- **2xx response** → success, no further retries
- **4xx response** → permanent client error, no retries (use this when you intentionally want to drop a delivery, e.g. signature failed)
- **5xx response or network error** → transient, retry per the schedule
- **Receiver timeout** → 10 seconds per attempt; respond fast and process asynchronously if you need to do real work

After the third attempt fails, the delivery is dropped silently. The job result is still available via `tasks/get` / `pack.result` for one hour, so receivers can poll as a fallback if they detect a missed webhook.

## Receiver patterns

### Inject as a chat message (most common)

The receiver takes the helmdeck payload, summarizes the pack output (or just dumps the artifact URLs), and POSTs to the agent's chat-injection endpoint as a fake user or system message. The agent's next LLM turn picks it up naturally:

```
[helmdeck] Pack slides.narrate completed.
Video: http://localhost:3000/artifacts/slides.narrate/<key>/video.mp4
Metadata: http://localhost:3000/artifacts/slides.narrate/<key>/metadata.json
```

### Forward to A2A protocol

If your agent runtime speaks [A2A](https://a2a-protocol.org/), the helmdeck payload maps cleanly onto an A2A `TaskUpdate` message — `task_id` becomes the A2A task identifier, `result` becomes the A2A response artifact.

### Pipe into a queue

Put the payload on Redis/SQS/Kafka and let downstream consumers handle it asynchronously. Useful when the same job result needs to fan out to multiple consumers (chat + audit log + Slack notification).

## Related

- [SEP-1686 Tasks](https://modelcontextprotocol.io/community/seps/1686-tasks) — the upstream MCP spec for long-running calls; helmdeck implements it via `tasks/get`
- [`examples/webhook-openclaw/`](../../examples/webhook-openclaw/) — concrete OpenClaw receiver
- [`docs/integrations/openclaw.md`](./openclaw.md#webhook-callback) — full OpenClaw walkthrough
- [Pack composition guide](./SKILLS.md#async-wrappers-for-long-running-packs) — how the LLM should choose between sync and async paths
