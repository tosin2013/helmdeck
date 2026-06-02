// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// blog_append_cta.go — append a natural-voice call-to-action section to a
// blog post markdown.
//
// Motivation: built-in blog pipelines (scrape-rewrite-blog,
// brief-rewrite-blog, doc-rewrite-blog) end with a polished article but
// no promotional links. A user intent like "promote this project" needs
// a closing section that names the project page / GitHub repo / source
// URL — and feels like a natural extension of the article's voice, not
// a boilerplate footer.
//
// Design:
//   - NO-OP when none of source_url / project_url / github_url are set.
//     The step can then slot into every blog pipeline unconditionally
//     and only fires when the caller wants promotion. This sidesteps the
//     lack of conditional pipeline steps without burning a model call
//     for the common no-CTA case.
//   - LLM-backed when at least one link is set. A deterministic appender
//     would always emit the same boilerplate footer; the LLM reads the
//     article, infers the voice from the persona block, and writes a
//     CTA that fits. It is instructed to return ONLY the new closing
//     section — the original article body is preserved verbatim and we
//     append the CTA in code, so the model cannot introduce drift.
//
// Voice matching: persona handling reuses resolveBlogRewritePersona from
// blog_rewrite_for_audience.go so the CTA voice is consistent with the
// pack that wrote the article (which the pipeline likely called just
// upstream).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	blogAppendCTADefaultMaxTokens = 600
	blogAppendCTAMinTokens        = 200
	blogAppendCTAMaxTokens        = 2000

	// blogAppendCTASystemPrompt is templated with audience + persona
	// directive + the formatted links block + an optional author copy
	// hint. The "Hard rules" deliberately constrain the model to emit
	// ONLY the closing section so the article body is preserved by the
	// deterministic appender below.
	blogAppendCTASystemPrompt = `You are writing a concluding call-to-action (CTA) section for an existing blog post. The article above is already finished — your job is to write the CLOSING section that promotes the supplied links.

Audience: %s
Style — %s

Links to promote (include all of them as inline markdown links in the new section):
%s
Optional copy hint from the author: %s

Hard rules — do not violate these:
- Output ONLY the new closing section as markdown. Do NOT repeat or rewrite the article above. Do NOT include the article's existing headings.
- Open the section with a markdown heading. Use ## Learn more, ## Try it yourself, ## Get involved, ## Where to next, or a similar natural variant — pick what matches the article's voice.
- 1-3 short paragraphs. Mention the linked resources by name and weave them into the prose. Do not list links as bare bullets.
- Match the article's tone and register exactly. If the article is third-person formal, stay third-person formal. If it's second-person conversational, stay there.
- Include the URLs as inline markdown links — for example "[the project page](url)" or "[on GitHub](url)". Every supplied URL must appear at least once.
- Do NOT add code fences around the section. Do NOT add a leading "---" separator. Do NOT include "In conclusion" / "To summarize" filler.`
)

type blogAppendCTAInput struct {
	Markdown   string `json:"markdown"`
	SourceURL  string `json:"source_url"`
	ProjectURL string `json:"project_url"`
	GitHubURL  string `json:"github_url"`
	// CTACopy is an optional plain-English hint from the author about
	// what the CTA should ask the reader to do ("encourage trying the
	// CLI", "invite contributors", "highlight the free tier"). The
	// model uses it as a steer; when empty it invents one from the
	// article's topic.
	CTACopy string `json:"cta_copy"`
	// Audience + Persona mirror blog.rewrite_for_audience so the CTA
	// voice can be locked to the article's voice when the pipeline
	// threads the same values through.
	Audience  string `json:"audience"`
	Persona   string `json:"persona"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

// BlogAppendCTA constructs the pack. The dispatcher (vault-resolved
// at the engine layer) does the LLM call when at least one link input
// is set; the no-link path bypasses the dispatcher entirely.
func BlogAppendCTA(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "blog.append_cta",
		Version:     "v1",
		Description: "Append a natural-voice call-to-action section to a blog post markdown, promoting the supplied links (project_url, github_url, source_url). NO-OP when ALL three link inputs are empty — the step can slot into every blog pipeline unconditionally and only fires when the caller wants promotion. LLM-backed so the CTA matches the article's voice rather than reading as a boilerplate footer.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"blog_markdown", "markdown"},
			Produces:       []string{"blog_markdown"},
			IntentKeywords: []string{"promote", "add a call to action", "add links to the project", "drive traffic to the repo", "include project link", "publish with promotion"},
			TypicalUse:     "Terminal post-processing step in blog pipelines when the caller wants the article to promote a project / repo / source URL. Slot it between content.ground and blog.publish; it no-ops when no link inputs are passed.",
			Limitations:    []string{"no-op when source_url/project_url/github_url are all empty", "does not modify the article body (only appends a closing CTA section)", "does not strip existing inline citations — for citation-stripping use a separate pass"},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"markdown"},
			Properties: map[string]string{
				"markdown":    "string",
				"source_url":  "string",
				"project_url": "string",
				"github_url":  "string",
				"cta_copy":    "string",
				"audience":    "string",
				"persona":     "string",
				"model":       "string",
				"max_tokens":  "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"markdown", "cta_added"},
			Properties: map[string]string{
				"markdown":     "string",
				"model_used":   "string",
				"cta_added":    "boolean",
				"persona_used": "string",
			},
		},
		Handler: blogAppendCTAHandler(d),
		Async:   true,
	}
}

func blogAppendCTAHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in blogAppendCTAInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Markdown) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "markdown is required"}
		}

		// No-op path: when no link inputs are set, return markdown
		// unchanged and DO NOT call the dispatcher. The pack can then
		// slot into every blog pipeline unconditionally without burning
		// a model call for the common no-CTA case.
		hasAnyLink := strings.TrimSpace(in.SourceURL) != "" ||
			strings.TrimSpace(in.ProjectURL) != "" ||
			strings.TrimSpace(in.GitHubURL) != ""
		if !hasAnyLink {
			ec.Report(100, "no CTA inputs supplied; passing markdown through unchanged")
			return json.Marshal(map[string]any{
				"markdown":  in.Markdown,
				"cta_added": false,
			})
		}

		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "blog.append_cta registered without a gateway dispatcher"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "model is required when one of source_url / project_url / github_url is set"}
		}

		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = blogAppendCTADefaultMaxTokens
		}
		if maxTokens < blogAppendCTAMinTokens {
			maxTokens = blogAppendCTAMinTokens
		}
		if maxTokens > blogAppendCTAMaxTokens {
			maxTokens = blogAppendCTAMaxTokens
		}

		var linksBlock strings.Builder
		if u := strings.TrimSpace(in.ProjectURL); u != "" {
			linksBlock.WriteString(fmt.Sprintf("- Project page: %s\n", u))
		}
		if u := strings.TrimSpace(in.GitHubURL); u != "" {
			linksBlock.WriteString(fmt.Sprintf("- GitHub repository: %s\n", u))
		}
		if u := strings.TrimSpace(in.SourceURL); u != "" {
			linksBlock.WriteString(fmt.Sprintf("- Original source: %s\n", u))
		}

		ctaCopy := strings.TrimSpace(in.CTACopy)
		if ctaCopy == "" {
			ctaCopy = "(none — invent a natural ask based on the article's topic)"
		}

		audience := strings.TrimSpace(in.Audience)
		if audience == "" {
			audience = "(infer from the article above)"
		}

		// Persona handling reuses the closed-set vocabulary from
		// blog.rewrite_for_audience so the CTA voice can be locked to
		// the article's voice when the pipeline threads the same value
		// through. Empty persona → "general" default. Unknown non-empty
		// keys become freeform style hints, same as blog.rewrite.
		personaDirective, personaUsed := resolveBlogRewritePersona(in.Persona)
		system := fmt.Sprintf(blogAppendCTASystemPrompt, audience, personaDirective, linksBlock.String(), ctaCopy)
		user := "Article:\n\n" + in.Markdown

		ec.Report(10, "calling model for CTA section")
		mt := maxTokens
		chat, err := d.Dispatch(ctx, gateway.ChatRequest{
			Model:     in.Model,
			MaxTokens: &mt,
			Messages: []gateway.Message{
				{Role: "system", Content: gateway.TextContent(system)},
				{Role: "user", Content: gateway.TextContent(user)},
			},
		})
		if err != nil {
			return nil, dispatchError("blog.append_cta gateway", err)
		}
		if len(chat.Choices) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned no choices"}
		}
		cta := unwrapCodeFence(strings.TrimSpace(chat.Choices[0].Message.Content.Text()))
		if cta == "" {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned an empty CTA section"}
		}

		// Append the CTA after the original article. The model was
		// instructed NOT to repeat the article, so the body is
		// preserved verbatim and the new section lands at the end.
		// Two newlines between body and CTA give the rendered output
		// a clean paragraph break regardless of how the article ended.
		combined := strings.TrimRight(in.Markdown, "\n") + "\n\n" + cta + "\n"

		ec.Report(100, "CTA appended")
		return json.Marshal(map[string]any{
			"markdown":     combined,
			"model_used":   in.Model,
			"cta_added":    true,
			"persona_used": personaUsed,
		})
	}
}
