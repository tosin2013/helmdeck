package builtin

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// makeCodeBlock returns a fenced code block with n synthetic lines so
// tests can dial overflow in by line count rather than crafting code
// by hand.
func makeCodeBlock(lang string, n int) string {
	var b strings.Builder
	b.WriteString("```")
	b.WriteString(lang)
	b.WriteString("\n")
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("// line %d\n", i))
	}
	b.WriteString("```")
	return b.String()
}

// TestSplit_60LineCode_ProducesThreeChunks proves the canonical case
// the user asked for: a code block too tall for one slide gets split
// into a sequence of "(cont. N/M)" continuation slides.
func TestSplit_60LineCode_ProducesThreeChunks(t *testing.T) {
	deck := "## Overview\n\nHere's the API surface:\n\n" + makeCodeBlock("go", 60) + "\n\nNotes follow.\n"
	out := splitOverflowSlides(deck)
	slides := splitSlides(stripFrontmatter(out))
	if len(slides) != 3 {
		t.Fatalf("want 3 slides for a 60-line code block (22/22/16), got %d:\n%s", len(slides), out)
	}
	// Continuation headings present in order.
	if !strings.Contains(slides[1], "(cont. 2/3)") {
		t.Errorf("second slide should carry (cont. 2/3); got:\n%s", slides[1])
	}
	if !strings.Contains(slides[2], "(cont. 3/3)") {
		t.Errorf("third slide should carry (cont. 3/3); got:\n%s", slides[2])
	}
	// Post-code content lands on the LAST continuation slide so we
	// don't lose it.
	if !strings.Contains(slides[2], "Notes follow") {
		t.Errorf("post-code content should land on the last cont. slide; got:\n%s", slides[2])
	}
	// Each continuation slide reopens the fence with the same lang
	// so renderers keep syntax highlighting.
	for i, s := range slides {
		opens := strings.Count(s, "```go")
		closes := regexp.MustCompile("(?m)^```\\s*$").FindAllString(s, -1)
		if opens != 1 {
			t.Errorf("slide %d should have exactly one ```go opener; got %d:\n%s", i, opens, s)
		}
		if len(closes) != 1 {
			t.Errorf("slide %d should have exactly one closing fence; got %d:\n%s", i, len(closes), s)
		}
	}
}

// TestSplit_ExactCap_NoSplit proves the splitter is conservative: a
// code block at exactly the cap stays as one slide. Avoids surprise
// splits on edge-of-budget content.
func TestSplit_ExactCap_NoSplit(t *testing.T) {
	deck := "## Cap\n\n" + makeCodeBlock("go", codeLinesPerSlide) + "\n"
	out := splitOverflowSlides(deck)
	slides := splitSlides(stripFrontmatter(out))
	if len(slides) != 1 {
		t.Fatalf("want 1 slide at exactly the cap (%d lines); got %d:\n%s", codeLinesPerSlide, len(slides), out)
	}
}

// TestSplit_PrefersBlankLineBoundary — when a function break (blank
// line) exists within ±3 lines of the chunk boundary, the splitter
// prefers it over slicing mid-statement. Catches regressions where
// the heuristic drifts.
func TestSplit_PrefersBlankLineBoundary(t *testing.T) {
	// 25 lines with a blank at line 23 (within radius of 22). Expect
	// chunk 1 to end at the blank line, chunk 2 to start clean.
	var lines []string
	for i := 1; i <= 25; i++ {
		if i == 23 {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, fmt.Sprintf("// line %d", i))
	}
	deck := "## Code\n\n```go\n" + strings.Join(lines, "\n") + "\n```\n"
	out := splitOverflowSlides(deck)
	slides := splitSlides(stripFrontmatter(out))
	if len(slides) != 2 {
		t.Fatalf("want 2 slides; got %d", len(slides))
	}
	// First chunk should include line 22 but stop at the blank line,
	// so it MUST NOT include "// line 24" (post-blank).
	if strings.Contains(slides[0], "// line 24") {
		t.Errorf("first chunk should stop at blank-line boundary; leaked line 24:\n%s", slides[0])
	}
	if !strings.Contains(slides[1], "// line 24") {
		t.Errorf("second chunk should pick up line 24 after the blank-line break; got:\n%s", slides[1])
	}
}

// TestSplit_ImageWithManyBullets — image alongside 12 bullets becomes
// two slides (image-only + bullets-only). Catches the second overflow
// case the user called out.
func TestSplit_ImageWithManyBullets(t *testing.T) {
	bullets := ""
	for i := 1; i <= 12; i++ {
		bullets += fmt.Sprintf("- bullet point %d\n", i)
	}
	deck := "## Why\n\n![arch diagram](https://example.com/img.png)\n\n" + bullets
	out := splitOverflowSlides(deck)
	slides := splitSlides(stripFrontmatter(out))
	if len(slides) != 2 {
		t.Fatalf("want 2 slides (image-only + bullets-only); got %d:\n%s", len(slides), out)
	}
	if !strings.Contains(slides[0], "![arch diagram]") {
		t.Errorf("first slide should hold the image; got:\n%s", slides[0])
	}
	if strings.Contains(slides[0], "bullet point 12") {
		t.Errorf("first slide should NOT hold bullets; got:\n%s", slides[0])
	}
	if !strings.Contains(slides[1], "bullet point 1") || !strings.Contains(slides[1], "bullet point 12") {
		t.Errorf("second slide should hold all 12 bullets; got:\n%s", slides[1])
	}
	if !strings.Contains(slides[1], "(cont. 2/2)") {
		t.Errorf("second slide should carry (cont. 2/2); got:\n%s", slides[1])
	}
}

// TestSplit_ImageWithFewBullets_NoSplit — image alongside small text
// stays on one slide. Tight slides are still valuable; we only split
// when the slide would actually overflow.
func TestSplit_ImageWithFewBullets_NoSplit(t *testing.T) {
	deck := "## Why\n\n![arch](https://example.com/img.png)\n\n- short caption\n- one more line\n"
	out := splitOverflowSlides(deck)
	slides := splitSlides(stripFrontmatter(out))
	if len(slides) != 1 {
		t.Fatalf("want 1 slide for image + tiny caption; got %d:\n%s", len(slides), out)
	}
}

// TestSplit_SpeakerNotesOnFirstChunk — when we split, the LLM-written
// notes describe the original concept; they should stay on the FIRST
// continuation chunk. Continuation chunks get a synthetic
// "Continuation of X" note so slides.narrate has something to read.
func TestSplit_SpeakerNotesOnFirstChunk(t *testing.T) {
	deck := "## Loop\n\n```go\n" + strings.Repeat("// fillers\n", 40) + "```\n\n<!-- The main event loop drives the server. -->\n"
	out := splitOverflowSlides(deck)
	slides := splitSlides(stripFrontmatter(out))
	if len(slides) < 2 {
		t.Fatalf("expected split; got %d slides", len(slides))
	}
	if !strings.Contains(slides[0], "The main event loop drives the server.") {
		t.Errorf("first slide should keep original speaker notes; got:\n%s", slides[0])
	}
	for i := 1; i < len(slides); i++ {
		if !strings.Contains(slides[i], "Continuation of") {
			t.Errorf("cont. slide %d should carry a synthetic Continuation-of note; got:\n%s", i, slides[i])
		}
		if strings.Contains(slides[i], "The main event loop drives") {
			t.Errorf("cont. slide %d should NOT duplicate the original note; got:\n%s", i, slides[i])
		}
	}
}

// TestSplit_NoCode_NoSplit — plain content slides (bullets, prose)
// pass through unchanged. Verifies the splitter is opt-in by
// content shape, not blanket.
func TestSplit_NoCode_NoSplit(t *testing.T) {
	deck := "## Intro\n\n- bullet 1\n- bullet 2\n- bullet 3\n\nMore prose follows.\n"
	out := splitOverflowSlides(deck)
	slides := splitSlides(stripFrontmatter(out))
	if len(slides) != 1 {
		t.Fatalf("plain content should not split; got %d slides:\n%s", len(slides), out)
	}
}

// TestSplit_PreservesFrontmatter — Marp YAML frontmatter is config,
// not a slide. Splitter must not touch it.
func TestSplit_PreservesFrontmatter(t *testing.T) {
	front := "---\nmarp: true\ntheme: helmdeck-tech\n---\n\n"
	body := "## Code\n\n" + makeCodeBlock("go", 40) + "\n"
	deck := front + body
	out := splitOverflowSlides(deck)
	if !strings.HasPrefix(out, front) {
		t.Errorf("frontmatter should be preserved verbatim at the top; got prefix:\n%s", out[:min(120, len(out))])
	}
}

// TestSplit_Idempotent — running the splitter twice yields the same
// deck. A deck that's already been split shouldn't expand further.
func TestSplit_Idempotent(t *testing.T) {
	deck := "## A\n\n" + makeCodeBlock("go", 50) + "\n"
	once := splitOverflowSlides(deck)
	twice := splitOverflowSlides(once)
	if once != twice {
		t.Errorf("splitOverflowSlides should be idempotent; differs on second pass:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// TestChunkCodeLines_BlankLinePreference exercises the helper in
// isolation so its boundary heuristic is covered explicitly.
func TestChunkCodeLines_BlankLinePreference(t *testing.T) {
	// 25 lines, blank at index 22 (the cap). Expect chunk 1 to end
	// at the blank, chunk 2 to start at index 23.
	lines := make([]string, 25)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	lines[22] = ""
	groups := chunkCodeLines(lines, 22, 3)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups; got %d", len(groups))
	}
	if len(groups[0]) != 22 {
		t.Errorf("first group should be 22 lines; got %d", len(groups[0]))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
