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
			// metadata_model threaded so the pipeline turns engagement
			// metadata ON by default for pipeline runs (the bare
			// slides.narrate pack stays opt-in for back-compat).
			step("narrate", "slides.narrate", `{"markdown":"${{ steps.outline.output.markdown }}","allow_silent_output":"${{ inputs.allow_silent_output }}","metadata_model":"openrouter/auto"}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"repo_url"},
			Produces:       []string{"mp4", "narrated_video", "engagement_metadata", "srt_captions"},
			IntentKeywords: []string{"video about repo", "narrate this codebase", "presentation from GitHub project", "explain repo as a video"},
			TypicalUse:     "When the user wants a narrated MP4 walkthrough of a GitHub repo — uses README + docs + code map as source. Emits an `engagement` object (title, description, chapters, hashtags, hook) sized for YouTube AND a sidecar `captions.srt` (YouTube/Vimeo auto-import as CC track).",
			Limitations: []string{
				"narrated video only — for a podcast use repo-readme-podcast",
				"requires ElevenLabs key for narration (falls back to silent video)",
				// Engagement-honesty entry — slide-deck-with-voiceover
				// is a structurally lower-retention format than talking-head;
				// metadata moves the artifact to median-within-format but
				// can't bridge the gap. See engagement.format_ceiling_note
				// in the slides.narrate output for the long-form note.
				"engagement metadata is generated post-hoc and is research-shaped (chapters at 0:00, title char-cap, hook structure); won't bridge the structural retention gap vs talking-head video — best for asynchronous explainer content",
				// Captions-honesty entry — sidecar SRT is free
				// (research-cited ~12-13% YouTube view boost via auto-imported
				// CC); burn-in is opt-in and carries real cost.
				"captions sidecar (captions.srt) is auto-imported by YouTube/Vimeo as the CC track; burned-in captions are opt-in via slides.narrate captions_burn_in:true and add 5-50% encode time + 20-50 MB per encoder thread — large decks on tight memory limits may OOM at burn-in",
			},
			Supersedes: []string{"repo.fetch", "repo.map", "slides.outline", "slides.narrate"},
		}),
		pipe("builtin.repo-readme-podcast", "Repo → podcast",
			"Clone a repo and generate a podcast about it from its README. Emits an `engagement` object (title, subtitle, summary, show notes, Podcasting 2.0 chapters, mid-roll CTA) alongside the MP3.",
			step("fetch", "repo.fetch", `{"url":"${{ inputs.repo_url }}"}`),
			step("podcast", "podcast.generate", `{"source_text":"${{ steps.fetch.output.readme.content }}","model":"openrouter/auto","speakers":`+defaultSpeakers+`,"allow_silent_output":true}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"repo_url"},
			Produces:       []string{"mp3", "podcast_audio", "engagement_metadata"},
			IntentKeywords: []string{"podcast from repo", "audio walkthrough of repo", "narrate repo readme"},
			TypicalUse:     "When the user wants an audio-only podcast of a repo (no video). Engagement metadata defaults follow Apple/Spotify spec.",
			Limitations: []string{
				"audio only — for a narrated video use repo-presentation",
				"requires ElevenLabs key for narration (falls back to silence-padded MP3)",
				"engagement metadata reflects Apple/Spotify spec; solo vs co-hosted retention is execution-dependent — pack supports both",
			},
			Supersedes: []string{"repo.fetch", "podcast.generate"},
		}),
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
		).withMeta(PipelineMetadata{
			Accepts:        []string{"description"},
			Produces:       []string{"mp4", "narrated_video"},
			IntentKeywords: []string{"describe a video", "narrated video from prompt", "make a video about", "narrate this idea as a video"},
			TypicalUse:     "When the user has a description but no source repo or deck — generates a narrated MP4 directly from prose.",
			Limitations: []string{
				"requires ElevenLabs key for narration (falls back to silent video)",
				"engagement metadata is produced as part of the intermediate podcast.generate step but the final hyperframes.render output is the MP4 — fetch engagement from the podcast step output if a publish bundle is needed",
			},
			Supersedes: []string{"podcast.generate", "hyperframes.compose", "hyperframes.render"},
		}),
		// builtin.scaffolded-narrated-video (#503 Option C, PR 7) —
		// the four-pack scaffold-mode chain wired up: podcast.generate
		// for narration, hyperframes.scaffold to pick an upstream
		// example, hyperframes.interpolate for LLM-driven content
		// rewriting, hyperframes.render for the MP4. Sibling pipeline
		// to prompt-narrated-video (above): same narration + render
		// halves, different compose strategy. Prompt-narrated-video
		// asks the LLM to author HTML from scratch (great on Tier A,
		// produces visually-flat output on Tier C). This one borrows
		// upstream's polished examples; the LLM only does content
		// interpolation, so Tier C produces visually-rich output
		// reliably.
		pipe("builtin.scaffolded-narrated-video", "Describe → narrated video (scaffolded)",
			"Describe a video; pick an upstream HyperFrames example (swiss-grid, decision-tree, code-snippet-dark-modern, kinetic-type, nyt-graph, tiktok-follow, etc. — 140+ in the catalog); generate podcast narration (podcast.generate), scaffold the project from the chosen example WITH the narration audio embedded (hyperframes.scaffold gets audio_url so upstream's `hyperframes init --audio` sets the composition duration to match the audio length), interpolate visible text + caption transcript to fit the topic (hyperframes.interpolate), and render the final MP4 (hyperframes.render). Tier-C-friendly: the LLM only does content interpolation, not HTML authoring — visual polish comes from the upstream example. Required input description, example. Optional inputs duration_target_min? (narration target in minutes; default 1 = 60s social-first; max 12 for long-form), resolution? (1080p/4k), aspect_ratio? (16:9/9:16/1:1). Silent without an elevenlabs-key. For an A-roll image, chain image.generate + hyperframes.attach_asset between interpolate and render. For freeform HTML control (Tier A authoring from scratch), use builtin.prompt-narrated-video instead.",
			// duration_target_min defaults to 60s (1 minute) for social-
			// first output. podcast.generate's own default is 8 min —
			// callers who want long-form pass duration_target_min: 12
			// at pipeline-run time. When inputs.duration_target_min is
			// empty the JSON value is the literal empty string, which
			// the pack's int field defaults to 0 → defaultIfZero applies
			// → uses podcast.generate's defaultPodcastDurationMin (8).
			// To get the 60s social default, the operator must pass
			// duration_target_min: 1 explicitly.
			step("podcast", "podcast.generate", `{"source_text":"${{ inputs.description }}","model":"openrouter/auto","speakers":`+defaultSpeakers+`,"allow_silent_output":true,"duration_target_min":"${{ inputs.duration_target_min }}"}`),
			// audio_url threads from podcast.generate's output so the
			// scaffold gets `hyperframes init --audio=<staged-audio>`.
			// Upstream embeds the audio element and sets data-duration
			// to the audio length — otherwise the scaffold uses the
			// example's intrinsic (~10s) default and the rendered video
			// is silent at scaffold-default duration.
			step("scaffold", "hyperframes.scaffold", `{"example":"${{ inputs.example }}","resolution":"${{ inputs.resolution }}","aspect_ratio":"${{ inputs.aspect_ratio }}","audio_url":"${{ steps.podcast.output.audio_url }}"}`),
			// duration_s threads from podcast.generate so the caption
			// transcript regenerates at the right length for the audio.
			step("interpolate", "hyperframes.interpolate", `{"project_artifact_key":"${{ steps.scaffold.output.project_artifact_key }}","description":"${{ inputs.description }}","model":"openrouter/auto","duration_seconds":"${{ steps.podcast.output.duration_s }}"}`),
			step("render", "hyperframes.render", `{"project_artifact_key":"${{ steps.interpolate.output.project_artifact_key }}","resolution":"${{ inputs.resolution }}","aspect_ratio":"${{ inputs.aspect_ratio }}"}`),
		).withMeta(PipelineMetadata{
			Accepts:        []string{"description", "example"},
			Produces:       []string{"mp4", "narrated_video", "scaffolded_video"},
			IntentKeywords: []string{"scaffolded narrated video", "narrated video using a hyperframes example", "make a video using swiss-grid", "make a video using decision-tree", "tier C narrated video", "polished narrated video"},
			TypicalUse:     "When the user wants a narrated MP4 with polished visuals borrowed from upstream's example catalog (vs. authoring HTML from scratch). Tier-C-friendly because the LLM only interpolates content into a known-good scaffold.",
			Limitations: []string{
				"no A-roll image/video — chain image.generate + hyperframes.attach_asset between interpolate and render if you need one",
				"requires ElevenLabs key for narration (falls back to silent video via podcast.generate's allow_silent_output)",
				"example name must be in upstream's registry — see the hyperframes.scaffold reference doc for the catalog (or pass an invalid name to surface the full list in the error)",
				"caption transcript timing is heuristic (~150 wpm cadence); not whisper-aligned to actual audio",
				"duration_target_min defaults to podcast.generate's intrinsic 8-minute target when unset; pass duration_target_min: 1 for a 60-second social-first video or duration_target_min: 12 for the long-form cap",
			},
			Supersedes: []string{"hyperframes.scaffold", "hyperframes.interpolate", "podcast.generate", "hyperframes.render"},
		}),

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
