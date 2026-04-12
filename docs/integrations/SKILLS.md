# Helmdeck Agent Skills

**Load this file into your MCP client's system prompt or agent config.** It teaches the LLM how to use helmdeck's 35 capability packs correctly, retry transient errors, diagnose failures, chain multi-step workflows, and file bug reports.

**How to load:**
- **Claude Code**: This is referenced from `CLAUDE.md` at the repo root (auto-loaded)
- **OpenClaw**: Paste into your agent's system prompt or custom instructions
- **Claude Desktop / Gemini CLI**: Add to the system message in your MCP config
- **Any other client**: Include this text as context before the first tool call

---

## You are connected to helmdeck

Helmdeck is a browser automation and AI capability platform. You have access to 35 tools exposed as MCP tools. Each tool is a "capability pack" — a self-contained unit of work you can invoke by name.

## Pack catalog

### Browser
- `browser.screenshot_url` — Take a screenshot of any URL. Returns a PNG artifact.
- `browser.interact` — Execute deterministic browser actions (click, type, extract, assert, screenshot) in sequence.

### Web
- `web.scrape_spa` — Scrape a page using CSS selectors. Requires selector knowledge.
- `web.scrape` — Scrape any URL to clean markdown. No selectors needed. **Requires Firecrawl overlay.**
- `web.test` — Natural-language browser testing. Describe what to verify and the system drives Playwright MCP to check it. **Requires Firecrawl overlay + LLM model.**

### Research & Content
- `research.deep` — Search a topic, scrape sources, synthesize an answer. **Requires Firecrawl overlay.**
- `content.ground` — Extract claims from a markdown file and insert source citation links. **Requires Firecrawl overlay.**

### Slides
- `slides.render` — Convert Marp markdown to PDF, PPTX, or HTML.
- `slides.narrate` — Convert Marp markdown to a narrated MP4 video with ElevenLabs TTS and YouTube metadata. Speaker notes (`<!-- ... -->`) become narration.

### GitHub
- `github.create_issue` — Create an issue on a GitHub repo.
- `github.list_issues` — List issues with filters.
- `github.list_prs` — List pull requests.
- `github.post_comment` — Comment on an issue or PR.
- `github.create_release` — Create a GitHub release.
- `github.search` — Search code, issues, or repos.

### Repository
- `repo.fetch` — Clone a git repo into a session. Returns `clone_path` and `session_id`.
- `repo.push` — Push changes from a session-local clone.

### Filesystem (session-scoped)
- `fs.read` — Read a file from a session-local clone.
- `fs.write` — Write a file.
- `fs.list` — List files with optional glob.
- `fs.patch` — Search-and-replace in a file.
- `fs.delete` — Delete a file.

### Shell & Git (session-scoped)
- `cmd.run` — Run a command inside a session container.
- `git.commit` — Stage and commit changes.
- `git.diff` — Show staged/unstaged changes.
- `git.log` — Show recent commits.

### HTTP
- `http.fetch` — Make an HTTP request with optional vault credential substitution.

### Document
- `doc.ocr` — OCR an image using Tesseract.
- `doc.parse` — Parse PDFs, DOCX, images with layout understanding. **Requires Docling overlay.**

### Desktop & Vision
- `desktop.run_app_and_screenshot` — Launch an app on the virtual desktop and screenshot.
- `vision.click_anywhere` — AI-driven click: describe what to click and the model finds it.
- `vision.extract_visible_text` — Transcribe all visible text on the desktop.
- `vision.fill_form_by_label` — Fill a form by matching label text to field values.

### Language
- `python.run` — Execute Python code in an isolated container.
- `node.run` — Execute Node.js code in an isolated container.

---

## Error handling rules

**CRITICAL: Follow these rules when a tool call fails. Do NOT refuse to retry based on previous errors.**

### "connection refused" on port 8931
**Cause:** Playwright MCP is still starting inside the sidecar container (takes 2-5 seconds after session creation).
**Action:** Wait 5 seconds and retry. The startup delay is normal. **Do not tell the user "the tool is unavailable" — it will be ready momentarily.**

### "disabled; set HELMDECK_*_ENABLED=true"
**Cause:** The optional service overlay (Firecrawl, Docling) is not running.
**Action:** Tell the user exactly what the error says — which env var to set and which compose overlay file to bring up. Quote the error message. Do not try alternative tools.

### "session_unavailable" or "engine has no session executor"
**Cause:** The pack needs a browser/desktop session. This is usually automatic.
**Action:** Retry the call. If it persists, tell the user the session runtime may not be configured.

### "vault: credential not found" or "vault: NAME not found"
**Cause:** The pack needs a credential that isn't stored in the vault yet.
**Action:** Tell the user to add the credential via the Management UI:
- Go to `http://localhost:3000` → **Credentials** panel → **Add Credential**
- Provide the name, type (usually `api_key`), host pattern, and the credential value

### "egress denied"
**Cause:** The target URL resolves to a blocked IP range (metadata, RFC 1918, loopback).
**Action:** Tell the user to add the destination to `HELMDECK_EGRESS_ALLOWLIST` in their `.env.local` if the access is intentional.

### Non-zero exit codes from internal tools (ffmpeg, marp, xdotool)
**Cause:** The tool inside the sidecar container failed.
**Action:** Quote the stderr output in your response so the user can debug. Common causes: missing fonts, file not found, invalid input format.

### "model returned no choices" or "no parseable JSON"
**Cause:** The LLM gateway returned an empty or malformed response.
**Action:** This is a model-side issue, not a helmdeck bug. Try a different model or simplify the prompt.

---

## Session chaining

Some packs share a session container for multi-step workflows. The key field is `_session_id`.

**Pattern:**
1. Call `repo.fetch` → returns `{clone_path, session_id}`
2. Pass `_session_id: "<session_id from step 1>"` to every follow-up call
3. Follow-up packs: `fs.read`, `fs.write`, `fs.list`, `fs.patch`, `fs.delete`, `cmd.run`, `git.commit`, `git.diff`, `git.log`, `repo.push`, `content.ground`

**Rules:**
- Always use the SAME `_session_id` for all steps in a workflow
- Sessions persist for 5 minutes after the last call (watchdog cleanup)
- `repo.fetch` creates the session; other packs reuse it
- If a session expires, call `repo.fetch` again to create a new one

**Example workflow:**
```
repo.fetch → fs.list → fs.read → fs.patch → git.diff → git.commit → repo.push
```
All calls after repo.fetch pass `_session_id` and `clone_path` from the first result.

---

## When to create a GitHub issue

You have access to `github.create_issue`. Use it to report **real bugs** in helmdeck.

### DO create an issue when:
- A pack returns error code `internal` (this is a helmdeck bug, not a user error)
- A tool call returns malformed JSON that doesn't match the documented output schema
- The same error persists after 3 retries with different inputs
- A pack silently returns empty output when the input was valid

### DON'T create an issue when:
- An overlay is disabled (`HELMDECK_*_ENABLED` not set) — this is a configuration issue
- A vault key is missing — this is a setup issue
- The model returns unparseable output — this is an LLM issue, not helmdeck
- The error message already tells the user exactly what to do

### Issue format:
Use `github.create_issue` with:
- `repo`: `tosin2013/helmdeck`
- `title`: `[pack-name] Brief description of the bug`
- `body`: Include the pack name, sanitized input (redact credentials), full error message, and steps to reproduce
- `labels`: `["bug", "area/packs"]`

---

## Developer guidance

For developers working on the helmdeck codebase:

### Project structure
- Pack implementations: `internal/packs/builtin/` — one `.go` file per pack
- Pack engine: `internal/packs/packs.go` — execution pipeline, schema validation
- Gateway adapters: `internal/gateway/` — Anthropic, OpenAI, Gemini, Ollama, Deepseek
- Vision pipeline: `internal/vision/vision.go` — Step, StepNative, computer-use dispatch
- Desktop REST: `internal/api/desktop.go` — xdotool/scrot endpoints
- Session runtime: `internal/session/docker/runtime.go` — container lifecycle
- MCP server: `internal/api/mcp_server.go` + `mcp_sse.go` — tool exposure to clients
- Audit: `internal/audit/audit.go` — structured event logging

### Testing patterns
- Table-driven tests with `fakeRuntime`, `recordingExecutor`, `scriptedDispatcher` stubs
- `httptest.NewServer` for external API mocks (Firecrawl, ElevenLabs, Playwright MCP)
- Pack handlers tested directly via `ExecutionContext` (no engine needed for unit tests)
- Run: `go test ./...` before committing

### Validation
- `scripts/validate-phase-6-5.sh` — direct REST pack validation
- `scripts/validate-openclaw.sh` — agent round-trip validation via OpenClaw
- `docs/integrations/pack-demo-playbook.md` — manual LLM prompt walkthrough

### Architecture decisions
- ADR documents: `docs/adrs/` — read the relevant ADR before modifying a subsystem
- ADR 035 covers the "host, don't rebuild" architecture (Firecrawl, Docling, Playwright MCP)
- ADR 035 §2026 revision covers native computer-use tool routing (T807f)

### Contributing
- Create a branch, make changes, run `go test ./...`, open a PR
- Pack count is tracked in `docs/PACKS.md` — update when adding new packs
- Milestones tracked in `docs/MILESTONES.md` — update task status when completing work
