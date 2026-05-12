---
title: Capability pack catalog
description: Reference table for every shipped helmdeck capability pack — input/output schema, session requirement, execution engine, vault credentials. 38 packs total.
keywords: [helmdeck, capability packs, browser automation, web scraping, GitHub, vault, MCP, slides, vision, repo, filesystem]
sidebar_label: PACKS reference
priority: 0.9
changefreq: weekly
---

# Helmdeck — Built-in Capability Pack Reference

38 packs ship in the control plane binary. All are available as MCP tools (via `/api/v1/mcp/sse` or `/api/v1/mcp/ws`) and as REST endpoints (`POST /api/v1/packs/<name>`).

## Quick reference

| Pack | Session? | Engine | Input (key fields) | Output (key fields) |
| :--- | :---: | :--- | :--- | :--- |
| **Browser** | | | | |
| `browser.screenshot_url` | ✅ | chromedp | `{url}` | `{artifact_key, size}` + PNG artifact |
| `browser.interact` | ✅ | chromedp | `{url, actions[]}` | `{steps_completed, screenshots[], extractions{}, assertions_passed}` |
| **Web** | | | | |
| `web.scrape_spa` | ✅ | chromedp | `{url, fields{name: {selector, format}}}` | `{data{}, missing[]}` |
| `web.scrape` | ❌ | Firecrawl | `{url, formats?, wait_ms?}` | `{markdown, html?, title?, links?, status}` — requires `HELMDECK_FIRECRAWL_ENABLED=true` |
| `web.test` | ✅ | Playwright MCP + LLM | `{url, instruction, model, max_steps?, assertions?}` | `{completed, steps[], steps_used, final_snapshot, assertions_passed, reason}` — needs a session whose `playwright_mcp_endpoint` is populated (T807a) |
| `research.deep` | ❌ | Firecrawl + LLM | `{query, model, limit?, max_tokens?}` | `{query, sources[], synthesis, model}` — requires `HELMDECK_FIRECRAWL_ENABLED=true` |
| `content.ground` | ✅ | LLM + Firecrawl | `{clone_path, path, model, max_claims?, topic?}` | `{path, claims_considered, claims_grounded, grounding[], skipped[], sha256, file_changed}` — requires `HELMDECK_FIRECRAWL_ENABLED=true` |
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
| `repo.fetch` | ✅ | session exec + vault | `{url, ref?, depth?, credential?}` | `{clone_path, commit, files, session_id, tree[], tree_total, tree_truncated, readme{path,content,truncated}, entrypoints[], doc_hints[], signals{has_readme,has_docs_dir,has_code,doc_file_count,code_file_count,sparse}}` — context envelope (ADR 022 §2026-04-15 revision) so agents orient on the first turn |
| `repo.map` | ✅ | session exec + ctags + python3 | `{clone_path, token_budget?, include_globs?}` | `{map, tokens_estimated, files_covered, files_total}` — Aider-style structural symbol map (ADR 036) |
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
| `slides.render` | ✅ | Marp + Chromium + mmdc | `{markdown, format, mermaid?, hero_image_prompt?, hero_image_model?}` | `{artifact_key, hero_image_model_used?}` + PDF/PPTX artifact — `mermaid:true` (default) pre-renders ```` ```mermaid ```` fences to inline SVG; `hero_image_prompt` (v0.12.0 #146) chains `image.generate` and base64-inlines the result before slide 1. |
| `slides.narrate` | ✅ | Marp + ElevenLabs + ffmpeg + LLM | `{markdown, voice_id?, model_id?, resolution?, fade_ms?, metadata_model?, hero_image_prompt?, hero_image_model?}` | `{video_artifact_key, video_size, slide_count, total_duration_s, has_narration, voice_used?, metadata_artifact_key?, metadata?, hero_image_model_used?}` — MP4 video with per-slide TTS narration from `<!-- speaker notes -->` + YouTube metadata. `hero_image_prompt` (v0.12.0 #146) inlines a chained hero image INTO slide 1 (no separator, preserves narration). ElevenLabs API key from vault `elevenlabs-key`. |
| **Blog** | | | | |
| `blog.publish` | ❌ | Ghost Admin API + goldmark + LLM | `{destination, format, title, body OR (prompt+model), tags?, status?, published_at?, host?, credential?, feature_image_artifact_key?, hero_image?, hero_image_prompt?, hero_image_model?}` | `{destination, format, body_source, model_used?, hero_image_model_used?}` + ghost: `{post_id, url, html_url, status, published_at, feature_image_url?}` OR artifact: `{artifact_key, size, feature_image_artifact_key?}` — publishes to a Ghost blog (live API) or stores rendered markdown/HTML as a helmdeck artifact. Two body modes (agent supplies body OR prompt+model the pack expands via LLM). Feature image is operator-supplied via `feature_image_artifact_key` OR auto-generated via `hero_image:true` (v0.12.0 #146); Ghost-mode uploads via `/images/upload/` then stamps `feature_image`. Ghost vault credential `ghost-admin-key` (id:hexsecret). |
| **Podcast** | | | | |
| `podcast.generate` | ✅ | ElevenLabs TTS + ffmpeg + LLM (engine-pluggable) | `{speakers, script OR (prompt+model) OR (source_url/source_text+model), engine?, model_id?, theme?, duration_target_min?, silence_between_turns_ms?, generate_cover_prompt?, cover_image?, cover_image_model?}` | `{engine, audio_artifact_key, audio_size, duration_s, speaker_count, turn_count, script_source, model_used?, voices_used, has_narration, theme, cover_image_prompt?, cover_image_artifact_key?, cover_image_model_used?}` — multi-speaker (1..N) podcast MP3. Three input modes: agent-supplied script, prompt+model (LLM generates dialogue), or long-form content (URL/text → LLM converts). Five themes (`interview`/`debate`/`news-roundup`/`deep-dive`/`solo-essay`) bake in podcast best practices. `cover_image:true` (v0.12.0 #146) auto-generates cover artwork via `image.generate`. Day 1: ElevenLabs only (vault `elevenlabs-key`); future engines (PlayHT, Hume.ai, Resemble.ai) slot in via `engine` field. Silent-fallback when key missing. |
| **Image** | | | | |
| `image.generate` | ❌ | fal.ai sync `fal.run` (engine-pluggable) | `{prompt, engine?, model?, image_size?, num_images?, seed?, credential?}` | `{image_artifact_key, image_size, engine, model_used, prompt_used, seed_used?, image_artifact_keys?}` — text → image. Day 1: fal.ai only (vault `fal-key`, `HELMDECK_FAL_KEY`); default model `fal-ai/flux/schnell` (~$0.003/image, 1-3s). 1-4 images per call. `engine` field reserved for Replicate as a community PR. Hard-fails when credential missing. |
| **Document** | | | | |
| `doc.ocr` | ✅ | Tesseract | `{image_path}` | `{text}` |
| `doc.parse` | ❌ | Docling | `{source_url OR source_b64+filename, formats?, do_ocr?, ocr_lang?}` | `{source, markdown, text?, html?, status, processing_time}` — requires `HELMDECK_DOCLING_ENABLED=true` |
| **Desktop** | | | | |
| `desktop.run_app_and_screenshot` | ✅ | Xvfb + xdotool | `{command, args?}` | `{artifact_key}` + PNG artifact |
| *(desktop REST primitives)* | ✅ | xdotool / scrot / ffmpeg | T807f: 15 endpoints under `/api/v1/desktop/` — screenshot, click, type, key, launch, windows, focus, double_click, triple_click, drag, scroll, modifier_click, mouse_move, wait, zoom + agent_status for noVNC witness mode. Used by `vision.*` native tool-use path. | |
| **Vision** | | | | |
| `vision.click_anywhere` | ✅ | screenshot + LLM (native tool-use for Anthropic/OpenAI/Gemini; JSON-prompt fallback for Ollama/Deepseek) | `{goal, model, max_steps?}` | `{completed, steps, final_action}` — T807f: uses provider-native computer-use tool schema when available, per-step screenshot artifacts uploaded for replay |
| `vision.extract_visible_text` | ✅ | screenshot + LLM | `{model}` | `{text, model}` |
| `vision.fill_form_by_label` | ✅ | screenshot + LLM | `{model, fields{label: value}, max_steps?}` | `{completed, fields_filled, steps}` |
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
- **ElevenLabs TTS** (`slides.narrate`): reads vault entry `elevenlabs-key` at handler time. When missing, video renders with silence instead of narration. Add via the Vault panel → Name: `elevenlabs-key`, Type: `api_key`, Host: `api.elevenlabs.io`

## Artifact handling

Packs that produce files (screenshots, PDFs, OCR source images) upload them to the S3-compatible artifact store (Garage). The response includes:

- `artifact_key` — the storage key (e.g. `browser.screenshot_url/abc123-screenshot.png`)
- A signed URL for download (expires in 15 min)

The Artifact Explorer panel at `/artifacts` in the Management UI lists all artifacts with inline image preview and download.

For MCP clients: when the artifact is an image under 1 MB, the MCP response includes a `type: "image"` content block with base64-encoded bytes (T302b) so vision-capable LLMs can see the screenshot in one round trip.

## Upcoming packs

No packs are currently in the upcoming queue — Phase 6.5 is feature-complete. Next phase: `v1.0 — Kubernetes & GA` (Phase 7), see `docs/MILESTONES.md`.

## Source files

All packs live in `internal/packs/builtin/`:

| File | Packs |
| :--- | :--- |
| `browser_interact.go` | `browser.interact` |
| `screenshot_url.go` | `browser.screenshot_url` |
| `scrape_spa.go` | `web.scrape_spa` |
| `web_scrape.go` | `web.scrape` |
| `webtest.go` | `web.test` |
| `research_deep.go` | `research.deep` |
| `content_ground.go` | `content.ground` |
| `doc_parse.go` | `doc.parse` |
| `fs_packs.go` | `fs.*`, `cmd.run`, `git.*` |
| `repo_fetch.go` | `repo.fetch` |
| `repo_push.go` | `repo.push` |
| `http_fetch.go` | `http.fetch` |
| `image_generate.go` | `image.generate` |
| `github.go` | `github.*` |
| `slides_render.go` | `slides.render` |
| `slides_narrate.go` | `slides.narrate` |
| `slides_notes.go` | (speaker notes parser for `slides.narrate`) |
| `doc_ocr.go` | `doc.ocr` |
| `desktop_run_app.go` | `desktop.run_app_and_screenshot` |
| `vision_packs.go` | `vision.*` |
| `python_run.go` | `python.run` |
| `node_run.go` | `node.run` |
