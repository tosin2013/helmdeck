---
title: research.deep
description: Search → scrape sources → synthesize an answer with citations. Firecrawl-backed for the search/scrape, gateway LLM for the synthesis. Async on heavy queries.
keywords: [helmdeck, research, deep, firecrawl, search, synthesis, MCP]
---

# `research.deep`

The "go research this topic for me" pack. Caller hands in a `query` and a `model`; the pack runs Firecrawl's `/v1/search` (Google-backed by default; SearXNG when wired), scrapes each top result to clean Markdown, then asks the gateway LLM to synthesize a 3–6 sentence answer **grounded in the sources with inline citations**. Returns the synthesis + the source list verbatim.

Use it when the agent needs **current** information that wouldn't be in its training data, OR when "the answer must cite sources" matters more than "the answer must be fast." For pure summarization of content the agent already has in context, just ask the model — there's no Firecrawl round-trip needed.

## Setup prerequisite

Like [`web.scrape`](../web/scrape.md), this pack needs the Firecrawl overlay running and the env-var toggle set:

```bash
docker compose -f deploy/compose/compose.yaml \
               -f deploy/compose/compose.firecrawl.yml \
               --env-file deploy/compose/.env.local up -d

# in deploy/compose/.env.local:
HELMDECK_FIRECRAWL_ENABLED=true
```

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `query` | `string` | yes | — | **Use keywords, not full questions** (`"WebAssembly performance benchmarks 2026"`, not `"how fast is WebAssembly?"`). Search engines reward terse keyword strings; the synthesis step turns them back into prose. |
| `limit` | `number` | no | `5` | Max sources to scrape. Capped at 10. Each additional source adds ~10–20s of wall-clock. **Wins over `length_intent`** when both are set; preserves back-compat for existing callers. |
| `model` | `string` | yes (generate only) | — | Provider/model for the synthesis step. `openrouter/openai/gpt-4o-mini` is a good cheap default; weak/local models work but produce shakier syntheses. **Not required when `inspect:true`** — inspect doesn't call the model. |
| `max_tokens` | `number` | no | `1024` | Cap on synthesis output. Doesn't affect search/scrape. |
| `length_intent` | `string` | no | — | JIT length sizing (issue [#532](https://github.com/tosin2013/helmdeck/issues/532)) — one of `summary` / `thorough` / `exhaustive`. Maps directly to `limit`. Cost-cap shaped: more sources = more Firecrawl scrapes + more synthesis tokens. Honored only when `limit` is unset. |
| `inspect` | `boolean` | no | `false` | When `true`, pack returns the resolved limit + intent without firing Firecrawl or the synthesis LLM. Cheap planning helper — also skips the `HELMDECK_FIRECRAWL_ENABLED` gate so agents can plan in environments where Firecrawl isn't wired. |

### Length intent → limit

`research.deep` is **cost-cap shaped**: the "length" being controlled isn't output words or duration but the number of source URLs scraped per call. Each source costs a Firecrawl SERP page hit + a per-source markdown scrape + a slice of the synthesis LLM's context window.

| Intent | `limit` value |
|---|---|
| `summary` | 3 |
| `thorough` | 5 (matches the legacy default) |
| `exhaustive` | 10 (matches the hard cap) |

**Precedence**: `inspect:true` short-circuit → explicit `limit` (`"explicit"`, clamped to `[1, 10]`) → `length_intent` (`"intent:*"`) → legacy default 5 (`"default"`). Existing callers passing `limit` see **ZERO behavior change**.

## Outputs

| Field | Type | Notes |
|---|---|---|
| `query` | `string` | Echo. |
| `sources` | `array` | `[{url, title, description, markdown}]` — verbatim Firecrawl payload per source, post-scrape. |
| `synthesis` | `string` | 3–6 sentences. Every claim should cite a source by URL or title. If the model thinks the sources don't answer the query, it says so explicitly rather than confabulating. |
| `model` | `string` | Echo. |
| `limit_applied` | `number` | The limit value actually passed to Firecrawl (after precedence + cap). |
| `sources_used` | `number` | Count after empty-markdown filtering. `< limit_applied` when some search results came back with empty scrapes. |
| `length_intent_applied` | `string` | Where the limit came from — `intent:summary` / `intent:thorough` / `intent:exhaustive` / `explicit` / `default`. |
| `truncated` | `boolean` | `true` when the synthesis LLM hit `finish_reason=length`. Re-run with smaller `length_intent` or larger `max_tokens`. |

### Inspect-mode response

When `inspect:true`, the pack returns a planning response — no Firecrawl, no LLM, no Firecrawl-enabled gate:

| Field | Type | Notes |
|---|---|---|
| `query` | `string` | Echo. |
| `inspect` | `boolean` | Always `true`. |
| `suggested_limit` | `number` | What the resolver would pick. |
| `length_intent_applied` | `string` | Where the limit came from. |
| `reason` | `string` | Human-readable explanation. |

## Vault credentials needed

**None.** Firecrawl runs on the private `baas-net`; the LLM uses whatever provider key is configured in the *AI Providers* UI panel.

## Use it from your agent (OpenClaw chat-UI worked example)

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Use helmdeck__research-deep with query="WebAssembly performance benchmarks 2026", limit=3, model=openrouter/openai/gpt-oss-120b. Tell me the synthesis text (in your own summary) and the URLs of the sources it cited.

**Tool call** (8 calls, no failures):

```json
{
  "name": "helmdeck__research-deep",
  "arguments": {
    "query": "WebAssembly performance benchmarks 2026",
    "limit": 3,
    "model": "openrouter/openai/gpt-oss-120b"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"0ffd62111fe02536a8dab2432177da0b","pack":"research.deep","state":"working","progress":0,"started_at":"2026-05-08T15:23:16Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "0ffd62111fe02536a8dab2432177da0b"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"0ffd62111fe02536a8dab2432177da0b","pack":"research.deep","state":"working","progress":10,"message":"searching: \"WebAssembly performance benchmarks 2026\"","started_at":"2026-05-08T15:23:16Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "0ffd62111fe02536a8dab2432177da0b"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"0ffd62111fe02536a8dab2432177da0b","pack":"research.deep","state":"working","progress":60,"message":"synthesizing from 3 sources","started_at":"2026-05-08T15:23:16Z"}
```

```json
{
  "name": "helmdeck__pack-result",
  "arguments": {
    "job_id": "0ffd62111fe02536a8dab2432177da0b"
  }
}
```

**Pack response** (truncated):

```json
{"error":"not_ready","message":"pack.result: job 0ffd62111fe02536a8dab2432177da0b still working — keep polling pack.status"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "0ffd62111fe02536a8dab2432177da0b"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"0ffd62111fe02536a8dab2432177da0b","pack":"research.deep","state":"working","progress":60,"message":"synthesizing from 3 sources","started_at":"2026-05-08T15:23:16Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "0ffd62111fe02536a8dab2432177da0b"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"0ffd62111fe02536a8dab2432177da0b","pack":"research.deep","state":"working","progress":60,"message":"synthesizing from 3 sources","started_at":"2026-05-08T15:23:16Z"}
```

```json
{
  "name": "helmdeck__pack-status",
  "arguments": {
    "job_id": "0ffd62111fe02536a8dab2432177da0b"
  }
}
```

**Pack response** (truncated):

```json
{"job_id":"0ffd62111fe02536a8dab2432177da0b","pack":"research.deep","state":"completed","progress":100,"message":"synthesizing from 3 sources","started_at":"2026-05-08T15:23:16Z","ended_at":"2026-05-08T15:23:29Z"}
```

```json
{
  "name": "helmdeck__pack-result",
  "arguments": {
    "job_id": "0ffd62111fe02536a8dab2432177da0b"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"research.deep","version":"v1","output":{"model":"openrouter/openai/gpt-oss-120b","query":"WebAssembly performance benchmarks 2026","sources":[{"url":"https://www.hostmycode.com/blog/webassembly-runtime-performance-analysis-v8-wasmtime-wamr-benchmarks-production-deployments-2026","title":"WebAssembly Runtime Performance Analysis: V8, Wasmtime, and WAMR ...","description":"Compare WebAssembly runtime performance with V8, Wasmtime, and WAMR benchmarks. Production deployment insights for 2026.","markdown":"[Back to blog](https://www.hostmycode.com/blog)\n\nBlog\n\nWebAssembly Runtime Performance Analysis: V8, Wasmtime, and WAMR Benchmarks for Production Deployments in 2026\n==============================================================================================================\n
  ...[truncated]
```

**Agent's final reply**:

> **Synthesis (in my own words)**  
> 
> In the 2026 benchmark landscape, three major WebAssembly runtimes dominate:
> 
> | Runtime | Warm‑state speed (vs. native) | Cold‑start latency* | Memory footprint (peak) |
> |---------|------------------------------|----------------------|--------------------------|
> | **V8 (JS‑engine)** | ~94 % of native speed on compute‑heavy tasks (e.g., matrix multiplication) | 15‑30 ms for module instantiation | 2‑4× higher than Wasmtime because of JIT artefacts |
> | **Wasmtime** | ~82 % of native speed, very consistent across runs | 5‑7 ms cold start (ahead of most others) | 40‑60 % lower memory than V8 (≈95 MB vs. 180 MB in the same benchmark) |
> | **WAMR (AOT mode)** | ~85 % of native speed, good balance of speed & size | 5‑8 ms (AOT) – interpreter mode fastest at ~2 ms but 3‑5× slower overall | Smallest footprint (≈45 MB) – ideal for edge/IoT |
> 
> **Key take‑aways**
> 
> * **Raw compute** – V8 still squeezes the highest throughput after its JIT “warm‑up” phase, reaching 94 % of native performance on a matrix‑multiplication workload.  
> * **Predictable start‑up & memory** – Wasmtime offers the most predictable performance (no warm‑up delay) and uses considerably less memory, making it attractive for server‑side and edge services where resources are limited.  
> * **Ultra‑low‑latency / constrained environments** – The pure interpreter Wasm3 boots in ~2 ms, but its steady‑state execution is ~45 ms (much slower). WAMR’s AOT mode gives a good middle ground: sub‑5‑ms cold start with a modest 45 MB footprint, suited for IoT gateways or edge nodes.  
> 
> Overall, 2026 benchmarks show:
> 
> * **V8** leads on peak throughput when you can afford the memory and warm‑up cost.  
> * **Wasmtime** balances start‑up speed, memory efficiency, and steady performance—making it the general‑purpose choice for server‑less and edge workloads.  
> * **WAMR (and Wasm3)** shines where the smallest memory usage or the absolute fastest cold start matters, even if execution speed is lower.
> 
> ---
> 
> **Source URLs**
> 
> 1. https://www.hostmycode.com/blog/webassembly-runtime-performance-analysis-v8-wasmtime-wamr-benchmarks-production-deployments-2026  
> 2. https://wasmruntime.com/en/benchmarks  
> 3. https://www.javacodegeeks.com/2026/04/webassembly-in-2026-three-years-of-almost-ready.html

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0058).*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/research.deep \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "query": "WebAssembly performance benchmarks 2026",
    "limit": 3,
    "model": "openrouter/openai/gpt-oss-120b"
  }'
```

Response shape (truncated):

```json
{
  "pack": "research.deep",
  "version": "v1",
  "output": {
    "query": "WebAssembly performance benchmarks 2026",
    "sources": [
      {
        "url": "https://example.com/wasm-bench",
        "title": "WASM benchmark suite 2026",
        "description": "Cross-runtime perf comparison …",
        "markdown": "# Benchmark suite\n\n…"
      },
      … 2 more …
    ],
    "synthesis": "WebAssembly's 2026 cross-runtime benchmarks (Wasmtime vs Wasmer vs WAVM) show a ~15% perf gap on tight loops compared to native. The bottleneck is documented in [WASM benchmark suite 2026](https://example.com/wasm-bench) as instruction-cache pressure rather than codegen quality. …",
    "model": "openrouter/openai/gpt-oss-120b"
  }
}
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `query` empty | `query is required` |
| `invalid_input` | `model` empty | `model is required (provider/model)` |
| `invalid_input` | Firecrawl overlay disabled | `research.deep is disabled; set HELMDECK_FIRECRAWL_ENABLED=true …` |
| `handler_failed` | Firecrawl returned 0 results for the query | `firecrawl returned no results for "<query>"; refine the search terms` |
| `handler_failed` | Synthesis model returned no choices | `model returned no choices; check provider quota` |
| `timeout` | Pack-internal 180s timeout exceeded | `pack timed out after 180s` |

## Session chaining

**No session.** Stateless — the input is the query, the output is the synthesis. Compatible with anything; common chain: `research.deep` → `slides.narrate` (turn the synthesis into a presentation) or `research.deep` → `content.ground` (use the sources as grounding for a longer-form blog post you're writing).

## Async behavior

**`Async: true`.** Wall-clock = search (~1–3s) + per-source scrape (~5–10s each in parallel) + synthesis LLM round-trip (~5–30s on the model). For `limit=5` on an open-weight model, plan for **30–90 seconds end-to-end**. SDK clients that speak SEP-1686 handle the polling transparently — see [SKILLS.md §"Long-running packs"](/integrations/SKILLS#long-running-packs--three-paths-in-priority-order).

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `research.deep`.
- Source: [`internal/packs/builtin/research_deep.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/research_deep.go).
- ADR 035 — MCP Server Hosting & Pack Evolution (Firecrawl overlay rationale).
- Companion packs: [`web.scrape`](../web/scrape.md) (single URL scrape), [`content.ground`](../content/ground.md) (rewrite weak claims with citations).
