---
title: Pipeline prompt templates
description: Copy-and-fill prompt templates for every built-in helmdeck pipeline. Replace the {{VARIABLES}} and ask your agent to run the pipeline.
keywords: [helmdeck, pipelines, prompt templates, helmdeck__pipeline-run, variables]
---

# Pipeline prompt templates

One template per built-in pipeline (a saved, multi-step chain). Replace every
`{{VARIABLE}}`, then ask your agent to run it — the agent calls
`helmdeck__pipeline-run` with your values, returns a `run_id`, and polls
`helmdeck__pipeline-run-status`. Variables map to the pipeline's
`${{ inputs.* }}` references (see `internal/pipelines/seed.go`). See the
[pipelines overview](/reference/packs/repo/solve) and [SKILL.md](/integrations/SKILLS)
for when to run a pipeline vs. call packs directly.

> Provider-dependent steps degrade gracefully: podcast/narrate pipelines run
> silently without an `elevenlabs-key`; grounding/research need the Firecrawl
> overlay; `blog.publish` defaults to an artifact (no Ghost needed).
>
> **If a run fails**, `helmdeck__pipeline-run-status` reports a `failure_class`
> (`caller_fixable` — fix the input/model and re-run; `pack_bug` — file the
> linked issue; `transient` — re-run; `state_changed` — refresh and re-run) and
> a one-line `failure_reason`. Re-run with the same inputs via
> `helmdeck__pipeline-rerun`. See [When a pipeline fails](/howto/when-a-pipeline-fails).
> For a step's `model`, pick a routable id from `helmdeck://models`.

---

## Content pipelines

#### `builtin.grounded-deck` — fact-check markdown, then render a PDF deck

**Template**
```
Use helmdeck__pipeline-run to run the builtin.grounded-deck pipeline with inputs:
markdown = {{MARKDOWN}}
```

**Variables**
- `{{MARKDOWN}}` — the markdown to fact-check + rewrite, then slide (input `markdown`, required).

**Notes** — runs `content.ground` (rewrite) → `slides.render` (PDF). Needs the Firecrawl overlay for grounding.

#### `builtin.grounded-blog` — fact-check markdown, then publish a blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.grounded-blog pipeline with inputs:
markdown = {{MARKDOWN}}
title = {{TITLE}}
```

**Variables**
- `{{MARKDOWN}}` — the markdown to ground + publish (input `markdown`, required).
- `{{TITLE}}` — blog post title (input `title`, required).

**Notes** — `content.ground` (rewrite) → `blog.publish` (markdown artifact by default).

#### `builtin.research-deck` — research a topic, render it as a deck

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-deck pipeline with inputs:
query = {{QUERY}}
```

**Variables**
- `{{QUERY}}` — the topic to deep-research (input `query`, required).

**Notes** — `research.deep` → `slides.render` (PDF). Needs the Firecrawl overlay.

#### `builtin.research-narrate` — research a topic, render a narrated video

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-narrate pipeline with inputs:
query = {{QUERY}}
```

**Variables**
- `{{QUERY}}` — the topic to research (input `query`, required).

**Notes** — `research.deep` → `slides.narrate`. Narrates silently without an `elevenlabs-key`.

#### `builtin.research-podcast` — research a topic, generate a podcast

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-podcast pipeline with inputs:
query = {{QUERY}}
```

**Variables**
- `{{QUERY}}` — the topic to research (input `query`, required).

**Notes** — `research.deep` → `podcast.generate` (default 2-speaker voices). Silent MP3 without an `elevenlabs-key`.

#### `builtin.research-ground-deck` — research, fact-check, then deck

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-ground-deck pipeline with inputs:
query = {{QUERY}}
```

**Variables**
- `{{QUERY}}` — the topic to research, ground, and slide (input `query`, required).

**Notes** — `research.deep` → `content.ground` (rewrite) → `slides.render`.

#### `builtin.research-blog` — research a topic, publish the synthesis as a blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-blog pipeline with inputs:
query = {{QUERY}}
title = {{TITLE}}
```

**Variables**
- `{{QUERY}}` — the topic to research (input `query`, required).
- `{{TITLE}}` — blog post title (input `title`, required).

**Notes** — `research.deep` → `blog.publish`.

---

## Web & document pipelines

#### `builtin.scrape-deck` — scrape a URL, render it as a deck

**Template**
```
Use helmdeck__pipeline-run to run the builtin.scrape-deck pipeline with inputs:
url = {{URL}}
```

**Variables**
- `{{URL}}` — the page to scrape to markdown, then slide (input `url`, required).

**Notes** — `web.scrape` → `slides.render`. Needs the Firecrawl overlay.

#### `builtin.scrape-ground-blog` — scrape a URL, fact-check, publish a blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.scrape-ground-blog pipeline with inputs:
url = {{URL}}
title = {{TITLE}}
```

**Variables**
- `{{URL}}` — the page to scrape (input `url`, required).
- `{{TITLE}}` — blog post title (input `title`, required).

**Notes** — `web.scrape` → `content.ground` (rewrite) → `blog.publish`. Needs Firecrawl.

#### `builtin.doc-ground-blog` — parse a document, fact-check, publish a blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.doc-ground-blog pipeline with inputs:
source_url = {{SOURCE_URL}}
title = {{TITLE}}
```

**Variables**
- `{{SOURCE_URL}}` — URL of the document (PDF/DOCX/PPTX/…) to parse (input `source_url`, required).
- `{{TITLE}}` — blog post title (input `title`, required).

**Notes** — `doc.parse` → `content.ground` (rewrite) → `blog.publish`. Needs the Docling overlay (parse) + Firecrawl (ground).

---

## Repo & video pipelines

#### `builtin.repo-readme-narrate` — clone a repo, narrate a deck from its README

**Template**
```
Use helmdeck__pipeline-run to run the builtin.repo-readme-narrate pipeline with inputs:
repo_url = {{REPO_URL}}
```

**Variables**
- `{{REPO_URL}}` — the git repo to clone (input `repo_url`, required). Public repos work as-is; for private, clone the pipeline and add a `credential`.

**Notes** — `repo.fetch` → `slides.narrate` from the README. Silent without an `elevenlabs-key`.

#### `builtin.repo-readme-podcast` — clone a repo, generate a podcast about it

**Template**
```
Use helmdeck__pipeline-run to run the builtin.repo-readme-podcast pipeline with inputs:
repo_url = {{REPO_URL}}
```

**Variables**
- `{{REPO_URL}}` — the git repo to clone (input `repo_url`, required).

**Notes** — `repo.fetch` → `podcast.generate` from the README (default voices). Silent without an `elevenlabs-key`.

#### `builtin.html-video` — render an HTML/CSS/JS composition to MP4

**Template**
```
Use helmdeck__pipeline-run to run the builtin.html-video pipeline with inputs:
composition_html = {{COMPOSITION_HTML}}
```

**Variables**
- `{{COMPOSITION_HTML}}` — the HTML/CSS/JS composition to render (input `composition_html`, required). Embed an `<audio src>` (e.g. a `podcast.generate` `audio_url`) for narrated video.

**Notes** — single step `hyperframes.render` (1080p, 16:9). Short-form only (≤12 min, 512 MiB).
