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
	"regexp"
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

	slidesOutlineDefaultPersona = "general"

	slidesOutlineSystemPrompt = `You are a presentation designer. Restate the user's content as a Marp slide deck.%s

Rules:
- Output ONLY Marp markdown. No preamble, no explanation, and do NOT wrap the whole deck in a code fence.
- Separate EVERY slide with a line containing only three dashes: ---
- Produce between 2 and %d slides. Cover the material faithfully; do not pad to hit a number and do not cram everything onto one slide.
- Each slide: a short "#" or "##" title, then a few concise bullet points (not paragraphs).
- REQUIRED: the FIRST slide is a TITLE slide — a single "#" deck title with NO bullets (add a one-line subtitle/byline only if an Author is given). The LAST slide is a CLOSING slide. These are not optional.
- If an Author/byline is provided, put it as a one-line subtitle on the title slide.
- Structure skeleton (--- are the slide separators):
    # Deck Title

    ---

    ## A section
    - a concise point

    ---

    ## Closing
    - the closing for this audience%s%s%s`

	slidesOutlineNarrationRule = "\n- After each slide's visible content, add an HTML comment with 1-3 sentences of spoken narration for that slide: <!-- narration here -->"

	// slidesOutlineImagePromptRule is appended when IncludeImagePrompts=true.
	// The model is told to embed one image_prompt comment per slide; a
	// handler-side parser then extracts them into the typed output array.
	slidesOutlineImagePromptRule = "\n- For each slide, also add a one-line HTML comment describing the ideal supporting visual: <!-- image_prompt: a one-sentence description of the chart/diagram/illustration/photo that would best support this slide --> (place it after the speaker-notes comment when narration is on, otherwise after the bullets)."
)

// slidesOutlinePersonas maps a known audience persona to a 2-3 line style
// directive injected into the system prompt — it shapes tone, slide content
// (code blocks, diagrams, scannability), and what the closing slide does. An
// unknown non-empty persona is treated as a freeform audience hint (see
// resolvePersonaDirective), never rejected. Vocabulary matches
// blog.rewrite_for_audience and content.ground so the picker is consistent
// across packs (the user has to learn one set of personas).
var slidesOutlinePersonas = map[string]string{
	"general":     "Audience: general. Use clear, jargon-light language. Mix prose paragraphs sparingly with bullets where they earn their keep. Closing slide: a concise recap of the key takeaways.",
	"technical":   "Audience: technical/engineering. Be precise; keep concrete details, APIs, and trade-offs. Include a fenced code block on any slide whose claim is best shown in code. Suggest a mermaid `flowchart` or `sequenceDiagram` block when a slide describes a process or architecture. Closing slide: a recap plus clear next steps / how to get started.",
	"marketing":   "Audience: marketing/prospects. Lead with benefits and outcomes, not internals. Scannable bullets; one outcome per bullet; avoid long paragraphs. Closing slide: a strong call-to-action (what to do next, where to learn more).",
	"executive":   "Audience: executives. Emphasize impact, cost, risk, and decisions; minimize detail. Use numbers wherever the source supports them; one chart-worthy claim per slide max. Closing slide: the decision/ask and expected outcome.",
	"educational": "Audience: learners. Build concepts step by step with simple examples. Include a 'Try this' slide near the end with a concrete exercise. Suggest a mermaid diagram on any slide that describes a sequence of steps. Closing slide: a recap plus suggested practice / further reading.",
	"academic":    "Audience: peer / conference / research summary. Hedged language ('appears to', 'suggests'). Preserve the source's limitations and counter-examples in view. Closing slide: a paragraph on open questions / future work.",
}

// resolvePersonaDirective returns the style directive to inject into the prompt
// and the canonical label to report in persona_used. Empty → the default
// persona; a known key → its directive; an unknown non-empty string → a freeform
// audience hint (so callers aren't limited to the fixed set).
func resolvePersonaDirective(p string) (directive, used string) {
	key := strings.ToLower(strings.TrimSpace(p))
	if key == "" {
		key = slidesOutlineDefaultPersona
	}
	if d, ok := slidesOutlinePersonas[key]; ok {
		return d, key
	}
	trimmed := strings.TrimSpace(p)
	return "Tailor tone, emphasis, and the closing slide for this audience: " + trimmed, trimmed
}

type slidesOutlineInput struct {
	Text      string `json:"text"`
	Model     string `json:"model"`
	MaxSlides int    `json:"max_slides"`
	Title     string `json:"title"`
	// Author, when set, becomes the title-slide byline.
	Author string `json:"author"`
	// Persona shapes tone + slide content + closing slide (general /
	// technical / marketing / executive / educational / academic, or any
	// freeform audience string). Default general. Same vocabulary as
	// blog.rewrite_for_audience.
	Persona string `json:"persona"`
	// Audience is a free-form audience description (e.g. "platform engineers
	// at a Series B startup") layered on top of Persona. Persona controls
	// register/structure; Audience is a who-they-are hint.
	Audience string `json:"audience"`
	// Angle is what the deck is about for this audience (e.g. "what to copy
	// vs what to skip"). Layered on top of Persona + Audience.
	Angle string `json:"angle"`
	// Narration is a *bool so absent means "default on" — emit `<!-- … -->`
	// speaker notes (needed by slides.narrate; harmless for slides.render).
	Narration *bool `json:"narration,omitempty"`
	MaxTokens int   `json:"max_tokens"`
	// ExportOutline, when true, persists the final deck markdown as a
	// downloadable artifact alongside whatever the downstream render/narrate
	// step produces. Surfaces outline_artifact_key on the output.
	ExportOutline bool `json:"export_outline"`
	// IncludeImagePrompts, when true, asks the model to embed a one-line
	// `<!-- image_prompt: … -->` comment in each slide's speaker notes
	// describing the ideal supporting visual. The handler then parses those
	// comments back out into a structured image_prompts: [{slide_index,
	// prompt}] array on the output — for downstream image-gen consumers.
	IncludeImagePrompts bool `json:"include_image_prompts"`
}

// SlidesOutline constructs the pack. It uses the same gateway dispatcher as
// research.deep / content.ground; register it in the dispatcher-gated block.
func SlidesOutline(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "slides.outline",
		Version:     "v1",
		Description: "Restate prose/markdown as a structured Marp slide deck (--- separated slides with titles, bullets, and speaker notes), ready for slides.render or slides.narrate. Guarantees a multi-slide deck or fails caller_fixable when the content is too thin.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"markdown", "text"},
			Produces:       []string{"marp_markdown", "outline_markdown"},
			IntentKeywords: []string{"outline as slides", "make this a deck", "structure as presentation", "persona-aware deck"},
			TypicalUse:     "Generator pack — turns prose into a structured Marp deck honoring persona/audience/angle. Chain slides.render after for PDF or slides.narrate after for narrated MP4.",
			Limitations:    []string{"does not render PDF (chain slides.render)", "does not produce video (chain slides.narrate)", "fails caller_fixable when source is too thin for ≥2 slides — give it a full README/synthesis, not a single sentence"},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"text", "model"},
			Properties: map[string]string{
				"text":                  "string",
				"model":                 "string",
				"max_slides":            "number",
				"title":                 "string",
				"author":                "string",
				"persona":               "string",
				"audience":              "string",
				"angle":                 "string",
				"narration":             "boolean",
				"max_tokens":            "number",
				"export_outline":        "boolean",
				"include_image_prompts": "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"markdown", "slide_count", "model"},
			Properties: map[string]string{
				"markdown":             "string",
				"slide_count":          "number",
				"model":                "string",
				"has_title_slide":      "boolean",
				"persona_used":         "string",
				"outline_artifact_key": "string",
				"image_prompts":        "array",
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
		imagePromptRule := ""
		if in.IncludeImagePrompts {
			imagePromptRule = slidesOutlineImagePromptRule
		}
		personaDirective, personaUsed := resolvePersonaDirective(in.Persona)
		// Optional audience+angle block — appended after "Marp slide deck."
		// when either is set. Persona shapes register; audience/angle layer
		// on top with who-they-are + what-this-is-about context.
		audienceBlock := ""
		if a := strings.TrimSpace(in.Audience); a != "" {
			audienceBlock = "\n\nAudience: " + a + "."
		}
		if g := strings.TrimSpace(in.Angle); g != "" {
			audienceBlock += "\nAngle: " + g + "."
		}
		system := fmt.Sprintf(slidesOutlineSystemPrompt,
			audienceBlock,
			maxSlides,
			narrationRule,
			imagePromptRule,
			"\n- "+personaDirective,
		)

		// Carry the title + author into the user message so the model places
		// them on the title slide it generates.
		var hdr []string
		if t := strings.TrimSpace(in.Title); t != "" {
			hdr = append(hdr, "Title: "+t)
		}
		if a := strings.TrimSpace(in.Author); a != "" {
			hdr = append(hdr, "Author/byline: "+a)
		}
		user := in.Text
		if len(hdr) > 0 {
			user = strings.Join(hdr, "\n") + "\n\n" + in.Text
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

		// Title-slide guarantee: when a title is provided and the model didn't
		// lead with a matching title slide, prepend one (with the author byline).
		// We don't invent a title when none was given — we rely on the prompt.
		// Done BEFORE splitSlides so the prepended slide counts toward
		// slide_count and the >=2 floor (a prepend can only raise the count).
		heading, leadsWithTitle := slidesOutlineFirstHeading(deck)
		hasTitleSlide := leadsWithTitle
		if t := strings.TrimSpace(in.Title); t != "" {
			matches := leadsWithTitle && strings.Contains(heading, strings.ToLower(t))
			if !matches {
				var b strings.Builder
				b.WriteString("# " + t + "\n")
				if a := strings.TrimSpace(in.Author); a != "" {
					b.WriteString("\n" + a + "\n")
				}
				b.WriteString("\n---\n\n")
				deck = b.String() + deck
			}
			hasTitleSlide = true
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
			"markdown":        deck,
			"slide_count":     len(slides),
			"model":           in.Model,
			"has_title_slide": hasTitleSlide,
			"persona_used":    personaUsed,
		}

		// Image-prompt post-pass: when include_image_prompts is on, walk
		// each slide's body, pull out every "<!-- image_prompt: … -->"
		// comment, and emit a typed image_prompts array so downstream
		// consumers don't have to parse markdown. Always include the field
		// when requested (empty array if the model didn't comply) so
		// downstream pipeline refs are well-defined.
		if in.IncludeImagePrompts {
			out["image_prompts"] = extractImagePrompts(slides)
		}

		// Outline artifact emission: when export_outline=true, persist the
		// final deck markdown as a downloadable artifact alongside whatever
		// the downstream render/narrate step produces. The artifact key
		// surfaces on the output so a pipeline step can reference it.
		if in.ExportOutline && ec.Artifacts != nil {
			art, err := ec.Artifacts.Put(ctx, "slides.outline", "outline.md", []byte(deck), "text/markdown")
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeInternal,
					Message: fmt.Sprintf("export_outline: artifact write failed: %v", err), Cause: err}
			}
			out["outline_artifact_key"] = art.Key
		}

		return json.Marshal(out)
	}
}

// imagePrompt is one parsed `<!-- image_prompt: … -->` comment together with
// the 1-indexed slide it came from (so 1 = title slide).
type imagePrompt struct {
	SlideIndex int    `json:"slide_index"`
	Prompt     string `json:"prompt"`
}

// imagePromptRE matches the image_prompt comment shape the system prompt
// asks for. Permissive on whitespace so a model that formats the comment
// slightly differently still gets parsed.
var imagePromptRE = regexp.MustCompile(`<!--\s*image_prompt\s*:\s*([\s\S]*?)\s*-->`)

// extractImagePrompts walks each slide body and pulls out every image_prompt
// comment, returning the structured array surfaced on the pack output.
// Always returns a non-nil slice (may be empty) so downstream pipeline
// templating sees a well-defined array.
func extractImagePrompts(slides []string) []imagePrompt {
	prompts := []imagePrompt{}
	for i, body := range slides {
		for _, m := range imagePromptRE.FindAllStringSubmatch(body, -1) {
			text := strings.TrimSpace(m[1])
			if text == "" {
				continue
			}
			prompts = append(prompts, imagePrompt{SlideIndex: i + 1, Prompt: text})
		}
	}
	return prompts
}

// slidesOutlineFirstHeading returns the lowercased heading text of the deck's
// FIRST slide when that slide leads with a Markdown heading (a title-slide
// shape), and whether it found one. Used to decide whether the deck already
// opens with a title slide — so the title-slide guarantee never duplicates one
// the model produced. The match is intentionally loose (substring); the worst
// case is a suppressed prepend or a benign extra title slide, never an invalid
// deck.
func slidesOutlineFirstHeading(deck string) (string, bool) {
	slides := splitSlides(stripFrontmatter(deck))
	if len(slides) == 0 {
		return "", false
	}
	for _, line := range strings.Split(slides[0], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			return "", false // first content isn't a heading → not a title slide
		}
		return strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "# "))), true
	}
	return "", false
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
