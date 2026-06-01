---
title: Pack demo playbook
description: 20+ copy-pasteable LLM prompts that exercise every shipped helmdeck capability pack against any MCP client (OpenClaw, Claude Code, Claude Desktop, Gemini CLI, Hermes Agent). Use it to verify a fresh install or demo helmdeck.
keywords: [helmdeck, demo playbook, MCP test prompts, pack validation, OpenClaw, Claude Code, integration test]
priority: 0.8
changefreq: monthly
---

# Pack Demo Playbook — LLM Prompts for Testing Every Pack

**Status:** v0.8.0 · 35 packs · validated against OpenClaw (23/23 pass)

This playbook contains copy-pasteable prompts you can send to **any MCP client** (OpenClaw, Claude Code, Claude Desktop, Gemini CLI, Hermes Agent) connected to helmdeck. Each prompt exercises one pack and tells you what to expect. Use it to:

1. **Validate a new integration** — paste every prompt, confirm each pack lands in the audit log
2. **Demo helmdeck to your team** — walk through the prompts in order, show artifacts in the Management UI
3. **Regression test after upgrades** — re-run the prompts after a version bump

> **Where to start when the user prompt spans multiple actions.** The `### Orchestration meta-packs` section below covers `helmdeck.route` (single intent), `helmdeck.plan` (multi-intent decomposition), and the memory pair (`helmdeck.memory_store` / `helmdeck.memory_forget`). Both route and plan see packs **and** pipelines through the same catalog projection, honor pipeline `metadata.supersedes`, and write per-caller audit rows that future calls mine as priors. For a fresh-install validation run, call `helmdeck.plan` once with a representative multi-action prompt before walking the per-pack prompts — it exercises the orchestration layer that the per-pack section doesn't.

## Prerequisites

| Requirement | How to check | Needed for |
|---|---|---|
| Helmdeck stack running | `curl http://localhost:3000/healthz` | All packs |
| MCP client connected | Client shows helmdeck tools in tool list | All packs |
| Firecrawl overlay | `HELMDECK_FIRECRAWL_ENABLED=true` in .env.local + compose.firecrawl.yml up | web.scrape, research.deep, content.ground |
| Docling overlay | `HELMDECK_DOCLING_ENABLED=true` in .env.local + compose.docling.yml up | doc.parse |
| ElevenLabs vault key | `elevenlabs-key` in Vault panel | slides.narrate (degrades to silent without) |
| Vision-capable LLM | Model that accepts images (gpt-4o, claude-opus-4-6, etc.) | vision.* packs |
| Desktop-mode session | Automatic — vision packs request `HELMDECK_MODE=desktop` | vision.* packs |

## How to use

1. Connect your MCP client to helmdeck (see `docs/integrations/{your-client}.md`)
2. Copy a prompt from below and paste it into your client's chat
3. The LLM will call the helmdeck tool — watch the response
4. Check the **Audit Logs** panel at `http://localhost:3000` to confirm the call landed
5. Check the **Artifact Explorer** panel for any files (screenshots, PDFs, videos)

## Prompts by category

---

### Browser packs

#### `browser.screenshot_url` — Take a screenshot of a webpage

**Prompt:**
```
Take a screenshot of https://example.com using the helmdeck browser screenshot tool.
```

**Expected:** A PNG screenshot artifact. The LLM should describe the page content ("Example Domain" heading). Check `/artifacts` in the UI for the image.

---

#### `browser.interact` — Run deterministic browser actions

**Prompt:**
```
Use the helmdeck browser interact tool to:
1. Navigate to https://example.com
2. Extract the text from the h1 element
3. Assert the text contains "Example Domain"
4. Take a screenshot

Use these exact actions: [{"action":"extract","selector":"h1","format":"text"},{"action":"assert_text","text":"Example Domain"},{"action":"screenshot"}]
```

**Expected:** `assertions_passed: true`, `steps_completed: 3`, one screenshot in the extractions.

---

### Web packs

#### `web.scrape` — Scrape a page to clean markdown (Firecrawl)

**Prompt:**
```
Scrape https://example.com to markdown using the helmdeck web scrape tool.
```

**Expected:** Clean markdown starting with `Example Domain` heading. Requires Firecrawl overlay.

---

#### `web.scrape_spa` — Scrape with CSS selectors (built-in)

**Prompt:**
```
Use the helmdeck web scrape SPA tool to extract the top headlines from https://news.ycombinator.com with fields {"top":{"selector":"span.titleline > a","format":"text"}}
```

**Expected:** A `data` object with a `top` field containing headline text. No overlay needed.

---

#### `web.test` — Natural language browser testing

**Prompt:**
```
Use the helmdeck web test tool to test https://example.com. The instruction is: "Confirm the page has the heading Example Domain and a link that says More information." Use max_steps 5 and assertions ["Example Domain", "More information"].
```

**Expected:** `completed: true`, `assertions_passed: true`, a step trace showing browser_navigate → browser_snapshot → done. Requires Firecrawl + LLM.

---

### Research & Content packs

#### `research.deep` — Search + scrape + synthesize

**Prompt:**
```
Use the helmdeck research deep tool to research "WebAssembly performance benchmarks" with limit 5.
```

**Tip:** Use keywords, not full questions. Firecrawl's search passes the query to Google — short keyword queries get better results than natural language questions from self-hosted instances.

**Expected:** 3-5 sources with URLs + markdown bodies, plus a synthesized paragraph citing the sources. Requires Firecrawl + LLM.

---

#### `content.ground` — Source-backed fact-checking and prose improvement

**Prompt (citation only — default):**
```
Use the helmdeck content ground tool with text "Large language models are trained on massive datasets and can generate human-like text. Transformer architecture revolutionized natural language processing. RAG combines retrieval with generation to reduce hallucinations." and topic "artificial intelligence".
```

**Prompt (rewrite mode — rewrites weak claims into stronger prose using sources):**
```
Use the helmdeck content ground tool with rewrite true and text "Large language models are trained on massive datasets and can generate human-like text. Transformer architecture revolutionized natural language processing. RAG combines retrieval with generation to reduce hallucinations. Fine-tuning allows models to specialize in specific domains. RLHF is used to align AI models with human preferences." and topic "artificial intelligence".
```

**Prompt (file mode — from a cloned repo):**
```
First clone https://github.com/octocat/Hello-World.git using the helmdeck repo fetch tool. Then use content ground on the README file with the clone_path and session_id from the clone.
```

**Tip:** Use `rewrite true` for blog-quality output — the LLM reads each source and rewrites vague claims into specific, authoritative prose with inline citations. Without `rewrite`, it just appends `[source](url)` links. Use keywords in your `topic` field to help the search engine find better sources.

**Expected:** `claims_grounded >= 1`, `grounded_text` with `[source](url)` citations. With `rewrite true`, claims are rewritten using source content (e.g. "Rust guarantees memory safety" becomes "Rust guarantees memory safety without needing a garbage collector through its ownership system..."). A `grounded.md` artifact is uploaded to `/artifacts` for download. Each grounding entry includes a `snippet` showing the source excerpt that supports the claim. Requires Firecrawl + LLM.

---

### Orchestration meta-packs — route, plan, memory_store, memory_forget

These four packs don't *do* a task themselves; they help the agent **pick** and **sequence** the right packs and pipelines. They share one catalog projection (`helmdeck://routing-guide`) and one per-caller memory namespace, so the agent's choices get sharper as it accumulates history.

| Meta-pack | When to call | Returns |
| --- | --- | --- |
| `helmdeck.route` | Single intent, *"which one tool fits?"* | One recommendation + alternatives + a structured `gap_warning` when nothing fits |
| `helmdeck.plan` | Multi-intent prompt, *"do A, then B, then C"* | Ordered `steps[]` (each {tool, args, rationale}) + a derived `rewritten_prompt` |
| `helmdeck.memory_store` | User shared a durable preference/fact | The stored entry + the namespace it landed in |
| `helmdeck.memory_forget` | User asked to clear defaults / a specific key | Number of rows removed, scoped by `scope:` selector |

Both route and plan see **packs AND pipelines** through the same catalog, and both honor pipeline `metadata.supersedes` so a curated pipeline wins over re-decomposing its constituent packs.

---

#### `helmdeck.route` — Pick ONE tool for a single-intent prompt (ADR 047 PR #3)

**Prompt:**
```
Use the helmdeck route tool with user_intent "I have a PDF of a research paper and I want it turned into a blog post" and model "openrouter/openrouter/free".
```

**Expected:** A JSON `recommendation` naming a real pack or pipeline (likely `builtin.doc-rewrite-blog` because that pipeline's `metadata.accepts` covers `pdf` and `produces` covers `blog_markdown`), `suggested_inputs` pre-filled from any learned `helmdeck://my-defaults` for this caller, up to 3 `alternatives`, and `reasoning` citing the chosen pack's metadata. When NOTHING in the catalog fits (e.g. "transcribe a YouTube video"), `gap_warning` populates with a `proposed_pack` sketch ({name, input_schema, output_schema, integration_pattern, why_useful}) — the agent confirms and either files a GitHub issue or pivots.

---

#### `helmdeck.plan` — Decompose a multi-intent prompt into ordered tool calls (ADR 049 PR #1)

**Prompt (pack-chain — three distinct actions):**
```
Use the helmdeck plan tool with user_intent "remember this MiniMax M3 launch announcement as a durable fact, then write a 300-word technical blog post about it, then generate an illustration for the post" and model "openrouter/openrouter/free".
```

**Expected:** A 3-step `steps[]` array. Step 1 calls `helmdeck.memory_store` to persist the fact. Step 2 likely calls `helmdeck__pipeline-run` with `args.id` = a blog pipeline (e.g. `builtin.brief-rewrite-blog`) **rather than chaining three packs by hand** — the planner's pipeline-aware rule P1 fires when the pipeline covers `accepts`/`produces` end-to-end, and rule P2 fires when the pipeline's `metadata.supersedes` lists packs the user mentioned. Step 3 calls `image.generate`. `complexity` is `pack-chain`. `rewritten_prompt` is the same plan rendered as `Step N: call X with args Y — rationale` lines an agent can execute line-by-line.

**Prompt (pipeline-direct — one step to a curated pipeline):**
```
Use the helmdeck plan tool with user_intent "I have this brief, rewrite it as a blog post and ground every claim with citations" and model "openrouter/openrouter/free".
```

**Expected:** ONE step calling `helmdeck__pipeline-run` with `args.id` = `builtin.brief-rewrite-blog`. `complexity` is `pipeline-direct`. The planner did NOT decompose the pipeline's three internal stages into three separate steps — that's the supersedes rule in action.

**Prompt (single-action — degenerate case, exercises route fallback):**
```
Use the helmdeck plan tool with user_intent "take a screenshot of github.com" and model "openrouter/openrouter/free".
```

**Expected:** ONE step calling `browser.screenshot_url`. `complexity` is `single-action`. The agent should consider calling `helmdeck.route` next time — route is cheaper for the one-tool case.

**Tip — agent execution paths:** if your runtime can iterate `steps[]` and dispatch each tool, do that. If your model produces brittle tool-calls when handed long JSON specs, feed the `rewritten_prompt` string back as the next user message — it's the same plan as a single natural-language instruction. Both surfaces encode the same plan and can't drift (the handler derives `rewritten_prompt` from `steps` post-LLM).

**Tip — unknown steps:** any step whose `tool` doesn't resolve to a registered pack or pipeline id is demoted to `"tool": "unknown"` with a populated `rationale`. Surface those to the user (or to `helmdeck.route`'s gap-warning flow) — do NOT dispatch them. Partial demotion is fine: a 4-step plan can have 3 valid steps + 1 `unknown`.

---

#### `helmdeck.memory_store` — Persist a durable user fact (ADR 048 PR #2)

**Prompt:**
```
Use the helmdeck memory_store tool with key "preferences/blog-persona", value "technical, with mermaid diagrams when explaining architecture", category "preferences", and ttl_seconds 7776000.
```

**Expected:** The entry lands under the caller's bare namespace. Subsequent `helmdeck://my-memory` reads show it grouped by category. Categories `pack_history`, `pipeline_history`, and `plan_history` are reserved for engine audit hooks and rejected with `reserved_category` if you try to write into them.

**Tip:** Read `helmdeck://my-memory` BEFORE storing to avoid duplicates and to discover existing keys. The agent should peek at the top of a session, not every turn.

---

#### `helmdeck.memory_forget` — Clear learned defaults or a specific key (ADR 047 PR #2)

**Prompt (forget everything):**
```
Use the helmdeck memory_forget tool with scope "all".
```

**Prompt (forget a specific fact):**
```
Use the helmdeck memory_forget tool with scope "key:preferences/blog-persona".
```

**Prompt (forget all pack defaults for a specific pack):**
```
Use the helmdeck memory_forget tool with scope "pack:blog.rewrite_for_audience".
```

**Expected:** A JSON response with `removed_count`. Other scopes: `packs` (all pack audit), `pipelines` (all pipeline audit), `pipeline:<id>` (one pipeline's audit). Cache rows (per-pack output cache) are NEVER touched — forget only targets audit and user-fact categories. Use this when the user says *"forget my defaults"* or *"don't remember that"*.

---

### Meta-pack workflows — combining route + plan + memory

These prompts show how the meta-packs compose for end-to-end orchestration. Use them as templates for the agent's first turn in a fresh session.

#### Cold-start orientation — read defaults, then route

**Prompt:**
```
1. Read helmdeck://my-defaults to discover what packs and inputs I've used most.
2. Read helmdeck://my-memory to discover what user facts are already stored.
3. Then, given the intent "draft a blog post about my last research topic", call helmdeck route to pick the best tool, pre-filling suggested_inputs from the defaults you just read.
```

**Expected:** Three calls in sequence — two resource reads, then `helmdeck.route` returning a recommendation whose `suggested_inputs` reflects the agent's actual usage history. On a fresh deployment with no audit history, defaults are empty and route falls back to keyword matching.

---

#### Plan-then-execute — let the planner decompose, then dispatch each step

**Prompt:**
```
I want to (a) remember that I prefer dark-themed slide decks, (b) take the README at https://github.com/tosin2013/helmdeck/blob/main/README.md and produce a 7-slide presentation as a video.

Step 1: call helmdeck plan with user_intent matching that ask and model "openrouter/openrouter/free".
Step 2: execute the returned steps in order. After each step, report progress to me.
Step 3: when all steps complete, list every artifact produced.
```

**Expected:** Plan returns a multi-step decomposition (memory_store for the dark-theme preference, then a slides pipeline like `scrape-deck` or `repo-presentation` for the README → narrated video). The agent dispatches each step in order and reports progress. If any step's tool is `"unknown"`, the agent surfaces it instead of dispatching.

---

#### Plan-then-route — when the user's intent has a gap

**Prompt:**
```
I want to transcribe a YouTube video, summarize the transcript, and generate slides from the summary.

Step 1: call helmdeck plan with user_intent matching that ask.
Step 2: if any step in the returned plan has tool="unknown", call helmdeck route on that step's intent to surface a gap_warning. Otherwise execute the plan.
```

**Expected:** YouTube transcription doesn't exist in helmdeck's catalog today. Plan demotes step 1 to `"tool": "unknown"`. The agent calls route on the transcription intent, and route returns a `gap_warning` with a `proposed_pack` named (e.g.) `youtube.transcript` plus an integration pattern. The agent reports the gap to the user and offers to file a GitHub issue or proceed with an alternative.

---

#### Forget cycle — after the user changes their mind

**Prompt:**
```
I told you earlier I preferred React, but I'm switching to Solid. Forget the React preference and store the new one.
```

**Expected:** Two calls — `helmdeck.memory_forget` with `scope: "key:preferences/frontend-framework"` (or whatever key was used originally), then `helmdeck.memory_store` with the same key and the new value. The agent confirms both actions and reports the new state by reading `helmdeck://my-memory`.

---

### Slides packs

#### `slides.render` — Render slides to PDF

**Prompt:**
```
Use the helmdeck slides render tool to create a PDF from this Marp markdown:

---
marp: true
---

# Welcome to Helmdeck

Browser automation for AI agents.

---

# Key Features

- 35 capability packs
- Cross-provider computer use
- Vault-aware credential injection

---

# Thank You

Questions?
```

**Expected:** A PDF artifact with 3 slides. Download from `/artifacts` in the UI.

---

#### `slides.narrate` — Create narrated video with YouTube metadata

**Prompt:**
```
Use the helmdeck slides narrate tool to create a video from this Marp deck. Also generate YouTube metadata.

---
marp: true
---

# AI and the Future of Work

<!-- Welcome to this presentation about how artificial intelligence is changing the way we work. -->

The workplace is evolving rapidly.

---

# Key Trends

<!-- There are three major trends we're seeing in AI adoption across industries. -->

- Automation of repetitive tasks
- AI-assisted decision making
- Human-AI collaboration

---

# What's Next

<!-- Thank you for watching. The future of work is being written right now, and AI is holding the pen. -->

The best is yet to come.
```

**Expected:** An MP4 video artifact + a metadata.json with YouTube title, description (with timestamps), tags. If ElevenLabs key is in vault, the video has narration; otherwise silence. Check both artifacts in `/artifacts`.

---

### Agent workflows — chaining packs for complex tasks

These prompts show how the LLM can **compose multiple packs** in a single conversation to accomplish higher-level goals. No new tools needed — the LLM generates content and pipes it through existing packs.

#### Marketing agent — generate a pitch deck video from an idea

**Prompt:**
```
I need a 5-slide marketing pitch video for "Helmdeck — browser automation for AI agents". Target audience: DevOps engineers. Tone: professional but energetic.

Please:
1. Write the Marp markdown with compelling copy and speaker notes for narration
2. Use the helmdeck slides narrate tool to create the video with YouTube metadata
```

**Expected:** The LLM writes a full Marp deck with `---` delimiters, `<!-- speaker notes -->` for each slide, then calls `slides.narrate` to produce an MP4 + YouTube metadata. The video has narrated voiceover from the speaker notes. Check `/artifacts` for both files.

---

#### Product launch video from bullet points

**Prompt:**
```
Turn these bullet points into a narrated presentation video:

- Product: CloudSync Pro
- Problem: Teams waste hours manually syncing data between cloud services
- Solution: Automated real-time sync with conflict resolution
- Pricing: Free tier, $29/mo Pro, $99/mo Enterprise
- CTA: Start your free trial at cloudsync.pro

Create the Marp slides with speaker notes and use helmdeck slides narrate to make the video. Generate YouTube metadata too.
```

**Expected:** LLM generates 4-5 slides with professional copy, speaker notes that expand each bullet into a narration script, then produces a narrated MP4.

---

#### Technical blog writer — research + write + ground

**Prompt:**
```
Write a 500-word blog post about "How Kubernetes handles pod scheduling" and ground it with real sources. Use the helmdeck content ground tool with rewrite true.
```

**Expected:** The LLM writes the blog post, passes it to `content.ground` with `rewrite: true`, and the pack searches for sources, verifies them, and rewrites claims to be more authoritative with inline citations. Download `grounded.md` from `/artifacts`.

---

#### Research report — deep search + formatted slides

**Prompt:**
```
Research "edge computing trends 2026" using the helmdeck research deep tool with limit 5. Then create a presentation summarizing the findings and use slides narrate to make a video.
```

**Expected:** Two tool calls chained: (1) `research.deep` searches and synthesizes the topic, (2) LLM formats the synthesis into a Marp deck with speaker notes, (3) `slides.narrate` produces the video. Three artifacts total: research sources, video, YouTube metadata.

---

### Async pattern — long-running packs

Some packs run too long for typical MCP client timeouts (60s default in the TypeScript SDK that backs OpenClaw, Claude Desktop's older versions, and most JS-based MCP clients). For those, use `pack.start` + `pack.status` + `pack.result`.

#### `pack.start` + `pack.status` + `pack.result` — narrate slides without timing out

**Prompt:**
```
Use helmdeck's async pattern to render this Marp deck without timing out:
1. Call pack.start with pack "slides.narrate" and input containing the markdown below
2. Poll pack.status every 5 seconds, reporting progress to me
3. When state is "done", call pack.result to get the video

---
marp: true
---

# Quarterly Update

<!-- Welcome to the Q1 update. We had a strong quarter across all metrics. -->

Revenue, headcount, and customer satisfaction all up.

---

# What's Next

<!-- Looking ahead to Q2, we're focused on three priorities: shipping the new product, expanding into Europe, and hiring senior engineers. -->

Three priorities for Q2.
```

**Expected:** The agent calls `pack.start` and gets back a `job_id`. It then polls `pack.status` and reports progress messages like "audio 1/2", "encoding segment 2/2", "concatenating final video". Once `state` is `completed`, it calls `pack.result` and shows the video artifact link. Total wall time is the same as a direct call — the win is that NO 60s timeout fires because each individual JSON-RPC request finishes in milliseconds.

---

#### Webhook push — no polling, result delivered as a fresh chat message

**Prereq:** The `helmdeck-callback` sidecar from `examples/webhook-openclaw/` is running and reachable from helmdeck (see `docs/integrations/openclaw.md#webhook-callback`).

**Prompt:**
```
Render this Marp deck as a narrated video using helmdeck slides narrate.
Pass webhook_url=http://helmdeck-callback:8080/done and
webhook_secret=$HELMDECK_WEBHOOK_SECRET in the input.
Don't poll — I'll get notified in chat when it's ready.

---
marp: true
---

# Push Demo

<!-- This is a demo of the helmdeck webhook push flow. -->

The result will arrive as a system message.
```

**Expected:**
1. The agent calls `slides.narrate` ONCE with the webhook params; the response is a SEP-1686 task envelope (no polling).
2. The agent acknowledges: "Started slides.narrate, task ID `pack_…`. Waiting for the webhook."
3. ~60-180s later, a fresh **system message** appears in chat:
   ```
   [helmdeck] Pack `slides.narrate` completed.
     video_artifact_key: http://localhost:3000/artifacts/slides.narrate/<key>/video.mp4
     metadata_artifact_key: http://localhost:3000/artifacts/slides.narrate/<key>/metadata.json
     job_id: <hex>
   ```
4. The agent's next turn picks up that context and offers follow-ups (e.g. "Want me to upload this to YouTube?").

This proves the push-to-LLM path: helmdeck → callback bridge → OpenClaw chat injection → fresh LLM turn. NO -32001, NO polling, NO chained tool calls.

---

### GitHub packs

#### `github.list_issues` — List repository issues

**Prompt:**
```
List the issues in the tosin2013/helmdeck repository using the helmdeck GitHub list issues tool.
```

**Expected:** An array of issues with number, title, state, URL.

---

#### `github.search` — Search code across GitHub

**Prompt:**
```
Search for "EgressGuard" in the tosin2013/helmdeck repository using the helmdeck GitHub search tool with type "code".
```

**Expected:** Code search results showing files containing "EgressGuard" with match snippets.

---

#### `github.list_prs` — List pull requests

**Prompt:**
```
List the open pull requests in the tosin2013/helmdeck repository using the helmdeck GitHub list PRs tool.
```

**Expected:** Array of PRs (may be empty if no open PRs).

---

### HTTP pack

#### `http.fetch` — Make an HTTP request

**Prompt:**
```
Use the helmdeck HTTP fetch tool to GET https://httpbin.org/json with a User-Agent header set to "Helmdeck-Demo/1.0".
```

**Expected:** JSON response from httpbin with a slideshow object.

---

### Repository packs

#### `repo.fetch` + `fs.list` + `fs.read` — Clone and explore a repo

**Prompt:**
```
Clone https://github.com/octocat/Hello-World.git using the helmdeck repo fetch tool with depth 1. Then list the files in the clone using fs list with the clone_path and session_id from the result. Then read the README file.
```

**Expected:** Three tool calls chained via `_session_id`. Clone succeeds, file listing shows README, file content shows "Hello World" text.

---

#### `repo.fetch` context envelope — orient without extra calls (ADR 022 §2026-04-15)

**Prompt:**
```
Clone https://github.com/tosin2013/low-latency-performance-workshop.git and tell me what this repository is about. Use the context envelope from repo.fetch — do not call fs.list or fs.read unless you need to.
```

**Expected:** One `repo.fetch` call. Response contains:
- `readme.path: "README.adoc"` (auto-detected despite non-`.md` extension)
- `readme.content` starting with `= Low-Latency Performance Workshop...`
- `entrypoints` lists `Makefile`, `package.json`, `devfile.yaml`
- `signals.has_readme: true`, `has_docs_dir: true`, `sparse: false`
- `tree` includes `content/`, `docs/`, `blog-posts/` paths

The agent should summarize the workshop from `readme.content` alone. It must NOT respond "the repository appears empty" — the envelope makes that conclusion impossible when a README is present.

---

#### `repo.map` — Symbol-level structural map for code tasks (ADR 036)

**Prompt:**
```
Clone https://github.com/tosin2013/helmdeck.git at depth 1. Then call repo.map with a token budget of 1500 and include_globs ["*.go"]. Tell me where the MCP server and pack engine are defined.
```

**Expected:** Two tool calls. `repo.map` returns a `map` field shaped like:
```
internal/mcp/server.go:
  function Serve
  function dispatch
  struct PackServer
  ...
internal/packs/packs.go:
  struct Pack
  struct Engine
  function (e *Engine) Execute
  ...
```
with `files_covered ≤ files_total` and `tokens_estimated` close to 1500. Agent answers by pointing at the ranked files, not by opening them one by one.

---

#### `git.diff` + `git.log` — Inspect git state

**Prompt:**
```
After cloning a repo, use the helmdeck git log tool with the clone_path and session_id to see the last 5 commits.
```

**Expected:** Commit log with hashes, authors, dates, messages.

---

### Document packs

#### `doc.parse` — Parse a document with layout understanding (Docling)

**Prompt:**
```
Use the helmdeck doc parse tool to parse https://example.com with formats ["md"].
```

**Expected:** Markdown output with the page content. Requires Docling overlay.

---

#### `doc.ocr` — OCR an image

**Prompt:**
```
First take a screenshot of https://example.com using browser screenshot. Then run OCR on the screenshot artifact using the helmdeck doc OCR tool.
```

**Expected:** Two tool calls. OCR extracts text like "Example Domain" from the screenshot.

---

### Desktop + Vision packs

#### Before you run these: prepare a watchable session

The desktop/vision packs only "show their work" if you can see the session. Stand up a long-lived desktop-mode session and forward its noVNC port to your browser:

```bash
# on the helmdeck host — spawn a 1-hour desktop session
TOKEN=$(cat /tmp/helmdeck-jwt.txt)   # or mint one per openclaw-upgrade-runbook.md
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  http://localhost:3000/api/v1/sessions \
  -d '{"env":{"HELMDECK_MODE":"desktop"},"timeout_seconds":3600}' | jq -r .id
#   → e.g. 8510291f-85bb-45e0-a159-c44982660a3d

# on the helmdeck host — start the noVNC forwarder
./scripts/vnc-forward.sh
#   → "forwarding 127.0.0.1:6080 → helmdeck-session-<id>:6080 (172.18.0.x)"

# on your laptop — add 6080 to your existing SSH tunnel, then browse:
#   http://localhost:6080/vnc.html?autoconnect=true&resize=remote
```

You should see the XFCE4 desktop with a Chromium window already open (the sidecar auto-launches it in desktop mode).

Now pass `_session_id=<that id>` in every prompt below so the agent drives **the session you're watching** instead of spawning a fresh one.

---

#### Test 1 — minimum viable visible action (URL bar click + type)

**Prompt:**
```
I'm watching via noVNC. Use the desktop-mode session id <SESSION_ID>.

Use helmdeck's visible-desktop tools (vision.click_anywhere + the desktop.* REST primitives) to click in the URL bar of the already-open Chromium and type "example.com", then press Enter.

Do NOT use browser.interact — I need to see the cursor move in my noVNC tab.
```

**Expected (visible in noVNC):** cursor moves to Chromium's URL bar, URL bar text highlights, letters appear one at a time, page navigates to example.com. **Server-side**: audit log shows `vision.click_anywhere` or `desktop.click`/`desktop.type`/`desktop.key` calls — NOT `browser.interact`.

**If the agent still picks `browser.interact`**, the Tier 1 description rewrite wasn't enough — fall back to telling the agent explicitly "the user is watching; only vision.* and desktop.* primitives are acceptable." File the observation for Tier 2 renaming.

---

#### Test 2 — search + result click (2 min, exercises the full loop)

**Prompt:**
```
I'm watching via noVNC with session id <SESSION_ID>.

Drive the visible desktop to:
  1. Navigate Chromium to duckduckgo.com
  2. Search for "helmdeck github"
  3. Click the first result
  4. Tell me the repo's description from what's now on screen

Use vision.click_anywhere / vision.extract_visible_text / desktop.* REST primitives. NOT browser.interact.
```

**Expected:** full cursor-path visible in noVNC: URL bar → type → Enter → page loads → click search input → type query → Enter → click first result → page loads → agent reports the repo's description via `vision.extract_visible_text`.

---

#### Test 3 — app lifecycle (launch + drive a different app)

Exercises paradigm (a)+launch: the agent opens an app helmdeck did NOT pre-launch, then drives it.

**Prompt:**
```
I'm watching via noVNC with session id <SESSION_ID>.

Launch xterm on the visible desktop via desktop.run_app_and_screenshot. Then use desktop.type to run `ls /home/helmdeck && pwd` in the terminal, press Enter, and tell me what it printed.
```

**Expected:** xterm window appears in noVNC, command types character-by-character, output renders, agent reports the captured output.

**Note:** only apps installed in the sidecar image are launchable. Today that's chromium, xterm, xfce4 utilities. For others (LibreOffice, GIMP, IDEs), add them to `deploy/docker/sidecar.Dockerfile` and rebuild with `make sidecar-build`.

---

#### Individual packs — for reference

##### `desktop.run_app_and_screenshot` — Launch an app and capture the screen

**Prompt:**
```
Use the helmdeck desktop run app and screenshot tool to launch xterm with no args. Wait for it to appear and take a screenshot.
```

**Expected:** A screenshot artifact showing the xterm window on the XFCE4 desktop.

##### `vision.click_anywhere` — AI-driven click on a visual target

**Prompt:**
```
Use the helmdeck vision click anywhere tool on session <SESSION_ID> with the goal "click the address bar of Chromium" and max_steps 3.
```

**Expected:** `completed: true` with a step trace; the URL bar gains focus in noVNC. Per-step screenshots land in `/artifacts`.

##### `vision.extract_visible_text` — Transcribe everything on screen

**Prompt:**
```
Use the helmdeck vision extract visible text tool on session <SESSION_ID> to read all text currently visible on the desktop.
```

**Expected:** A text field containing whatever is on screen — typically Chromium's chrome (URL bar, tabs, default new-tab page) plus any XFCE4 panel text.

---

### Language packs

#### `python.run` — Execute Python code

**Prompt:**
```
Use the helmdeck Python run tool to execute this code: print("Hello from Python! " + str(2 + 2))
```

**Expected:** `stdout: "Hello from Python! 4"`, `exit_code: 0`.

---

#### `node.run` — Execute Node.js code

**Prompt:**
```
Use the helmdeck Node run tool to execute this code: console.log("Hello from Node! " + JSON.stringify({version: process.version}))
```

**Expected:** `stdout` with Node version info, `exit_code: 0`.

---

## Verification checklist

After running all prompts, verify in the Management UI:

- [ ] **Audit Logs** (`/audit`) — every pack call shows as `pack_call` or `mcp_call` with timestamp, actor, status
- [ ] **Artifact Explorer** (`/artifacts`) — screenshots, PDFs, videos visible with inline preview
- [ ] **Sessions** — any active sessions listed; terminated sessions cleaned up by watchdog
- [ ] **Credentials** — vault entries (github-token, elevenlabs-key) show last-used timestamps

## Adapting for a new integration

When adding a new MCP client integration:

1. **Copy** this playbook's prompts into your client's test run
2. **Record** which prompts pass/fail — the pack name maps 1:1 to the audit log
3. **Document** any client-specific quirks (tool name format, JSON escaping, session handling)
4. **Update** `docs/integrations/{client}.md` with a status banner and the pass count
5. **Update** `docs/integrations/README.md` status matrix

## Resource requirements

Captured from a full-stack validation run on 2026-04-12:

| Component | Memory | Required? |
|---|---|---|
| helmdeck-control-plane | 10 MiB | Yes |
| helmdeck-garage (S3 store) | 4 MiB | Yes |
| Sidecar session (each) | 188 MiB (1 GiB cap) | Per browser/desktop pack call |
| Firecrawl (all containers) | ~1.7 GiB | For web.scrape, research.deep, content.ground |
| Docling | 886 MiB | For doc.parse |
| OpenClaw (example client) | 529 MiB | Optional — any MCP client works |

**Minimum:** 14 MiB (control plane + garage). **Full stack:** ~3.4 GiB.
