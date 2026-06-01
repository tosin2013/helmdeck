// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package llmcontext

// reasoning.go — strip hybrid-reasoning-model output (ADR 051 PR #1).
//
// Models that do extended reasoning before producing a final answer
// (Claude 3.7 Sonnet thinking mode, OpenAI o3-mini, DeepSeek V4 Pro,
// the Moonshot Kimi K2 series) emit their chain-of-thought as
// <think>…</think> or <reasoning>…</reasoning> blocks BEFORE the
// structured payload. Some provider aggregators strip these on the
// way back; many don't — OpenRouter in particular passes the raw
// stream through. Our JSON parsers then hit the reasoning block first
// and fail.
//
// StripReasoningTokens drops those blocks so a downstream
// json.Decoder + substring-fallback path can succeed. Idempotent:
// clean input passes through unchanged, so the helper is safe to call
// unconditionally on every LLM response. Pure function, no
// dependencies beyond the standard library.
//
// Block patterns we recognize:
//
//	<think>…</think>           — most common; Claude / DeepSeek / Kimi
//	<reasoning>…</reasoning>   — OpenAI o-series / some Anthropic
//	[REASONING]…[/REASONING]   — square-bracket variant seen in
//	                              quantized open-weights inference
//
// Matching is CASE-INSENSITIVE, MULTI-LINE (the body can span newlines),
// and requires a closing tag. Unclosed open tags are left untouched —
// without the closing marker we can't tell where the reasoning ends,
// and dropping the tail would silently lose the actual answer.

import (
	"regexp"
	"strings"
)

// reasoningPatterns enumerates the tag pairs we recognize. Compiled
// once at package init for amortized matcher cost. The (?s) flag makes
// `.` match newlines (reasoning blocks are typically multi-line);
// (?i) is case-insensitive (Kimi emits lowercase, some o-series
// emit capitalized).
var reasoningPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think>`),
	regexp.MustCompile(`(?is)<reasoning\b[^>]*>.*?</reasoning>`),
	regexp.MustCompile(`(?is)\[REASONING\].*?\[/REASONING\]`),
}

// StripReasoningTokens removes recognized hybrid-reasoning blocks from
// s. The returned string is what was OUTSIDE every well-formed block.
// Whitespace surrounding stripped blocks is normalized so the result
// looks like the model never emitted them — concretely: each strip
// leaves at most a single newline between the surviving fragments,
// and leading/trailing whitespace on the final result is trimmed.
//
// Calling StripReasoningTokens on a string with no blocks returns the
// trimmed input verbatim — idempotent and cheap.
func StripReasoningTokens(s string) string {
	if s == "" {
		return s
	}
	cleaned := s
	for _, re := range reasoningPatterns {
		// ReplaceAllString returns the input unchanged when the
		// pattern doesn't match, so multi-pattern application is
		// cheap on clean input.
		cleaned = re.ReplaceAllString(cleaned, "")
	}
	// Normalize the gaps the strips left behind. Without this, an
	// input of "<think>...</think>\n\n{...}" would become "\n\n{...}"
	// — the leading whitespace is harmless to json.Decoder but
	// confusing to operators reading the trim-aware log lines.
	cleaned = strings.TrimSpace(cleaned)
	// Collapse runs of blank lines that the strips may have left
	// inside the surviving text — preserves intentional paragraph
	// breaks, avoids the "9 blank lines where the think block was"
	// artifact.
	cleaned = collapseBlankLines(cleaned)
	return cleaned
}

// collapseBlankLines reduces any run of 2+ blank lines to a single
// blank line. Operates on the string-builder level rather than via
// regex so the heuristic stays auditable (and we don't trip on the
// (?m) edge cases regex modes have around \r\n vs \n).
func collapseBlankLines(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevBlank := false
	for _, line := range strings.Split(s, "\n") {
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && prevBlank {
			// Skip — we already wrote one blank.
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		prevBlank = isBlank
	}
	return b.String()
}

// HasReasoningTokens reports whether s contains any recognized
// reasoning block. Useful for callers that want to log "we stripped
// N bytes" without re-running the strip pass.
func HasReasoningTokens(s string) bool {
	if s == "" {
		return false
	}
	for _, re := range reasoningPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
