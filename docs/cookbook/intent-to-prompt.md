---
title: Intent → prompt cookbook
description: Common natural-language intents mapped to the OpenClaw prompt that resolves them — plus the direct REST/MCP invocation underneath. Copy a recipe, swap the variables, paste into your client.
keywords: [helmdeck, cookbook, openclaw, prompts, pipelines, recipes, intent decomposition, mcp]
---

# Intent → prompt cookbook

> *"What should I type to get helmdeck to do X?"*

This page is the answer to that question, organized by the intent class users actually have. Each recipe shows three things:

1. **The OpenClaw natural-language prompt** that resolves cleanly (works against `openrouter/auto`; Tier C free models may be inconsistent — see [Calibrate model tiers](/howto/calibrate-model-tiers) and the [tier-aware prompting discussion in ADR 052](/adrs/052-av-output-validation-post-step) for why).
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
| **What to look at** | Per-pack `validation` field on AV runs; `provider_calls` table for LLM call status + `finish_reason` (per [ADR 051](/adrs/051-failure-mode-aware-dispatch)); the warn-level log lines for soft-surface failures |
| **Tip** | The [When a pipeline fails](/howto/when-a-pipeline-fails) HOWTO walks the diagnostic surface in depth — including the `validation` field as a fast-path for AV pipelines and the typed-error codes for credential/quota failures. |

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
| **Tip** | The same pattern PR #424 / engagement-metadata users follow when they want a clean slate per-deck or per-podcast theme. Memory writes are paired with cleanup hooks by design ([stored feedback](/explanation/memory-cleanup)). |

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
- [ADR 052 — AV output validation as a default-on post-step](/adrs/052-av-output-validation-post-step) — why the `validation` field exists and how to read it
- [ADR 051 — failure-mode-aware dispatch](/adrs/051-failure-mode-aware-dispatch) — why some models work and some don't on the same prompt
