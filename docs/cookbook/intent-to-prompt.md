---
title: Intent → prompt cookbook
description: Common natural-language intents mapped to the OpenClaw prompt that resolves them — plus the direct REST/MCP invocation underneath. Copy a recipe, swap the variables, paste into your client.
keywords: [helmdeck, cookbook, openclaw, prompts, pipelines, recipes, intent decomposition, mcp]
---

# Intent → prompt cookbook

> *"What should I type to get helmdeck to do X?"*

This page is the answer to that question, organized by the intent class users actually have. Each recipe shows three things:

1. **The OpenClaw natural-language prompt** that resolves cleanly (works against `openrouter/auto`; Tier C free models may be inconsistent — see [Calibrate model tiers](/howto/calibrate-model-tiers) and the [tier-aware prompting discussion in ADR 052](/adrs/av-output-validation-post-step) for why).
2. **The direct invocation** — exact pack/pipeline name + input JSON shape, for when you're scripting or want to skip the planner entirely.
3. **What you get back** — the artifact + structured output fields that land in the run record.

When in doubt, **prefer pipelines over packs**. Pipelines wire multi-step decompositions deterministically (no LLM planning involved), have stable input/output contracts, and bake in the engagement/validation defaults the bare packs leave opt-in.

## Repos → content

### "I want a narrated walkthrough video of a GitHub repo"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Run the `builtin.repo-presentation` pipeline against `{{REPO_URL}}` with audience `{{AUDIENCE}}` and angle `{{ANGLE}}`* |
| **Direct invocation** | `helmdeck__pipelines-run` → `pipeline: builtin.repo-presentation`, `repo_url: ...` |
| **Outputs** | `video_artifact_key` (MP4) + `captions_artifact_key` (SRT) + `engagement_artifact_key` (JSON: title/description/chapters/hashtags/hook) + `validation_artifact_key` (JSON: 13-check AV-quality report) |
| **Tip** | Pass `audience` ("senior engineers building agents"), `angle` ("what helmdeck unlocks vs MCP-server-of-the-week"), `persona` ("open-source maintainer") to shape promotion vs educational vs internal-demo tone. `title` pins the deck title; `author` adds a byline to slide 1. |

### "I want a podcast from a GitHub repo's README"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Run `builtin.repo-readme-podcast` against `{{REPO_URL}}`* |
| **Direct invocation** | `helmdeck__pipelines-run` → `pipeline: builtin.repo-readme-podcast`, `repo_url: ...` |
| **Outputs** | `audio_artifact_key` (MP3) + `engagement_artifact_key` (Apple-Podcasts-shaped: title/subtitle/show_notes_md/chapters/cta) + `validation_artifact_key` |
| **Tip** | The same engagement+validation defaults that ship on the bare pack (PR #424, [PR #432](https://github.com/tosin2013/helmdeck/pull/432)) apply here. Pass `theme: "deep-dive"` for technical depth, `theme: "interview"` for a two-host feel. |

### "I want a slide deck about a research topic" (no narration)

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Run `builtin.research-deck` on `{{TOPIC}}`* |
| **Direct invocation** | `helmdeck__pipelines-run` → `pipeline: builtin.research-deck`, `topic: ...` |
| **Outputs** | `artifact_key` (PDF or PPTX based on `format`) |
| **Tip** | This pipeline composes `research.deep` (multi-source Firecrawl + LLM synthesis with citations) → `slides.outline` → `slides.render`. Add `--narrate` by running `builtin.research-podcast` for audio, or chain the deck output through `slides.narrate` for video. |

### "I want a blog post from research"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Run `builtin.research-blog` on `{{TOPIC}}` for audience `{{AUDIENCE}}`* |
| **Direct invocation** | `helmdeck__pipelines-run` → `pipeline: builtin.research-blog`, `topic: ...`, `audience: ...` |
| **Outputs** | `artifact_key` (markdown post with inline `[1]` citations) |
| **Tip** | Pipeline composes `research.deep` → `blog.rewrite_for_audience` → `content.ground` (cite-only). Strip the citations in post-processing for conversational publication targets (dev.to / Medium); they're load-bearing on internal-docs targets. |

## Web → structured output

### "Take a screenshot of this URL"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Screenshot `{{URL}}` and save the artifact* |
| **Direct invocation** | `helmdeck__browser-screenshot_url` → `url: ...` |
| **Outputs** | `artifact_key` (PNG) + dimensions + load timing |
| **Tip** | Headless Chromium session is created + torn down per call. For multi-step browser interaction (login, navigate, scrape), use `browser.interact` for deterministic CDP or `web.scrape` for markdown extraction. |

### "Scrape this article into clean markdown"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Scrape `{{URL}}` to markdown* |
| **Direct invocation** | `helmdeck__web-scrape` → `url: ...` |
| **Outputs** | `markdown` (string) + `metadata` (title/author/published_at if extractable) |
| **Tip** | Backed by Firecrawl; requires `HELMDECK_FIRECRAWL_ENABLED=true`. For SPAs that render content client-side and need schema-driven extraction, use `web.scrape_spa` with a CSS-selector schema. |

### "Fact-check a markdown file and add citations"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Run `content.ground` on this markdown to add citations: `{{MARKDOWN}}`* |
| **Direct invocation** | `helmdeck__content-ground` → `markdown: ...`, `mode: cite` |
| **Outputs** | `markdown_with_citations` (original prose + inline `[1](url)` citations) + `claims_grounded_count` |
| **Tip** | Default mode is `cite` (find sources for existing claims, insert citations in place). `mode: rewrite` rewrites the prose to be more defensible vs the sources — use when the input draft is aspirational; cite when it's already accurate. |

### "Extract structured data from a single-page web app"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Open `{{URL}}` and extract product price, title, and image — return JSON* |
| **Direct invocation** | `helmdeck__web-scrape_spa` → `url: ...`, `schema: {"title": "h1", "price": ".price-display", "image": ".product-image[src]"}` |
| **Outputs** | One JSON object per schema property, populated from the live DOM |
| **Tip** | `web.scrape` returns markdown for content-heavy sites; `web.scrape_spa` runs Chromium and extracts by CSS selector for SPAs that render content client-side. For tables and structured listings, use `helmdeck__web-test` with a Playwright-MCP loop — it's the right tool when the data needs interaction (scroll-to-load, click-to-expand). |

### "Compare two competitor products' marketing pages"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Scrape `{{URL_A}}` and `{{URL_B}}`, then write up a comparison covering positioning, pricing claims, and target audience* |
| **Direct invocation** | `helmdeck__web-scrape` × 2 → `helmdeck__blog-rewrite_for_audience` with both scrapes as `source_content`, `audience: "decision maker comparing options"`, `angle: "honest comparison"` |
| **Outputs** | Markdown comparison post grounded in the actual scraped content |
| **Tip** | The `blog.rewrite_for_audience` step is a *ghostwriter, not a marketer* — it'll write an honest comparison if you ask for one, not a hit piece on one side. To weight the comparison toward your product, pass that as the `persona`; to keep it balanced, use `persona: "industry analyst"`. |

## Repos → code work

### "Read a repo and tell me what it is"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Fetch `{{REPO_URL}}` and summarize what it does* |
| **Direct invocation** | `helmdeck__repo-fetch` → `url: ...` (then the agent reads `tree` / `readme` / `entrypoints` / `signals` from the envelope) |
| **Outputs** | `clone_path` + `tree` + `readme` + `entrypoints` + `signals` (file count, code languages, license, has_tests, has_ci) |
| **Tip** | The envelope (`tree` + `readme` + `entrypoints` + `signals`) is designed for first-turn orientation: the agent gets the README *and* the structural picture in ~2 KB without needing to walk the file tree. For deeper structural maps, chain `repo.map` (Aider-style symbol map). |

### "Search a codebase for symbol X"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *In `{{CLONE_PATH}}`, find `{{SYMBOL}}` and show me what calls it* |
| **Direct invocation** | `helmdeck__repo-map` → `clone_path: ...` (then `helmdeck__fs-read` or `cmd.run grep -rn ...` for follow-up) |
| **Outputs** | Structural symbol map (token-budget-aware) |
| **Tip** | `repo.map` IS Aider's repo-map under the hood. For "where is X defined / which files reference Y," lean on `cmd.run grep -rn` after `repo.fetch` — faster and smaller-token than re-reading the map. |

### "Patch a file in a clone"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *In `{{CLONE_PATH}}`, replace `{{OLD}}` with `{{NEW}}` in `{{PATH}}`* |
| **Direct invocation** | `helmdeck__fs-patch` → `clone_path: ...`, `path: ...`, `old_string: ...`, `new_string: ...` |
| **Outputs** | `applied: bool`, `patch_summary` |
| **Tip** | Path-safe inside the clone. For multi-file edits or complex transformations, chain `fs.read` → reason → `fs.patch` per file rather than `fs.write` (which replaces wholesale and is unsafe for surgical edits). |

### "Audit a repo's code for a security pattern"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Fetch `{{REPO_URL}}`, search for hardcoded credentials and unsafe `exec()` calls, summarize what you find* |
| **Direct invocation** | `helmdeck__repo-fetch` → `helmdeck__cmd-run` with `cmd: "grep -rn 'API_KEY\|SECRET\|password' --include='*.py' --include='*.js'"` → then the agent summarizes hits |
| **Outputs** | Per-file grep matches + agent's structured summary (which findings are real, which are documented patterns, severity hints) |
| **Tip** | The session-chaining contract (`_session_id` auto-threaded) means `cmd.run` reads from `repo.fetch`'s clone without re-cloning. Don't reach for `repo.map` for this — grep is cheaper and more precise for known-pattern searches. Use the symbol map when the question is *"who calls function X"* not *"where is string Y."* |

### "Generate developer documentation from a codebase"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Fetch `{{REPO_URL}}`, map its structure, then write up a developer onboarding doc covering entry points, key modules, and the testing strategy* |
| **Direct invocation** | `helmdeck__repo-fetch` → `helmdeck__repo-map` → `helmdeck__blog-rewrite_for_audience` with the map + readme as `source_content` |
| **Outputs** | Markdown developer doc grounded in actual repo structure (not a paraphrase of the README) |
| **Tip** | This composition lives inside the `builtin.repo-presentation` pipeline for the video-output variant. For text docs, chain it manually — there's no `builtin.repo-onboarding-doc` pipeline yet (worth filing if you use this composition often). The `blog.rewrite_for_audience` step takes `audience: "engineers new to this codebase"` for the right voice. |

## Validation + reliability

### "Is this artifact good?"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Run `av.validate` on `{{ARTIFACT_KEY}}`* |
| **Direct invocation** | `helmdeck__av-validate` → `video_artifact_key: ...` *(or `audio_artifact_key`)* |
| **Outputs** | `validation.checks[]` + `passed` + `failed` + `warnings` + `all_passed` |
| **Tip** | Mostly an ad-hoc check — `av.validate` already runs as a default-on post-step on `slides.narrate` and `podcast.generate`, so the `validation` field is in their run records. Use direct invocation when an artifact was produced outside those packs OR when you want strict-mode CI gating: pass `strict:true` to surface `fail`-severity check failures as a typed `CodeArtifactFailed` (publish-gate use case). |

### "Why did my pipeline fail?"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Show me the audit log entries for the last pipeline run* |
| **Direct invocation** | `helmdeck__inspect-audit-log` *(via REST `/api/v1/audit?limit=N`)* |
| **What to look at** | Per-pack `validation` field on AV runs; `provider_calls` table for LLM call status + `finish_reason` (per [ADR 051](/adrs/failure-mode-aware-dispatch)); the warn-level log lines for soft-surface failures |
| **Tip** | The [When a pipeline fails](/howto/when-a-pipeline-fails) HOWTO walks the diagnostic surface in depth — including the `validation` field as a fast-path for AV pipelines and the typed-error codes for credential/quota failures. |

### "Strict-mode validate before publishing"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Validate `{{ARTIFACT_KEY}}` strictly — fail if anything fails* |
| **Direct invocation** | `helmdeck__av-validate` → `video_artifact_key: ...`, `strict: true` |
| **Outputs** | Either the `validation` structured report (on pass) OR a typed `CodeArtifactFailed` error naming the failing checks |
| **Tip** | Use this as a CI publish gate. The default-on validation runs on `slides.narrate` / `podcast.generate` are soft-surface (findings land in the output as data, pack returns success). `strict:true` is the bridge to fail-fast for downstream consumers that can't tolerate processing a structurally-invalid artifact. See [ADR 052](/adrs/av-output-validation-post-step) §"Strict mode" for the rationale. |

## Media & creativity

### "Generate AI artwork from a text prompt"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Generate an image: `{{PROMPT}}`* |
| **Direct invocation** | `helmdeck__image-generate` → `prompt: ...`, `engine: "fal.ai"`, `model: "fal-ai/flux/schnell"` |
| **Outputs** | `image_artifact_key` (PNG) + `prompt_used` + `model_used` + `seed_used` |
| **Tip** | Day-1 ships fal.ai (`fal-key` in vault or `HELMDECK_FAL_KEY` env). Default model is `fal-ai/flux/schnell` at ~$0.003/image in 1-3 seconds — great for iteration. For polish, try `fal-ai/flux/pro` (slower, more expensive). Pass `num_images: N` (1-4) to generate alternatives in one call. |

### "Find stock photos for a topic"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Find stock photos of `{{QUERY}}`, horizontal, large* |
| **Direct invocation** | `helmdeck__stock-search` → `query: ...`, `orientation: "landscape"`, `size: "large"` |
| **Outputs** | Array of photos with `artifact_key`, `photographer` (attribution), `source_url`, `width`, `height`, `alt_text` |
| **Tip** | Pexels-backed (real photos, not AI). When the prompt needs an actual photo of a specific real-world thing (a brand, a place, a person), use this; when the prompt needs creative interpretation (an illustration, a concept, an abstract scene), use `image.generate`. The two compose well — search first for hero photos, generate for spot illustrations. |

### "Build a quick demo video from a HyperFrames description"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Make a 15-second HyperFrames video showing `{{DESCRIPTION}}`, landscape aspect ratio* |
| **Direct invocation** | `helmdeck__hyperframes-compose` → `description: ...`, `aspect_ratio: "16:9"` → `helmdeck__hyperframes-render` with the resulting `composition_html` |
| **Outputs** | `video_artifact_key` (MP4, short-form ≤12 min at 1080p) + `duration_s` + `has_audio` |
| **Tip** | `hyperframes.compose` is LLM-backed (canvas + GSAP scaffolding from a plain description); `hyperframes.render` is deterministic (headless Chromium + ffmpeg). For longer-form narrated content with slide structure, use `slides.narrate` — HyperFrames is the right tool for short motion graphics, not slide decks. |

### "Generate marketing copy for an upcoming release"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Write release marketing copy for `{{REPO_URL}}` v`{{VERSION}}`, audience: developers shipping LLM products* |
| **Direct invocation** | `helmdeck__repo-fetch` → `helmdeck__blog-rewrite_for_audience` with the README + CHANGELOG as `source_content` → `helmdeck__image-generate` for a hero image |
| **Outputs** | Marketing-tuned blog post + hero image + optionally a press-kit deck (chain through `slides.outline` + `slides.render`) |
| **Tip** | This composition has no canned pipeline today — chain it manually. The `blog.rewrite_for_audience` step shapes the voice ("hyped engineering announcement" vs "honest release notes" via `angle`). For social-card-sized graphics specifically, pass `image_size: "square_hd"` to `image.generate`. |

## Memory

### "Remember a fact for next time"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Remember that {{FACT}}* |
| **Direct invocation** | `helmdeck__memory-store` → `key: ...`, `value: ...`, `ttl: 24h` *(or longer)* |
| **Outputs** | `stored_at`, `expires_at` |
| **Tip** | Memory is per-subject (per agent / per user, depending on how OpenClaw bridges identity). `ttl` is optional; default ~1 hour. For learned defaults that helmdeck.plan should reuse, the agent doesn't need to call this explicitly — helmdeck.plan's `my-defaults projection` reads from the same memory store automatically. See [agent-memory](/reference/agent-memory). |

### "Forget what you learned about X"

| Field | Value |
|---|---|
| **OpenClaw prompt** | *Forget any defaults you learned about `{{TOPIC}}`* |
| **Direct invocation** | `helmdeck__memory-forget` → `category: ...` *(or `key`)* |
| **Outputs** | `forgotten_count` |
| **Tip** | The same pattern engagement-metadata users follow when they want a clean slate per-deck or per-podcast theme. Memory writes are paired with cleanup hooks by design — TTL + programmatic forget + category tag co-ship with any memory-write feature. |

## Picking the right pipeline for your goal

When your intent doesn't match any recipe above, ask yourself:

- **"Do I want a finished artifact (MP4, MP3, PDF, blog post)?"** → look for a pipeline whose `Produces` matches the artifact type. The `builtin.*` pipelines bake in best practices (engagement metadata, captions, validation) the bare packs leave opt-in.
- **"Do I want a structured answer to a question?"** → `research.deep` for multi-source synthesis, `content.ground` for citation-strengthening, `helmdeck.plan` for "decompose this into steps you'd run."
- **"Do I want to do one thing fast?"** → call the bare pack directly. `browser.screenshot_url`, `repo.fetch`, `fs.read`, `cmd.run`, `image.generate`, `stock.search` — all sub-second, no LLM in the loop.

The full pack catalog with input/output contracts is at [docs/PACKS.md](/PACKS); the prompt template surface for every pack and pipeline is at [reference/prompt-templates](/reference/prompt-templates).

## Related

- [Prompt templates](/reference/prompt-templates) — fill-in-the-blank prompts for every pack and pipeline (this cookbook is the *intent-first* index over those templates)
- [Calibrate model tiers](/howto/calibrate-model-tiers) — when your model returns empty plans or length-truncated JSON, this is the first thing to check
- [Free models and context](/howto/free-models-and-context) — what to expect from Tier C free models like the Nemotron / Kimi K2 series
- [When a pipeline fails](/howto/when-a-pipeline-fails) — diagnostic surface including the `validation` field
- [ADR 052 — AV output validation as a default-on post-step](/adrs/av-output-validation-post-step) — why the `validation` field exists and how to read it
- [ADR 051 — failure-mode-aware dispatch](/adrs/failure-mode-aware-dispatch) — why some models work and some don't on the same prompt
