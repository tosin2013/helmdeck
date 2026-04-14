// helmdeck → OpenClaw webhook bridge
//
// Run this as a sidecar service in the same docker network as both
// helmdeck-control-plane and openclaw-openclaw-gateway-1. Helmdeck
// POSTs pack results here when an async pack completes; this script
// verifies the HMAC signature, formats the result as a chat message,
// and POSTs it to OpenClaw's chat-injection endpoint so the LLM
// sees it as fresh context on its next turn.
//
// Why this exists: MCP forbids server-initiated sampling, so a true
// "push to LLM" path has to live OUTSIDE MCP. This bridge is the
// minimal piece of glue that makes it work.
//
// Env vars:
//   OPENCLAW_INJECT_URL — chat-injection endpoint on OpenClaw
//                        (default: http://openclaw-openclaw-gateway-1:3210/api/chat/inject)
//   WEBHOOK_SECRET     — shared HMAC secret; MUST match helmdeck's
//                        webhook_secret value
//   PORT               — listen port (default 8080)
//   HELMDECK_BASE_URL  — base URL for artifact links shown to the LLM
//                        (default: http://localhost:3000)
//
// Wire contract for the inbound POST is documented in
// docs/integrations/webhooks.md.

const http = require("http");
const crypto = require("crypto");

const PORT = parseInt(process.env.PORT || "8080", 10);
const SECRET = process.env.WEBHOOK_SECRET || "";
const INJECT_URL = process.env.OPENCLAW_INJECT_URL ||
  "http://openclaw-openclaw-gateway-1:3210/api/chat/inject";
const ARTIFACT_BASE = process.env.HELMDECK_BASE_URL || "http://localhost:3000";

// verifySig recomputes the HMAC and compares in constant time. The
// header format "sha256=<hex>" matches GitHub/Stripe/Slack so this
// reads identically to anyone who has wired up a webhook before.
function verifySig(rawBody, headerSig) {
  if (!SECRET) return true; // signature verification disabled
  if (!headerSig || !headerSig.startsWith("sha256=")) return false;
  const expected = "sha256=" + crypto.createHmac("sha256", SECRET).update(rawBody).digest("hex");
  try {
    return crypto.timingSafeEqual(Buffer.from(headerSig), Buffer.from(expected));
  } catch {
    return false; // length mismatch
  }
}

// formatMessage builds the human-readable summary the LLM will see
// in its chat context. We deliberately keep this plain text — the
// LLM can interpret it without parsing structured JSON, and chat
// UIs render URLs as clickable links automatically.
function formatMessage(payload) {
  const lines = [`[helmdeck] Pack \`${payload.pack}\` ${payload.state}.`];
  if (payload.state === "completed") {
    // Pull the artifact list off the inline tool result; it's a
    // text content block whose text is JSON. Best-effort — if the
    // pack output schema differs, fall back to the raw snippet.
    try {
      const text = payload.result?.content?.[0]?.text || "";
      const parsed = JSON.parse(text);
      const artKeys = ["video_artifact_key", "metadata_artifact_key", "artifact_key"];
      for (const k of artKeys) {
        if (parsed[k]) {
          lines.push(`  ${k}: ${ARTIFACT_BASE}/artifacts/${parsed[k]}`);
        }
      }
      if (parsed.synthesis) lines.push(`  synthesis: ${parsed.synthesis.slice(0, 400)}`);
      if (parsed.grounded_text) lines.push(`  grounded_text length: ${parsed.grounded_text.length} chars`);
    } catch {
      // keep going — receiver shouldn't crash on payload variance
    }
  } else if (payload.state === "failed") {
    const errText = payload.error?.content?.[0]?.text || "(no detail)";
    lines.push(`  error: ${errText.slice(0, 600)}`);
  }
  lines.push(`  job_id: ${payload.job_id}`);
  return lines.join("\n");
}

// injectIntoOpenClaw POSTs the formatted message to OpenClaw's
// chat-injection endpoint. The exact API may vary by OpenClaw
// version — adjust the body shape here if your gateway expects
// a different format.
async function injectIntoOpenClaw(message, payload) {
  const body = JSON.stringify({
    role: "system",
    content: message,
    metadata: {
      source: "helmdeck-webhook",
      pack: payload.pack,
      job_id: payload.job_id,
      task_id: payload.task_id,
    },
  });
  const url = new URL(INJECT_URL);
  return new Promise((resolve, reject) => {
    const req = http.request({
      method: "POST",
      hostname: url.hostname,
      port: url.port,
      path: url.pathname + url.search,
      headers: {
        "Content-Type": "application/json",
        "Content-Length": Buffer.byteLength(body),
      },
    }, (res) => {
      res.resume();
      res.on("end", () => resolve(res.statusCode));
    });
    req.on("error", reject);
    req.write(body);
    req.end();
  });
}

const server = http.createServer((req, res) => {
  if (req.method === "GET" && req.url === "/healthz") {
    res.writeHead(200, { "Content-Type": "text/plain" });
    res.end("ok\n");
    return;
  }
  if (req.method !== "POST") {
    res.writeHead(405).end();
    return;
  }
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", async () => {
    const rawBody = Buffer.concat(chunks);
    const sig = req.headers["x-helmdeck-signature"];
    if (!verifySig(rawBody, sig)) {
      res.writeHead(401).end("invalid signature");
      console.log(`[${new Date().toISOString()}] rejected: invalid signature for ${req.url}`);
      return;
    }
    let payload;
    try {
      payload = JSON.parse(rawBody.toString("utf8"));
    } catch {
      res.writeHead(400).end("invalid json");
      return;
    }
    const message = formatMessage(payload);
    console.log(`[${new Date().toISOString()}] event=${payload.event_type} pack=${payload.pack} job=${payload.job_id}`);
    try {
      const status = await injectIntoOpenClaw(message, payload);
      console.log(`  -> openclaw inject status=${status}`);
      res.writeHead(200).end("ok");
    } catch (err) {
      console.error(`  -> openclaw inject failed: ${err.message}`);
      // Return 5xx so helmdeck retries — better than dropping silently.
      res.writeHead(503).end("downstream failed");
    }
  });
});

server.listen(PORT, () => {
  console.log(`helmdeck-webhook bridge listening on :${PORT}`);
  console.log(`  inject target: ${INJECT_URL}`);
  console.log(`  signature verification: ${SECRET ? "enabled" : "DISABLED (no WEBHOOK_SECRET set)"}`);
});
