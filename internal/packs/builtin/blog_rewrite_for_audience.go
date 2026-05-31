// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// blog_rewrite_for_audience.go — turn a source document (markdown) into an
// original blog post for a stated audience, with a stated angle.
//
// Motivation: chains like doc.parse → content.ground → blog.publish produced
// a citation-strengthened transcription of the source — useful as research
// notes, but if you posted it as a blog you'd be republishing someone else's
// work with a different formatting. This pack is the missing transform: it
// asks the gateway LLM to translate the source into an original post that
// speaks to the audience, leads with why-it-matters, de-jargons the source's
// terms, connects them to the tools the audience uses, and adds the author's
// perspective. The source becomes a starting point, not the output.
//
// Hallucination resistance: the system prompt explicitly forbids claims not
// present in the source. Downstream pipelines typically run content.ground
// AFTER this pack with rewrite:false as a citation pass on the new prose, so
// any drift gets caught.

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
	blogRewriteDefaultMaxTokens = 4096
	blogRewriteMinTokens        = 1024
	blogRewriteMaxTokens        = 16384
	blogRewriteDefaultPersona   = "general"

	// blogRewriteSystemPrompt is templated with audience + angle + an
	// optional title block AND a persona-specific style directive. The
	// body codifies the strategic advice agents commonly give about
	// turning a transcription into a real post: lead with why-it-matters,
	// de-jargon, connect to the audience's tools, add perspective, stay
	// grounded in source. The persona block tunes tone/register/length —
	// without it the model defaulted to formal-academic for every input.
	blogRewriteSystemPrompt = `You are writing an ORIGINAL blog post, not a summary of the source.

Audience: %s
Angle: %s
%s
Style — %s

Hard rules — do not violate these:
- Lead with WHY THIS MATTERS to the audience above. Open with a concrete consequence or pain point they recognize, not with "this paper" or "this document".
- DE-JARGON the source's technical terms. Every term of art gets a relatable analogy or a code parallel the audience would recognize.
- CONNECT to tools / patterns the audience uses today. If the source describes a primitive, name the products or frameworks built on it.
- ADD PERSPECTIVE. Include an "Author's note" section near the end where you draw the connection the angle above calls for. Mark it explicitly.
- STAY GROUNDED. Do not state any technical fact that isn't in the source. If the source doesn't support a claim, leave it out. Speculation about future products is fine if you tag it ("Looking ahead: …").
- CITE the source up top with one line: "Source: <one-line attribution>". Keep the link out — a downstream step adds links.
- Voice: follow the Style block above for tone and register. Avoid filler ("It's important to note…", "In conclusion…"). Avoid bulleted lists for everything — mix prose paragraphs with bullets where they add scannability.
- Length: follow the Style block above; default 600-1000 words.

Output the blog body as markdown ONLY. No preamble like "Here is the post:". No code fences around the whole thing.`
)

// blogRewritePersonas maps a closed-set persona key to the style directive
// injected into the system prompt. The vocabulary matches slides.outline's
// so the picker is consistent across packs (general / technical / marketing /
// executive / educational + the blog-specific `academic`). An unknown
// non-empty key is treated as a freeform audience hint.
var blogRewritePersonas = map[string]string{
	"general":     "conversational, accessible. Use second person (\"you\"). Mix prose paragraphs with the occasional bullet list. 700-900 words. Close with one practical takeaway.",
	"technical":   "precise, hands-on. Assume the reader has the tool in front of them — use real field names, function signatures, config snippets, file paths from the source. Code blocks where they earn their keep. Embed a mermaid `flowchart` or `sequenceDiagram` block when the source describes a process or architecture. 800-1200 words. Close with concrete next steps (\"try this in your repo\" / \"check these env vars\").",
	"marketing":   "benefits-led, scannable, outcome-focused. Lead with what the reader gets, not how it works. Short paragraphs (2-3 sentences). Bold key claims sparingly. 500-800 words. Close with a clear call-to-action (try, sign up, learn more).",
	"executive":   "impact-led, brief, decision-oriented. Open with the bottom line in one sentence. Use numbers where the source supports them; promote a numeric comparison into a small markdown table when the source compares more than two values. Skip the implementation detail. 400-600 words. Close with the decision or ask.",
	"educational": "step-by-step, beginner-friendly. Start with \"What you'll learn\" (3 bullets). Build concepts in order with simple examples. Show a minimal code block before each concept's explanation when the source has runnable code. Embed a mermaid diagram where it helps build a mental model (sequence of steps, parts of a whole). Headings act as a learning path. 900-1400 words. Close with \"Practice\" + \"Further reading\".",
	"academic":    "formal register, hedged, citation-dense. Third person mostly. Acknowledge limitations and counter-examples from the source. Include a mermaid diagram or numbered figure when the source presents structured data or relationships. Longer paragraphs are fine. 1000-1500 words. Close with a paragraph on open questions or future work.",
}

// resolveBlogRewritePersona returns the style directive to inject and the
// canonical label echoed in persona_used. Empty → default persona; a known
// key (case-insensitive) → its directive; an unknown non-empty string → a
// freeform style hint, exactly the slides.outline pattern.
func resolveBlogRewritePersona(p string) (directive, used string) {
	key := strings.ToLower(strings.TrimSpace(p))
	if key == "" {
		key = blogRewriteDefaultPersona
	}
	if d, ok := blogRewritePersonas[key]; ok {
		return d, key
	}
	trimmed := strings.TrimSpace(p)
	return "tailor tone, register, and length for this audience: " + trimmed, trimmed
}

type blogRewriteInput struct {
	SourceContent string `json:"source_content"`
	Audience      string `json:"audience"`
	Angle         string `json:"angle"`
	Title         string `json:"title"`
	Model         string `json:"model"`
	MaxTokens     int    `json:"max_tokens"`
	// Persona tunes tone/register/length on top of audience+angle. Closed
	// set: general / technical / marketing / executive / educational /
	// academic; anything else is a freeform style hint. See
	// blogRewritePersonas.
	Persona string `json:"persona"`
}

// BlogRewriteForAudience constructs the pack. The dispatcher (vault-resolved
// at the engine layer) does the LLM call; this pack only owns the prompt
// shape and the input/output contract.
func BlogRewriteForAudience(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "blog.rewrite_for_audience",
		Version:     "v1",
		Description: "Translate a source document (markdown) into an original blog post for a stated audience and angle. NOT a summarizer — leads with why-it-matters, de-jargons, connects to the audience's tools, adds perspective. Stays grounded in source_content (no claims that aren't in the source). Pair with content.ground (rewrite:false) downstream for a citation pass.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"markdown", "source_content"},
			Produces:       []string{"blog_markdown"},
			IntentKeywords: []string{"rewrite for audience", "expand brief into blog", "translate source into blog post", "make this technical/marketing/executive"},
			TypicalUse:     "Generator pack at the heart of every *-rewrite-blog pipeline — turns a brief/scrape/parsed-doc into an original post for a stated audience.",
			Limitations:    []string{"does not fetch sources (call doc.parse / web.scrape / research.deep first)", "does not insert inline citations (chain content.ground rewrite:false after)", "does not publish to a CMS (chain blog.publish to save the artifact)"},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"source_content", "audience", "model"},
			Properties: map[string]string{
				"source_content": "string",
				"audience":       "string",
				"angle":          "string",
				"title":          "string",
				"model":          "string",
				"max_tokens":     "number",
				"persona":        "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"markdown", "model"},
			Properties: map[string]string{
				"markdown":     "string",
				"model":        "string",
				"persona_used": "string",
			},
		},
		Handler: blogRewriteHandler(d),
		// One gateway LLM call — same async path as slides.outline so the
		// JSON-RPC request stays short.
		Async: true,
	}
}

func blogRewriteHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "blog.rewrite_for_audience registered without a gateway dispatcher"}
		}
		var in blogRewriteInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.SourceContent) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "source_content is required (markdown of the source document)"}
		}
		if strings.TrimSpace(in.Audience) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "audience is required (e.g. 'developers building AI agents')"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model id; see helmdeck://models)"}
		}

		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = blogRewriteDefaultMaxTokens
		}
		if maxTokens < blogRewriteMinTokens {
			maxTokens = blogRewriteMinTokens
		}
		if maxTokens > blogRewriteMaxTokens {
			maxTokens = blogRewriteMaxTokens
		}

		// Build the system prompt. angle defaults to a neutral
		// "personal perspective" instruction when the caller doesn't
		// supply one — the AUTHOR'S-NOTE rule above requires something
		// to write about, so we never leave it empty.
		angle := strings.TrimSpace(in.Angle)
		if angle == "" {
			angle = "your honest personal perspective on what's interesting or surprising about this source"
		}
		titleBlock := ""
		if t := strings.TrimSpace(in.Title); t != "" {
			titleBlock = fmt.Sprintf("Title: %s\n", t)
		}
		personaDirective, personaUsed := resolveBlogRewritePersona(in.Persona)
		system := fmt.Sprintf(blogRewriteSystemPrompt, in.Audience, angle, titleBlock, personaDirective)
		user := "Source:\n\n" + in.SourceContent

		ec.Report(10, fmt.Sprintf("rewriting for audience: %s (persona: %s)", in.Audience, personaUsed))
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
			return nil, dispatchError("blog.rewrite_for_audience gateway", err)
		}
		if len(chat.Choices) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned no choices"}
		}
		body := unwrapCodeFence(strings.TrimSpace(chat.Choices[0].Message.Content.Text()))
		if body == "" {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned an empty rewrite"}
		}

		ec.Report(100, "rewrite complete")
		return json.Marshal(map[string]any{
			"markdown":     body,
			"model":        in.Model,
			"persona_used": personaUsed,
		})
	}
}
