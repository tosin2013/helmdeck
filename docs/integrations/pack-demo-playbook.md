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

#### `content.ground` — Add source links to a blog post

**Prompt (text mode — no repo needed):**
```
Use the helmdeck content ground tool with text "Quantum computers use qubits instead of classical bits. WebAssembly enables near-native performance in web browsers. Rust guarantees memory safety without garbage collection." and topic "computer science".
```

**Prompt (file mode — from a cloned repo):**
```
First clone https://github.com/octocat/Hello-World.git using the helmdeck repo fetch tool. Then use content ground on the README file with the clone_path and session_id from the clone.
```

**Tip:** Text mode is simpler — pass markdown directly and get grounded text back. File mode is for when the content is already in a repo you're editing.

**Expected:** `claims_grounded >= 1`, `grounded_text` with `[source](url)` links inserted after factual claims. A `grounded.md` artifact is uploaded to `/artifacts` for download. Requires Firecrawl + LLM.

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
