// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// slides_contrast_lint.go (#202 Option B, pragmatic shape) —
// markdown-level static analysis of a Marp deck's frontmatter
// `style:` block + embedded <style> tags, looking for the two
// failure modes the issue's reproducer surfaced:
//
//   1. ANTI-PATTERN: `section { background: ... }` overridden
//      without also restyling the nested element types that inherit
//      against the new background. The reproducer was "dark blue
//      section bg + default light tables glaring through."
//
//   2. DIRECT WCAG VIOLATION: a single CSS rule sets both
//      `color` and `background-color` (or `background`) and the
//      computed contrast ratio is below 4.5:1 (WCAG AA body text).
//
// Both checks run against the markdown source, not the rendered
// HTML — we don't need a computed-style engine for them. That keeps
// the lint pure Go, fast, and free of jsdom/headless-Chrome
// dependencies. It will miss subtle inheritance bugs that only
// surface after full CSS cascade resolution; those can be added in
// a follow-up if operators report them.
//
// The lint emits warnings (informational), never hard errors —
// slides.render still succeeds and uploads the artifact. The
// warnings live in the pack's response so the agent can see them
// and decide whether to re-render with a corrected palette.

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// ContrastWarning is one item in the slides.render response's
// `warnings` array. The shape is stable enough for agents to switch
// on `rule` — see contrast_lint_*_test.go for the rule vocabulary.
type ContrastWarning struct {
	Rule           string `json:"rule"`
	Selector       string `json:"selector,omitempty"`
	ForegroundHex  string `json:"foreground,omitempty"`
	BackgroundHex  string `json:"background,omitempty"`
	Ratio          string `json:"ratio,omitempty"`
	Recommendation string `json:"recommendation"`
}

// LintContrast inspects the markdown's frontmatter `style:` block
// and any embedded <style> tags, then returns a list of warnings.
// An empty/nil result means no contrast risks were detected by the
// static analysis. Curated-theme decks (helmdeck-dark / helmdeck-
// corporate) typically produce zero warnings because the themes
// declare every element's colors explicitly.
//
// LintContrast is deliberately pure (no I/O, no context) so it can
// be called from the pack handler, exercised directly in tests, and
// reused by future Management-UI surfaces.
func LintContrast(markdown string) []ContrastWarning {
	css := extractCSS(markdown)
	if css == "" {
		return nil
	}
	rules := parseCSSRules(css)

	var warnings []ContrastWarning

	// Anti-pattern check: did anything override `section` background?
	// If so, look for explicit restyles of the inheritance-risk
	// element types. Missing overrides are the #202 reproducer.
	sectionBg := findSectionBackgroundOverride(rules)
	if sectionBg != "" {
		nestedRisks := []string{"table", "td", "th", "code", "blockquote", "pre"}
		missing := []string{}
		for _, sel := range nestedRisks {
			if !hasRuleForSelector(rules, sel) {
				missing = append(missing, sel)
			}
		}
		if len(missing) > 0 {
			warnings = append(warnings, ContrastWarning{
				Rule:     "section-background-without-nested-overrides",
				Selector: "section",
				Recommendation: fmt.Sprintf(
					"section background was customized (%s) but the following nested element types were not restyled: %s. "+
						"They inherit Marp default colors that may not be legible against the new background. "+
						"Either add CSS rules for each (e.g. `table, th, td { color: ...; background-color: ...; }`) "+
						"or switch to the `helmdeck-dark` / `helmdeck-corporate` curated themes which override every nested element by default.",
					sectionBg, strings.Join(missing, ", ")),
			})
		}
	}

	// Direct WCAG violation: any single rule that sets BOTH color
	// and background-color (or background) gets its pair contrast-
	// checked. Below 4.5:1 → warn.
	for _, r := range rules {
		fg := r.color
		bg := r.background
		if fg == "" || bg == "" {
			continue
		}
		fgRGB, ok1 := parseHexColor(fg)
		bgRGB, ok2 := parseHexColor(bg)
		if !ok1 || !ok2 {
			// Non-hex values (named colors, rgb(...), gradients) skip
			// this check intentionally — false negatives are
			// preferable to false alarms for the agent. Curated
			// themes use hex; agent-authored CSS usually does too.
			continue
		}
		ratio := wcagContrastRatio(fgRGB, bgRGB)
		if ratio < 4.5 {
			warnings = append(warnings, ContrastWarning{
				Rule:          "wcag-aa-text-contrast",
				Selector:      r.selector,
				ForegroundHex: fg,
				BackgroundHex: bg,
				Ratio:         fmt.Sprintf("%.2f:1", ratio),
				Recommendation: fmt.Sprintf(
					"`%s` sets color=%s on background=%s which gives a WCAG ratio of %.2f:1, below the AA minimum of 4.5:1 for body text. "+
						"Darken the background or lighten the foreground until the ratio is ≥ 4.5:1.",
					r.selector, fg, bg, ratio),
			})
		}
	}

	return warnings
}

// --- CSS extraction ---------------------------------------------------

// extractCSS pulls together the markdown's CSS surface:
//  1. the frontmatter `style:` block (Marp's preferred location)
//  2. every <style>...</style> tag in the body
//
// Returns the concatenated CSS or empty string when neither is
// present. The two sources are joined with a newline so rule
// parsing sees them as a single stream.
func extractCSS(markdown string) string {
	var b strings.Builder

	// 1. Frontmatter style block.
	if end := frontmatterEndIndex(markdown); end > 0 {
		fmRaw := markdown[:end]
		// The frontmatter body sits between the two `---` markers.
		// Trim them and parse as YAML to pull out `style`.
		fmInner := strings.TrimPrefix(fmRaw, "---\n")
		fmInner = strings.TrimPrefix(fmInner, "---\r\n")
		fmInner = strings.TrimSuffix(fmInner, "\n---\n")
		fmInner = strings.TrimSuffix(fmInner, "\r\n---\r\n")
		fmInner = strings.TrimSuffix(fmInner, "\n---")
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(fmInner), &parsed); err == nil {
			if s, ok := parsed["style"].(string); ok && s != "" {
				b.WriteString(s)
				b.WriteString("\n")
			}
		}
	}

	// 2. Embedded <style>...</style> blocks. Scoped or not — both
	// land in the CSS pool for lint purposes. Matching is permissive:
	// <style>, <style scoped>, <style type="text/css"> all work.
	for _, m := range styleTagRe.FindAllStringSubmatch(markdown, -1) {
		if len(m) >= 2 {
			b.WriteString(m[1])
			b.WriteString("\n")
		}
	}

	return b.String()
}

// styleTagRe captures the body of <style>...</style> blocks,
// including attribute variants like <style scoped> and
// <style type="text/css">.
var styleTagRe = regexp.MustCompile(`(?is)<style[^>]*>(.*?)</style>`)

// --- CSS rule parsing -------------------------------------------------

// cssRule is a minimal selector+declarations bundle for the lint
// rules. We don't need a full CSS AST — just selectors and the
// handful of color-related declarations.
type cssRule struct {
	selector   string
	color      string
	background string
}

// ruleRe captures `selector { declarations }` blocks. The body is
// captured non-greedily so adjacent rules don't merge.
var ruleRe = regexp.MustCompile(`(?s)([^{}\n][^{}]*?)\{([^{}]*)\}`)

// parseCSSRules pulls the selector + color/background declarations
// out of a CSS string. Comments are stripped first so they don't
// confuse the selector regex.
func parseCSSRules(css string) []cssRule {
	css = stripCSSComments(css)
	var out []cssRule
	for _, m := range ruleRe.FindAllStringSubmatch(css, -1) {
		if len(m) < 3 {
			continue
		}
		selectors := strings.TrimSpace(m[1])
		body := m[2]
		decls := parseDeclarations(body)

		// Split comma-separated selectors so `table, th, td { … }`
		// produces three rule entries — the lint walks per-selector.
		for _, sel := range strings.Split(selectors, ",") {
			sel = strings.TrimSpace(sel)
			if sel == "" {
				continue
			}
			out = append(out, cssRule{
				selector:   sel,
				color:      decls["color"],
				background: decls["background-color"],
			})
			// Fallback: `background: <color>` shorthand. We accept it
			// only when there's no `background-color` already.
			if decls["background-color"] == "" && decls["background"] != "" {
				out[len(out)-1].background = decls["background"]
			}
		}
	}
	return out
}

// stripCSSComments removes /* ... */ comments. Important: the
// lint's contrast-pair check would false-positive if a commented-
// out hex value sneaked into the rule body.
var cssCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)

func stripCSSComments(css string) string {
	return cssCommentRe.ReplaceAllString(css, "")
}

// parseDeclarations turns a CSS rule body ("color: #fff; background: #000")
// into a map of property → value. Whitespace and trailing semicolons
// are tolerated. Multi-word values (e.g. `background: linear-gradient(...)`)
// are preserved verbatim — the WCAG check just skips non-hex values
// later.
func parseDeclarations(body string) map[string]string {
	out := map[string]string{}
	for _, decl := range strings.Split(body, ";") {
		decl = strings.TrimSpace(decl)
		if decl == "" {
			continue
		}
		colon := strings.Index(decl, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(decl[:colon]))
		val := strings.TrimSpace(decl[colon+1:])
		out[key] = val
	}
	return out
}

// findSectionBackgroundOverride returns the background value of the
// first rule whose selector starts with `section` and that sets
// `background-color` or `background`. Empty string when no such rule
// exists. Used by the anti-pattern check.
func findSectionBackgroundOverride(rules []cssRule) string {
	for _, r := range rules {
		s := strings.TrimSpace(r.selector)
		// Match `section`, `section.foo`, `section:nth-child(...)`, etc.
		// but NOT selectors like `body > section table` where section
		// isn't the targeted element.
		if s == "section" || strings.HasPrefix(s, "section.") ||
			strings.HasPrefix(s, "section:") || strings.HasPrefix(s, "section[") {
			if r.background != "" {
				return r.background
			}
		}
	}
	return ""
}

// hasRuleForSelector returns true if any rule's selector mentions
// `name` as a standalone element (not a substring of another word).
// Used by the anti-pattern check to determine whether the deck has
// already overridden the at-risk nested element types.
func hasRuleForSelector(rules []cssRule, name string) bool {
	for _, r := range rules {
		// Split on common compound-selector punctuation so we can
		// recognize e.g. `table, th, td` and `table td` both as
		// covering `table`.
		fields := strings.FieldsFunc(r.selector, func(r rune) bool {
			return r == ' ' || r == '>' || r == '+' || r == '~' || r == ','
		})
		for _, f := range fields {
			// Trim attribute brackets, class/id suffixes, pseudo-class
			// suffixes — we only care that the element name matches.
			for _, c := range []string{".", "#", "[", ":"} {
				if i := strings.Index(f, c); i >= 0 {
					f = f[:i]
				}
			}
			if f == name {
				return true
			}
		}
	}
	return false
}

// --- WCAG contrast math -----------------------------------------------

type rgb struct {
	R, G, B uint8
}

// parseHexColor accepts `#RGB`, `#RRGGBB`, with leading whitespace
// or trailing space tolerated. Returns (rgb, true) on success;
// (zero, false) for any non-hex shape (rgb(...), named, gradients).
//
// This is the deliberate cutoff for the lint: anything the parser
// can't reduce to three byte channels is treated as "out of scope
// for static contrast" and the rule is skipped. False negatives are
// preferable to false alarms when the agent is trying to learn the
// pattern.
func parseHexColor(s string) (rgb, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return rgb{}, false
	}
	hex := s[1:]
	switch len(hex) {
	case 3:
		// Expand #RGB to #RRGGBB by doubling each nibble.
		r, g, b := hex[0], hex[1], hex[2]
		hex = string([]byte{r, r, g, g, b, b})
	case 6:
		// already normalized
	default:
		return rgb{}, false
	}
	var c rgb
	for i, ch := range []byte(hex) {
		v := hexDigit(ch)
		if v < 0 {
			return rgb{}, false
		}
		switch i {
		case 0:
			c.R = uint8(v) << 4
		case 1:
			c.R |= uint8(v)
		case 2:
			c.G = uint8(v) << 4
		case 3:
			c.G |= uint8(v)
		case 4:
			c.B = uint8(v) << 4
		case 5:
			c.B |= uint8(v)
		}
	}
	return c, true
}

func hexDigit(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

// relativeLuminance implements the sRGB relative-luminance formula
// from WCAG 2.x §relative luminance. Domain: 0–1 per channel after
// gamma decode; result: 0–1 (black=0, white=1).
func relativeLuminance(c rgb) float64 {
	srgb := func(v uint8) float64 {
		fv := float64(v) / 255.0
		if fv <= 0.03928 {
			return fv / 12.92
		}
		return math.Pow((fv+0.055)/1.055, 2.4)
	}
	return 0.2126*srgb(c.R) + 0.7152*srgb(c.G) + 0.0722*srgb(c.B)
}

// wcagContrastRatio is the standard (L1 + 0.05) / (L2 + 0.05)
// formula. The result is symmetric — caller doesn't have to know
// which color is lighter. Output range: 1.0 (identical) to 21.0
// (black-on-white).
func wcagContrastRatio(a, b rgb) float64 {
	la := relativeLuminance(a)
	lb := relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
