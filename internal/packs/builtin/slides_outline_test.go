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
