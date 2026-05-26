---
title: Pack prompt templates
description: Copy-and-fill prompt templates for every helmdeck capability pack, grouped by family. Replace the {{VARIABLES}} and paste into your MCP client.
keywords: [helmdeck, packs, prompt templates, MCP, copy-paste, variables]
---

# Pack prompt templates

One template per capability pack. Replace every `{{VARIABLE}}` and paste into
your MCP client; the agent picks the tool. Variables map to each pack's
`InputSchema` (see [per-pack reference](/reference/packs/) for full schemas,
defaults, and error codes). Session-scoped packs (`fs.*`, `cmd.run`, `git.*`,
`repo.*`) need a `clone_path` + `_session_id` from a prior `repo.fetch` in the
same conversation.

---

## Browser

#### `browser.screenshot_url` — screenshot a URL

**Template**
```
Use helmdeck to take a screenshot of {{URL}}.
```

**Variables**
- `{{URL}}` — page to capture (input `url`, required).

#### `browser.interact` — deterministic browser actions

**Template**
```
Use helmdeck__browser-interact against {{URL}} with these actions: {{ACTIONS}}.
```

**Variables**
- `{{URL}}` — starting page (input `url`, required).
- `{{ACTIONS}}` — JSON array of steps, e.g. `[{"action":"extract","selector":"h1","format":"text"},{"action":"screenshot"}]` (input `actions`, required).

---

## Web

#### `web.scrape` — scrape a URL to clean markdown

**Template**
```
Use helmdeck__web-scrape to scrape {{URL}} to markdown.
```

**Variables**
- `{{URL}}` — page to scrape (input `url`, required).

**Notes** — needs the Firecrawl overlay.

#### `web.scrape_spa` — render a SPA and extract fields by CSS selector

**Template**
```
Use helmdeck__web-scrape-spa on {{URL}} and extract these fields: {{FIELDS}}.
```

**Variables**
- `{{URL}}` — page to render (input `url`, required).
- `{{FIELDS}}` — `{name: cssSelector}` map of fields to extract (input `fields`, required).

**Notes** — needs the Firecrawl overlay.

#### `web.test` — natural-language browser test

**Template**
```
Use helmdeck__web-test on {{URL}}: {{INSTRUCTION}}. Assert: {{ASSERTIONS}}.
```

**Variables**
- `{{URL}}` — page under test (input `url`, required).
- `{{INSTRUCTION}}` — what to do in plain English (input `instruction`, required).
- `{{ASSERTIONS}}` — list of things that must hold (input `assertions`, optional).
- `{{MODEL}}` — model id (input `model`, required; default `openrouter/auto`).

---

## Research & Content

#### `research.deep` — search + synthesize a topic

**Template**
```
Use helmdeck__research-deep to research {{QUERY}} and give me a synthesis with sources.
```

**Variables**
- `{{QUERY}}` — topic to research (input `query`, required).
- `{{MODEL}}` — model id (input `model`, required; default `openrouter/auto`).

**Notes** — needs the Firecrawl overlay; async.

#### `content.ground` — fact-check + rewrite markdown against sources

**Template**
```
Use helmdeck__content-ground to fact-check and rewrite this markdown, citing sources:
{{MARKDOWN}}
```

**Variables**
- `{{MARKDOWN}}` — the text to ground (input `text`; or pass `clone_path`+`path` for a file).
- `{{TOPIC}}` — optional topic hint to focus sourcing (input `topic`, optional).
- `{{MODEL}}` — model id (input `model`, required; default `openrouter/auto`). `rewrite:true` produces the rewritten `grounded_text`.

**Notes** — needs the Firecrawl overlay; async.

---

## Slides

#### `slides.render` — Marp deck → PDF/PPTX/HTML

**Template**
```
Use helmdeck__slides-render to render this Marp markdown as {{FORMAT}}:
{{MARKDOWN}}
```

**Variables**
- `{{MARKDOWN}}` — the Marp deck markdown (input `markdown`, required).
- `{{FORMAT}}` — `pdf` | `pptx` | `html` (input `format`, optional, default `pdf`).
- `{{HERO_IMAGE_PROMPT}}` — optional fal.ai prompt for a slide-1 hero image (input `hero_image_prompt`, optional).

#### `slides.narrate` — Marp deck → narrated MP4

**Template**
```
Use helmdeck__slides-narrate to turn this Marp deck into a narrated video:
{{MARKDOWN}}
```

**Variables**
- `{{MARKDOWN}}` — the Marp deck markdown (input `markdown`, required).
- `{{VOICE_ID}}` — ElevenLabs voice id (input `voice_id`, optional; discover via `helmdeck://voices`).

**Notes** — needs `elevenlabs-key` (or pass `allow_silent_output:true`); async.

---

## GitHub

#### `github.create_issue` — open an issue

**Template**
```
Use helmdeck__github-create-issue to open an issue on {{REPO}} titled "{{TITLE}}" with body: {{BODY}}.
```

**Variables**
- `{{REPO}}` — `owner/repo` (input `repo`, required).
- `{{TITLE}}` — issue title (input `title`, required).
- `{{BODY}}` — issue body (input `body`, optional).
- `{{LABELS}}` — labels (input `labels`, optional).

**Notes** — needs a vault GitHub PAT (`credential`, default `github-token`).

#### `github.create_pr` — open a pull request

**Template**
```
Use helmdeck__github-create-pr on {{REPO}} from {{HEAD}} into {{BASE}}, titled "{{TITLE}}".
```

**Variables**
- `{{REPO}}` — `owner/repo` (required). `{{HEAD}}` — source branch (required). `{{BASE}}` — target branch (required). `{{TITLE}}` — PR title (required). `{{BODY}}` — PR body (optional).

**Notes** — needs a vault GitHub PAT.

#### `github.create_release` — create a release for a tag

**Template**
```
Use helmdeck__github-create-release on {{REPO}} for tag {{TAG}} named "{{NAME}}".
```

**Variables**
- `{{REPO}}` — `owner/repo` (required). `{{TAG}}` — tag (required). `{{NAME}}`/`{{BODY}}` — release name/notes (optional).

**Notes** — needs a vault GitHub PAT.

#### `github.list_issues` — list issues

**Template**
```
Use helmdeck__github-list-issues on {{REPO}} (state {{STATE}}, labels {{LABELS}}).
```

**Variables**
- `{{REPO}}` — `owner/repo` (required). `{{STATE}}` (open/closed/all), `{{LABELS}}`, `{{ASSIGNEE}}` — optional filters.

#### `github.list_prs` — list pull requests

**Template**
```
Use helmdeck__github-list-prs on {{REPO}} (state {{STATE}}).
```

**Variables**
- `{{REPO}}` — `owner/repo` (required). `{{STATE}}`, `{{BASE}}`, `{{HEAD}}` — optional filters.

#### `github.post_comment` — comment on an issue or PR

**Template**
```
Use helmdeck__github-post-comment on {{REPO}} issue #{{ISSUE_NUMBER}} with: {{BODY}}.
```

**Variables**
- `{{REPO}}` — `owner/repo` (required). `{{ISSUE_NUMBER}}` — issue/PR number (required). `{{BODY}}` — comment text (required).

**Notes** — needs a vault GitHub PAT.

#### `github.search` — search code/issues/PRs

**Template**
```
Use helmdeck__github-search to search {{TYPE}} for: {{QUERY}}.
```

**Variables**
- `{{QUERY}}` — search query (required). `{{TYPE}}` — `code` | `issues` | `prs` (optional).

---

## Blog

#### `blog.publish` — render/publish a post

**Template**
```
Use helmdeck__blog-publish to publish a {{FORMAT}} post titled "{{TITLE}}" with body:
{{BODY}}
```

**Variables**
- `{{FORMAT}}` — `markdown` | `html` (input `format`, required). `{{TITLE}}` — title (required).
- `{{BODY}}` — post body (input `body`; or use `prompt`+`model` to generate).
- `{{DESTINATION}}` — `artifact` (default) or `ghost` (optional; Ghost needs `host` + `credential`).

---

## Podcast

#### `podcast.generate` — multi-speaker podcast MP3

**Template**
```
Use helmdeck__podcast-generate to make a podcast from this source text: {{SOURCE_TEXT}}. Speakers: {{SPEAKERS}}.
```

**Variables**
- `{{SPEAKERS}}` — `{name: voice_id}` map (input `speakers`, required; discover voices via `helmdeck://voices`).
- `{{SOURCE_TEXT}}` — content to turn into a show (input `source_text`; or `source_url`, or `prompt`+`model`, or a `script`).
- `{{THEME}}` — `interview`|`debate`|`news-roundup`|`deep-dive`|`solo-essay` (input `theme`, optional).

**Notes** — needs `elevenlabs-key` (or `allow_silent_output:true`); async.

---

## Image

#### `image.generate` — text → image via fal.ai

**Template**
```
Use helmdeck__image-generate to generate an image: {{PROMPT}}.
```

**Variables**
- `{{PROMPT}}` — image description (input `prompt`, required).
- `{{MODEL}}` — fal.ai model (input `model`, optional; discover via `helmdeck://image-models`). `{{NUM_IMAGES}}` — 1-4 (optional).

**Notes** — needs `fal-key` / `HELMDECK_FAL_KEY`.

---

## Stock photography

#### `stock.search` — Pexels stock photos

**Template**
```
Use helmdeck__stock-search to find {{COUNT}} {{ORIENTATION}} photos of {{QUERY}}.
```

**Variables**
- `{{QUERY}}` — search terms (input `query`, required). `{{COUNT}}` (1-4), `{{ORIENTATION}}` (landscape/portrait/square) — optional.

**Notes** — needs `pexels-key` / `HELMDECK_PEXELS_API_KEY`.

---

## Video

#### `hyperframes.render` — HTML/CSS/JS composition → MP4

**Template**
```
Use helmdeck__hyperframes-render to render this composition to MP4 ({{RESOLUTION}}, {{ASPECT_RATIO}}):
{{COMPOSITION_HTML}}
```

**Variables**
- `{{COMPOSITION_HTML}}` — the HTML/CSS/JS composition (input `composition_html`, required; embed an `<audio src>` for narrated video).
- `{{RESOLUTION}}` — `1080p` | `4k` (optional). `{{ASPECT_RATIO}}` — `16:9` | `9:16` | `1:1` (optional).

**Notes** — short-form only (≤12 min, 512 MiB).

---

## Repository

#### `repo.fetch` — clone a git repo into a session

**Template**
```
Use helmdeck__repo-fetch to clone {{REPO_URL}} (ref {{REF}}).
```

**Variables**
- `{{REPO_URL}}` — git URL, HTTPS or SSH (input `url`, required).
- `{{REF}}` — branch/tag/SHA (input `ref`, optional). `{{CREDENTIAL}}` — vault name for private repos (optional).

**Notes** — returns `clone_path` + `session_id`; pass both to follow-on `fs.*` / `cmd.run` / `git.*` / `repo.push`.

#### `repo.map` — symbol-level repo map

**Template**
```
Use helmdeck__repo-map on the cloned repo (clone_path {{CLONE_PATH}}, same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` — from a prior `repo.fetch` (input `clone_path`, required). `{{TOKEN_BUDGET}}` — map size cap (optional).

**Notes** — session-scoped: pass the `_session_id` from `repo.fetch`.

#### `repo.push` — push committed changes back to the remote

**Template**
```
Use helmdeck__repo-push to push the clone at {{CLONE_PATH}} to branch {{BRANCH}} (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` — from `repo.fetch` (required). `{{BRANCH}}` — target branch (optional). `{{CREDENTIAL}}` — vault credential (optional).

**Notes** — session-scoped; needs push credentials in the vault.

#### `swe.solve` — autonomous code-fix agent

**Template**
```
Use helmdeck__swe-solve on {{REPO_URL}} to: {{TASK}}. Mode: {{MODE}}.
```

**Variables**
- `{{REPO_URL}}` — repo to fix (input `repo_url`, required). `{{TASK}}` — the problem statement (input `task`, required).
- `{{MODE}}` — `patch` (default) | `branch` | `pull_request` (optional). `{{CREDENTIAL}}` / `{{MODEL}}` — optional.

**Notes** — never pushes to the default branch; async; produces a trajectory artifact.

---

## Filesystem (session-scoped — need clone_path + _session_id from repo.fetch)

#### `fs.read` — read a file

**Template**
```
Use helmdeck__fs-read to read {{PATH}} from clone_path {{CLONE_PATH}} (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{PATH}}` — file path relative to the clone (required).

#### `fs.write` — write a file

**Template**
```
Use helmdeck__fs-write to write to {{PATH}} in clone_path {{CLONE_PATH}} (same _session_id):
{{CONTENT}}
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{PATH}}` (required) · `{{CONTENT}}` — file contents (required).

#### `fs.list` — list files

**Template**
```
Use helmdeck__fs-list on clone_path {{CLONE_PATH}} with glob {{GLOB}} (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{GLOB}}` — e.g. `*.md` (optional) · `{{PATH}}` — subdir (optional).

#### `fs.patch` — replace literal strings in a file

**Template**
```
Use helmdeck__fs-patch on {{PATH}} in clone_path {{CLONE_PATH}}: replace {{SEARCH}} with {{REPLACE}} (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{PATH}}` (required) · `{{SEARCH}}`/`{{REPLACE}}` — the single edit (or pass an `edits` array).

#### `fs.delete` — delete a file

**Template**
```
Use helmdeck__fs-delete to delete {{PATH}} from clone_path {{CLONE_PATH}} (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{PATH}}` (required).

---

## Shell & Git (session-scoped)

#### `cmd.run` — run a shell command in the clone

**Template**
```
Use helmdeck__cmd-run in clone_path {{CLONE_PATH}} to run: {{COMMAND}} (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{COMMAND}}` — argv array, e.g. `["go","test","./..."]` (required) · `{{STDIN}}` (optional).

#### `git.commit` — stage + commit

**Template**
```
Use helmdeck__git-commit in clone_path {{CLONE_PATH}} with message "{{MESSAGE}}" (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{MESSAGE}}` — commit message (required).

#### `git.diff` — show uncommitted diff

**Template**
```
Use helmdeck__git-diff on clone_path {{CLONE_PATH}} (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{STAGED}}` — staged-only, true/false (optional).

#### `git.log` — recent commit history

**Template**
```
Use helmdeck__git-log on clone_path {{CLONE_PATH}}, last {{COUNT}} commits (same _session_id).
```

**Variables**
- `{{CLONE_PATH}}` (required) · `{{COUNT}}` — number of commits (optional).

---

## HTTP

#### `http.fetch` — HTTP request with vault substitution + egress guard

**Template**
```
Use helmdeck__http-fetch to {{METHOD}} {{URL}} with headers {{HEADERS}} and body {{BODY}}.
```

**Variables**
- `{{URL}}` — request URL (input `url`, required). `{{METHOD}}` (GET/POST/…), `{{HEADERS}}`, `{{BODY}}` — optional. Use `${vault:name}` placeholders for secrets.

---

## Document

#### `doc.parse` — document → clean markdown (Docling)

**Template**
```
Use helmdeck__doc-parse to parse {{SOURCE_URL}} to markdown.
```

**Variables**
- `{{SOURCE_URL}}` — URL of the doc (input `source_url`; or pass `source_b64`). `{{DO_OCR}}` — OCR scanned PDFs, true/false (optional).

**Notes** — needs the Docling overlay.

#### `doc.ocr` — OCR an image (Tesseract)

**Template**
```
Use helmdeck__doc-ocr to extract text from {{SOURCE_URL}} (language {{LANGUAGE}}).
```

**Variables**
- `{{SOURCE_URL}}` — image URL (input `source_url`; or `source_b64`). `{{LANGUAGE}}` — Tesseract lang code (optional).

---

## Desktop & Vision (operate the visible desktop — operator can watch via noVNC)

#### `desktop.run_app_and_screenshot` — launch an app on the desktop + screenshot

**Template**
```
Use helmdeck__desktop-run-app-and-screenshot to launch {{COMMAND}} with args {{ARGS}}.
```

**Variables**
- `{{COMMAND}}` — executable (input `command`, required). `{{ARGS}}` — args array (optional). `{{WAIT_MS}}` — settle delay (optional).

#### `vision.click_anywhere` — find + click a target by description

**Template**
```
Use helmdeck__vision-click-anywhere with goal: {{GOAL}}.
```

**Variables**
- `{{GOAL}}` — what to click, in plain English (input `goal`, required). `{{MODEL}}` — vision model (required; default `openrouter/auto`).

#### `vision.extract_visible_text` — transcribe the screen

**Template**
```
Use helmdeck__vision-extract-visible-text to read everything on the screen.
```

**Variables**
- `{{MODEL}}` — vision model (input `model`, required; default `openrouter/auto`).

#### `vision.fill_form_by_label` — fill a form by matching labels

**Template**
```
Use helmdeck__vision-fill-form-by-label with fields: {{FIELDS}}.
```

**Variables**
- `{{FIELDS}}` — `{label: value}` map (input `fields`, required). `{{MODEL}}` — vision model (required; default `openrouter/auto`).

---

## Language

#### `python.run` — run Python in a container

**Template**
```
Use helmdeck__python-run to run this Python: {{CODE}}.
```

**Variables**
- `{{CODE}}` — Python to execute (input `code`; or pass a `command` array). `{{STDIN}}` / `{{CWD}}` — optional.

#### `node.run` — run Node.js in a container

**Template**
```
Use helmdeck__node-run to run this Node.js: {{CODE}}.
```

**Variables**
- `{{CODE}}` — JS to execute (input `code`; or pass a `command` array). `{{STDIN}}` / `{{CWD}}` — optional.
