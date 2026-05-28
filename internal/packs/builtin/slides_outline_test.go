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
