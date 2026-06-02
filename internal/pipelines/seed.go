// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import "encoding/json"

// Builtins returns the curated starter pipelines auto-seeded at startup
// (ADR 041). Each has a stable builtin.* id so re-seeding is idempotent.
// Required-but-non-content fields (model, format, resolution, speaker
// voices) use sensible literal defaults so a starter runs with minimal
// inputs; the genuinely user-supplied content (markdown/query/url/…) is
// templated from ${{ inputs.* }}. Callers who want different defaults
// clone-then-edit (built-ins are read-only).
//
// Defaults: model "openrouter/auto"; ElevenLabs premade voices
// (Rachel/Domi) so podcast/narrate run on any account. allow_silent_output
// is threaded from the caller's inputs (NOT defaulted true) — a caller
// asking for "grounded-narrate" or "repo-presentation" wants audio,
// and a missing/rejected ElevenLabs credential should fail-fast with
// credential_invalid instead of silently producing a soundless video.
// Pass allow_silent_output:true on the run input to opt into the
// silence fallback (CI smoke / demo placeholder).
func Builtins() []*Pipeline {
	return []*Pipeline{
		pipe("builtin.grounded-deck", "Grounded slide deck",
			"Cite markdown's factual claims against web sources (content.ground), structure it into a deck (slides.outline), then render a PDF. Optional inputs: persona? (general/technical/marketing/executive/educational/academic — tunes register + per-slide content like code blocks for technical, CTA for marketing), audience?, angle?, title?, author?, export_outline? (saves the Marp markdown as outline.md alongside the PDF), include_image_prompts? (per-slide image-prompt comments + image_prompts: [{slide_index, prompt}] output array).",
			// rewrite:false — citation-only grounding (a full-document
			// rewrite is wasted here since slides.outline restructures the
			// text into slides next). Blog pipelines keep rewrite:true.
			step("ground", "content.ground", `{"text":"${{ inputs.markdown }}","model":"openrouter/auto","rewrite":false}`),
			step("outline", "slides.outline", `{"text":"${{ steps.ground.output.grounded_text }}","model":"openrouter/auto","persona":"${{ inputs.persona }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","title":"${{ inputs.title }}","author":"${{ inputs.author }}","export_outline":"${{ inputs.export_outline }}","include_image_prompts":"${{ inputs.include_image_prompts }}"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"markdown"},
			Produces:       []string{"pdf", "slide_deck"},
			IntentKeywords: []string{"make deck", "slide deck from notes", "presentation from markdown", "grounded slides"},
			TypicalUse:     "When the user has markdown notes / a README and wants a cited PDF slide deck with audience-aware persona.",
			Limitations:    []string{"PDF only — use grounded-narrate for an MP4", "needs ≥2 slides of source material (slides.outline rejects thin content caller_fixable)"},
			Supersedes:     []string{"content.ground", "slides.outline", "slides.render"},
		}),
		pipe("builtin.brief-rewrite-blog", "Brief → rewrite → blog",
			"Translate a pasted brief / pitch / outline of ideas into an ORIGINAL blog post for a stated audience (blog.rewrite_for_audience — expands the brief, leads with why-it-matters, de-jargons, connects to the audience's tools, adds perspective; stays grounded in the brief's framing), then cite the new prose against web sources (content.ground, citation-only). Output includes inline [1] citations from content.ground — strip in post-processing for conversational publication targets (dev.to / Medium / company blog). Optionally append a natural-voice call-to-action via blog.append_cta when one of project_url / github_url / source_url is set (no-op otherwise). Then save as a blog-post artifact. Inputs: brief, audience, angle?, persona?, title; optional CTA inputs project_url?, github_url?, source_url?, cta_copy?. Use this when the user pastes a brief like \"Title Idea: …\\nThe Hook: …\\nWhat to Cover: …\\nTarget Audience: …\" — NOT for a finished draft. Replaces builtin.grounded-blog, which only added citations to whatever it received (an annotator, not a generator) so a brief came back as ~the brief, not a real blog post.",
			step("rewrite", "blog.rewrite_for_audience", `{"source_content":"${{ inputs.brief }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","persona":"${{ inputs.persona }}","title":"${{ inputs.title }}","model":"openrouter/auto"}`),
			step("ground", "content.ground", `{"text":"${{ steps.rewrite.output.markdown }}","model":"openrouter/auto","rewrite":false}`),
			// blog.append_cta is a no-op when no promotional inputs are
			// passed, so the step slots in unconditionally. When at
			// least one of project_url / github_url / source_url is
			// set it LLM-rewrites a closing CTA in the article's voice
			// using the same audience+persona threaded into the rewrite
			// step above.
			step("cta", "blog.append_cta", `{"markdown":"${{ steps.ground.output.grounded_text }}","source_url":"${{ inputs.source_url }}","project_url":"${{ inputs.project_url }}","github_url":"${{ inputs.github_url }}","cta_copy":"${{ inputs.cta_copy }}","audience":"${{ inputs.audience }}","persona":"${{ inputs.persona }}","model":"openrouter/auto"}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.cta.output.markdown }}"}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"brief", "markdown"},
			Produces:       []string{"blog_markdown"},
			IntentKeywords: []string{"make blog from brief", "expand pitch into blog post", "write blog from outline notes", "title idea + hook + what to cover"},
			TypicalUse:     "When the user pastes a brief (Title Idea / Hook / What to Cover / Target Audience) and wants an original blog post.",
			Limitations:    []string{"not for finished drafts — use content.ground directly for citation-only", "not for documents (use doc-rewrite-blog) or URLs (use scrape-rewrite-blog)", "output includes inline [1] citations from content.ground; strip in post-processing for conversational targets"},
			Supersedes:     []string{"blog.rewrite_for_audience", "content.ground", "blog.publish"},
		}),
		pipe("builtin.grounded-narrate", "Grounded narrated video",
			"Cite markdown's factual claims against web sources (content.ground), structure it into a deck (slides.outline), then render a narrated MP4 (slides.narrate). Falls back to silent video when no elevenlabs-key is configured. Optional inputs: persona?, audience?, angle?, title?, author?, export_outline? (Marp markdown artifact), include_image_prompts? — same set as builtin.grounded-deck.",
			// rewrite:false matches grounded-deck — slides.outline restructures
			// the cited prose into slides next, so a full rewrite would be
			// wasted work.
			step("ground", "content.ground", `{"text":"${{ inputs.markdown }}","model":"openrouter/auto","rewrite":false}`),
			step("outline", "slides.outline", `{"text":"${{ steps.ground.output.grounded_text }}","model":"openrouter/auto","persona":"${{ inputs.persona }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","title":"${{ inputs.title }}","author":"${{ inputs.author }}","export_outline":"${{ inputs.export_outline }}","include_image_prompts":"${{ inputs.include_image_prompts }}"}`),
			// allow_silent_output deliberately omitted (changed
			// behavior). Callers asking for "grounded NARRATE" want
			// audio; if the ElevenLabs credential is missing or
			// rejected, fail-fast with credential_invalid instead
			// of silently producing a video without audio. The
			// caller can still opt into silence explicitly:
			// pass "allow_silent_output":true in their input.
			step("narrate", "slides.narrate", `{"markdown":"${{ steps.outline.output.markdown }}","allow_silent_output":"${{ inputs.allow_silent_output }}"}`),
		),
		pipe("builtin.grounded-podcast", "Grounded podcast",
			"Cite markdown's factual claims against web sources (content.ground), then generate a multi-speaker podcast (podcast.generate).",
			step("ground", "content.ground", `{"text":"${{ inputs.markdown }}","model":"openrouter/auto","rewrite":false}`),
			step("podcast", "podcast.generate", `{"source_text":"${{ steps.ground.output.grounded_text }}","model":"openrouter/auto","speakers":`+defaultSpeakers+`,"allow_silent_output":true}`),
		),
		pipe("builtin.research-deck", "Research → slide deck",
			"Deep-research a topic, structure the synthesis into a deck (slides.outline), then render a PDF. Optional inputs: persona?, audience?, angle?, title?, author?, export_outline?, include_image_prompts? — same set as builtin.grounded-deck.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("outline", "slides.outline", `{"text":"${{ steps.research.output.synthesis }}","model":"openrouter/auto","persona":"${{ inputs.persona }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","title":"${{ inputs.title }}","author":"${{ inputs.author }}","export_outline":"${{ inputs.export_outline }}","include_image_prompts":"${{ inputs.include_image_prompts }}"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		),
		pipe("builtin.research-narrate", "Research → narrated video",
			"Deep-research a topic, structure the synthesis into a deck (slides.outline), then render a narrated video. Optional inputs: persona?, audience?, angle?, title?, author?, export_outline?, include_image_prompts? — same set as builtin.grounded-deck.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("outline", "slides.outline", `{"text":"${{ steps.research.output.synthesis }}","model":"openrouter/auto","persona":"${{ inputs.persona }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","title":"${{ inputs.title }}","author":"${{ inputs.author }}","export_outline":"${{ inputs.export_outline }}","include_image_prompts":"${{ inputs.include_image_prompts }}"}`),
			// allow_silent_output deliberately omitted — caller can
			// opt in by passing it explicitly. See grounded-narrate
			// for the rationale.
			step("narrate", "slides.narrate", `{"markdown":"${{ steps.outline.output.markdown }}","allow_silent_output":"${{ inputs.allow_silent_output }}"}`),
		),
		pipe("builtin.research-podcast", "Research → podcast",
			"Deep-research a topic, then generate a multi-speaker podcast.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("podcast", "podcast.generate", `{"source_text":"${{ steps.research.output.synthesis }}","model":"openrouter/auto","speakers":`+defaultSpeakers+`,"allow_silent_output":true}`),
		),
		pipe("builtin.scrape-rewrite-blog", "Scrape → rewrite → blog",
			"Scrape a URL to markdown, then translate it into an ORIGINAL blog post for a stated audience (blog.rewrite_for_audience — de-jargons, leads with why-it-matters, connects to the audience's tools, adds perspective; stays grounded in the scraped source), then cite the new prose against web sources (content.ground, citation-only). Output includes inline [1] citations from content.ground — strip in post-processing for conversational publication targets (dev.to / Medium / company blog). Optionally append a natural-voice call-to-action via blog.append_cta when one of project_url / github_url / source_url is set (no-op otherwise). Then save as a blog-post artifact. Inputs: url, audience, angle?, persona? (general/technical/marketing/executive/educational/academic), title; optional CTA inputs project_url?, github_url?, source_url?, cta_copy?. Replaces builtin.scrape-ground-blog, which produced a citation-strengthened transcription that read as republishing the scraped page.",
			step("scrape", "web.scrape", `{"url":"${{ inputs.url }}"}`),
			step("rewrite", "blog.rewrite_for_audience", `{"source_content":"${{ steps.scrape.output.markdown }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","persona":"${{ inputs.persona }}","title":"${{ inputs.title }}","model":"openrouter/auto"}`),
			step("ground", "content.ground", `{"text":"${{ steps.rewrite.output.markdown }}","model":"openrouter/auto","rewrite":false}`),
			// blog.append_cta is a no-op when no promotional inputs
			// are passed (see brief-rewrite-blog above for the design
			// rationale).
			step("cta", "blog.append_cta", `{"markdown":"${{ steps.ground.output.grounded_text }}","source_url":"${{ inputs.source_url }}","project_url":"${{ inputs.project_url }}","github_url":"${{ inputs.github_url }}","cta_copy":"${{ inputs.cta_copy }}","audience":"${{ inputs.audience }}","persona":"${{ inputs.persona }}","model":"openrouter/auto"}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.cta.output.markdown }}"}`),
		),
		pipe("builtin.research-ground-deck", "Research → ground → deck",
			"Deep-research a topic, cite the synthesis against web sources (content.ground), structure it into a deck (slides.outline), then render. Optional inputs: persona?, audience?, angle?, title?, author?, export_outline?, include_image_prompts? — same set as builtin.grounded-deck.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			// rewrite:false — citation-only; slides.outline structures the
			// cited synthesis into slides next, so a prose rewrite is wasted.
			step("ground", "content.ground", `{"text":"${{ steps.research.output.synthesis }}","model":"openrouter/auto","rewrite":false}`),
			step("outline", "slides.outline", `{"text":"${{ steps.ground.output.grounded_text }}","model":"openrouter/auto","persona":"${{ inputs.persona }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","title":"${{ inputs.title }}","author":"${{ inputs.author }}","export_outline":"${{ inputs.export_outline }}","include_image_prompts":"${{ inputs.include_image_prompts }}"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		),
		pipe("builtin.doc-rewrite-blog", "Document → rewrite → blog",
			"Parse a document (PDF/DOCX/…), then translate it into an ORIGINAL blog post for a stated audience (blog.rewrite_for_audience — de-jargons, leads with why-it-matters, connects to the audience's tools, adds perspective; stays grounded in the source), then cite the new prose against web sources (content.ground, citation-only). Output includes inline [1] citations from content.ground — strip in post-processing for conversational publication targets (dev.to / Medium / company blog). Optionally append a natural-voice call-to-action via blog.append_cta when one of project_url / github_url / cta_source_url is set (no-op otherwise; cta_source_url is separate from source_url so the CTA stays opt-in). Then save as a blog-post artifact. Inputs: source_url (doc URL), audience, angle?, persona? (general/technical/marketing/executive/educational/academic), title; optional CTA inputs project_url?, github_url?, cta_source_url?, cta_copy?. Replaces builtin.doc-ground-blog, which produced a citation-strengthened transcription that read as republishing rather than as an original post.",
			step("parse", "doc.parse", `{"source_url":"${{ inputs.source_url }}"}`),
			// rewrite is the new step — turns the source into an
			// original post for the audience+angle the caller supplied.
			step("rewrite", "blog.rewrite_for_audience", `{"source_content":"${{ steps.parse.output.markdown }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","persona":"${{ inputs.persona }}","title":"${{ inputs.title }}","model":"openrouter/auto"}`),
			// citation-only ground (rewrite:false) — the rewrite step
			// already restructured the prose; this pass verifies it
			// against web sources and inserts inline citations.
			step("ground", "content.ground", `{"text":"${{ steps.rewrite.output.markdown }}","model":"openrouter/auto","rewrite":false}`),
			// CTA append — the CTA's source URL is a SEPARATE input
			// (cta_source_url) to keep it OPT-IN. inputs.source_url
			// is the doc URL being parsed and is always set; threading
			// that into the CTA would fire the LLM call on every
			// doc-rewrite-blog run regardless of whether the caller
			// wanted promotion. Callers who do want the doc URL
			// surfaced in the CTA pass it explicitly as cta_source_url.
			step("cta", "blog.append_cta", `{"markdown":"${{ steps.ground.output.grounded_text }}","source_url":"${{ inputs.cta_source_url }}","project_url":"${{ inputs.project_url }}","github_url":"${{ inputs.github_url }}","cta_copy":"${{ inputs.cta_copy }}","audience":"${{ inputs.audience }}","persona":"${{ inputs.persona }}","model":"openrouter/auto"}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.cta.output.markdown }}"}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"pdf", "docx", "source_url"},
			Produces:       []string{"blog_markdown"},
			IntentKeywords: []string{"blog from PDF", "blog from document", "summarize paper as blog", "post about this paper"},
			TypicalUse:     "When the user has a document (PDF / DOCX / academic paper) and wants an original blog post for a stated audience.",
			Limitations:    []string{"requires HELMDECK_DOCLING_ENABLED", "source_url must have a document extension (web pages → scrape-rewrite-blog)"},
			Supersedes:     []string{"doc.parse", "blog.rewrite_for_audience", "content.ground", "blog.publish"},
		}),
		pipe("builtin.scrape-deck", "Scrape → slide deck",
			"Scrape a URL to markdown, structure it into a deck (slides.outline), then render a PDF (no grounding). Optional inputs: persona?, audience?, angle?, title?, author?, export_outline?, include_image_prompts? — same set as builtin.grounded-deck.",
			step("scrape", "web.scrape", `{"url":"${{ inputs.url }}"}`),
			step("outline", "slides.outline", `{"text":"${{ steps.scrape.output.markdown }}","model":"openrouter/auto","persona":"${{ inputs.persona }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","title":"${{ inputs.title }}","author":"${{ inputs.author }}","export_outline":"${{ inputs.export_outline }}","include_image_prompts":"${{ inputs.include_image_prompts }}"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		),
		pipe("builtin.research-rewrite-blog", "Research → rewrite → blog",
			"Deep-research a topic (research.deep — multi-source synthesis via Firecrawl search + scrape, deeper than a single web.scrape), then translate the synthesis into an ORIGINAL blog post for a stated audience (blog.rewrite_for_audience), then cite the new prose against web sources (content.ground, citation-only). Output includes inline [1] citations from content.ground — strip in post-processing for conversational publication targets (dev.to / Medium / company blog). Optionally append a natural-voice call-to-action via blog.append_cta when one of project_url / github_url / cta_source_url is set (no-op otherwise). Then save as a blog-post artifact. Inputs: query, audience, angle?, persona? (general/technical/marketing/executive/educational/academic), title; optional CTA inputs project_url?, github_url?, cta_source_url?, cta_copy?. Picks the deeper synthesis path when the topic warrants multi-source research rather than a single-URL scrape (compare with builtin.scrape-rewrite-blog). Replaces builtin.research-blog, which saved the raw synthesis without tailoring it to an audience — useful as research notes but generic as a blog post.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("rewrite", "blog.rewrite_for_audience", `{"source_content":"${{ steps.research.output.synthesis }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","persona":"${{ inputs.persona }}","title":"${{ inputs.title }}","model":"openrouter/auto"}`),
			step("ground", "content.ground", `{"text":"${{ steps.rewrite.output.markdown }}","model":"openrouter/auto","rewrite":false}`),
			step("cta", "blog.append_cta", `{"markdown":"${{ steps.ground.output.grounded_text }}","source_url":"${{ inputs.cta_source_url }}","project_url":"${{ inputs.project_url }}","github_url":"${{ inputs.github_url }}","cta_copy":"${{ inputs.cta_copy }}","audience":"${{ inputs.audience }}","persona":"${{ inputs.persona }}","model":"openrouter/auto"}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.cta.output.markdown }}"}`),
		),
		pipe("builtin.repo-presentation", "Repo → presentation video",
			"Clone a repo, map its code structure (repo.map) and gather its docs, outline a deck from the README + docs + structure (slides.outline), then render a narrated video — a fuller picture than the README alone. Optional inputs: persona?, audience?, angle?, title?, author?, export_outline?, include_image_prompts? — same set as builtin.grounded-deck.",
			step("fetch", "repo.fetch", `{"url":"${{ inputs.repo_url }}"}`),
			// repo.map reuses repo.fetch's session (the runner auto-threads
			// _session_id) so it reads the same clone and returns a symbol map.
			step("map", "repo.map", `{"clone_path":"${{ steps.fetch.output.clone_path }}"}`),
			// Feed the LLM the README + the repo's own docs + its code
			// structure, so the deck reflects what the project is AND how it's
			// built — not a paraphrase of the front page. docs.content is "" when
			// the repo has none (repo.fetch always emits it), so this resolves.
			step("outline", "slides.outline", `{"text":"# README\n${{ steps.fetch.output.readme.content }}\n\n# Project docs\n${{ steps.fetch.output.docs.content }}\n\n# Code structure (symbol map)\n${{ steps.map.output.map }}","model":"openrouter/auto","persona":"${{ inputs.persona }}","audience":"${{ inputs.audience }}","angle":"${{ inputs.angle }}","title":"${{ inputs.title }}","author":"${{ inputs.author }}","export_outline":"${{ inputs.export_outline }}","include_image_prompts":"${{ inputs.include_image_prompts }}"}`),
			// allow_silent_output deliberately omitted — caller can
			// opt in by passing it explicitly. See grounded-narrate
			// for the rationale.
			step("narrate", "slides.narrate", `{"markdown":"${{ steps.outline.output.markdown }}","allow_silent_output":"${{ inputs.allow_silent_output }}"}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"repo_url"},
			Produces:       []string{"mp4", "narrated_video"},
			IntentKeywords: []string{"video about repo", "narrate this codebase", "presentation from GitHub project", "explain repo as a video"},
			TypicalUse:     "When the user wants a narrated MP4 walkthrough of a GitHub repo — uses README + docs + code map as source.",
			Limitations:    []string{"narrated video only — for a podcast use repo-readme-podcast", "requires ElevenLabs key for narration (falls back to silent video)"},
			Supersedes:     []string{"repo.fetch", "repo.map", "slides.outline", "slides.narrate"},
		}),
		pipe("builtin.repo-readme-podcast", "Repo → podcast",
			"Clone a repo and generate a podcast about it from its README.",
			step("fetch", "repo.fetch", `{"url":"${{ inputs.repo_url }}"}`),
			step("podcast", "podcast.generate", `{"source_text":"${{ steps.fetch.output.readme.content }}","model":"openrouter/auto","speakers":`+defaultSpeakers+`,"allow_silent_output":true}`),
		),
		pipe("builtin.html-video", "HTML composition → MP4",
			"Render an HTML/CSS/JS composition (authored by your agent, not hand-typed) to a deterministic MP4. Optional inputs resolution? (720p/1080p/4k) and aspect_ratio? (16:9 default / 9:16 for Shorts/TikTok / 1:1 square) are threaded to hyperframes.render — a composition whose intrinsic dimensions are vertical (e.g. 1080×1920) needs aspect_ratio:\"9:16\" set explicitly. To generate the composition from a plain description instead, use builtin.prompt-video.",
			step("render", "hyperframes.render", `{"composition_html":"${{ inputs.composition_html }}","resolution":"${{ inputs.resolution }}","aspect_ratio":"${{ inputs.aspect_ratio }}"}`),
		),
		pipe("builtin.prompt-video", "Describe → video",
			"Describe a video in plain language; an LLM generates a HyperFrames composition (hyperframes.compose) and renders it to a silent MP4 (hyperframes.render) — no hand-written HTML. Optional inputs resolution? (720p/1080p/4k) and aspect_ratio? (16:9 default / 9:16 for vertical / 1:1 square) are threaded to both the compose and render steps so the generated composition matches the target orientation.",
			step("compose", "hyperframes.compose", `{"description":"${{ inputs.description }}","model":"openrouter/auto","aspect_ratio":"${{ inputs.aspect_ratio }}"}`),
			step("render", "hyperframes.render", `{"composition_html":"${{ steps.compose.output.composition_html }}","resolution":"${{ inputs.resolution }}","aspect_ratio":"${{ inputs.aspect_ratio }}"}`),
		),
		pipe("builtin.prompt-narrated-video", "Describe → narrated video",
			"Describe a video; generate a podcast narration (podcast.generate), compose visuals synced to it (hyperframes.compose), and render a narrated MP4 (hyperframes.render). Silent without an elevenlabs-key. Optional inputs resolution? (720p/1080p/4k) and aspect_ratio? (16:9/9:16/1:1) are threaded to compose and render so the composition matches the target orientation.",
			step("podcast", "podcast.generate", `{"source_text":"${{ inputs.description }}","model":"openrouter/auto","speakers":`+defaultSpeakers+`,"allow_silent_output":true}`),
			// audio_url + duration_s flow from the podcast step so the composition
			// embeds the narration and matches its length; podcast.generate always
			// emits both (audio_url is "" on a keyless store → a silent video).
			step("compose", "hyperframes.compose", `{"description":"${{ inputs.description }}","model":"openrouter/auto","aspect_ratio":"${{ inputs.aspect_ratio }}","audio_url":"${{ steps.podcast.output.audio_url }}","duration_seconds":"${{ steps.podcast.output.duration_s }}"}`),
			step("render", "hyperframes.render", `{"composition_html":"${{ steps.compose.output.composition_html }}","resolution":"${{ inputs.resolution }}","aspect_ratio":"${{ inputs.aspect_ratio }}"}`),
		),

		// ── Coding (beta) — ADR 046 ─────────────────────────────────
		// Each name ends with " (beta)" so the UI renders a beta Badge;
		// each description starts with "[beta]" so MCP-listing agents
		// see the status too. Drop both markers when the pipelines
		// graduate.
		pipe("builtin.issue-to-pr", "Issue → PR (beta)",
			"[beta] Read a GitHub issue by number (github.get_issue), hand its title + body to swe.solve in pull_request mode, and emit the opened PR's URL. Requires a `github-token` vault credential and an LLM gateway key for swe.solve. Single-issue scope; the production batch loop (process every open issue, conditional skip) is ADR 044 slice 2.",
			step("issue", "github.get_issue", `{"repo":"${{ inputs.repo }}","issue_number":"${{ inputs.issue_number }}"}`),
			step("solve", "swe.solve", `{"repo_url":"https://github.com/${{ inputs.repo }}.git","task":"${{ steps.issue.output.title }}\n\n${{ steps.issue.output.body }}","mode":"pull_request"}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"repo", "issue_number"},
			Produces:       []string{"pr_url"},
			IntentKeywords: []string{"fix issue and open PR", "implement GitHub issue", "issue to PR", "close this issue with a PR"},
			TypicalUse:     "When the user has a specific GitHub issue and wants an agent-driven PR opened against it.",
			Limitations:    []string{"beta — single issue per run", "requires github-token vault credential + LLM gateway", "no conditional skip for wontfix/closed (ADR 044 slice 2)"},
			Supersedes:     []string{"github.get_issue", "swe.solve"},
		}),
		pipe("builtin.repo-solve-pr", "Repo + task → PR (beta)",
			"[beta] Hand swe.solve a repo URL + a free-form task description; it clones, runs the mini-swe-agent loop, pushes a branch, and opens a pull request. For tasks not yet tracked as an issue. Returns pr_url.",
			step("solve", "swe.solve", `{"repo_url":"${{ inputs.repo_url }}","task":"${{ inputs.task }}","mode":"pull_request"}`),
		),
		pipe("builtin.repo-solve-patch", "Repo + task → diff (beta)",
			"[beta] Safe preview: swe.solve runs the agent loop and returns the unified diff WITHOUT pushing a branch or opening a PR. Use this when you want a human review of the agent's work before anything reaches the remote.",
			step("solve", "swe.solve", `{"repo_url":"${{ inputs.repo_url }}","task":"${{ inputs.task }}","mode":"patch"}`),
		),
		pipe("builtin.repo-solve-branch", "Repo + task → branch (beta)",
			"[beta] swe.solve pushes a branch with the agent's commits but does NOT open a pull request. Use when PR creation lives in another system (GitLab MR, a custom bot) and you want to wire it up downstream.",
			step("solve", "swe.solve", `{"repo_url":"${{ inputs.repo_url }}","task":"${{ inputs.task }}","mode":"branch"}`),
		),
	}
}

// defaultSpeakers is a 2-speaker map using stable ElevenLabs premade
// voice IDs (Rachel + Domi) so podcast starters run on any account.
const defaultSpeakers = `{"Host":"21m00Tcm4TlvDq8ikWAM","Guest":"AZnzlk1XvdvUeBnXmlld"}`

func step(id, pack, input string) Step {
	return Step{ID: id, Pack: pack, Input: json.RawMessage(input)}
}

func pipe(id, name, desc string, steps ...Step) *Pipeline {
	return &Pipeline{ID: id, Name: name, Description: desc, Builtin: true, Steps: steps}
}

// withMeta attaches the structured routing metadata declared in ADR 047
// onto a pipeline. Designed to be chained off pipe(...) at the call site
// so the metadata reads next to the steps — kept as a method rather
// than a pipe() variadic so the common no-metadata case stays terse.
func (p *Pipeline) withMeta(m PipelineMetadata) *Pipeline {
	p.Metadata = m
	return p
}
