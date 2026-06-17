---
title: content.ground
description: Extract claims from markdown and append source citations after each one. Two modes — text in / patched text out, or session-clone file → patched file in place. The grounding pack.
keywords: [helmdeck, content, ground, citations, claims, firecrawl, MCP]
---

# `content.ground`

The "ground these claims with sources" pack. Caller supplies markdown — either inline as `text` or by reference to a file in a session clone (`clone_path` + `path`). The pack:

1. Asks an LLM to extract up to `max_claims` high-impact claims (with strict JSON schema; claims must be exact substrings of the source text).
2. For each claim, runs Firecrawl `/v1/search` and picks the first non-empty URL.
3. Appends ` [source](url)` after each grounded claim, in place.
4. Returns the patched text (or writes back the file in clone mode).

The "claims must be exact substrings" rule is load-bearing: it prevents the model from drifting between "what was claimed" and "what got cited," which is the most common failure mode in two-context-window grounding.

This pack exists as one tool instead of an agent-orchestrated `research.deep` + `fs.patch` chain because (a) the claim text must match the source file exactly, which is fragile across two LLM context windows, (b) one file write per run reduces session-executor RPC overhead, and (c) a strict JSON schema keeps every caller consistent.

## Setup prerequisite

Needs the Firecrawl overlay (same toggle as [`research.deep`](../research/deep.md) and [`web.scrape`](../web/scrape.md)):

```bash
HELMDECK_FIRECRAWL_ENABLED=true
```

If the env var is unset the pack fails fast with `invalid_input`. If the env var is set but the `firecrawl` service is unreachable (every search call errors at the transport layer), the pack fails with `handler_failed` and a message pointing at the service URL — rather than silently returning "no sources found." Partial successes are preserved: when Firecrawl is healthy and some queries genuinely return zero results, those claims show up under `skipped` and the run still completes.

## Inputs

Two input modes — supply **either** `text` (in-memory) **or** `clone_path` + `path` (session-file mode), not both.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `text` | `string` | one of | — | Markdown to ground inline. The patched markdown comes back in the response; nothing is written to disk. **Use this when the user provides markdown in chat.** |
| `clone_path` | `string` | one of | — | Session clone root. Required if `path` is set. |
| `path` | `string` | with `clone_path` | — | Relative markdown file path inside the clone (e.g. `posts/2026-quantum.md`). The pack patches it in place. |
| `model` | `string` | no | resolves via `defaultPackModel()` | Provider/model for claim extraction. Strict JSON-schema output; needs a tool-capable model. |
| `max_claims` | `number` | no | `5` | Explicit cap on claims to ground. Hard cap at 8 (Firecrawl per-call cost). **Wins over `length_intent`** when both are set; back-compat preserved. |
| `topic` | `string` | no | — | Hint for the claim extractor. e.g. `"quantum computing"` narrows extraction to topic-relevant claims and biases the search step. |
| `rewrite` | `boolean` | no | `false` | When `true`, the LLM also rewrites weak claims into stronger prose backed by the discovered source. More expensive (multiple LLM passes); use when "make this blog post more credible" is the goal. |
| `max_completion_tokens` | `number` | no | `2048` | Cap on the claim-extractor LLM's completion. Raise when running against a verbose weak model or a long post — JSON truncation surfaces as an unparseable-JSON handler error. Hard upper bound: `8192`. |
| `length_intent` | `string` | no | — | JIT length sizing (issue [#531](https://github.com/tosin2013/helmdeck/issues/531)) — one of `summary` / `thorough` / `exhaustive`. Maps directly to `max_claims` (3 / 5 / 8). Honored only when `max_claims` is unset; **strictly back-compat** — thorough's value matches the legacy default and exhaustive's matches the legacy ceiling. |
| `inspect` | `boolean` | no | `false` | When `true`, pack returns the resolved `max_claims` + intent without firing Firecrawl or the LLMs. Also skips the `HELMDECK_FIRECRAWL_ENABLED` gate so agents can plan in environments where the overlay isn't wired. |
| `_session_id` | `string` | yes (file mode) | — | Required when `clone_path` is set; not used in text mode. |

### Length intent → max_claims

`content.ground` is **cost-cap shaped**: each claim costs a Firecrawl `/v1/search` call + a per-source LLM verify call. Intent maps directly to the existing `max_claims` input:

| Intent | `max_claims` value |
|---|---|
| `summary` | 3 |
| `thorough` | 5 (matches legacy default) |
| `exhaustive` | 8 (matches legacy ceiling) |

**Precedence**: `inspect:true` short-circuit → explicit `max_claims` (`"explicit"`, clamped to `[1, 8]`) → `length_intent` (`"intent:*"`) → legacy default 5 (`"default"`). Existing callers passing `max_claims` see **ZERO behavior change** — the resolver's clamping matches the inline check that used to live in the handler.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `path` | `string` | Echo (only in file mode). |
| `claims_considered` | `number` | Claims the LLM extracted (≤ `max_claims`). |
| `claims_grounded` | `number` | Of those, how many had a source found via search. |
| `grounding` | `array` | `[{claim: "<exact substring>", url, title}]` for every grounded claim. |
| `skipped` | `array` | Claims with no usable source. The agent can decide whether to soften them or remove them. |
| `grounded_text` | `string` | The grounded markdown (citations inserted; rewritten prose when `rewrite: true`). **Always present** — equal to the input when no claims were grounded — so a pipeline step can wire `${{ steps.<id>.output.grounded_text }}` without the reference ever failing. |
| `sha256` | `string` | Hex sha256 of the patched content. |
| `artifact_key` | `string` | (File mode, when the file changed.) Key of the uploaded grounded-markdown artifact. |
| `file_changed` | `boolean` | (File mode only.) `false` when no claims were grounded → file untouched. |
| `max_claims_applied` | `number` | The cap actually used (after precedence + clamping). |
| `length_intent_applied` | `string` | Where the cap came from — `intent:summary` / `intent:thorough` / `intent:exhaustive` / `explicit` / `default`. |
| `truncated` | `boolean` | `true` when EITHER the claim extractor LLM hit `finish_reason=length` (the claim list may be incomplete) OR the rewrite step truncated (the citation-only fallback ships instead). Re-run with smaller `length_intent` or larger `max_completion_tokens`. |
| `claims_cached` | `number` | Per-claim cache hits (issue [#523](https://github.com/tosin2013/helmdeck/issues/523)). Cached claims skip BOTH the Firecrawl `/v1/search` call AND the verify LLM call entirely. Operators see "fixed a typo, re-ran, 4 of 5 claims served from cache" telemetry. Zero when no engine `MemoryStore` is wired. |
| `firecrawl_calls` | `number` | Actual Firecrawl `/v1/search` calls fired in this run (excludes cache hits). Distinguishes "0 of 5 claims hit Firecrawl on re-run after a typo fix" from "all 5 claims hit Firecrawl on the cold path." |

### Two-layer cache (engine + per-claim)

content.ground runs under two cache layers:

| Layer | Key | TTL | Hits | Misses |
|---|---|---|---|---|
| **Engine `MemoryConfig`** (ADR 047, PR #522) | sha256(caller + input bytes) | 24h | Identical re-run (same markdown, same options) replays the entire pack output | Any byte-level change to the markdown |
| **Handler-internal per-claim** ([#523](https://github.com/tosin2013/helmdeck/issues/523), this release) | sha256(claim_text + "\0" + search_query) | 7 days | Each claim whose text + query is unchanged across edits replays its Firecrawl + verify result | Claim text or query changed between runs |

The engine cache is the fastest path (no handler execution); the per-claim cache is the second-fastest. Together they cover the two common workflows:

- **Idempotent re-run** (same input): engine cache hits → pack output replays in ~milliseconds
- **Typo fix and re-cite** (different input bytes, same claim set): engine cache misses, per-claim cache hits → only the LLM extractor runs; every claim skips Firecrawl + verify

The per-claim cache stores even empty picks (claims for which no source was found). Failed Firecrawl searches (transport errors) are NOT cached — a transient outage shouldn't poison the cache for 7 days.
| `inspect` | `boolean` | Inspect-mode only — always `true` in the inspect response. |
| `suggested_max_claims` | `number` | Inspect-mode only — what the pack would pick. |
| `reason` | `string` | Inspect-mode only — human-readable explanation. |

## Vault credentials needed

**None.** LLM provider key resolved through the *AI Providers* panel.

## Operator env var: tunable Phase 2 concurrency

`HELMDECK_CONTENT_GROUND_CONCURRENCY` overrides the per-call cap on how many claims the pack Firecrawl-searches + LLM-verifies in parallel (issue [#524](https://github.com/tosin2013/helmdeck/issues/524)). Default `4`, range `[1, 32]`. Out-of-range or non-numeric values silently fall back to the default (no panic, no error — an operator typo can't break grounding).

| When to lower | When to raise |
|---|---|
| Free-tier Firecrawl (~10 concurrent `/v1/search` ceiling); shared LLM gateway with strict TPM limits; cost-sensitive | Self-hosted Firecrawl with relaxed rate limits; dedicated LLM gateway; want faster wall-clock on long posts (12+ claims) |

Set on the control-plane container:

```bash
HELMDECK_CONTENT_GROUND_CONCURRENCY=8
```

Re-read on every handler entry — restart not required after updating the env.

## Use it from your agent (OpenClaw chat-UI worked example)

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Use helmdeck__content-ground in text mode with text="WebAssembly delivers near-native performance and runs in every modern browser. Rust is the most-loved language six years running on Stack Overflow surveys.", model=openrouter/openai/gpt-oss-120b, max_claims=2, topic="web platform". Tell me how many claims were grounded vs skipped, and the URLs that backed each grounded claim.

**Tool call** (17 calls, no failures):

```json
{
  "name": "helmdeck__content-ground",
  "arguments": {
    "text": "WebAssembly delivers near-native performance and runs in every modern browser. Rust is the most-loved language six years running on Stack Overflow surveys.",
    "model": "openrouter/openai/gpt-oss-120b",
    "max_claims": 2,
    "topic": "web platform"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":0,"started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":10,"message":"extracting claims","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":10,"message":"extracting claims","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":20,"message":"grounding claim 1/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":20,"message":"grounding claim 1/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-result",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"error":"not_ready","message":"pack.result: job 843b8d162737d4b020c204a2b9cb959c still working — keep polling pack.status"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"working","progress":50,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d1627374b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"error":"unknown_job","message":"pack.status: job_id not found"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"843b8d162737d4b020c204a2b9cb959c","pack":"content.ground","state":"completed","progress":100,"message":"grounding claim 2/2","started_at":"2026-05-08T15:24:22Z","ended_at":"2026-05-08T15:25:07Z"}
```

```json
{
  "name": "helmdeck__pack-result",
  "arguments": {
    "job_id": "843b8d162737d4b020c204a2b9cb959c"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"content.ground","version":"v1","output":{"artifact_key":"content.ground/db6c17a1d7c112f1-grounded.md","claims_considered":2,"claims_grounded":1,"file_changed":true,"grounded_text":"WebAssembly delivers near-native performance and runs in every modern browser. Rust is the most-loved language six years running on Stack Overflow surveys. [source](https://stackoverflow.blog/2025/01/01/developers-want-more-more-more-the-2024-results-from-stack-overflow-s-annual-developer-survey/)","grounding":[{"claim":"Rust is the most-loved language six years running on Stack Overflow surveys.","url":"https://stackoverflow.blog/2025/01/01/developers-want-more-more-more-the-2024-results-from-stack-overflow-s-annual-developer-survey/","title":"Developers want more, more, more: the 2024 results from Sta
  ...[truncated]
```

**Agent's final reply**:

> The grounding run considered **2 claims** and **grounded 1** of them.
> 
> **Grounded claim & source**
> - **Claim:** “Rust is the most‑loved language six years running on Stack Overflow surveys.”  
>   **URL:** https://stackoverflow.blog/2025/01/01/developers-want-more-more-more-the-2024-results-from-stack-overflow-s-annual-developer-survey/
> 
> The other claim (“WebAssembly delivers near‑native performance and runs in every modern browser.”) was **skipped** because no supporting source was found.

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0122).*

## Developer reference (`curl`)

### Text mode (no session)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/content.ground \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "text":       "WebAssembly delivers near-native performance and runs in every modern browser.",
    "model":      "openrouter/openai/gpt-oss-120b",
    "max_claims": 2,
    "topic":      "web platform"
  }'
```

Response (text mode):

```json
{
  "pack": "content.ground",
  "version": "v1",
  "output": {
    "claims_considered": 2,
    "claims_grounded":   2,
    "grounding": [
      {"claim": "WebAssembly delivers near-native performance",
       "url": "https://webassembly.org/docs/performance/",
       "title": "WebAssembly Performance"},
      {"claim": "runs in every modern browser",
       "url": "https://caniuse.com/wasm",
       "title": "Can I use WebAssembly"}
    ],
    "skipped": [],
    "text":    "WebAssembly delivers near-native performance [source](https://webassembly.org/docs/performance/) and runs in every modern browser [source](https://caniuse.com/wasm).",
    "sha256":  "abc123..."
  }
}
```

### File mode (session clone)

After a `repo.fetch` that has a markdown file at `posts/draft.md`:

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/content.ground \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d "{
    \"_session_id\": \"$SESSION\",
    \"clone_path\":  \"$CLONE\",
    \"path\":        \"posts/draft.md\",
    \"model\":       \"openrouter/openai/gpt-oss-120b\",
    \"max_claims\":  5,
    \"rewrite\":     false
  }"
```

The patched file is written back in place. `file_changed: true` if any claims grounded.

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | Neither `text` nor (`clone_path` + `path`) supplied | `must provide either text or clone_path+path` |
| `invalid_input` | Both `text` AND `clone_path` supplied | `provide either text or clone_path+path, not both` |
| `invalid_input` | `model` empty | `model is required (provider/model)` |
| `invalid_input` | Firecrawl overlay disabled | `content.ground is disabled; set HELMDECK_FIRECRAWL_ENABLED=true …` |
| `invalid_input` | (file mode) `clone_path` outside safe roots | `clone_path must be an absolute path under /tmp/helmdeck- or /home/helmdeck/work/` |
| `invalid_input` | `model` names a provider the gateway can't route (e.g. `minimax/…` — MiniMax is reachable only as `openrouter/minimax/…`) | `claim extractor dispatch: unknown provider: … — pick a configured model from the helmdeck://models resource …`. Read [`helmdeck://models`](#discovering-valid-models) for the routable IDs. |
| `handler_failed` | Claim extractor returned malformed JSON | `could not parse claim extraction: <raw>` |
| `handler_failed` | Every claim's exact-substring check failed | `no extracted claim was found verbatim in the source text` |
| `session_unavailable` | (file mode) Engine has no session executor | `engine has no session executor` |

## Discovering valid models

The `model` input is a full `provider/model` string the gateway routes on (e.g. `openrouter/openai/gpt-oss-120b`). If you name a provider the gateway isn't configured for — a common trap is `minimax/…`, since MiniMax is reachable only **through** OpenRouter as `openrouter/minimax/minimax-m2.7` — the pack fails fast with `invalid_input` and points you here.

Read the **`helmdeck://models`** MCP resource (or `GET /v1/models`) for the chat models the gateway can actually route to right now, as ready-to-paste `provider/model` IDs. It's the chat-model companion to `helmdeck://voices` and `helmdeck://image-models`. Pick one verbatim — don't guess.

## Session chaining

**Optional.** Text mode is stateless. File mode requires `_session_id` + `clone_path` from `repo.fetch`. Common file-mode chain:

```
repo.fetch → fs.list (find markdown files) → content.ground (per-file, with rewrite=true)
           → git.diff → git.commit → repo.push
```

## Async behavior

Synchronous. Wall-clock = `claim extraction LLM call (~3–10s)` + `per-claim Firecrawl search (~1–3s each)` + `(if rewrite) per-claim rewrite LLM call (~5–20s each)`. A 5-claim run with `rewrite: false` is typically 15–30 seconds; `rewrite: true` can hit 60–120 seconds.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `content.ground`.
- Source: [`internal/packs/builtin/content_ground.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/content_ground.go).
- ADR 035 — MCP Server Hosting & Pack Evolution.
- Companion packs: [`research.deep`](../research/deep.md) (the source-discovery primitive), [`web.scrape`](../web/scrape.md) (Firecrawl single-URL), [`fs.patch`](../fs/patch.md) (the alternative if you don't need the strict-substring guarantee).
