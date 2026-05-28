// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// slides_outline.go — turn prose/markdown into a STRUCTURED Marp slide deck.
//
// The deck/narrate pipelines used to feed raw prose (a README, a research
// synthesis, grounded text) straight into slides.render / slides.narrate. But
// those packs split slides only on `---` (see splitSlides), and prose has no
// `---` — so the whole document collapsed onto ONE slide and produced a
// degenerate ~7-second video that still reported "succeeded".
//
// slides.outline is the missing transform: it asks the gateway LLM to restate
// the content as a real Marp deck — `---`-separated slides with a title,
// concise bullets, and a `<!-- speaker note -->` per slide for narration —
// ready for slides.render or slides.narrate.
//
// Deterministic bounds (so the output is predictable, not open-ended):
//   - max_slides is clamped to a hard ceiling;
//   - the completion-token budget is clamped;
//   - the result is VALIDATED to be a real multi-slide deck. If the model
//     produced fewer than slidesOutlineMinSlides slides — almost always
//     because the input content was too thin to fill a deck — the pack returns
//     CodeInvalidInput (caller_fixable: "give me more material"), NOT a silent
//     one-slide deck. A pipeline then fails legibly instead of emitting a 7s blob.

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
	slidesOutlineDefaultMaxSlides = 18
	slidesOutlineMaxSlidesCap     = 30
	// slidesOutlineMinSlides is the floor that makes the output a *deck*. One
	// slide is the degenerate prose-as-deck case this pack exists to prevent.
	slidesOutlineMinSlides      = 2
	slidesOutlineMaxTokensFloor = 2048
	slidesOutlineMaxTokensCeil  = 8192

	slidesOutlineSystemPrompt = `You are a presentation designer. Restate the user's content as a Marp slide deck.

Rules:
- Output ONLY Marp markdown. No preamble, no explanation, and do NOT wrap the whole deck in a code fence.
- Separate EVERY slide with a line containing only three dashes: ---
- Produce between 2 and %d slides. Cover the material faithfully; do not pad to hit a number and do not cram everything onto one slide.
- Each slide: a short "#" or "##" title, then a few concise bullet points (not paragraphs).
- Start with a title slide and end with a short summary/closing slide.%s`

	slidesOutlineNarrationRule = "\n- After each slide's visible content, add an HTML comment with 1-3 sentences of spoken narration for that slide: <!-- narration here -->"
)

type slidesOutlineInput struct {
	Text      string `json:"text"`
	Model     string `json:"model"`
	MaxSlides int    `json:"max_slides"`
	Title     string `json:"title"`
	// Narration is a *bool so absent means "default on" — emit `<!-- … -->`
	// speaker notes (needed by slides.narrate; harmless for slides.render).
	Narration *bool `json:"narration,omitempty"`
	MaxTokens int   `json:"max_tokens"`
}

// SlidesOutline constructs the pack. It uses the same gateway dispatcher as
// research.deep / content.ground; register it in the dispatcher-gated block.
func SlidesOutline(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "slides.outline",
		Version:     "v1",
		Description: "Restate prose/markdown as a structured Marp slide deck (--- separated slides with titles, bullets, and speaker notes), ready for slides.render or slides.narrate. Guarantees a multi-slide deck or fails caller_fixable when the content is too thin.",
		InputSchema: packs.BasicSchema{
			Required: []string{"text", "model"},
			Properties: map[string]string{
				"text":       "string",
				"model":      "string",
				"max_slides": "number",
				"title":      "string",
				"narration":  "boolean",
				"max_tokens": "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"markdown", "slide_count", "model"},
			Properties: map[string]string{
				"markdown":    "string",
				"slide_count": "number",
				"model":       "string",
			},
		},
		Handler: slidesOutlineHandler(d),
		// One gateway LLM call; async keeps the JSON-RPC request short.
		Async: true,
	}
}

func slidesOutlineHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "slides.outline registered without a gateway dispatcher"}
		}
		var in slidesOutlineInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Text) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "text is required"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model id; see helmdeck://models)"}
		}

		maxSlides := in.MaxSlides
		if maxSlides <= 0 {
			maxSlides = slidesOutlineDefaultMaxSlides
		}
		if maxSlides > slidesOutlineMaxSlidesCap {
			maxSlides = slidesOutlineMaxSlidesCap
		}
		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			// ~300 tokens of deck per slide is a comfortable budget.
			maxTokens = maxSlides * 300
		}
		if maxTokens < slidesOutlineMaxTokensFloor {
			maxTokens = slidesOutlineMaxTokensFloor
		}
		if maxTokens > slidesOutlineMaxTokensCeil {
			maxTokens = slidesOutlineMaxTokensCeil
		}

		narrationRule := slidesOutlineNarrationRule
		if in.Narration != nil && !*in.Narration {
			narrationRule = ""
		}
		system := fmt.Sprintf(slidesOutlineSystemPrompt, maxSlides, narrationRule)

		user := in.Text
		if t := strings.TrimSpace(in.Title); t != "" {
			user = "Title: " + t + "\n\n" + in.Text
		}

		ec.Report(10, fmt.Sprintf("outlining into up to %d slides", maxSlides))
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
			return nil, dispatchError("slides.outline gateway", err)
		}
		if len(chat.Choices) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: "gateway returned no choices"}
		}
		deck := unwrapCodeFence(strings.TrimSpace(chat.Choices[0].Message.Content.Text()))
		if deck == "" {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: "gateway returned an empty deck"}
		}

		// Validate it's a real multi-slide deck using the SAME splitter
		// slides.render / slides.narrate use — so the count we guarantee here
		// is exactly what they'll see downstream.
		slides := splitSlides(stripFrontmatter(deck))
		if len(slides) < slidesOutlineMinSlides {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("content was too thin to structure into a deck — produced %d slide(s), need at least %d. Provide more material (a fuller README/synthesis), or lower max_slides.",
					len(slides), slidesOutlineMinSlides)}
		}

		out := map[string]any{
			"markdown":    deck,
			"slide_count": len(slides),
			"model":       in.Model,
		}
		return json.Marshal(out)
	}
}

// unwrapCodeFence strips a single ```…``` fence wrapping the entire string —
// models sometimes return the whole deck inside a ```markdown block, which
// would otherwise be parsed as one slide.
func unwrapCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (``` or ```markdown) and a trailing fence.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimRight(s, "\n")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
