// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"testing"
)

func TestParseSlidesAndNotes_BasicDeck(t *testing.T) {
	md := `---
marp: true
theme: default
---

# Welcome

<!-- This is the opening slide. Welcome everyone. -->

First slide content.

---

# Topic One

<!-- Explain topic one in detail. -->
<!-- Also mention the caveats. -->

Bullet points here.

---

# Thank You

No speaker notes on this slide.
`
	slides := parseSlidesAndNotes(md)
	if len(slides) != 3 {
		t.Fatalf("slide count = %d, want 3", len(slides))
	}

	// Slide 0: has one comment
	if slides[0].Index != 0 {
		t.Errorf("slide 0 index = %d", slides[0].Index)
	}
	if slides[0].Notes != "This is the opening slide. Welcome everyone." {
		t.Errorf("slide 0 notes = %q", slides[0].Notes)
	}
	if slides[0].Content == "" {
		t.Error("slide 0 content should not be empty")
	}
	// Content should NOT contain the HTML comment
	if contains(slides[0].Content, "<!--") {
		t.Errorf("slide 0 content still contains comment: %q", slides[0].Content)
	}

	// Slide 1: has two comments — should be joined
	if slides[1].Notes != "Explain topic one in detail.\nAlso mention the caveats." {
		t.Errorf("slide 1 notes = %q", slides[1].Notes)
	}

	// Slide 2: no comments
	if slides[2].Notes != "" {
		t.Errorf("slide 2 notes = %q, want empty", slides[2].Notes)
	}
	if !contains(slides[2].Content, "Thank You") {
		t.Errorf("slide 2 content missing heading: %q", slides[2].Content)
	}
}

func TestParseSlidesAndNotes_NoFrontmatter(t *testing.T) {
	md := `# Only Slide

<!-- notes here -->

Content.
`
	slides := parseSlidesAndNotes(md)
	if len(slides) != 1 {
		t.Fatalf("slide count = %d, want 1", len(slides))
	}
	if slides[0].Notes != "notes here" {
		t.Errorf("notes = %q", slides[0].Notes)
	}
}

func TestParseSlidesAndNotes_EmptySlide(t *testing.T) {
	md := `---
marp: true
---

# Slide 1

---

---

# Slide 3
`
	slides := parseSlidesAndNotes(md)
	// Should have 3 slides: "# Slide 1", empty, "# Slide 3"
	if len(slides) != 3 {
		t.Fatalf("slide count = %d, want 3", len(slides))
	}
	if slides[1].Content != "" {
		t.Errorf("empty slide content = %q, want empty", slides[1].Content)
	}
	if slides[1].Notes != "" {
		t.Errorf("empty slide notes = %q, want empty", slides[1].Notes)
	}
}

func TestParseSlidesAndNotes_MultilineNotes(t *testing.T) {
	md := `---
marp: true
---

# Slide

<!--
This note spans
multiple lines.
It should be preserved.
-->

Content.
`
	slides := parseSlidesAndNotes(md)
	if len(slides) != 1 {
		t.Fatalf("slide count = %d, want 1", len(slides))
	}
	if !contains(slides[0].Notes, "multiple lines") {
		t.Errorf("multiline notes not preserved: %q", slides[0].Notes)
	}
}

func TestParseSlidesAndNotes_FrontmatterOnly(t *testing.T) {
	// Edge case: document is just frontmatter with no content
	md := `---
marp: true
---`
	slides := parseSlidesAndNotes(md)
	// Should produce one empty slide (or zero — either is acceptable
	// since there's no content). The important thing is no panic.
	for _, s := range slides {
		if s.Content != "" || s.Notes != "" {
			t.Errorf("unexpected content in frontmatter-only doc: %+v", s)
		}
	}
}

func TestParseSlidesAndNotes_NoDelimiters(t *testing.T) {
	// Single slide, no --- at all, no frontmatter
	md := `# Just One Slide

<!-- speaker note -->

Some text.`
	slides := parseSlidesAndNotes(md)
	if len(slides) != 1 {
		t.Fatalf("slide count = %d, want 1", len(slides))
	}
	if slides[0].Notes != "speaker note" {
		t.Errorf("notes = %q", slides[0].Notes)
	}
}

func TestStripFrontmatter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"standard frontmatter",
			"---\nmarp: true\ntheme: default\n---\n\n# Slide 1",
			"# Slide 1",
		},
		{
			"no frontmatter",
			"# Slide 1\n\nContent",
			"# Slide 1\n\nContent",
		},
		{
			"frontmatter only",
			"---\nmarp: true\n---",
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripFrontmatter(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractNotes(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantNotes string
		wantClean bool // true = clean should not contain <!--
	}{
		{
			"single comment",
			"# Title\n\n<!-- note -->\n\nBody",
			"note",
			true,
		},
		{
			"two comments",
			"<!-- first -->\n\n<!-- second -->",
			"first\nsecond",
			true,
		},
		{
			"no comments",
			"# Title\n\nBody",
			"",
			true,
		},
		{
			"multiline",
			"<!--\nline1\nline2\n-->",
			"line1\nline2",
			true,
		},
		// --- image_prompt filter (the narrator-says-image-prompt bug) ---
		{
			// The actual production shape: slides.outline puts speaker
			// notes AND the image_prompt comment side-by-side. ONLY the
			// speaker notes should reach the TTS engine.
			"speaker notes plus image_prompt — only notes spoken",
			"# Title\n\n<!-- speaker notes here -->\n<!-- image_prompt: A chart of revenue. -->\n\nBody",
			"speaker notes here",
			true,
		},
		{
			// A slide with ONLY an image_prompt and no speaker notes:
			// the narrator path treats it as no narration and falls
			// back to silence — that's the right outcome.
			"image_prompt only — empty notes",
			"<!-- image_prompt: A flowchart. -->",
			"",
			true,
		},
		{
			// Image prompt in the middle of multiple speaker notes is
			// filtered out; the other freeform notes survive in order.
			"image_prompt interleaved with speaker notes — image_prompt dropped",
			"<!-- intro line -->\n<!-- image_prompt: A bar chart. -->\n<!-- outro line -->",
			"intro line\noutro line",
			true,
		},
		{
			// Case-insensitivity matters because a model can produce
			// IMAGE_PROMPT or Image_Prompt by accident; we don't want
			// those to slip through to the TTS engine.
			"IMAGE_PROMPT uppercase — still filtered",
			"<!-- speaker line -->\n<!-- IMAGE_PROMPT: A diagram. -->",
			"speaker line",
			true,
		},
		{
			// Whitespace-tolerant: leading/trailing space inside the
			// comment body must not let the filter slip.
			"image_prompt with weird whitespace — filtered",
			"<!--   image_prompt:   spaced prompt   -->",
			"",
			true,
		},
		{
			// CRITICAL false-positive guard: a freeform note that
			// happens to MENTION the words image_prompt in its body
			// must still be spoken. HasPrefix on the TRIMMED inner
			// only matches when the prefix is at the very start.
			"freeform note containing image_prompt as substring — preserved",
			"<!-- The image_prompt feature is documented in the README. -->",
			"The image_prompt feature is documented in the README.",
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notes, clean := extractNotes(tc.in)
			notes = trimSpace(notes)
			if notes != tc.wantNotes {
				t.Errorf("notes = %q, want %q", notes, tc.wantNotes)
			}
			if tc.wantClean && contains(clean, "<!--") {
				t.Errorf("clean still has comment: %q", clean)
			}
		})
	}
}

// helpers
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
