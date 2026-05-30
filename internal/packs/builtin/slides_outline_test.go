// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func runSlidesOutline(t *testing.T, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	pack := SlidesOutline(disp)
	ec := &packs.ExecutionContext{Pack: pack, Input: json.RawMessage(input)}
	return pack.Handler(context.Background(), ec)
}

func TestSlidesOutline_HappyPath_MultiSlide(t *testing.T) {
	deck := "# Project\n\n- what it is\n\n<!-- welcome to the project -->\n\n---\n\n## How it works\n\n- step one\n- step two\n\n<!-- here's how -->\n\n---\n\n## Summary\n\n- recap\n\n<!-- thanks -->"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"A long README describing a project in detail...","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Markdown   string `json:"markdown"`
		SlideCount int    `json:"slide_count"`
		Model      string `json:"model"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.SlideCount != 3 {
		t.Errorf("slide_count = %d, want 3", out.SlideCount)
	}
	if !strings.Contains(out.Markdown, "---") {
		t.Errorf("markdown should be a multi-slide deck with --- separators: %q", out.Markdown)
	}
	if out.Model != "openrouter/auto" {
		t.Errorf("model = %q", out.Model)
	}
	// Output-schema contract: the engine validates this on Execute.
	if verr := SlidesOutline(disp).OutputSchema.Validate(raw); verr != nil {
		t.Errorf("output violates declared OutputSchema: %v", verr)
	}
}

// TestSlidesOutline_ThinContent_InvalidInput is the determinism guarantee: a
// model that returns a single-slide "deck" (no `---`) — almost always because
// the input was too thin — must fail caller_fixable, NOT emit a 1-slide blob.
func TestSlidesOutline_ThinContent_InvalidInput(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Hello\n\nThat's all there is."}} // no --- → 1 slide
	_, err := runSlidesOutline(t, disp, `{"text":"hi","model":"openrouter/auto"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("want invalid_input (content too thin), got %v", err)
	}
	if !strings.Contains(pe.Message, "too thin") {
		t.Errorf("message should explain the thin-content failure, got: %s", pe.Message)
	}
}

// TestSlidesOutline_UnwrapsCodeFence — models often wrap the whole deck in a
// ```markdown fence; without unwrapping it the deck would parse as one slide.
func TestSlidesOutline_UnwrapsCodeFence(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"```markdown\n# A\n\n---\n\n## B\n```"}}
	raw, err := runSlidesOutline(t, disp, `{"text":"enough prose to make a couple slides","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Markdown   string `json:"markdown"`
		SlideCount int    `json:"slide_count"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.SlideCount != 2 {
		t.Errorf("slide_count = %d, want 2 (fence stripped)", out.SlideCount)
	}
	if strings.HasPrefix(out.Markdown, "```") {
		t.Errorf("code fence not stripped: %q", out.Markdown)
	}
}

// slidesOutlineOut is the decoded output (incl. the persona/title fields).
type slidesOutlineOut struct {
	Markdown      string `json:"markdown"`
	SlideCount    int    `json:"slide_count"`
	Model         string `json:"model"`
	HasTitleSlide bool   `json:"has_title_slide"`
	PersonaUsed   string `json:"persona_used"`
}

func decodeSlidesOutline(t *testing.T, raw json.RawMessage) slidesOutlineOut {
	t.Helper()
	var out slidesOutlineOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	return out
}

// TestSlidesOutline_PrependsTitleWhenModelOmits — when a title is given and the
// model's first slide isn't a matching title, the pack guarantees a title slide.
func TestSlidesOutline_PrependsTitleWhenModelOmits(t *testing.T) {
	deck := "## Intro\n\n- a point\n\n---\n\n## Summary\n\n- recap"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"plenty of prose for a deck","model":"openrouter/auto","title":"My Deck"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeSlidesOutline(t, raw)
	if !strings.HasPrefix(out.Markdown, "# My Deck") {
		t.Errorf("deck should start with the prepended title slide, got: %q", out.Markdown)
	}
	if out.SlideCount != 3 { // 2 model slides + 1 prepended title
		t.Errorf("slide_count = %d, want 3 (model 2 + prepended title)", out.SlideCount)
	}
	if !out.HasTitleSlide {
		t.Errorf("has_title_slide should be true")
	}
}

// TestSlidesOutline_NoDuplicateTitle — when the model already leads with a
// matching title slide, the pack must not prepend a second one.
func TestSlidesOutline_NoDuplicateTitle(t *testing.T) {
	deck := "# My Deck\n\n---\n\n## Body\n\n- point\n\n---\n\n## Summary\n\n- recap"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"plenty of prose","model":"openrouter/auto","title":"My Deck"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeSlidesOutline(t, raw)
	if n := strings.Count(out.Markdown, "# My Deck"); n != 1 {
		t.Errorf("title slide should appear exactly once, found %d: %q", n, out.Markdown)
	}
	if out.SlideCount != 3 {
		t.Errorf("slide_count = %d, want 3 (no prepend)", out.SlideCount)
	}
}

// TestSlidesOutline_AuthorByline — author lands on the prepended title slide.
func TestSlidesOutline_AuthorByline(t *testing.T) {
	deck := "## Intro\n\n- a point\n\n---\n\n## Summary\n\n- recap"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"plenty of prose","model":"openrouter/auto","title":"My Deck","author":"Jane Doe"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeSlidesOutline(t, raw)
	titleSlide, _, _ := strings.Cut(out.Markdown, "\n---")
	if !strings.Contains(titleSlide, "# My Deck") || !strings.Contains(titleSlide, "Jane Doe") {
		t.Errorf("title slide should carry the title + author byline, got: %q", titleSlide)
	}
	// The author hint is also passed to the model in the user message.
	if got := disp.captured[0].Messages[1].Content.Text(); !strings.Contains(got, "Author/byline: Jane Doe") {
		t.Errorf("user message should carry the author hint, got: %q", got)
	}
}

// TestSlidesOutline_PersonaInjected — a known persona injects its directive into
// the system prompt and is echoed in persona_used.
func TestSlidesOutline_PersonaInjected(t *testing.T) {
	deck := "# A\n\n---\n\n## Close\n\n- cta"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"plenty of prose","model":"openrouter/auto","persona":"marketing"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeSlidesOutline(t, raw)
	if out.PersonaUsed != "marketing" {
		t.Errorf("persona_used = %q, want marketing", out.PersonaUsed)
	}
	if sys := disp.captured[0].Messages[0].Content.Text(); !strings.Contains(sys, "call-to-action") {
		t.Errorf("system prompt should carry the marketing directive, got: %q", sys)
	}
}

// TestSlidesOutline_FreeformPersona — an unknown persona becomes a freeform hint.
func TestSlidesOutline_FreeformPersona(t *testing.T) {
	deck := "# A\n\n---\n\n## Close\n\n- recap"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"plenty of prose","model":"openrouter/auto","persona":"Series B investors"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeSlidesOutline(t, raw)
	if out.PersonaUsed != "Series B investors" {
		t.Errorf("persona_used = %q, want the freeform hint echoed", out.PersonaUsed)
	}
	if sys := disp.captured[0].Messages[0].Content.Text(); !strings.Contains(sys, "Series B investors") {
		t.Errorf("system prompt should carry the freeform audience hint, got: %q", sys)
	}
}

// TestSlidesOutline_DefaultPersona — absent persona defaults to general.
func TestSlidesOutline_DefaultPersona(t *testing.T) {
	deck := "# A\n\n---\n\n## Close\n\n- recap"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"plenty of prose","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out := decodeSlidesOutline(t, raw); out.PersonaUsed != "general" {
		t.Errorf("persona_used = %q, want general", out.PersonaUsed)
	}
}

// TestSlidesOutline_NoTitleInputNoPrepend — without a title input the pack does
// NOT invent a title slide (it relies on the prompt instead).
func TestSlidesOutline_NoTitleInputNoPrepend(t *testing.T) {
	deck := "## Intro\n\n- a point\n\n---\n\n## Summary\n\n- recap"
	disp := &scriptedDispatcherWT{replies: []string{deck}}
	raw, err := runSlidesOutline(t, disp, `{"text":"plenty of prose","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeSlidesOutline(t, raw)
	if out.Markdown != deck {
		t.Errorf("deck should be unchanged when no title input given, got: %q", out.Markdown)
	}
	if out.SlideCount != 2 {
		t.Errorf("slide_count = %d, want 2", out.SlideCount)
	}
}

func TestSlidesOutline_MissingFields(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"no text", `{"model":"openrouter/auto"}`},
		{"no model", `{"text":"some prose"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSlidesOutline(t, &scriptedDispatcherWT{}, tc.input)
			pe := &packs.PackError{}
			if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
				t.Errorf("want invalid_input, got %v", err)
			}
		})
	}
}

// scriptedSlidesReply is a 3-slide deck used by the persona-directive tests.
// Real enough to satisfy the >=2 floor; doesn't matter what the bullets say.
const scriptedSlidesReply = "# Title\n\n---\n\n## Body\n\n- a point\n\n---\n\n## Closing\n\n- recap"

// TestSlidesOutline_PersonaDirectiveInPrompt — each closed-set persona resolves
// to a distinct enriched directive that lands in the system prompt; the output
// echoes the canonical key as persona_used. Without this the directives
// regress to the terse general-only version that motivated the enrichment.
func TestSlidesOutline_PersonaDirectiveInPrompt(t *testing.T) {
	for _, tc := range []struct {
		persona  string // input
		used     string // expected persona_used
		mustHave string // distinctive phrase the enriched directive injects
	}{
		{"general", "general", "Mix prose paragraphs"},
		{"technical", "technical", "fenced code block"},
		{"marketing", "marketing", "Scannable bullets"},
		{"executive", "executive", "numbers wherever the source supports"},
		{"educational", "educational", "Try this"},
		{"academic", "academic", "open questions / future work"},
		{"TECHNICAL", "technical", "fenced code block"}, // case-insensitive
	} {
		t.Run(tc.persona, func(t *testing.T) {
			disp := &scriptedDispatcherWT{replies: []string{scriptedSlidesReply}}
			input := `{"text":"a long description of a system worth multiple slides","model":"openrouter/auto","persona":"` + tc.persona + `"}`
			raw, err := runSlidesOutline(t, disp, input)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			sys := disp.captured[0].Messages[0].Content.Text()
			if !strings.Contains(sys, tc.mustHave) {
				t.Errorf("system prompt should contain %q for persona %q, got:\n%s", tc.mustHave, tc.persona, sys)
			}
			var out struct {
				PersonaUsed string `json:"persona_used"`
			}
			_ = json.Unmarshal(raw, &out)
			if out.PersonaUsed != tc.used {
				t.Errorf("persona_used = %q, want %q", out.PersonaUsed, tc.used)
			}
		})
	}
}

// TestSlidesOutline_AudienceAngleInPrompt — when set, audience and angle
// land in the system prompt as a labeled block so the model knows who
// it's writing for and what angle to take. Neither is required; both
// degrade gracefully when omitted.
func TestSlidesOutline_AudienceAngleInPrompt(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{scriptedSlidesReply}}
	_, err := runSlidesOutline(t, disp, `{
		"text":"a multi-slide source description",
		"model":"openrouter/auto",
		"audience":"platform engineers at a Series B startup",
		"angle":"what to copy and what to skip"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	for _, must := range []string{"platform engineers at a Series B startup", "what to copy and what to skip"} {
		if !strings.Contains(sys, must) {
			t.Errorf("system prompt missing %q: %s", must, sys)
		}
	}
}

// TestSlidesOutline_ExportOutlineEmitsArtifact — when export_outline=true,
// the pack writes the final deck markdown as an artifact and surfaces
// outline_artifact_key on the output. Without the flag the artifact is
// not produced (default behavior preserved).
func TestSlidesOutline_ExportOutlineEmitsArtifact(t *testing.T) {
	mem := packs.NewMemoryArtifactStore()
	eng := packs.New(packs.WithArtifactStore(mem))
	pack := SlidesOutline(&scriptedDispatcherWT{replies: []string{scriptedSlidesReply}})

	// With the flag.
	res, err := eng.Execute(context.Background(), pack, json.RawMessage(`{
		"text":"long source","model":"openrouter/auto","export_outline":true
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Artifacts) == 0 {
		t.Fatalf("export_outline=true should emit an artifact")
	}
	if res.Artifacts[0].ContentType != "text/markdown" {
		t.Errorf("artifact content_type = %q, want text/markdown", res.Artifacts[0].ContentType)
	}
	if res.Artifacts[0].Size == 0 {
		t.Errorf("artifact size = 0; deck markdown should be non-empty")
	}
	var out struct {
		Key string `json:"outline_artifact_key"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Key == "" {
		t.Errorf("outline_artifact_key empty when artifact was produced")
	}

	// Without the flag — nothing emitted.
	pack = SlidesOutline(&scriptedDispatcherWT{replies: []string{scriptedSlidesReply}})
	res, err = eng.Execute(context.Background(), pack, json.RawMessage(`{
		"text":"long source","model":"openrouter/auto"
	}`))
	if err != nil {
		t.Fatalf("Execute (no flag): %v", err)
	}
	if len(res.Artifacts) != 0 {
		t.Errorf("default behavior should not emit an artifact, got %+v", res.Artifacts)
	}
}

// TestSlidesOutline_ImagePromptsRuleAndParsing — when
// include_image_prompts=true, the system prompt gains the image_prompt
// rule AND a handler-side post-pass extracts the comments into a
// structured array on the output. Covers both the prompt-injection side
// and the parsing side.
func TestSlidesOutline_ImagePromptsRuleAndParsing(t *testing.T) {
	// Scripted deck has two image_prompt comments — slide 2 and slide 3.
	reply := "# Title\n\n---\n\n## Body\n\n- a point\n\n<!-- image_prompt: A flowchart showing the request path -->\n\n---\n\n## Closing\n\n- recap\n\n<!-- image_prompt: Side-by-side comparison of before/after metrics -->"
	disp := &scriptedDispatcherWT{replies: []string{reply}}
	raw, err := runSlidesOutline(t, disp, `{
		"text":"long source","model":"openrouter/auto","include_image_prompts":true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	if !strings.Contains(sys, "<!-- image_prompt:") {
		t.Errorf("system prompt should contain the image_prompt rule, got:\n%s", sys)
	}
	var out struct {
		ImagePrompts []struct {
			SlideIndex int    `json:"slide_index"`
			Prompt     string `json:"prompt"`
		} `json:"image_prompts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ImagePrompts) != 2 {
		t.Fatalf("expected 2 image_prompts, got %d: %+v", len(out.ImagePrompts), out.ImagePrompts)
	}
	if out.ImagePrompts[0].SlideIndex != 2 || !strings.Contains(out.ImagePrompts[0].Prompt, "flowchart") {
		t.Errorf("first prompt wrong: %+v", out.ImagePrompts[0])
	}
	if out.ImagePrompts[1].SlideIndex != 3 || !strings.Contains(out.ImagePrompts[1].Prompt, "before/after") {
		t.Errorf("second prompt wrong: %+v", out.ImagePrompts[1])
	}
}

// TestSlidesOutline_ImagePromptsEmptyWhenAbsent — when
// include_image_prompts=true but the model didn't emit any image_prompt
// comments, the output array is present (empty) so downstream pipeline
// references don't fail with an unresolved field error.
func TestSlidesOutline_ImagePromptsEmptyWhenAbsent(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{scriptedSlidesReply}} // no image_prompt comments
	raw, err := runSlidesOutline(t, disp, `{
		"text":"x x x","model":"openrouter/auto","include_image_prompts":true
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	// Field must be present even when empty.
	var raw2 map[string]any
	_ = json.Unmarshal(raw, &raw2)
	if _, ok := raw2["image_prompts"]; !ok {
		t.Errorf("image_prompts field should always be present when include_image_prompts=true, even if empty")
	}
}

// TestSlidesOutline_FreeformPersonaPassThrough — an unknown persona key is
// passed through as a freeform style hint; persona_used echoes the trimmed
// original string. Matches blog.rewrite_for_audience's behavior.
func TestSlidesOutline_FreeformPersonaPassThrough(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{scriptedSlidesReply}}
	raw, err := runSlidesOutline(t, disp, `{
		"text":"x x x","model":"openrouter/auto","persona":"deadpan irreverent"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	if !strings.Contains(sys, "deadpan irreverent") {
		t.Errorf("freeform persona should be passed through to the prompt; missing in:\n%s", sys)
	}
	var out struct {
		PersonaUsed string `json:"persona_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.PersonaUsed != "deadpan irreverent" {
		t.Errorf("persona_used = %q, want freeform passthrough", out.PersonaUsed)
	}
}

// TestSlidesOutline_DefaultPersonaWhenOmitted — empty persona resolves to
// "general" so the pack always has a directive in the prompt.
func TestSlidesOutline_DefaultPersonaWhenOmitted(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{scriptedSlidesReply}}
	raw, err := runSlidesOutline(t, disp, `{"text":"x x x","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		PersonaUsed string `json:"persona_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.PersonaUsed != "general" {
		t.Errorf("default persona = %q, want general", out.PersonaUsed)
	}
}

// TestExtractImagePrompts_Direct — direct unit-level coverage of the parser
// against the helper, including the permissive-whitespace cases the regex
// is meant to handle.
func TestExtractImagePrompts_Direct(t *testing.T) {
	slides := []string{
		"## A\n\n- bullet\n",
		"## B\n\n- bullet\n\n<!--image_prompt:no spaces-->\n",
		"## C\n\n- bullet\n\n<!--   image_prompt:   leading/trailing spaces  -->\n",
		"## D\n\n- bullet\n\n<!-- image_prompt: -->\n", // empty body, must be skipped
	}
	got := extractImagePrompts(slides)
	if len(got) != 2 {
		t.Fatalf("expected 2 prompts, got %d: %+v", len(got), got)
	}
	if got[0].SlideIndex != 2 || got[0].Prompt != "no spaces" {
		t.Errorf("first wrong: %+v", got[0])
	}
	if got[1].SlideIndex != 3 || got[1].Prompt != "leading/trailing spaces" {
		t.Errorf("second wrong: %+v", got[1])
	}
}
