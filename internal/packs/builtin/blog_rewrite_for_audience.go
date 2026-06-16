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
	"unicode"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	blogRewriteDefaultMaxTokens = 4096
	blogRewriteMinTokens        = 1024
	blogRewriteMaxTokens        = 16384
	blogRewriteDefaultPersona   = "general"

	// JIT length-sizing (issue #525 / #526). Calling agents declare an
	// intent and the pack picks a word target appropriate for the
	// source size. Static AGENTS.md word caps don't scale with source —
	// a 7000-word source compressed against a 1300-2000 word target
	// silently drops most of the content. The pack already sees the
	// source; it's the right component to size the output.
	blogRewriteIntentSummary    = "summary"
	blogRewriteIntentThorough   = "thorough"
	blogRewriteIntentExhaustive = "exhaustive"
	blogRewriteIntentDefault    = blogRewriteIntentThorough

	// Tokens-per-word budget for plumbing max_tokens. English averages
	// ~1.3 tokens/word; bump to 1.7 to leave headroom for the model's
	// formatting (markdown headings, lists) and the "Source:" line.
	blogRewriteTokensPerWord = 1.7
)

// blogRewriteIntentRow holds the sizing parameters for one intent. The
// ratio multiplies source word count to pick a target; floor and ceiling
// clamp at extremes so a 100-word source with intent=exhaustive still
// gets a usable target, and a 50k-word source with intent=summary
// doesn't blow up the model's max_tokens.
type blogRewriteIntentRow struct {
	ratio   float64
	floor   int
	ceiling int
}

// blogRewriteIntentTable is the initial sizing heuristic. Numbers are
// defaults to be revisited as empirical data lands per #526; live in
// named constants so the operator can read them, not load-bearing.
var blogRewriteIntentTable = map[string]blogRewriteIntentRow{
	blogRewriteIntentSummary:    {ratio: 0.10, floor: 300, ceiling: 1200},
	blogRewriteIntentThorough:   {ratio: 0.30, floor: 800, ceiling: 2500},
	blogRewriteIntentExhaustive: {ratio: 0.55, floor: 1500, ceiling: 6000},
}

const (
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

	// JIT length-sizing inputs (issue #526). All optional; back-compat
	// preserved when all four are zero/empty (handler falls back to the
	// default intent, which matches today's effective behavior).
	//
	// LengthIntent: declarative — "summary" / "thorough" / "exhaustive".
	// Pack measures source, picks a target from blogRewriteIntentTable.
	LengthIntent string `json:"length_intent"`
	// Inspect: pack measures source, returns the suggestion, and does
	// NOT call the model. Useful when an agent wants to negotiate.
	Inspect bool `json:"inspect"`
	// TargetWordsMin / TargetWordsMax: explicit numeric overrides. Both
	// must be > 0 to be honored; partial values fall through to intent.
	TargetWordsMin int `json:"target_words_min"`
	TargetWordsMax int `json:"target_words_max"`
}

// countWords returns a whitespace-delimited word count for s. Markdown
// fences/headings/lists count as words — fine for sizing because the
// model's output will contain the same kind of markup at similar rates.
func countWords(s string) int {
	return len(strings.Fields(s))
}

// blogRewriteSize captures the chosen target range for one call. Min
// and max bracket the desired output length; chosen is the midpoint
// the pack reports as target_words_chosen. applied names where the
// numbers came from ("intent:thorough" / "explicit" / "default").
type blogRewriteSize struct {
	min, max, chosen int
	applied          string
}

// sizeForIntent resolves a target word range from the intent table.
// Clamping at floor/ceiling keeps both extremes safe — a tiny source
// with intent=exhaustive still gets a usable target, and a huge source
// with intent=summary stays under the model's max_tokens.
func sizeForIntent(sourceWords int, intent string) blogRewriteSize {
	key := strings.ToLower(strings.TrimSpace(intent))
	if key == "" {
		key = blogRewriteIntentDefault
	}
	row, ok := blogRewriteIntentTable[key]
	if !ok {
		// Unknown intent string falls back to the default rather than
		// erroring — agents that misspell shouldn't fail the whole
		// rewrite; the chosen target is still sensible.
		row = blogRewriteIntentTable[blogRewriteIntentDefault]
		key = blogRewriteIntentDefault
	}
	chosen := int(float64(sourceWords) * row.ratio)
	if chosen < row.floor {
		chosen = row.floor
	}
	if chosen > row.ceiling {
		chosen = row.ceiling
	}
	// ±15% bracket around chosen, re-clamped to the row bounds. Gives
	// the model some leeway without letting it drift outside the
	// intent's range.
	minTarget := chosen * 85 / 100
	maxTarget := chosen * 115 / 100
	if minTarget < row.floor {
		minTarget = row.floor
	}
	if maxTarget > row.ceiling {
		maxTarget = row.ceiling
	}
	return blogRewriteSize{min: minTarget, max: maxTarget, chosen: chosen, applied: "intent:" + key}
}

// resolveBlogRewriteSize applies the input precedence: explicit numeric
// (both bounds set) > length_intent > default. Returns the chosen size
// + a label naming which path was taken so the output can echo it back.
func resolveBlogRewriteSize(sourceWords int, in *blogRewriteInput) blogRewriteSize {
	if in.TargetWordsMin > 0 && in.TargetWordsMax > 0 && in.TargetWordsMax >= in.TargetWordsMin {
		return blogRewriteSize{
			min:     in.TargetWordsMin,
			max:     in.TargetWordsMax,
			chosen:  (in.TargetWordsMin + in.TargetWordsMax) / 2,
			applied: "explicit",
		}
	}
	return sizeForIntent(sourceWords, in.LengthIntent)
}

// detectBlogRewriteTruncation decides whether the rewrite was cut short
// by max_tokens. The strong signal is finishReason=="length"; when the
// gateway provider doesn't expose finishReason (Ollama doesn't always),
// we fall back to a heuristic: output near the upper target bound AND
// ending without sentence-terminating punctuation. Imperfect, but
// better than silent truncation when the agent has no way to ask.
func detectBlogRewriteTruncation(finishReason, body string, outputWords, maxTarget int) bool {
	if strings.EqualFold(finishReason, "length") {
		return true
	}
	if maxTarget <= 0 {
		return false
	}
	if outputWords < (maxTarget*95)/100 {
		return false
	}
	// Look at the last ~30 runes for a sentence terminator. Code fences
	// and tables don't end with punctuation in normal prose, so the
	// heuristic is conservative; it's intentionally a hint, not a
	// guarantee. The strong signal is finishReason.
	trimmed := strings.TrimRightFunc(body, unicode.IsSpace)
	if trimmed == "" {
		return false
	}
	tail := trimmed
	if len(tail) > 30 {
		tail = tail[len(tail)-30:]
	}
	last := rune(tail[len(tail)-1])
	// Decode the last rune properly so trailing multi-byte characters
	// (em-dash, ellipsis) don't get misread as raw bytes.
	for _, r := range tail {
		last = r
	}
	switch last {
	case '.', '!', '?', ')', '"', '\'', '`':
		return false
	}
	return true
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
			IntentKeywords: []string{"rewrite for audience", "expand brief into blog", "translate source into blog post", "make this technical/marketing/executive", "summarize this", "thorough rewrite", "exhaustive rewrite"},
			TypicalUse:     "Generator pack at the heart of every *-rewrite-blog pipeline — turns a brief/scrape/parsed-doc into an original post for a stated audience. Use length_intent (summary / thorough / exhaustive) to scale the output to the source; the pack sizes the target word range from the source it actually sees.",
			Limitations:    []string{"does not fetch sources (call doc.parse / web.scrape / research.deep first)", "does not insert inline citations (chain content.ground rewrite:false after)", "does not publish to a CMS (chain blog.publish to save the artifact)", "truncated:true signals the model hit max_tokens — re-run with a smaller length_intent or larger max_tokens"},
		},
		InputSchema: packs.BasicSchema{
			// `model` is no longer Required: when omitted, the handler
			// resolves a default via defaultPackModel() — same
			// rationale as content.ground (see model_defaults.go).
			Required: []string{"source_content", "audience"},
			Properties: map[string]string{
				"source_content":    "string",
				"audience":          "string",
				"angle":             "string",
				"title":             "string",
				"model":             "string",
				"max_tokens":        "number",
				"persona":           "string",
				"length_intent":     "string",
				"inspect":           "boolean",
				"target_words_min":  "number",
				"target_words_max":  "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			// markdown + model are always present (markdown is empty
			// when inspect:true — the schema validator just wants the
			// field; callers branch on `inspect` in the output).
			Required: []string{"markdown", "model"},
			Properties: map[string]string{
				"markdown":              "string",
				"model":                 "string",
				"persona_used":          "string",
				"source_words":          "number",
				"target_words_chosen":   "number",
				"target_words_min":      "number",
				"target_words_max":      "number",
				"output_words":          "number",
				"compression_ratio":     "number",
				"length_intent_applied": "string",
				"truncated":             "boolean",
				// Inspect mode only — present when the call was an
				// `inspect:true` short-circuit (no model call).
				"inspect":              "boolean",
				"suggested_target":     "number",
				"suggested_target_min": "number",
				"suggested_target_max": "number",
				"reason":               "string",
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
		// Resolve a default when the caller omitted model. See
		// model_defaults.go for the precedence chain.
		in.Model = defaultPackModel(in.Model)

		sourceWords := countWords(in.SourceContent)
		size := resolveBlogRewriteSize(sourceWords, &in)

		// Inspect mode: short-circuit before any dispatcher use. Pack
		// returns the measurements + suggestion so the agent can
		// negotiate. Per issue #525, this is the cheap path; it MUST
		// NOT call the model. Validating the dispatcher first would
		// block gateway-less deployments from inspecting.
		if in.Inspect {
			ec.Report(100, fmt.Sprintf("inspect: source=%d words, suggested target=%d-%d", sourceWords, size.min, size.max))
			reason := fmt.Sprintf("source is %d words; applying %s for a target near %d words (range %d-%d)", sourceWords, size.applied, size.chosen, size.min, size.max)
			return json.Marshal(map[string]any{
				// markdown + model populated empty to satisfy the
				// OutputSchema's Required list — engine validators
				// reject missing required fields even when the
				// semantic mode (inspect) doesn't produce them.
				"markdown":              "",
				"model":                 in.Model,
				"inspect":               true,
				"source_words":          sourceWords,
				"suggested_target":      size.chosen,
				"suggested_target_min":  size.min,
				"suggested_target_max":  size.max,
				"length_intent_applied": size.applied,
				"reason":                reason,
			})
		}

		// Real generate path requires a dispatcher; inspect doesn't.
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "blog.rewrite_for_audience registered without a gateway dispatcher"}
		}

		// Plumb max_tokens. When the caller didn't set one, derive
		// from the chosen target's upper bound; honor caller's value
		// when it's at least the budget we'd derive (so an explicit
		// max_tokens never silently truncates a chosen target).
		derivedMax := int(float64(size.max) * blogRewriteTokensPerWord)
		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = derivedMax
			if maxTokens < blogRewriteDefaultMaxTokens {
				maxTokens = blogRewriteDefaultMaxTokens
			}
		} else if maxTokens < derivedMax {
			maxTokens = derivedMax
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
		// Append the chosen target range as a stronger, JIT-derived
		// length directive that overrides the persona block's own
		// range. Without this the persona's "800-1200 words" silently
		// out-votes a chosen "exhaustive" target of e.g. 3300-4400.
		system += fmt.Sprintf("\n\nTarget length for this post: %d-%d words (aim for the middle, ~%d). This target overrides any word-count range in the Style block above; treat the Style block as tone guidance only.", size.min, size.max, size.chosen)

		user := "Source:\n\n" + in.SourceContent

		ec.Report(10, fmt.Sprintf("rewriting for audience: %s (persona: %s, target: %d-%d words)", in.Audience, personaUsed, size.min, size.max))
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

		outputWords := countWords(body)
		truncated := detectBlogRewriteTruncation(chat.Choices[0].FinishReason, body, outputWords, size.max)
		compressionRatio := 0.0
		if sourceWords > 0 {
			compressionRatio = float64(outputWords) / float64(sourceWords)
		}

		ec.Report(100, fmt.Sprintf("rewrite complete: %d words (target %d-%d, truncated=%v)", outputWords, size.min, size.max, truncated))
		return json.Marshal(map[string]any{
			"markdown":              body,
			"model":                 in.Model,
			"persona_used":          personaUsed,
			"source_words":          sourceWords,
			"target_words_chosen":   size.chosen,
			"target_words_min":      size.min,
			"target_words_max":      size.max,
			"output_words":          outputWords,
			"compression_ratio":     compressionRatio,
			"length_intent_applied": size.applied,
			"truncated":             truncated,
		})
	}
}
