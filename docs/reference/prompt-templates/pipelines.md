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
`${{ inputs.* }}` references (see `internal/pipelines/seed.go`). New to
pipelines? Read [How a pipeline run works](#how-a-pipeline-run-works) just
below; see [SKILL.md](/integrations/SKILLS) for when to run a pipeline vs. call
packs directly.

> **Fill every `{{VARIABLE}}` before running.** They are placeholders, not
> defaults — an agent should ask the user for each value, or propose one (e.g.
> a generated `title`) and confirm it, before calling `helmdeck__pipeline-run`.
> An input left as a literal `{{TITLE}}` is **rejected** with a `caller_fixable`
> error (the run never starts), rather than silently producing a post titled
> "{{TITLE}}". Fill it in and re-run.

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

## How a pipeline run works

Every template below drives the same four-step loop — your agent handles it; you
just supply the inputs:

1. **Start** — the agent calls `helmdeck__pipeline-run` with the pipeline id and
   your `inputs`, and gets a `run_id` back immediately (runs are **async**; a
   narrate/podcast/video pipeline can take minutes).
2. **Poll** — `helmdeck__pipeline-run-status` with the `run_id` reports `status`
   (`pending` → `running` → `succeeded` / `failed`), each step's output, and the
   artifacts each step produced.
3. **Collect** — on `succeeded`, the run lists each step's artifacts (a PDF,
   MP4, MP3, or a published-post URL/key); fetch the final one by its artifact
   key.
4. **Recover** — on `failed`, read `failure_class` + `failure_reason` and
   `helmdeck__pipeline-rerun` (see the callout above). A `caller_fixable`
   failure means *your* input/model needs a tweak, not that helmdeck is broken.

Inputs map 1:1 to the pipeline's `${{ inputs.* }}` references — each template's
**Variables** list is exactly the set that pipeline needs, nothing more.

## Customizing a built-in (private repos, models, voices)

The built-ins are **read-only** — editing one returns `builtin_readonly`. To
change a step's input — add a `credential` for a private repo, pin a `model`,
set podcast `speakers`/`plan` — **clone it**: fetch the builtin's definition,
edit the step input, and `POST` it back. The server gives the clone a fresh
`pipe_<id>` and `builtin: false`; everything else (including the
`${{ inputs.* }}` / `${{ steps.* }}` wiring) carries over unchanged.

```bash
# Clone builtin.repo-presentation and add a credential so it can clone a
# private repo. (JWT minted as in the swe.solve reference.)
curl -fsS -H "Authorization: Bearer $JWT" \
  http://localhost:3000/api/v1/pipelines/builtin.repo-presentation \
| jq 'del(.id, .builtin, .created_at, .updated_at)
      | .name = "repo-presentation (private)"
      | .steps[0].input.credential = "github-token"' \
| curl -fsS -X POST http://localhost:3000/api/v1/pipelines \
    -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' -d @-
```

The response includes the new `pipe_<id>`; run it exactly like a built-in
(`helmdeck__pipeline-run` with that id). Your agent can do the same through the
pipeline CRUD tools instead of `curl`.

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

**Notes** — `content.ground` (citation-only) → `slides.outline` → `slides.render` (PDF). Needs the Firecrawl overlay for grounding. Optional inputs: `persona`, `audience`, `angle`, `title`, `author`, `export_outline`, `include_image_prompts`.

#### `builtin.brief-rewrite-blog` — expand a brief into an original blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.brief-rewrite-blog pipeline with inputs:
brief = {{BRIEF}}
audience = {{AUDIENCE}}
title = {{TITLE}}
```

**Variables**
- `{{BRIEF}}` — a pasted brief/pitch/outline (Title Idea / Hook / What to Cover / Target Audience) (input `brief`, required).
- `{{AUDIENCE}}` — who the post is for (input `audience`, required).
- `{{TITLE}}` — blog post title (input `title`, required). Optional: `angle`, `persona`.

**Notes** — `blog.rewrite_for_audience` (expands the brief into an original post) → `content.ground` (citation-only) → `blog.publish` (markdown artifact). **Replaces the old `builtin.grounded-blog`**, which only annotated whatever it received. For a finished draft that just needs citations, call `content.ground` directly. Clone with a `credential` + `host` to publish to Ghost.

#### `builtin.grounded-narrate` — fact-check markdown, render a narrated video

**Template**
```
Use helmdeck__pipeline-run to run the builtin.grounded-narrate pipeline with inputs:
markdown = {{MARKDOWN}}
```

**Variables**
- `{{MARKDOWN}}` — the markdown to ground, outline, and narrate (input `markdown`, required). Optional: `persona`, `audience`, `angle`, `title`, `author`, `export_outline`, `include_image_prompts`.

**Notes** — `content.ground` (citation-only) → `slides.outline` → `slides.narrate`. Falls back to silent video without an `elevenlabs-key`. Needs Firecrawl for grounding.

#### `builtin.grounded-podcast` — fact-check markdown, generate a podcast

**Template**
```
Use helmdeck__pipeline-run to run the builtin.grounded-podcast pipeline with inputs:
markdown = {{MARKDOWN}}
```

**Variables**
- `{{MARKDOWN}}` — the markdown to ground, then voice as a podcast (input `markdown`, required).

**Notes** — `content.ground` (citation-only) → `podcast.generate` (default 2-speaker voices). Silent MP3 without an `elevenlabs-key`. Needs Firecrawl.

#### `builtin.research-deck` — research a topic, render it as a deck

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-deck pipeline with inputs:
query = {{QUERY}}
```

**Variables**
- `{{QUERY}}` — the topic to deep-research (input `query`, required).

**Notes** — `research.deep` → `slides.outline` → `slides.render` (PDF). Needs the Firecrawl overlay.

#### `builtin.research-narrate` — research a topic, render a narrated video

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-narrate pipeline with inputs:
query = {{QUERY}}
```

**Variables**
- `{{QUERY}}` — the topic to research (input `query`, required).

**Notes** — `research.deep` → `slides.outline` → `slides.narrate`. Narrates silently without an `elevenlabs-key`.

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

**Notes** — `research.deep` → `content.ground` (citation-only) → `slides.outline` → `slides.render`.

#### `builtin.research-rewrite-blog` — research a topic, write an original blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.research-rewrite-blog pipeline with inputs:
query = {{QUERY}}
audience = {{AUDIENCE}}
title = {{TITLE}}
```

**Variables**
- `{{QUERY}}` — the topic to research (input `query`, required).
- `{{AUDIENCE}}` — who the post is for (input `audience`, required).
- `{{TITLE}}` — blog post title (input `title`, required). Optional: `angle`, `persona`.

**Notes** — `research.deep` → `blog.rewrite_for_audience` → `content.ground` (citation-only) → `blog.publish`. **Replaces `builtin.research-blog`**, which saved the raw synthesis untailored to an audience. Clone with a `credential` + `host` to publish to Ghost.

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

**Notes** — `web.scrape` → `slides.outline` → `slides.render`. Needs the Firecrawl overlay.

#### `builtin.scrape-rewrite-blog` — scrape a URL, write an original blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.scrape-rewrite-blog pipeline with inputs:
url = {{URL}}
audience = {{AUDIENCE}}
title = {{TITLE}}
```

**Variables**
- `{{URL}}` — the page to scrape (input `url`, required).
- `{{AUDIENCE}}` — who the post is for (input `audience`, required).
- `{{TITLE}}` — blog post title (input `title`, required). Optional: `angle`, `persona`.

**Notes** — `web.scrape` → `blog.rewrite_for_audience` → `content.ground` (citation-only) → `blog.publish`. Needs Firecrawl. **Replaces `builtin.scrape-ground-blog`**, which produced a citation-strengthened transcription that read as republishing the page. Clone with a `credential` + `host` to publish to Ghost.

#### `builtin.doc-rewrite-blog` — parse a document, write an original blog post

**Template**
```
Use helmdeck__pipeline-run to run the builtin.doc-rewrite-blog pipeline with inputs:
source_url = {{SOURCE_URL}}
audience = {{AUDIENCE}}
title = {{TITLE}}
```

**Variables**
- `{{SOURCE_URL}}` — URL of the document (PDF/DOCX/PPTX/…) to parse (input `source_url`, required).
- `{{AUDIENCE}}` — who the post is for (input `audience`, required).
- `{{TITLE}}` — blog post title (input `title`, required). Optional: `angle`, `persona`.

**Notes** — `doc.parse` → `blog.rewrite_for_audience` → `content.ground` (citation-only) → `blog.publish`. Needs the Docling overlay (parse) + Firecrawl (ground). **Replaces `builtin.doc-ground-blog`**, which produced a transcription rather than an original post. `source_url` must have a document extension (web pages → `scrape-rewrite-blog`). Clone with a `credential` + `host` to publish to Ghost.

---

## Repo & video pipelines

#### `builtin.repo-presentation` — clone a repo, narrate a deck from its README + docs + structure

**Template**
```
Use helmdeck__pipeline-run to run the builtin.repo-presentation pipeline with inputs:
repo_url = {{REPO_URL}}
```

**Variables**
- `{{REPO_URL}}` — the git repo to clone (input `repo_url`, required). Public repos work as-is; for private, clone the pipeline and add a `credential`.

**Notes** — `repo.fetch` → `repo.map` → `slides.outline` → `slides.narrate`. Builds the deck from the README **plus the repo's docs and code structure** (a fuller picture than the README alone). Silent without an `elevenlabs-key`.

#### `builtin.repo-readme-podcast` — clone a repo, generate a podcast about it

**Template**
```
Use helmdeck__pipeline-run to run the builtin.repo-readme-podcast pipeline with inputs:
repo_url = {{REPO_URL}}
```

**Variables**
- `{{REPO_URL}}` — the git repo to clone (input `repo_url`, required).

**Notes** — `repo.fetch` → `podcast.generate` from the README (default voices). Silent without an `elevenlabs-key`.

#### `builtin.prompt-video` — describe a video, render a silent MP4

**Template**
```
Use helmdeck__pipeline-run to run the builtin.prompt-video pipeline with inputs:
description = {{DESCRIPTION}}
```

**Variables**
- `{{DESCRIPTION}}` — plain-language description of the video to make (input `description`, required).

**Notes** — `hyperframes.compose` (LLM generates the composition) → `hyperframes.render` (1080p, 16:9). No HTML to hand-write; short-form only (≤12 min, 512 MiB).

#### `builtin.prompt-narrated-video` — describe a video, render a narrated MP4

**Template**
```
Use helmdeck__pipeline-run to run the builtin.prompt-narrated-video pipeline with inputs:
description = {{DESCRIPTION}}
```

**Variables**
- `{{DESCRIPTION}}` — the video's topic/script intent (input `description`, required).

**Notes** — `podcast.generate` (narration) → `hyperframes.compose` (visuals synced to the audio) → `hyperframes.render`. Silent (no narration track) without an `elevenlabs-key`.

#### `builtin.html-video` — render a hand-authored HTML/CSS/JS composition to MP4

**Template**
```
Use helmdeck__pipeline-run to run the builtin.html-video pipeline with inputs:
composition_html = {{COMPOSITION_HTML}}
```

**Variables**
- `{{COMPOSITION_HTML}}` — the HTML/CSS/JS composition to render (input `composition_html`, required). **Your agent authors this** (it's not hand-typed) following the HyperFrames contract; or use `builtin.prompt-video` to generate it from a description. Embed an `<audio src>` (e.g. a `podcast.generate` `audio_url`) for narrated video.

**Notes** — single step `hyperframes.render` (1080p, 16:9). Short-form only (≤12 min, 512 MiB).

---

## Coding pipelines (beta — ADR 046)

These wrap `swe.solve` (and `github.*`) for autonomous code changes. Each requires an LLM gateway key; the GitHub-touching ones also need a `github-token` vault credential.

#### `builtin.issue-to-pr` — read a GitHub issue, open a PR that addresses it

**Template**
```
Use helmdeck__pipeline-run to run the builtin.issue-to-pr pipeline with inputs:
repo = {{REPO}}
issue_number = {{ISSUE_NUMBER}}
```

**Variables**
- `{{REPO}}` — `owner/name` (input `repo`, required).
- `{{ISSUE_NUMBER}}` — the issue number to address (input `issue_number`, required).

**Notes** — `github.get_issue` → `swe.solve` (`pull_request` mode) → opens a PR, returns `pr_url`. Single-issue scope; the batch loop is ADR 044 slice 2. Needs `github-token` + an LLM gateway.

#### `builtin.repo-solve-pr` — repo + task → pull request

**Template**
```
Use helmdeck__pipeline-run to run the builtin.repo-solve-pr pipeline with inputs:
repo_url = {{REPO_URL}}
task = {{TASK}}
```

**Variables**
- `{{REPO_URL}}` — the git repo to work in (input `repo_url`, required).
- `{{TASK}}` — free-form task description (input `task`, required).

**Notes** — single step `swe.solve` (`pull_request` mode): clones, runs the agent loop, pushes a branch, opens a PR. For work not yet tracked as an issue.

#### `builtin.repo-solve-patch` — repo + task → diff (safe preview)

**Template**
```
Use helmdeck__pipeline-run to run the builtin.repo-solve-patch pipeline with inputs:
repo_url = {{REPO_URL}}
task = {{TASK}}
```

**Variables**
- `{{REPO_URL}}` — the git repo (input `repo_url`, required).
- `{{TASK}}` — task description (input `task`, required).

**Notes** — `swe.solve` (`patch` mode): returns the unified diff WITHOUT pushing or opening a PR. Use for human review before anything reaches the remote.

#### `builtin.repo-solve-branch` — repo + task → pushed branch (no PR)

**Template**
```
Use helmdeck__pipeline-run to run the builtin.repo-solve-branch pipeline with inputs:
repo_url = {{REPO_URL}}
task = {{TASK}}
```

**Variables**
- `{{REPO_URL}}` — the git repo (input `repo_url`, required).
- `{{TASK}}` — task description (input `task`, required).

**Notes** — `swe.solve` (`branch` mode): pushes a branch with the agent's commits but does NOT open a PR. Use when PR creation lives in another system (GitLab MR, a custom bot).
