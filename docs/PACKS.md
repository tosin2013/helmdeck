# Helmdeck — Built-in Capability Pack Reference

31 packs ship in the control plane binary. All are available as MCP tools (via `/api/v1/mcp/sse` or `/api/v1/mcp/ws`) and as REST endpoints (`POST /api/v1/packs/<name>`).

## Quick reference

| Pack | Session? | Engine | Input (key fields) | Output (key fields) |
| :--- | :---: | :--- | :--- | :--- |
| **Browser** | | | | |
| `browser.screenshot_url` | ✅ | chromedp | `{url}` | `{artifact_key, size}` + PNG artifact |
| `browser.interact` | ✅ | chromedp | `{url, actions[]}` | `{steps_completed, screenshots[], extractions{}, assertions_passed}` |
| **Web** | | | | |
| `web.scrape_spa` | ✅ | chromedp | `{url, fields{name: {selector, format}}}` | `{data{}, missing[]}` |
| `web.scrape` | ❌ | Firecrawl | `{url, formats?, wait_ms?}` | `{markdown, html?, title?, links?, status}` — requires `HELMDECK_FIRECRAWL_ENABLED=true` |
| **Filesystem** | | | | |
| `fs.read` | ✅ | session exec | `{clone_path, path}` | `{content, sha256, size}` |
| `fs.write` | ✅ | session exec | `{clone_path, path, content}` | `{sha256, size}` |
| `fs.list` | ✅ | session exec | `{clone_path, path?, glob?}` | `{files[], count}` |
| `fs.patch` | ✅ | session exec | `{clone_path, path, search, replace}` | `{applied, sha256}` |
| `fs.delete` | ✅ | session exec | `{clone_path, path}` | `{deleted, path}` |
| **Shell** | | | | |
| `cmd.run` | ✅ | session exec | `{clone_path, command[]}` | `{stdout, stderr, exit_code}` |
| **Git** | | | | |
| `git.commit` | ✅ | session exec | `{clone_path, message, all?}` | `{commit}` |
| `git.diff` | ✅ | session exec | `{clone_path, staged?}` | `{diff, files_changed}` |
| `git.log` | ✅ | session exec | `{clone_path, count?}` | `{log, count}` |
| **Repository** | | | | |
| `repo.fetch` | ✅ | session exec + vault | `{url, ref?, depth?, credential?}` | `{clone_path, commit, files, session_id}` |
| `repo.push` | ✅ | session exec + vault | `{clone_path, remote?, branch?, force?, credential?}` | `{url, branch, commit}` |
| **HTTP** | | | | |
| `http.fetch` | ❌ | Go HTTP + vault | `{url, method?, headers?, body?}` | `{status, headers, body}` |
| **GitHub** | | | | |
| `github.create_issue` | ❌ | GitHub REST | `{repo, title, body?, labels?}` | `{number, url, html_url}` |
| `github.list_issues` | ❌ | GitHub REST | `{repo, state?, labels?, assignee?}` | `{issues[], count}` |
| `github.list_prs` | ❌ | GitHub REST | `{repo, state?, head?, base?}` | `{prs[], count}` |
| `github.post_comment` | ❌ | GitHub REST | `{repo, issue_number, body}` | `{id, url}` |
| `github.create_release` | ❌ | GitHub REST | `{repo, tag, name?, body?, draft?}` | `{id, url, upload_url}` |
| `github.search` | ❌ | GitHub REST | `{query, type?}` | `{total_count, items[]}` |
| **Slides** | | | | |
| `slides.render` | ✅ | Marp + Chromium | `{markdown, format}` | `{artifact_key}` + PDF/PPTX artifact |
| **Document** | | | | |
| `doc.ocr` | ✅ | Tesseract | `{image_path}` | `{text}` |
| `doc.parse` | ❌ | Docling | `{source_url OR source_b64+filename, formats?, do_ocr?, ocr_lang?}` | `{source, markdown, text?, html?, status, processing_time}` — requires `HELMDECK_DOCLING_ENABLED=true` |
| **Desktop** | | | | |
| `desktop.run_app_and_screenshot` | ✅ | Xvfb + xdotool | `{command, args?}` | `{artifact_key}` + PNG artifact |
| **Vision** | | | | |
| `vision.click_anywhere` | ✅ | screenshot + LLM | `{target, description}` | `{clicked, coordinates}` |
| `vision.extract_visible_text` | ✅ | screenshot + LLM | `{}` | `{text}` |
| `vision.fill_form_by_label` | ✅ | screenshot + LLM | `{fields{label: value}}` | `{filled}` |
| **Language** | | | | |
| `python.run` | ✅ | Python sidecar | `{code}` | `{stdout, stderr, exit_code}` |
| `node.run` | ✅ | Node sidecar | `{code}` | `{stdout, stderr, exit_code}` |

**Session?** = requires a sidecar container. Packs with `✅` use `_session_id` for session pinning across chained calls.

## Session pinning

Packs that need a session container can be chained via the `_session_id` field:

```
1. repo.fetch → returns {session_id, clone_path}
2. fs.list   {clone_path, _session_id: "<from step 1>"}
3. fs.read   {clone_path, path: "README", _session_id: "<from step 1>"}
4. fs.patch  {clone_path, path: "README", search: "old", replace: "new", _session_id}
5. git.diff  {clone_path, _session_id}
6. git.commit{clone_path, message: "fix", all: true, _session_id}
7. repo.push {clone_path, credential: "github-token", _session_id}
```

`repo.fetch` sets `PreserveSession: true` so its session persists for follow-on packs. All other session packs terminate their session on return unless `_session_id` pins to an existing one. Abandoned sessions are cleaned up by the watchdog after the default 5-minute timeout.

## Credential handling

Packs that access external services use vault-stored credentials via the `credential` field:

- **SSH packs** (`repo.fetch`/`repo.push` with SSH URLs): auto-resolve from vault by host match
- **HTTPS packs** (`repo.fetch`/`repo.push` with HTTPS URLs): pass `"credential": "github-token"` to name a vault entry
- **GitHub packs**: default to vault entry `github-token` if it exists; work without auth for public repo reads
- **HTTP fetch**: use `${vault:NAME}` placeholder syntax in headers/body — the control plane substitutes before sending

## Artifact handling

Packs that produce files (screenshots, PDFs, OCR source images) upload them to the S3-compatible artifact store (Garage). The response includes:

- `artifact_key` — the storage key (e.g. `browser.screenshot_url/abc123-screenshot.png`)
- A signed URL for download (expires in 15 min)

The Artifact Explorer panel at `/artifacts` in the Management UI lists all artifacts with inline image preview and download.

For MCP clients: when the artifact is an image under 1 MB, the MCP response includes a `type: "image"` content block with base64-encoded bytes (T302b) so vision-capable LLMs can see the screenshot in one round trip.

## Upcoming packs

| Pack | Phase | Engine | Description |
| :--- | :--- | :--- | :--- |
| `web.test` | 6.5 | Playwright MCP | Natural language browser testing via accessibility tree |
| `research.deep` | 6.5 | Firecrawl | Crawl + search + synthesize across multiple sources |
| `content.ground` | 6.5 | Composite | Ground a blog post with real links to authoritative sources |

## Source files

All packs live in `internal/packs/builtin/`:

| File | Packs |
| :--- | :--- |
| `browser_interact.go` | `browser.interact` |
| `screenshot_url.go` | `browser.screenshot_url` |
| `scrape_spa.go` | `web.scrape_spa` |
| `web_scrape.go` | `web.scrape` |
| `doc_parse.go` | `doc.parse` |
| `fs_packs.go` | `fs.*`, `cmd.run`, `git.*` |
| `repo_fetch.go` | `repo.fetch` |
| `repo_push.go` | `repo.push` |
| `http_fetch.go` | `http.fetch` |
| `github.go` | `github.*` |
| `slides_render.go` | `slides.render` |
| `doc_ocr.go` | `doc.ocr` |
| `desktop_run_app.go` | `desktop.run_app_and_screenshot` |
| `vision_packs.go` | `vision.*` |
| `python_run.go` | `python.run` |
| `node_run.go` | `node.run` |
