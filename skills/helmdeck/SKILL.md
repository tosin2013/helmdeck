---
name: helmdeck
description: Use helmdeck's 40 capability packs (browser, web scraping, content grounding, podcast/slide/blog/video production, image generation, repo orientation, filesystem, git, GitHub, HTTP, vision, document OCR/parse, Python/Node execution) via MCP — all prefixed `helmdeck__*` in the tool catalog.
metadata:
  openclaw:
    skillKey: helmdeck
    helmdeckVersion: "v0.12.0"
    source: https://github.com/tosin2013/helmdeck/blob/main/skills/helmdeck/SKILL.md
---

<!-- This SKILL.md is the canonical helmdeck agent skill. Stamped at
     helmdeck v0.13.0 (#200 added hyperframes.render, bringing the
     in-tree catalog to 40 packs). Re-run scripts/configure-openclaw.sh
     after any helmdeck release so your OpenClaw agent picks up new
     packs and updated decision tables. -->

## You are connected to helmdeck

Helmdeck is a browser automation and AI capability platform. You have access to 40 tools exposed as MCP tools. Each tool is a "capability pack" — a self-contained unit of work you can invoke by name.

## Pack catalog

### Browser
- `browser.screenshot_url` — Take a screenshot of any URL. Returns a PNG artifact.
- `browser.interact` — Execute deterministic browser actions (click, type, extract, assert, screenshot) in sequence against a **HEADLESS** Chromium via CDP. **Not visible on the desktop** — operators watching via noVNC see nothing. Use when speed + determinism matter and nobody's watching. When the user IS watching, see "Driving the visible desktop" below.

### Web
- `web.scrape_spa` — Scrape a page using CSS selectors. Requires selector knowledge.
- `web.scrape` — Scrape any URL to clean markdown. No selectors needed. **Requires Firecrawl overlay.**
- `web.test` — Natural-language browser testing. Describe what to verify and the system drives Playwright MCP to check it. **Requires Firecrawl overlay + LLM model.**

### Research & Content
- `research.deep` — Search a topic, scrape sources, synthesize an answer. **Use keywords, not full questions** (e.g. "WebAssembly performance" not "what is WebAssembly"). Default limit is 5. **Requires Firecrawl overlay.**
- `content.ground` — Extract claims from markdown and insert source citation links. **Two modes:** pass `text` directly (no session needed) OR pass `clone_path` + `path` for a file in a cloned repo. Always use the `text` field when the user provides markdown inline — do NOT ask for a file path. Produces a downloadable `grounded.md` artifact. **Requires Firecrawl overlay.**

### Slides
- `slides.render` — Convert Marp markdown to PDF, PPTX, or HTML. **Theme picking** (#202): prefer one of the helmdeck-shipped curated themes for any deck that needs custom styling — `theme: helmdeck-dark` (modern technical/conference look) or `theme: helmdeck-corporate` (business/exec deck). Both declare colors for every nested element type (`section`, `h1`-`h6`, `p`, `a`, `table`, `th`, `td`, `code`, `pre`, `blockquote`) so the common LLM mistake of changing `section { background }` without restyling tables/code/blockquotes can't happen. **Authoring custom CSS?** Follow the WCAG-AA rule of thumb: body text needs **4.5:1** contrast against its background. When you override `section { background }` in a `style:` block, you MUST also override `table, th, td { background-color, color }`, `code { ... }`, `blockquote { ... }`, `pre { ... }` — otherwise the previous theme's table colors will glare through against the new background. The pack runs a static lint and surfaces problems via a `warnings: [{rule, selector, recommendation}]` array in the response; check it on every render and re-render if violations are flagged. Marp built-ins (`default`, `gaia`, `uncover`) are also safe — they're already tuned for contrast.
- `slides.narrate` — Convert Marp markdown to a narrated MP4 video with ElevenLabs TTS and YouTube metadata. Speaker notes (`<!-- ... -->`) become narration. **CRITICAL: Pass the markdown EXACTLY as the user provides it — preserve `---` slide delimiters, `<!-- -->` HTML comments, and newlines. Do NOT escape or strip any formatting.** The markdown field must start with `---\nmarp: true\n---` frontmatter.
  - **Resource scaling**: encoding is sequential, so memory is bounded per-segment — not per-deck. Slide count scales **time** (~10-30s per slide) and **disk in `/tmp`** (~30-50 MB per segment MP4 until concat); it does **NOT** scale memory. The memory knob is `resolution`: default `1920x1080` needs ~1.1 GB for ffmpeg + ~700 MB for the Chromium baseline, which the session's 2 GB cap covers. Larger resolutions (e.g. `3840x2160`) may OOM — drop to `1280x720` if the user reports exit 137 from ffmpeg. Decks of 20-25 slides at 1080p are the tested default; anything much longer just takes longer, not more memory.
  - **Duration & YouTube optimization**: each slide's on-screen time = length of its TTS audio (slides without speaker notes get `default_slide_duration`, default 5s). ElevenLabs runs at ~150-160 wpm, so **1 word of speaker notes ≈ 0.4s of video**. Total length = sum of per-slide TTS durations (returned as `total_duration_s` in the output). Targets for a 20-25 slide deck:
    - **<30 words/slide** → <4 min video (too short for YouTube; feels thin)
    - **30-60 words/slide** → 4-7 min (short-form)
    - **80-120 words/slide** → **8-12 min (YouTube sweet spot; unlocks mid-roll ads at ≥8 min and keeps retention for tutorial content)**
    - **150-200 words/slide** → 15-20 min (long-form, viable for deep-dive content)
    - **250+ words/slide** → 25+ min (risky on retention unless the content is dense / entertainment-grade)
  When the user asks for a video about topic X without specifying a target length, default to **~100 words per slide** aiming for the 8-12 min sweet spot. When the user says "make me a 10-minute video from N slides," compute `target_words_per_slide ≈ (600 / N) * (150/60) ≈ 1500/N` and shape speaker notes to that word count. Trust `total_duration_s` in the result — that's the authoritative timing after ElevenLabs has actually synthesized.

### GitHub
- `github.create_issue` — Create an issue on a GitHub repo.
- `github.list_issues` — List issues with filters.
- `github.list_prs` — List pull requests.
- `github.post_comment` — Comment on an issue or PR.
- `github.create_release` — Create a GitHub release.
- `github.search` — Search code, issues, or repos.

### Blog
- `blog.publish` — Publish a post to a Ghost blog (live Admin API) OR write rendered markdown/HTML to the helmdeck artifact store. Two body modes: pass `body` directly OR pass `prompt+model` and the pack expands the body via the gateway LLM. Two formats: `markdown` (default; rendered via goldmark when Ghost wants HTML) or `html` (passes through). Vault credential `ghost-admin-key` (id:hexsecret) for Ghost destination. Composes naturally with `research.deep` (find sources) → `content.ground` (cite sources in the body) → `blog.publish` (ship it).

### Podcast
- `podcast.generate` — Multi-speaker (1..N) podcast MP3 from a script, prompt, OR long-form content (URL/text). Speakers are a `{name: voice_id}` map; the same pack handles solo monologue and multi-host dialogue. Five closed-set `theme`s bake in podcast best practices: `interview`, `debate`, `news-roundup`, `deep-dive`, `solo-essay`. Day 1 uses ElevenLabs (vault `elevenlabs-key`); the `engine` field is reserved for future TTS providers. **Critical**: when using prompt or source modes, the agent supplies the speakers map upfront (with voice IDs) — the pack tells the LLM which speaker names to use. `generate_cover_prompt: true` returns an image-gen prompt for cover art; v0.12.0+ also accepts `cover_image: true` to chain image.generate directly and return a `cover_image_artifact_key`. Composes with `research.deep` → `podcast.generate` (theme `news-roundup` or `deep-dive`) for evidence-grounded shows.

### Image
- `image.generate` — Text → image via fal.ai (`fal-ai/flux/schnell` default, ~$0.003/image, 1-3s). Vault `fal-key` or `HELMDECK_FAL_KEY`. 1-4 images per call. Use for podcast covers, slide shields, blog hero images. The `engine` field is `"fal"` only day 1; Replicate is reserved for a community PR. Pair with `podcast.generate`'s `generate_cover_prompt: true` to chain prompt → cover art in two pack calls — or use the v0.12.0 chained inputs (`cover_image`, `hero_image_prompt`, `feature_image_artifact_key`) on the content packs to skip the intermediate step entirely.

### Video
- `hyperframes.render` — HTML/CSS/JS composition → deterministic MP4 via Chromium BeginFrame + ffmpeg (upstream [hyperframes CLI](https://github.com/heygen-com/hyperframes)). Sizing is composable: `resolution` (1080p / 4k) × `aspect_ratio` (`16:9` standard, `9:16` Shorts/TikTok/Reels, `1:1` IG feed). Six supported tuples map to the upstream CLI's `--resolution` presets (`landscape` / `portrait` / `square` ± `-4k`). **Author the composition at the target aspect ratio** — upstream's resolution flag is an integer-multiple upscale knob, not a dimension setter. Two modes with NO handler branching: composition has no `<audio>` tag → silent animation; composition has an inline `<audio src>` → MP4 carries audio. **Chained workflow**: call `podcast.generate` first, embed the returned presigned audio URL as the composition's `<audio src>`, then `hyperframes.render` produces a narrated video. **Short-form only** (≤12 min, 512 MiB cap); larger compositions return CodeHandlerFailed pointing at #201 for the long-form streaming track. Runs inside the `helmdeck-sidecar-hyperframes` image (env override `HELMDECK_SIDECAR_HYPERFRAMES`).

### Repository
- `repo.fetch` — Clone a git repo into a session. Returns `clone_path`, `session_id`, **and a context envelope** (`tree`, `readme`, `entrypoints`, `signals`) so you can orient immediately without follow-up calls. See "Repo discovery pattern" below.
- `repo.map` — Return a symbol-level structural map (functions, types, classes) of a cloned repo, budgeted to a token target. Opt-in follow-on for code-understanding tasks; inspired by Aider's repo-map.
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

### Desktop & Vision (operate the VISIBLE desktop — operator can watch via noVNC)
- `desktop.run_app_and_screenshot` — Launch an app on the visible XFCE4 desktop. Chromium is **already pre-launched**; use this for any OTHER app (xterm, file manager). Returns a post-launch screenshot.
- `vision.click_anywhere` — AI-driven click on the visible desktop: describe the target ("the URL bar", "the Sign In button") and a vision model clicks it via xdotool. Loops until the goal is reached.
- `vision.extract_visible_text` — Screenshot the visible desktop + ask a vision model to transcribe every readable piece of text. Useful for "what's on the screen now" and verifying prior actions.
- `vision.fill_form_by_label` — Fill form fields on the visible desktop by matching label text, typing via xdotool.

**There are also 16 low-level `desktop.*` REST primitives** exposed at `/api/v1/desktop/*` — `screenshot`, `click`, `type`, `key`, `launch`, `windows`, `focus`, `double_click`, `triple_click`, `drag`, `scroll`, `modifier_click`, `mouse_move`, `wait`, `zoom`, `agent_status`. Use these when you want deterministic step-by-step control without vision-model latency. You know the pixel coordinates (from `vision.extract_visible_text` or a prior `desktop.screenshot`); drive precisely.

### Language
- `python.run` — Execute Python code in an isolated container.
- `node.run` — Execute Node.js code in an isolated container.

### Async wrappers (for long-running packs)
- `pack.start` — Start any pack asynchronously. Returns `{job_id, state, started_at}` immediately. Use for heavy packs to avoid client-side `-32001 Request timed out` errors.
- `pack.status` — Poll the state of a `pack.start` job. Returns `{state, progress, message}`. Poll every 2-5 seconds. State transitions: `running` → `done` or `failed`.
- `pack.result` — Retrieve the final result of a completed async job. Errors with `not_ready` if the job is still running. Job results are kept for 1 hour after completion.

### Operator-supplied subprocess packs (`cmd.*`, v0.12.0)
Operators can drop executables into `$HELMDECK_COMMAND_PACKS_DIR` to register additional packs under the `cmd.*` namespace. Protocol: stdin = your input JSON, stdout = the response JSON, non-zero exit = `handler_failed` with stderr surfaced. The catalog above lists only built-in packs; check `tools/list` (or `helmdeck://packs`) at runtime for the operator's custom ones.

---

## MCP resources

Beyond packs, helmdeck exposes read-only resources for catalog discovery. Use `resources/list` to enumerate, `resources/read` to fetch.

- `helmdeck://packs` — Live pack catalog. Equivalent to `tools/list` but as a browsable resource.
- `helmdeck://sessions` — Live session list (id, status, image, created_at).
- `helmdeck://voices` — ElevenLabs voice catalog (id, name, labels, preview URL) for `podcast.generate`'s `speakers` and `slides.narrate`'s `voice_id`. Requires `elevenlabs-key` in the vault.
- `helmdeck://image-models` (v0.12.0 #158) — Curated fal.ai model catalog for `image.generate` and the chained image inputs (`cover_image_model`, `hero_image_model`). Each entry has cost, p50 latency, max resolution, capabilities. **Read this before picking a non-default model** so you understand cost/quality trade-offs.

## Chained image generation (v0.12.0 #146)

Four content packs can auto-generate cover/hero/feature artwork without a separate `image.generate` call:

- `podcast.generate` — `cover_image: true` emits `cover_image_artifact_key`.
- `slides.render` — `hero_image_prompt: "<text>"` inlines the PNG before slide 1.
- `slides.narrate` — `hero_image_prompt: "<text>"` inlines INTO slide 1 (so the per-slide TTS pipeline still sees content).
- `blog.publish` — `feature_image_artifact_key: "<key>"` OR `hero_image: true`. For Ghost, uploads to `/images/upload/` then stamps `feature_image`.

Use the chained inputs when the cover is part of the same call. Call `image.generate` separately when iterating on the cover, reusing one image across packs, or using different models per pack.

---

## Driving the visible desktop (when the operator is watching)

**Helmdeck runs two parallel browser-automation surfaces. Pick the right one for the task.**

| Surface | Where Chromium runs | Operator sees it? | Speed | When to use |
|---|---|---|---|---|
| `browser.interact`, `browser.screenshot_url`, `web.scrape*` | Headless Chromium driven via CDP (port 9222) | ❌ No — invisible | Fast, deterministic | Automated scraping, scheduled jobs, anywhere nobody is watching |
| `vision.*` packs + `desktop.*` REST primitives | Visible Chromium on the XFCE4 desktop (Xvfb display `:99`) | ✅ Yes — via noVNC | Slower per action | When the user wants to watch, or when the task is fundamentally "drive this UI like a human" |

**Every helmdeck desktop-mode session boots with Chromium already launched on the XFCE4 display.** You don't need to open it. You CAN'T find it on a taskbar — XFCE4 has one but it's the wrong mental model. Just start clicking: the Chromium window is already visible at startup.

### Decision table

| User's ask | Pick |
|---|---|
| "Scrape X and give me the data" | `web.scrape` (if Firecrawl overlay is up) or `browser.interact` — headless, fast |
| "Search for X and tell me what's on the page" | `browser.interact` with actions `[navigate, type, key Enter, screenshot]` — headless is fine because the answer is the data, not the experience |
| "Go to this site and click around so I can watch" | `vision.click_anywhere` + `vision.extract_visible_text` or the `desktop.*` REST primitives — operator is watching via noVNC |
| "Log into my account and fill out this form" (operator wants to verify) | `vision.fill_form_by_label` with `_session_id` of the operator's desktop-mode session |
| "Take this screenshot of a specific URL for a blog post" | `browser.screenshot_url` — headless is optimal |
| "Use GIMP / LibreOffice / some other GUI app" | `desktop.run_app_and_screenshot` to launch, then `vision.click_anywhere` or `desktop.*` primitives to drive |

### Desktop-interaction primitive vocabulary

The 16 `desktop.*` REST endpoints are the OS-action vocabulary. Mirror Anthropic's `computer_20251124` schema + Gemini computer-use conventions. Coordinates are pixel-based on the fixed 1920×1080 Xvfb display:

`screenshot`, `click (button=left|right|middle)`, `double_click`, `triple_click`, `type`, `key (keysym like 'Return', 'ctrl+a')`, `scroll (direction=up|down|left|right, amount=N)`, `drag`, `mouse_move`, `modifier_click (modifiers=[shift|ctrl|alt|super])`, `wait (seconds, ≤30)`, `zoom (crop region)`, `launch (command+args)`, `windows (list X11 windows)`, `focus (windowId)`, `agent_status (for noVNC witness banner)`.

**Loop shape**: call `desktop.screenshot` → decide next action from the pixels → call the action primitive → repeat. For natural-language targeting ("click the blue Sign In button"), `vision.click_anywhere` wraps that screenshot-to-coordinates loop for you — cheaper on round trips when the model is tool-capable.

---

## Long-running packs — three paths, in priority order

Some packs do heavy work that takes 60-120+ seconds (especially with open-weight models). Calling them synchronously through MCP TS-SDK clients (which OpenClaw is built on; default 60s per-request JSON-RPC timeout) returns `MCP error -32001: Request timed out` even though the work is still running fine on the server.

**Heavy packs that need special handling:**
- `slides.narrate` — wall-clock **scales with slide count**: roughly 30-60s per slide (ElevenLabs TTS + per-segment 1080p ffmpeg). A 20-slide deck is typically 10-20 minutes end-to-end; a 5-slide teaser is 2-5 minutes. The pack's session timeout is 30 minutes; decks with >40 slides or 4K resolution may need a longer override. Tell the user the ballpark upfront so they know to expect it.
- `research.deep` with `limit > 3` — search + scrape + synthesize is 30-90s
- `content.ground` with `rewrite: true` — multiple LLM passes can run 60-120s
- Any future pack the user describes as "long" or "heavy" (book writing, multi-chapter generation, large batch operations)

These three packs are now marked `Async: true` server-side, which means **a normal `tools/call` no longer blocks** — it returns a SEP-1686 task envelope in milliseconds. The server then runs the pack in a background goroutine. There are three ways to retrieve the result, listed in order of preference:

### Path 1 — SEP-1686 `tasks/get` polling (most clients)

The server's response carries a task ID in `_meta.modelcontextprotocol.io/related-task.taskId`. SEP-1686-aware MCP SDKs auto-poll `tasks/get` under the hood and surface the eventual result to the LLM as if it were a normal sync return. **You don't have to do anything** — just call the pack the normal way; the SDK handles polling. If the SDK doesn't speak SEP-1686 yet, fall through to Path 2.

### Path 2 — Manual `pack.start` / `pack.status` / `pack.result` polling (universal fallback)

If the user reports "I called slides.narrate and got -32001," the client SDK isn't doing the polling for you. Manually use the trio:

1. Call `pack.start` with `{pack: "<name>", input: {...}}`. Returns `{job_id, state: "working"}`.
2. Loop: call `pack.status({job_id})` every 2-5 seconds. State transitions: `working` → `completed` or `failed`. Surface the `progress` and `message` fields to the user.
3. When `state == "completed"`, call `pack.result({job_id})` to retrieve the full pack output. When `state == "failed"`, `pack.result` returns the error.

### Path 3 — Webhook push (no polling at all)

If the user has a webhook receiver wired up (commonly: the bundled `helmdeck-callback` service from `examples/webhook-openclaw/`), pass `webhook_url` and `webhook_secret` in the pack's input arguments:

```
slides.narrate({
  markdown: "---\nmarp: true\n---\n# Hello",
  metadata_model: "openrouter/auto",
  webhook_url: "http://helmdeck-callback:8080/done",
  webhook_secret: "<secret-from-the-user>"
})
```

The pack returns a SEP-1686 task envelope immediately; when the work completes (minutes to tens of minutes later, depending on the pack — see the wall-clock estimates in the "heavy packs" list above), helmdeck POSTs the result to the webhook URL, which re-injects it into the chat as a fresh system message. **You'll see the result arrive as new context on a future turn — don't poll, don't wait, just acknowledge and let the user drive the next action.**

The user explicitly opts in by giving you a webhook_url + webhook_secret; never invent these on your own.

### Quick decision

| Situation | Path |
|---|---|
| Normal `tools/call` for a heavy pack returns task envelope | Path 1 (the SDK is handling it; do nothing) |
| Normal `tools/call` returned `-32001` | Path 2 (use pack.start/status/result manually) |
| User provided a webhook_url | Path 3 (pass it through; don't poll) |

**For short packs (`browser.screenshot_url`, `web.scrape`, `github.*`, `fs.*`)** — keep calling them directly. The whole task envelope/webhook story only applies to packs marked `Async: true` server-side.

---

## Pack composition — you are a creative agent

You are not limited to calling one pack per user request. **You can and should compose packs** to accomplish complex goals:

- **"Create a pitch deck video"** → YOU write the Marp markdown with speaker notes → call `slides.narrate` → video + YouTube metadata
- **"Write a blog post with sources"** → YOU write the prose → call `content.ground` with `rewrite: true` → grounded blog artifact
- **"Research a topic and present it"** → call `research.deep` → YOU format the synthesis as a Marp deck → call `slides.narrate`
- **"Generate code, test it, commit it"** → call `repo.fetch` → call `fs.write` → call `cmd.run` → call `git.commit` → call `repo.push`

When composing, YOU generate the creative content (slides, blog text, code) and the packs handle the production work (rendering, narration, grounding, committing). Do not ask the user to provide content you can generate yourself.

---

## Default model selection

Several packs require a `model` parameter (web.test, research.deep, content.ground, slides.narrate, vision.*). When the user does not specify a model:

- **Use `openrouter/auto`** as the default — it routes to the best available model automatically
- Do NOT ask the user "which model?" — just use the default and proceed
- If `openrouter/auto` fails, try `openai/gpt-4o-mini` as a fallback
- The user can always override by specifying a model in their prompt

---

## Error handling rules

**CRITICAL: Follow these rules when a tool call fails. Do NOT refuse to retry based on previous errors.**

### General rule: ALWAYS show the error
When ANY tool call fails, you MUST:
1. **Show the exact error code and message** in your response — never say "an error occurred" without the details
2. **Diagnose it** using the rules below
3. **Offer to file a GitHub issue** if it looks like a bug (see "When to create a GitHub issue" section below)
4. If you're working with a developer, show the full stderr / error payload so they can debug

### HTTP 401 "missing_bearer" or "token expired"
**Cause:** The JWT used to authenticate with helmdeck has expired (default TTL is 12 hours).
**Action:** Tell the user to re-mint the JWT and update the MCP server config. For OpenClaw: `openclaw-cli mcp set helmdeck '{"url":"...","headers":{"authorization":"Bearer NEW_TOKEN"}}'`

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

## Repo discovery pattern

When you call `repo.fetch`, the response carries a **context envelope** designed to eliminate the "is the repo empty?" question on the first turn. Use it before reaching for `fs.list` or `fs.read`:

- `tree` — array of file paths (`git ls-files` output, sorted, capped at 300). If `tree_truncated: true`, narrow with `fs.list` + a glob from `doc_hints`.
- `readme` — auto-detected top-level README. Matches `.md`, `.adoc`, `.rst`, `.txt` case-insensitively. **If `readme.content` is populated, the repo is NOT empty.** Never respond "the repo appears empty" when a README was surfaced.
- `entrypoints` — known orientation files (`Makefile`, `package.json`, `go.mod`, `CLAUDE.md`, `AGENTS.md`, etc.) with a `kind` classifier. Read these first when you need to understand how the repo builds or runs.
- `doc_hints` — static glob suggestions for `fs.list` (`docs/**/*.md`, `content/**/*.adoc`, etc.). No computation on the server side — just a prompt hint.
- `signals` — coarse classifier you can branch on in one check:
  - `has_readme` — a README was found (its content is in `readme.content`).
  - `has_docs_dir` — any of `docs/`, `doc/`, `content/`, `site/`, `book/`, `guide/`, `tutorials/`, `blog-posts/`, `examples/` exists at repo root.
  - `has_code` — any of `src/`, `cmd/`, `lib/`, `internal/`, `pkg/`, `app/` exists, OR at least one common source file (`.go`, `.py`, `.js`, `.ts`, `.rs`, `.java`, `.c`, `.cpp`, `.rb`) exists.
  - `doc_file_count` / `code_file_count` — raw counts of `.md`/`.adoc`/`.rst` docs vs. common source files.
  - `sparse` — `true` when `doc_file_count + code_file_count < 3`. Treat as "this repo looks barely-populated; confirm with user before proceeding."

### Branching on `signals`

Use this decision table after every `repo.fetch`:

| `signals` shape | What the agent should do next |
|---|---|
| `has_readme: true` | Repo is NOT empty. Read `readme.content` and proceed with the task. |
| `has_readme: false`, `has_docs_dir: true` | No top-level README but docs exist in a subdirectory. Use `doc_hints` with `fs.list` to find them. |
| `has_readme: false`, `has_docs_dir: false`, `has_code: true` | Code-only repo. Call `repo.map` to get a symbol-level map instead of reading files blindly. |
| `sparse: true` (or all three `has_*` flags false) | The repo genuinely lacks material. **Do NOT say "the repo is empty" and give up.** Surface what you observed ("I see N files but no README, docs, or recognizable source tree") and ask the user whether the URL is correct, whether to look at a specific branch/subpath, or what they want extracted. |

### When to call `repo.map`

Use `repo.map` when the task requires understanding code structure — e.g. "where is `FooHandler` defined?", "summarize the API surface", "rename this function across the codebase." It takes a `token_budget` (default 1500) and returns a ranked list of files with their top symbols.

**Do NOT call `repo.map` for docs-heavy tasks** (blog posts, presentations, tutorials) — it adds latency for no benefit. The `repo.fetch` envelope already tells you where the docs live.

```json
{
  "pack": "repo.map",
  "input": {
    "_session_id":  "<from repo.fetch>",
    "clone_path":   "<from repo.fetch>",
    "token_budget": 1500,
    "include_globs": ["*.go", "*.py"]
  }
}
```

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
