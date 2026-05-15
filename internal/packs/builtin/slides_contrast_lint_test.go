// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"math"
	"strings"
	"testing"
)

// --- WCAG math ----------------------------------------------------------

func TestParseHexColor(t *testing.T) {
	cases := []struct {
		in        string
		wantR     uint8
		wantG     uint8
		wantB     uint8
		wantOK    bool
	}{
		{"#000000", 0, 0, 0, true},
		{"#FFFFFF", 0xFF, 0xFF, 0xFF, true},
		{"#ffffff", 0xFF, 0xFF, 0xFF, true},
		{"#F1F5F9", 0xF1, 0xF5, 0xF9, true},
		{"#fff", 0xFF, 0xFF, 0xFF, true},
		{"#abc", 0xAA, 0xBB, 0xCC, true},
		{"  #0f172a  ", 0x0F, 0x17, 0x2A, true},
		// Non-hex inputs should explicitly skip (false, not panic).
		{"rgb(255,0,0)", 0, 0, 0, false},
		{"red", 0, 0, 0, false},
		{"linear-gradient(135deg, #fff, #000)", 0, 0, 0, false},
		{"#zzz", 0, 0, 0, false},
		{"#12345", 0, 0, 0, false},
		{"", 0, 0, 0, false},
	}
	for _, c := range cases {
		got, ok := parseHexColor(c.in)
		if ok != c.wantOK {
			t.Errorf("parseHexColor(%q) ok=%v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got.R != c.wantR || got.G != c.wantG || got.B != c.wantB {
			t.Errorf("parseHexColor(%q) = (%d,%d,%d), want (%d,%d,%d)",
				c.in, got.R, got.G, got.B, c.wantR, c.wantG, c.wantB)
		}
	}
}

func TestWCAGContrastRatio_KnownPairs(t *testing.T) {
	// Reference values verified against multiple online WCAG calculators.
	// Allow ±0.05 tolerance for floating-point drift across implementations.
	cases := []struct {
		fg, bg string
		want   float64
	}{
		// Extreme: black on white = 21:1
		{"#000000", "#FFFFFF", 21.0},
		// Identical colors = 1:1
		{"#888888", "#888888", 1.0},
		// helmdeck-dark body text on bg — well above WCAG AAA.
		{"#F1F5F9", "#0F172A", 16.3},
		// helmdeck-dark accent on bg — well above WCAG AA.
		{"#38BDF8", "#0F172A", 8.3},
		// helmdeck-corporate body text on bg — well above WCAG AAA.
		{"#1F2937", "#FFFFFF", 14.7},
		// The #202 reproducer: light grey on white = fails 4.5:1.
		// `lightgrey` is ~#D3D3D3.
		{"#D3D3D3", "#FFFFFF", 1.6},
	}
	for _, c := range cases {
		fg, _ := parseHexColor(c.fg)
		bg, _ := parseHexColor(c.bg)
		got := wcagContrastRatio(fg, bg)
		if math.Abs(got-c.want) > 0.5 {
			t.Errorf("wcagContrastRatio(%s,%s) = %.2f, want ~%.2f", c.fg, c.bg, got, c.want)
		}
	}
}

func TestWCAGContrastRatio_Symmetric(t *testing.T) {
	a, _ := parseHexColor("#123456")
	b, _ := parseHexColor("#abcdef")
	if math.Abs(wcagContrastRatio(a, b)-wcagContrastRatio(b, a)) > 1e-9 {
		t.Errorf("contrast ratio must be symmetric in argument order")
	}
}

// --- CSS extraction + rule parsing -------------------------------------

func TestExtractCSS_FrontmatterStyleBlock(t *testing.T) {
	md := "---\nmarp: true\nstyle: |\n  section { background: #0F172A; color: #F1F5F9; }\n  h1 { color: #38BDF8; }\n---\n\n# Hello\n"
	css := extractCSS(md)
	if !strings.Contains(css, "background: #0F172A") {
		t.Errorf("expected style block extracted, got: %q", css)
	}
	if !strings.Contains(css, "h1 { color: #38BDF8; }") {
		t.Errorf("expected h1 rule extracted, got: %q", css)
	}
}

func TestExtractCSS_EmbeddedStyleTag(t *testing.T) {
	md := "---\nmarp: true\n---\n\n<style>\nsection { background: #fff; color: #000; }\n</style>\n\n# Hello"
	css := extractCSS(md)
	if !strings.Contains(css, "section { background: #fff; color: #000; }") {
		t.Errorf("expected <style> tag body extracted, got: %q", css)
	}
}

func TestExtractCSS_ScopedStyleTag(t *testing.T) {
	md := "---\nmarp: true\n---\n\n<style scoped>\np { color: #f00; }\n</style>"
	css := extractCSS(md)
	if !strings.Contains(css, "color: #f00") {
		t.Errorf("expected scoped <style> body extracted, got: %q", css)
	}
}

func TestExtractCSS_NoCustomStyling_ReturnsEmpty(t *testing.T) {
	md := "---\nmarp: true\ntheme: gaia\n---\n\n# Plain deck"
	css := extractCSS(md)
	if css != "" {
		t.Errorf("expected empty CSS for theme-only deck, got: %q", css)
	}
}

func TestParseCSSRules_SimpleSelectorsAndDeclarations(t *testing.T) {
	css := `section { background: #fff; color: #000; }
            h1 { color: #f00; }`
	rules := parseCSSRules(css)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d: %+v", len(rules), rules)
	}
	if rules[0].selector != "section" || rules[0].color != "#000" || rules[0].background != "#fff" {
		t.Errorf("rule 0 = %+v, want section/#000/#fff", rules[0])
	}
	if rules[1].selector != "h1" || rules[1].color != "#f00" {
		t.Errorf("rule 1 = %+v, want h1/#f00", rules[1])
	}
}

func TestParseCSSRules_CommaSeparatedSelectorsExpand(t *testing.T) {
	css := `table, th, td { color: #fff; background-color: #0F172A; }`
	rules := parseCSSRules(css)
	if len(rules) != 3 {
		t.Fatalf("expected 3 expanded rules (table/th/td), got %d: %+v", len(rules), rules)
	}
	for _, r := range rules {
		if r.color != "#fff" || r.background != "#0F172A" {
			t.Errorf("rule %+v missing color/background propagation", r)
		}
	}
}

func TestParseCSSRules_StripComments(t *testing.T) {
	css := `/* color: #fff; */ section { background: #000; color: #fff; }`
	rules := parseCSSRules(css)
	if len(rules) != 1 || rules[0].selector != "section" {
		t.Errorf("expected 1 section rule, got %+v", rules)
	}
}

// --- anti-pattern detection (#202 reproducer) -------------------------

func TestLintContrast_TheReproducer_FlagsAntiPattern(t *testing.T) {
	// The reproducer from #202: dark blue section bg, no table/code
	// overrides. Default light tables now render against dark slide.
	md := `---
marp: true
style: |
  section { background: #1e3a8a; color: #ffffff; }
  h1 { color: #fbbf24; }
---

# Dark deck

| col1 | col2 |
|------|------|
| a    | b    |
`
	warnings := LintContrast(md)
	if len(warnings) == 0 {
		t.Fatalf("expected at least one warning for reproducer, got 0")
	}
	gotRule := false
	for _, w := range warnings {
		if w.Rule == "section-background-without-nested-overrides" {
			gotRule = true
			if !strings.Contains(w.Recommendation, "table") {
				t.Errorf("recommendation should mention 'table': %s", w.Recommendation)
			}
		}
	}
	if !gotRule {
		t.Errorf("expected section-background-without-nested-overrides rule, got warnings: %+v", warnings)
	}
}

func TestLintContrast_CuratedDarkTheme_ProducesNoWarnings(t *testing.T) {
	// When the deck just picks the curated theme, the agent didn't
	// author any custom CSS — there's nothing to lint. The frontmatter
	// `theme:` directive itself doesn't trigger the static check.
	md := "---\nmarp: true\ntheme: helmdeck-dark\n---\n\n# Hello\n"
	warnings := LintContrast(md)
	if len(warnings) != 0 {
		t.Errorf("curated-theme deck should produce no warnings, got: %+v", warnings)
	}
}

func TestLintContrast_FullyOverriddenDeck_NoAntiPatternWarning(t *testing.T) {
	// When the author DOES override `section` background AND restyle
	// every nested element, no anti-pattern warning fires.
	md := `---
marp: true
style: |
  section { background: #1e3a8a; color: #ffffff; }
  table, th, td { background-color: #1e293b; color: #f1f5f9; }
  code { background-color: #0b1220; color: #f0a500; }
  blockquote { background-color: #1e293b; color: #cbd5e1; }
  pre { background-color: #0b1220; color: #f1f5f9; }
---

# Hello
`
	warnings := LintContrast(md)
	for _, w := range warnings {
		if w.Rule == "section-background-without-nested-overrides" {
			t.Errorf("did not expect anti-pattern warning, got: %+v", w)
		}
	}
}

func TestLintContrast_DirectWCAGViolation_FlagsRatio(t *testing.T) {
	// Single rule with light-grey text on white bg = ratio ~1.6:1.
	// Should fire wcag-aa-text-contrast.
	md := `---
marp: true
style: |
  td { color: #d3d3d3; background-color: #ffffff; }
  table, th, code, blockquote, pre { color: #000; background-color: #fff; }
  section { color: #000; background: #fff; }
---

# Hello
`
	warnings := LintContrast(md)
	found := false
	for _, w := range warnings {
		if w.Rule == "wcag-aa-text-contrast" && w.Selector == "td" {
			found = true
			if !strings.Contains(w.Ratio, ":1") {
				t.Errorf("ratio should be a string like 1.61:1, got %q", w.Ratio)
			}
		}
	}
	if !found {
		t.Errorf("expected wcag-aa-text-contrast warning for td #d3d3d3/#fff, got: %+v", warnings)
	}
}

func TestLintContrast_PassingContrastInSameRule_NoViolation(t *testing.T) {
	// Dark text on white bg easily passes.
	md := `---
marp: true
style: |
  section { color: #111; background-color: #fff; }
  table, th, td, code, blockquote, pre { color: #111; background-color: #fff; }
---

# Hello
`
	warnings := LintContrast(md)
	for _, w := range warnings {
		if w.Rule == "wcag-aa-text-contrast" {
			t.Errorf("did not expect ratio warning for clearly-passing palette, got: %+v", w)
		}
	}
}

func TestLintContrast_NonHexColors_AreSkippedNotErrored(t *testing.T) {
	// rgb(...) and gradients shouldn't false-positive — we deliberately
	// limit the WCAG check to hex values to avoid surfacing bogus
	// warnings on plausible-looking-but-uncheckable inputs.
	md := `---
marp: true
style: |
  section { background: linear-gradient(135deg, #1a1a2e, #16213e); color: white; }
  table, th, td, code, blockquote, pre { color: white; }
---

# Hello
`
	warnings := LintContrast(md)
	for _, w := range warnings {
		if w.Rule == "wcag-aa-text-contrast" {
			t.Errorf("non-hex values must skip the ratio check, got: %+v", w)
		}
	}
}

func TestLintContrast_NoCustomCSS_ReturnsEmpty(t *testing.T) {
	md := "---\nmarp: true\ntheme: gaia\n---\n\n# Hello\n"
	warnings := LintContrast(md)
	if len(warnings) != 0 {
		t.Errorf("plain Marp deck should produce no warnings, got: %+v", warnings)
	}
}

// --- helpers ------------------------------------------------------------

func TestFindSectionBackgroundOverride_BasicAndModifierSelectors(t *testing.T) {
	rules := []cssRule{
		{selector: "h1", color: "#fff"},
		{selector: "section.dark", color: "#fff", background: "#000"},
	}
	if got := findSectionBackgroundOverride(rules); got != "#000" {
		t.Errorf("expected #000 from section.dark, got %q", got)
	}
}

func TestHasRuleForSelector_CompoundSelectors(t *testing.T) {
	rules := []cssRule{
		{selector: "table, th, td", color: "#fff"},
		{selector: "section h1", color: "#fff"},
	}
	if !hasRuleForSelector(rules, "table") {
		t.Errorf("expected match for 'table' in compound selector")
	}
	if !hasRuleForSelector(rules, "td") {
		t.Errorf("expected match for 'td' in compound selector")
	}
	if !hasRuleForSelector(rules, "h1") {
		t.Errorf("expected match for 'h1' in descendant selector")
	}
	if hasRuleForSelector(rules, "blockquote") {
		t.Errorf("did not expect match for 'blockquote'")
	}
}
