// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// slides_split.go — deterministic post-pass that splits Marp slides
// whose code blocks or image+bullets combinations would overflow the
// rendered page. Runs between the LLM's outline output and the
// slides.outline output assembly, so slide_count and the image-prompt
// projection both reflect the final post-split deck.
//
// Why programmatic, not LLM:
//   The LLM owns slide-break decisions via the system prompt, but it
//   has no notion of visual height — it just produces N slides and
//   trusts each one fits. Marp then silently clips whatever overflows.
//   A deterministic line-count splitter solves the actual reader
//   complaint ("the bottom of my code is missing") without a second
//   model round-trip.
//
// Why these thresholds:
//   - codeLinesPerSlide=22 — Marp's default 14pt monospace fits ~22
//     lines on a 16:9 deck at the default theme. Conservative; tight
//     enough to be safe, loose enough that small functions land in
//     one slide.
//   - imageBulletsCap=8 — an image alongside more than 8 lines of
//     text squeezes both. Below 8, the existing slidesFitCSS scales
//     the image and the slide reads cleanly.
//   - blank-line preference within ±3 lines of the chunk boundary —
//     when a function break exists nearby, we prefer it over slicing
//     mid-statement. Falls back to hard line-count when no blank line
//     is in range.

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	// codeLinesPerSlide is the maximum number of source-code lines
	// allowed inside one slide's fenced code block before the slide
	// gets split. Tuned for Marp's default 14pt monospace on 16:9.
	codeLinesPerSlide = 22

	// imageBulletsCap is the maximum number of non-image content
	// lines (bullets, paragraphs) allowed alongside an image on a
	// single slide. Beyond this, image and text get their own slides.
	imageBulletsCap = 8

	// blankLineSearchRadius — when picking where to break a long
	// code block, prefer a blank line within ±N lines of the target
	// boundary so we don't split mid-function when a natural break
	// exists nearby.
	blankLineSearchRadius = 3
)

// codeFenceRE matches an opening or closing ``` fence line, with
// optional language tag on the opener (e.g. ```go, ```typescript).
// We use line-anchored matching so a fence in the middle of a
// paragraph (rare) doesn't trigger.
var codeFenceRE = regexp.MustCompile("(?m)^```(.*)$")

// imageLineRE matches a markdown image line: ![alt](src) standing on
// its own (possibly with surrounding whitespace). Inline images
// inside a sentence don't count toward "image-heavy".
var imageLineRE = regexp.MustCompile(`(?m)^\s*!\[[^\]]*\]\([^)]+\)\s*$`)

// splitOverflowSlides walks the Marp deck and splits any slide whose
// code block exceeds codeLinesPerSlide or whose image-plus-text
// combination would overflow. Frontmatter is preserved verbatim.
// Slide delimiters are normalized back to "\n---\n" to match the
// existing renderer convention.
//
// Idempotent: a deck that already fits its content per-slide passes
// through unchanged.
func splitOverflowSlides(markdown string) string {
	frontmatter, body := splitFrontmatter(markdown)
	chunks := splitSlides(body)
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		for _, part := range splitOneSlide(chunk) {
			// Canonicalize chunk whitespace so the splitter is
			// idempotent: TrimSpace + a fixed "\n\n---\n\n" separator
			// matches the title-slide guarantee's join convention so
			// downstream splitSlides sees the same boundaries on a
			// second pass.
			out = append(out, strings.TrimSpace(part))
		}
	}
	joined := strings.Join(out, "\n\n---\n\n")
	// Preserve the input's trailing-newline shape so an unchanged
	// pass-through doesn't insert a stray "\n" the caller didn't ask
	// for. Tests that string-compare exact decks rely on this.
	if strings.HasSuffix(markdown, "\n") {
		joined += "\n"
	}
	return frontmatter + joined
}

// splitFrontmatter returns the YAML frontmatter prefix (including its
// trailing newline so it concatenates cleanly with the body) and the
// remainder. When no frontmatter is present, prefix is "" and body is
// the input verbatim. Distinct from slides_notes.go's stripFrontmatter
// which TrimSpaces its input and drops the prefix — we need to round-
// trip the original prefix intact.
func splitFrontmatter(md string) (prefix, body string) {
	if !strings.HasPrefix(md, "---") {
		return "", md
	}
	// Find the closing "---" that ends the frontmatter block.
	rest := md[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		// Malformed (no close) — treat the whole thing as body so
		// content isn't lost.
		return "", md
	}
	end := 3 + idx + 4 // past the second "---"
	// Skip the immediate newline after the closing "---" so the body
	// starts at real content.
	for end < len(md) && (md[end] == '\n' || md[end] == '\r') {
		end++
	}
	return md[:end], md[end:]
}

// splitOneSlide returns one-or-more slides covering the original
// chunk's content. Continuation slides carry a "(cont. N/M)" suffix
// on the heading so the reader sees the break is intentional.
func splitOneSlide(chunk string) []string {
	// Code overflow check runs first because it's the most common
	// and most disruptive overflow case (clipped code = unreadable).
	parts := splitCodeOverflow(chunk)
	// Each part then runs through the image-bullets check so a slide
	// that wasn't code-overflowed but has image+bullets gets split too.
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, splitImageBullets(p)...)
	}
	return out
}

// splitCodeOverflow detects a fenced code block longer than
// codeLinesPerSlide and splits the slide into a sequence:
//
//	chunk1: heading + pre-code + first 22 lines (+ closing fence)
//	chunkN: heading "(cont. N/M)" + N-th 22 lines (with fence reopened)
//	final:  heading "(cont. M/M)" + last partial + post-code content
//
// Slides without an oversized code block pass through unchanged.
// Multiple oversized code blocks in one slide are handled left-to-
// right; each split inherits the heading from the original.
func splitCodeOverflow(chunk string) []string {
	heading, headingLine, _ := extractHeading(chunk)
	notes, clean := extractNotes(chunk)

	pre, fenceOpen, body, fenceClose, post, found := findCodeBlock(clean)
	if !found {
		return []string{chunk}
	}
	bodyLines := strings.Split(body, "\n")
	// Trim a trailing empty line that strings.Split tacks on when the
	// body ends with \n — it inflates the count by 1.
	if len(bodyLines) > 0 && bodyLines[len(bodyLines)-1] == "" {
		bodyLines = bodyLines[:len(bodyLines)-1]
	}
	if len(bodyLines) <= codeLinesPerSlide {
		return []string{chunk}
	}

	groups := chunkCodeLines(bodyLines, codeLinesPerSlide, blankLineSearchRadius)
	total := len(groups)

	out := make([]string, 0, total)
	for i, g := range groups {
		var b strings.Builder
		switch {
		case i == 0:
			// First chunk keeps the original pre-code content
			// verbatim (heading + any intro paragraph already lives
			// there), plus the original speaker notes and the first
			// code group. Do NOT also write headingLine — it's part
			// of pre and would duplicate.
			b.WriteString(strings.TrimRight(pre, "\n"))
			b.WriteString("\n\n")
			b.WriteString(fenceOpen)
			b.WriteString("\n")
			b.WriteString(strings.Join(g, "\n"))
			b.WriteString("\n")
			b.WriteString(fenceClose)
			b.WriteString("\n")
			if notes != "" {
				b.WriteString("\n<!-- ")
				b.WriteString(notes)
				b.WriteString(" -->\n")
			}
		default:
			// Continuation chunks reopen the fence with the same
			// language tag, hold only the code group, and carry a
			// minimal "Continuation of X (chunk N of M)" note.
			contHeading := appendContSuffix(headingLine, heading, i+1, total)
			if contHeading != "" {
				b.WriteString(contHeading)
				b.WriteString("\n\n")
			}
			b.WriteString(fenceOpen)
			b.WriteString("\n")
			b.WriteString(strings.Join(g, "\n"))
			b.WriteString("\n")
			b.WriteString(fenceClose)
			b.WriteString("\n")
			b.WriteString(continuationNote(heading, i+1, total))
		}
		// The LAST continuation chunk carries any post-code content
		// (paragraphs after the closing fence) so we don't lose it.
		if i == total-1 {
			postClean := strings.TrimSpace(post)
			if postClean != "" {
				b.WriteString("\n")
				b.WriteString(postClean)
				b.WriteString("\n")
			}
		}
		out = append(out, strings.TrimRight(b.String(), "\n"))
	}
	return out
}

// splitImageBullets splits a slide that contains a standalone image
// line AND more than imageBulletsCap lines of non-image, non-empty,
// non-comment text content. Result:
//
//	chunk1: heading + image
//	chunk2: heading "(cont. 2/2)" + the bullets/paragraphs
//
// When the slide has no image, or the text alongside is small, the
// chunk passes through unchanged.
func splitImageBullets(chunk string) []string {
	if !imageLineRE.MatchString(chunk) {
		return []string{chunk}
	}
	heading, headingLine, _ := extractHeading(chunk)
	notes, clean := extractNotes(chunk)

	// Pull all standalone-image lines into one block and gather the
	// rest. We preserve order: image first (matching where the LLM
	// usually places it), bullets after.
	var images []string
	var textLines []string
	for _, line := range strings.Split(clean, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
			textLines = append(textLines, line)
		case imageLineRE.MatchString(line):
			images = append(images, line)
		case strings.HasPrefix(trim, "#"):
			// heading already pulled into headingLine; skip here
			continue
		default:
			textLines = append(textLines, line)
		}
	}

	// Count meaningful (non-blank) text lines.
	meaningful := 0
	for _, l := range textLines {
		if strings.TrimSpace(l) != "" {
			meaningful++
		}
	}
	if meaningful <= imageBulletsCap {
		return []string{chunk}
	}

	first := strings.Builder{}
	if headingLine != "" {
		first.WriteString(headingLine)
		first.WriteString("\n\n")
	}
	first.WriteString(strings.Join(images, "\n"))
	first.WriteString("\n")
	if notes != "" {
		first.WriteString("\n<!-- ")
		first.WriteString(notes)
		first.WriteString(" -->\n")
	}

	second := strings.Builder{}
	contHeading := appendContSuffix(headingLine, heading, 2, 2)
	if contHeading != "" {
		second.WriteString(contHeading)
		second.WriteString("\n\n")
	}
	second.WriteString(strings.TrimSpace(strings.Join(textLines, "\n")))
	second.WriteString("\n")
	second.WriteString(continuationNote(heading, 2, 2))

	return []string{
		strings.TrimRight(first.String(), "\n"),
		strings.TrimRight(second.String(), "\n"),
	}
}

// chunkCodeLines slices the body into groups of at most maxLines.
// Where possible, prefer to break at a blank line within ±radius of
// the target boundary so we don't slice mid-statement when a natural
// paragraph break exists nearby.
func chunkCodeLines(lines []string, maxLines, radius int) [][]string {
	if len(lines) == 0 || maxLines <= 0 {
		return [][]string{lines}
	}
	groups := [][]string{}
	i := 0
	for i < len(lines) {
		end := i + maxLines
		if end >= len(lines) {
			groups = append(groups, lines[i:])
			break
		}
		// Search a window around `end` for a blank line. Prefer
		// later boundaries first (don't shrink groups unnecessarily).
		best := end
		for offset := 0; offset <= radius; offset++ {
			if end+offset < len(lines) && strings.TrimSpace(lines[end+offset]) == "" {
				best = end + offset
				break
			}
			if end-offset > i && strings.TrimSpace(lines[end-offset]) == "" {
				best = end - offset
				break
			}
		}
		groups = append(groups, lines[i:best])
		i = best
		// Skip a single blank line at the cut so the next chunk
		// doesn't start with a leading empty line.
		if i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
	}
	return groups
}

// findCodeBlock locates the FIRST fenced code block in body and
// returns its components. found=false when no opener+closer pair
// exists. Multiple code blocks: caller can re-invoke on `post` for
// the next one if needed.
func findCodeBlock(body string) (pre, fenceOpen, code, fenceClose, post string, found bool) {
	openLoc := codeFenceRE.FindStringSubmatchIndex(body)
	if openLoc == nil {
		return body, "", "", "", "", false
	}
	pre = body[:openLoc[0]]
	fenceOpen = strings.TrimRight(body[openLoc[0]:openLoc[1]], "\n")
	rest := body[openLoc[1]:]
	rest = strings.TrimLeft(rest, "\n")
	closeLoc := codeFenceRE.FindStringSubmatchIndex(rest)
	if closeLoc == nil {
		return body, "", "", "", "", false
	}
	code = strings.TrimRight(rest[:closeLoc[0]], "\n")
	fenceClose = strings.TrimRight(rest[closeLoc[0]:closeLoc[1]], "\n")
	post = rest[closeLoc[1]:]
	return pre, fenceOpen, code, fenceClose, post, true
}

// extractHeading returns the slide's first heading text (without the
// leading "# " markers), the raw heading line, and the remainder of
// the chunk after the heading. When the slide has no heading, all
// three are empty strings (and the chunk passes through).
func extractHeading(chunk string) (text, line, rest string) {
	for i, l := range strings.Split(chunk, "\n") {
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "#") {
			text = strings.TrimSpace(strings.TrimLeft(trim, "#"))
			line = l
			parts := strings.SplitN(chunk, "\n", i+2)
			if len(parts) >= i+2 {
				rest = parts[i+1]
			}
			return text, line, rest
		}
	}
	return "", "", chunk
}

// appendContSuffix returns the heading line with a "(cont. N/M)"
// suffix appended to its text. When the slide had no heading we emit
// a synthetic "## (cont. N/M)" so the continuation is still visually
// distinct.
func appendContSuffix(headingLine, headingText string, n, m int) string {
	suffix := fmt.Sprintf(" (cont. %d/%d)", n, m)
	if headingLine == "" {
		return "## " + strings.TrimSpace("(cont. "+fmt.Sprintf("%d/%d", n, m)+")")
	}
	return strings.TrimRight(headingLine, " ") + suffix
}

// continuationNote returns a minimal speaker-notes comment so
// slides.narrate produces a sensible "Continuation of X" line on
// the cont. slides instead of repeating the first chunk's notes.
func continuationNote(headingText string, n, m int) string {
	if headingText == "" {
		return fmt.Sprintf("<!-- Continuation (chunk %d of %d). -->\n", n, m)
	}
	return fmt.Sprintf("<!-- Continuation of %q (chunk %d of %d). -->\n", headingText, n, m)
}
