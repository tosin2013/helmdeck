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
// (Rachel/Domi) so podcast/narrate run on any account, with
// allow_silent_output so a keyless deployment still produces output.
func Builtins() []*Pipeline {
	return []*Pipeline{
		pipe("builtin.grounded-deck", "Grounded slide deck",
			"Fact-check + add citations to markdown (content.ground), structure it into a deck (slides.outline), then render a PDF.",
			// rewrite:false — citation-only grounding (a full-document
			// rewrite is wasted here since slides.outline restructures the
			// text into slides next). Blog pipelines keep rewrite:true.
			step("ground", "content.ground", `{"text":"${{ inputs.markdown }}","model":"openrouter/auto","rewrite":false}`),
			step("outline", "slides.outline", `{"text":"${{ steps.ground.output.grounded_text }}","model":"openrouter/auto"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		),
		pipe("builtin.grounded-blog", "Grounded blog post",
			"Fact-check + rewrite markdown, then publish it as a blog post.",
			step("ground", "content.ground", `{"text":"${{ inputs.markdown }}","model":"openrouter/auto","rewrite":true}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.ground.output.grounded_text }}"}`),
		),
		pipe("builtin.research-deck", "Research → slide deck",
			"Deep-research a topic, structure the synthesis into a deck (slides.outline), then render a PDF.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("outline", "slides.outline", `{"text":"${{ steps.research.output.synthesis }}","model":"openrouter/auto"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		),
		pipe("builtin.research-narrate", "Research → narrated video",
			"Deep-research a topic, structure the synthesis into a deck (slides.outline), then render a narrated video.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("outline", "slides.outline", `{"text":"${{ steps.research.output.synthesis }}","model":"openrouter/auto"}`),
			step("narrate", "slides.narrate", `{"markdown":"${{ steps.outline.output.markdown }}","allow_silent_output":true}`),
		),
		pipe("builtin.research-podcast", "Research → podcast",
			"Deep-research a topic, then generate a multi-speaker podcast.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("podcast", "podcast.generate", `{"source_text":"${{ steps.research.output.synthesis }}","speakers":`+defaultSpeakers+`,"allow_silent_output":true}`),
		),
		pipe("builtin.scrape-ground-blog", "Scrape → ground → blog",
			"Scrape a URL to markdown, fact-check + rewrite it, then publish a blog post.",
			step("scrape", "web.scrape", `{"url":"${{ inputs.url }}"}`),
			step("ground", "content.ground", `{"text":"${{ steps.scrape.output.markdown }}","model":"openrouter/auto","rewrite":true}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.ground.output.grounded_text }}"}`),
		),
		pipe("builtin.research-ground-deck", "Research → ground → deck",
			"Deep-research a topic, fact-check + cite the synthesis, structure it into a deck (slides.outline), then render.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			// rewrite:false — citation-only; slides.outline structures the
			// cited synthesis into slides next, so a prose rewrite is wasted.
			step("ground", "content.ground", `{"text":"${{ steps.research.output.synthesis }}","model":"openrouter/auto","rewrite":false}`),
			step("outline", "slides.outline", `{"text":"${{ steps.ground.output.grounded_text }}","model":"openrouter/auto"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		),
		pipe("builtin.doc-ground-blog", "Document → ground → blog",
			"Parse a document (PDF/DOCX/…) to markdown, fact-check + rewrite, then publish.",
			step("parse", "doc.parse", `{"source_url":"${{ inputs.source_url }}"}`),
			step("ground", "content.ground", `{"text":"${{ steps.parse.output.markdown }}","model":"openrouter/auto","rewrite":true}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.ground.output.grounded_text }}"}`),
		),
		pipe("builtin.scrape-deck", "Scrape → slide deck",
			"Scrape a URL to markdown, structure it into a deck (slides.outline), then render a PDF (no grounding).",
			step("scrape", "web.scrape", `{"url":"${{ inputs.url }}"}`),
			step("outline", "slides.outline", `{"text":"${{ steps.scrape.output.markdown }}","model":"openrouter/auto"}`),
			step("render", "slides.render", `{"markdown":"${{ steps.outline.output.markdown }}","format":"pdf"}`),
		),
		pipe("builtin.research-blog", "Research → blog",
			"Deep-research a topic, then publish the synthesis directly as a blog post.",
			step("research", "research.deep", `{"query":"${{ inputs.query }}","model":"openrouter/auto"}`),
			step("publish", "blog.publish", `{"format":"markdown","title":"${{ inputs.title }}","body":"${{ steps.research.output.synthesis }}"}`),
		),
		pipe("builtin.repo-presentation", "Repo → presentation video",
			"Clone a repo, map its code structure (repo.map) and gather its docs, outline a deck from the README + docs + structure (slides.outline), then render a narrated video — a fuller picture than the README alone.",
			step("fetch", "repo.fetch", `{"url":"${{ inputs.repo_url }}"}`),
			// repo.map reuses repo.fetch's session (the runner auto-threads
			// _session_id) so it reads the same clone and returns a symbol map.
			step("map", "repo.map", `{"clone_path":"${{ steps.fetch.output.clone_path }}"}`),
			// Feed the LLM the README + the repo's own docs + its code
			// structure, so the deck reflects what the project is AND how it's
			// built — not a paraphrase of the front page. docs.content is "" when
			// the repo has none (repo.fetch always emits it), so this resolves.
			step("outline", "slides.outline", `{"text":"# README\n${{ steps.fetch.output.readme.content }}\n\n# Project docs\n${{ steps.fetch.output.docs.content }}\n\n# Code structure (symbol map)\n${{ steps.map.output.map }}","model":"openrouter/auto"}`),
			step("narrate", "slides.narrate", `{"markdown":"${{ steps.outline.output.markdown }}","allow_silent_output":true}`),
		),
		pipe("builtin.repo-readme-podcast", "Repo → podcast",
			"Clone a repo and generate a podcast about it from its README.",
			step("fetch", "repo.fetch", `{"url":"${{ inputs.repo_url }}"}`),
			step("podcast", "podcast.generate", `{"source_text":"${{ steps.fetch.output.readme.content }}","speakers":`+defaultSpeakers+`,"allow_silent_output":true}`),
		),
		pipe("builtin.html-video", "HTML composition → MP4",
			"Render an author-supplied HTML/CSS/JS composition to a deterministic MP4.",
			step("render", "hyperframes.render", `{"composition_html":"${{ inputs.composition_html }}","resolution":"1080p","aspect_ratio":"16:9"}`),
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
