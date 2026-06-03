// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// slides_notes.go — Marp speaker-notes parser for slides.narrate (T406).
//
// Marp uses HTML comments (<!-- ... -->) as speaker notes. This parser
// splits a Marp markdown deck on slide delimiters (---), extracts the
// speaker notes from each slide, and returns a per-slide struct with
// the clean content (comments stripped) and the raw notes text.
//
// Separated from slides_narrate.go so the parser can be unit-tested
// without any session or HTTP dependencies.

import (
	"regexp"
	"strings"
)

// slideContent holds one slide's parsed content and speaker notes.
type slideContent struct {
	Index   int    // 0-based slide index
	Content string // slide markdown with comments stripped
	Notes   string // extracted speaker notes (joined if multiple comments)
}

// notePattern matches HTML comments used as Marp speaker notes.
// Marp treats <!-- ... --> as notes; we extract the inner text.
// (?s) makes . match newlines so multi-line notes work.
var notePattern = regexp.MustCompile(`(?s)<!--\s*(.*?)\s*-->`)

// parseSlidesAndNotes splits a Marp markdown deck into per-slide
// content and speaker notes.
//
// Rules:
//   - The YAML frontmatter block (first --- ... --- section starting
//     at line 1) is stripped — it's Marp config, not a slide.
//   - Remaining content is split on \n---\n (the Marp slide delimiter).
//   - Within each slide, <!-- ... --> blocks are extracted as notes
//     and stripped from the content.
//   - Multiple comment blocks per slide are joined with newlines.
//   - Empty slides (blank between two ---) are included with empty
//     Content and Notes so the slide count stays correct for PNG
//     indexing.
func parseSlidesAndNotes(markdown string) []slideContent {
	body := stripFrontmatter(markdown)
	chunks := splitSlides(body)

	slides := make([]slideContent, 0, len(chunks))
	for i, chunk := range chunks {
		notes, clean := extractNotes(chunk)
		slides = append(slides, slideContent{
			Index:   i,
			Content: strings.TrimSpace(clean),
			Notes:   strings.TrimSpace(notes),
		})
	}
	return slides
}

// stripFrontmatter removes the YAML frontmatter block from the
// beginning of a Marp deck. Marp frontmatter starts with --- on
// line 1 and ends with the next ---. If the document doesn't start
// with ---, we return it unchanged.
func stripFrontmatter(md string) string {
	md = strings.TrimSpace(md)
	if !strings.HasPrefix(md, "---") {
		return md
	}
	// Find the closing --- of the frontmatter. Start searching
	// after the first line.
	rest := md[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		// No closing --- — the whole doc is frontmatter? Return
		// empty rather than treating the entire doc as one slide.
		return ""
	}
	// Skip past the closing --- and the newline after it.
	after := rest[idx+4:]
	return strings.TrimLeft(after, "\n\r")
}

// splitSlides splits the body (frontmatter already stripped) on the
// Marp slide delimiter: a line containing only ---. We accept the
// delimiter with optional surrounding whitespace on the line.
func splitSlides(body string) []string {
	// Split on lines that are just --- (possibly with leading/trailing
	// whitespace). This is the standard Marp delimiter.
	parts := regexp.MustCompile(`(?m)^\s*---\s*$`).Split(body, -1)

	// Filter out empty parts that result from leading/trailing delimiters.
	// But keep empty slides in the middle (they're intentional blank slides).
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return []string{body}
	}
	return parts
}

// extractNotes pulls all <!-- ... --> blocks from a slide chunk and
// returns (a) the joined SPOKEN-NOTES text — comments that are meant
// to be read aloud by slides.narrate's TTS — and (b) the chunk with
// every comment (notes + metadata) removed so the visible slide body
// stays clean. Structured-metadata comments emitted by slides.outline
// (image_prompt:, future per-slide annotation prefixes) are filtered
// out of the notes string so the narrator does not literally speak
// "image prompt colon: a chart of revenue by year." They are still
// stripped from the visible content via the catch-all ReplaceAllString
// below — metadata comments should never render on the slide either.
func extractNotes(chunk string) (notes, clean string) {
	matches := notePattern.FindAllStringSubmatch(chunk, -1)
	var notesBuf strings.Builder
	first := true
	for _, m := range matches {
		inner := m[1]
		if isStructuredMetadataComment(inner) {
			// Skip — this is a key:value-shaped comment that another
			// pack consumes structurally (e.g. slides.outline's
			// extractImagePrompts). Reading it aloud would be wrong.
			continue
		}
		if !first {
			notesBuf.WriteString("\n")
		}
		notesBuf.WriteString(inner)
		first = false
	}
	clean = notePattern.ReplaceAllString(chunk, "")
	return notesBuf.String(), clean
}

// isStructuredMetadataComment reports whether the inner text of an
// HTML comment matches the shape of a structured metadata directive
// emitted by slides.outline (or any future pack that piggy-backs on
// Marp's <!-- ... --> comment syntax for per-slide annotations).
//
// The current allowlist:
//   - image_prompt: — consumed by slides.outline.extractImagePrompts
//     to produce the typed image_prompts[] output array.
//
// Add new prefixes here as new structured-comment shapes ship.
// Intentionally an explicit allowlist instead of a generic
// "anything-with-a-colon" filter so legitimate freeform notes that
// happen to contain a colon ("Note: discuss further") still get
// spoken aloud.
func isStructuredMetadataComment(inner string) bool {
	t := strings.ToLower(strings.TrimSpace(inner))
	return strings.HasPrefix(t, "image_prompt:")
}
