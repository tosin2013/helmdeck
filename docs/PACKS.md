---
title: Capability pack catalog
description: Reference table for every shipped helmdeck capability pack — input/output schema, session requirement, execution engine, vault credentials. 52 packs total.
keywords: [helmdeck, capability packs, browser automation, web scraping, GitHub, vault, MCP, slides, vision, repo, filesystem]
sidebar_label: PACKS reference
priority: 0.9
changefreq: weekly
---

# Helmdeck — Built-in Capability Pack Reference

52 packs ship in the control plane binary (42 without an AI gateway configured — the 10 gateway-gated packs are the LLM/vision packs). All are available as MCP tools (via `/api/v1/mcp/sse` or `/api/v1/mcp/ws`) and as REST endpoints (`POST /api/v1/packs/<name>`).

## Quick reference

| Pack | Session? | Engine | Input (key fields) | Output (key fields) |
| :--- | :---: | :--- | :--- | :--- |
| **Orchestration (meta-packs)** | | | | |
| `helmdeck.route` | ❌ | LLM + catalog metadata + memory | `{user_intent, model, context?, max_tokens?}` | `{recommendation{kind,id,suggested_inputs}, alternatives[], gap_warning?, reasoning, model}` — recommends the best pack/pipeline for an intent; emits `gap_warning` when nothing fits. Needs a gateway. |
| `helmdeck.plan` | ❌ | LLM + catalog metadata + llmcontext | `{user_intent, model, context?, max_tokens?}` | `{steps[], rewritten_prompt, complexity, reasoning, compaction?, model}` — decomposes a multi-intent prompt into ordered tool/pipeline calls. Needs a gateway. |
| `helmdeck.memory_store` | ❌ | memory store | `{key, value, category?, tags?, ttl_seconds?}` | `{key, category, expires_at}` — persist a durable user fact (default category `user_facts`, 90-day TTL; min 1h / max 365d). Reserved categories `pack_history`/`pipeline_history` reject. |
| `helmdeck.memory_forget` | ❌ | memory store | `{scope?}` | `{scope, deleted}` — erase the caller's routing/audit history. `scope` ∈ `all` / `packs` / `pipelines` / `pack:<id>` / `pipeline:<id>` / `key:<exact>`. Never touches pack caches or vault. |
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
| **SWE** | | | | |
| `swe.solve` | ✅ | session exec + LLM + git | `{repo_url OR clone_path, task, model, output_mode?, base?, branch?}` | `{output_mode, summary, branch?, commit?, pr_url?, patch?}` — autonomous code-edit agent; `output_mode` ∈ `patch`/`branch`/`pull_request`. Backs the `repo-solve-*` and `issue-to-pr` pipelines. |
| **HTTP** | | | | |
| `http.fetch` | ❌ | Go HTTP + vault | `{url, method?, headers?, body?}` | `{status, headers, body}` |
| **Communication** | | | | |
| `email.send` | ❌ | Resend API + vault | `{to, from?, subject?, html?, cc?, bcc?, reply_to?}` | `{message_id}` — send a transactional email. Vault credential `resend-api-key`. |
| **GitHub** | | | | |
| `github.create_issue` | ❌ | GitHub REST | `{repo, title, body?, labels?}` | `{number, url, html_url}` |
| `github.list_issues` | ❌ | GitHub REST | `{repo, state?, labels?, assignee?}` | `{issues[], count}` |
| `github.get_issue` | ❌ | GitHub REST (5-min cache) | `{repo, issue_number, credential?}` | `{number, title, body, state, labels[], html_url, user}` — read one issue; pairs with `swe.solve` for issue→PR. |
| `github.list_prs` | ❌ | GitHub REST | `{repo, state?, head?, base?}` | `{prs[], count}` |
| `github.create_pr` | ❌ | GitHub REST | `{repo, head, base, title, body?, draft?, credential?}` | `{number, url, html_url}` — open a PR; final step of `swe.solve`'s `pull_request` mode. |
| `github.post_comment` | ❌ | GitHub REST | `{repo, issue_number, body}` | `{id, url}` |
| `github.create_release` | ❌ | GitHub REST | `{repo, tag, name?, body?, draft?}` | `{id, url, upload_url}` |
| `github.search` | ❌ | GitHub REST | `{query, type?}` | `{total_count, items[]}` |
| **Slides** | | | | |
| `slides.outline` | ✅ | LLM | `{content, title?, author?, persona?, model}` | `{markdown, persona_used, has_title_slide}` — restate prose as a structured Marp deck (feed this to `slides.render`/`narrate`). Needs a gateway. |
| `slides.render` | ✅ | Marp + Chromium + mmdc | `{markdown, format, mermaid?, hero_image_prompt?, hero_image_model?}` | `{artifact_key, hero_image_model_used?}` + PDF/PPTX artifact — `mermaid:true` (default) pre-renders ```` ```mermaid ```` fences to inline SVG; `hero_image_prompt` (v0.12.0 #146) chains `image.generate` and base64-inlines the result before slide 1. |
| `slides.narrate` | ✅ | Marp + ElevenLabs + ffmpeg + LLM | `{markdown, voice_id?, model_id?, resolution?, fade_ms?, metadata_model?, hero_image_prompt?, hero_image_model?}` | `{video_artifact_key, video_size, slide_count, total_duration_s, has_narration, voice_used?, metadata_artifact_key?, metadata?, hero_image_model_used?}` — MP4 video with per-slide TTS narration from `<!-- speaker notes -->` + YouTube metadata. `hero_image_prompt` (v0.12.0 #146) inlines a chained hero image INTO slide 1 (no separator, preserves narration). ElevenLabs API key from vault `elevenlabs-key`. |
| **Blog** | | | | |
| `blog.rewrite_for_audience` | ❌ | LLM | `{source_content, audience, model, angle?, title?, persona?, max_tokens?}` | `{markdown, persona_used, model}` — translate a source doc into an original blog post for a stated audience/angle (not a summarizer). Generator at the heart of the `*-rewrite-blog` pipelines. Needs a gateway. |
| `blog.publish` | ❌ | Ghost Admin API + goldmark + LLM | `{destination, format, title, body OR (prompt+model), tags?, status?, published_at?, host?, credential?, feature_image_artifact_key?, hero_image?, hero_image_prompt?, hero_image_model?}` | `{destination, format, body_source, model_used?, hero_image_model_used?}` + ghost: `{post_id, url, html_url, status, published_at, feature_image_url?}` OR artifact: `{artifact_key, size, feature_image_artifact_key?}` — publishes to a Ghost blog (live API) or stores rendered markdown/HTML as a helmdeck artifact. Two body modes (agent supplies body OR prompt+model the pack expands via LLM). Feature image is operator-supplied via `feature_image_artifact_key` OR auto-generated via `hero_image:true` (v0.12.0 #146); Ghost-mode uploads via `/images/upload/` then stamps `feature_image`. Ghost vault credential `ghost-admin-key` (id:hexsecret). |
| **Podcast** | | | | |
| `podcast.generate` | ✅ | ElevenLabs TTS + ffmpeg + LLM (engine-pluggable) | `{speakers, script OR (prompt+model) OR (source_url/source_text+model), engine?, model_id?, theme?, duration_target_min?, silence_between_turns_ms?, generate_cover_prompt?, cover_image?, cover_image_model?}` | `{engine, audio_artifact_key, audio_size, duration_s, speaker_count, turn_count, script_source, model_used?, voices_used, has_narration, theme, cover_image_prompt?, cover_image_artifact_key?, cover_image_model_used?}` — multi-speaker (1..N) podcast MP3. Three input modes: agent-supplied script, prompt+model (LLM generates dialogue), or long-form content (URL/text → LLM converts). Five themes (`interview`/`debate`/`news-roundup`/`deep-dive`/`solo-essay`) bake in podcast best practices. `cover_image:true` (v0.12.0 #146) auto-generates cover artwork via `image.generate`. Day 1: ElevenLabs only (vault `elevenlabs-key`); future engines (PlayHT, Hume.ai, Resemble.ai) slot in via `engine` field. Silent-fallback when key missing. |
| **Image / Stock** | | | | |
| `image.generate` | ❌ | fal.ai sync `fal.run` (engine-pluggable) | `{prompt, engine?, model?, image_size?, num_images?, seed?, credential?}` | `{image_artifact_key, image_size, engine, model_used, prompt_used, seed_used?, image_artifact_keys?}` — text → image. Day 1: fal.ai only (vault `fal-key`, `HELMDECK_FAL_KEY`); default model `fal-ai/flux/schnell` (~$0.003/image, 1-3s). 1-4 images per call. `engine` field reserved for Replicate as a community PR. Hard-fails when credential missing. |
| `stock.search` | ❌ | Pexels API + vault | `{query, count?, orientation?, size?, color?}` | `{photos[{artifact_key, photographer, photographer_url, source_url, width, height, alt_text}]}` — real (non-AI) stock photos. Same chained-input contract as `image.generate`. Vault `pexels-key` (or `HELMDECK_PEXELS_API_KEY`). |
| **Video (HyperFrames)** | | | | |
| `hyperframes.compose` | ✅ | LLM | `{description, aspect_ratio?, audio_url?, model}` | `{composition_html}` — generate a HyperFrames composition (canvas + GSAP scaffolding) from a plain-language description. Feed `composition_html` to `hyperframes.render`. Needs a gateway. |
| `hyperframes.render` | ✅ | headless Chromium + ffmpeg | `{composition_html, resolution?, aspect_ratio?}` | `{video_artifact_key, video_size, duration_s, has_audio}` — render an HTML/CSS/JS composition into a deterministic MP4. Short-form only (≤12 min @ 1080p, 512 MiB cap). `Async: true`. |
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

## Gateway-gated packs

10 of the 52 packs require an AI gateway (a configured chat-completion provider). Without one, the binary registers 42 packs and these are absent: `vision.click_anywhere`, `vision.extract_visible_text`, `vision.fill_form_by_label`, `web.test`, `research.deep`, `content.ground`, `slides.outline`, `blog.rewrite_for_audience`, `hyperframes.compose`, `slides.narrate`.

Beyond the built-ins, operators can register `cmd.*` subprocess packs (`HELMDECK_COMMAND_PACKS_DIR`) and install community packs from the marketplace (`helmdeck pack install <name>`); both appear in `tools/list` at runtime.

## Source files

All packs live in `internal/packs/builtin/`. Registration happens in `cmd/control-plane/main.go`:

| File | Packs |
| :--- | :--- |
| `route.go` | `helmdeck.route` |
| `plan.go` | `helmdeck.plan` |
| `memory_store.go` | `helmdeck.memory_store` |
| `memory_forget.go` | `helmdeck.memory_forget` |
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
| `repo_map.go` | `repo.map` |
| `repo_push.go` | `repo.push` |
| `swe_solve.go` | `swe.solve` |
| `http_fetch.go` | `http.fetch` |
| `email_send.go` | `email.send` |
| `image_generate.go` | `image.generate` |
| `stock_search.go` | `stock.search` |
| `github.go` | `github.*` (incl. `get_issue`, `create_pr`) |
| `slides_outline.go` | `slides.outline` |
| `slides_render.go` | `slides.render` |
| `slides_narrate.go` | `slides.narrate` |
| `slides_notes.go` | (speaker notes parser for `slides.narrate` — not a pack) |
| `blog_publish.go` | `blog.publish` |
| `blog_rewrite_for_audience.go` | `blog.rewrite_for_audience` |
| `podcast_generate.go` | `podcast.generate` |
| `hyperframes_compose.go` | `hyperframes.compose` |
| `hyperframes_render.go` | `hyperframes.render` |
| `doc_ocr.go` | `doc.ocr` |
| `desktop_run_app.go` | `desktop.run_app_and_screenshot` |
| `vision_packs.go` | `vision.*` |
| `python_run.go` | `python.run` |
| `node_run.go` | `node.run` |
