# Pack Demo Playbook — LLM Prompts for Testing Every Pack

**Status:** v0.8.0 · 35 packs · validated against OpenClaw (23/23 pass)

This playbook contains copy-pasteable prompts you can send to **any MCP client** (OpenClaw, Claude Code, Claude Desktop, Gemini CLI, Hermes Agent) connected to helmdeck. Each prompt exercises one pack and tells you what to expect. Use it to:

1. **Validate a new integration** — paste every prompt, confirm each pack lands in the audit log
2. **Demo helmdeck to your team** — walk through the prompts in order, show artifacts in the Management UI
3. **Regression test after upgrades** — re-run the prompts after a version bump

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

#### `desktop.run_app_and_screenshot` — Launch an app and capture the screen

**Prompt:**
```
Use the helmdeck desktop run app and screenshot tool to launch chromium with args ["--no-sandbox", "https://example.com"]. Wait for it to load and take a screenshot.
```

**Expected:** A screenshot artifact showing Chromium with example.com loaded (may be blank if Chromium takes time to render).

---

#### `vision.click_anywhere` — AI-driven click on a visual target

**Prompt:**
```
Use the helmdeck vision click anywhere tool with the goal "click the heading that says Example Domain" and max_steps 4.
```

**Expected:** `completed: true` or a step trace showing the model's attempts. Per-step screenshots uploaded to `/artifacts` for replay. Requires a vision-capable model.

---

#### `vision.extract_visible_text` — Transcribe everything on screen

**Prompt:**
```
Use the helmdeck vision extract visible text tool to read all text currently visible on the desktop.
```

**Expected:** A text field containing whatever is on the desktop (may be empty if no windows are open).

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
