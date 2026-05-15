// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/session"
)

// --- frontmatter-detection unit tests -----------------------------------

func TestMarkdownReferencesCuratedTheme(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want bool
	}{
		{
			name: "helmdeck-dark plain",
			md:   "---\nmarp: true\ntheme: helmdeck-dark\n---\n\n# Hi\n",
			want: true,
		},
		{
			name: "helmdeck-corporate plain",
			md:   "---\nmarp: true\ntheme: helmdeck-corporate\n---\n",
			want: true,
		},
		{
			name: "helmdeck-dark double-quoted",
			md:   "---\nmarp: true\ntheme: \"helmdeck-dark\"\n---\n",
			want: true,
		},
		{
			name: "helmdeck-dark single-quoted",
			md:   "---\nmarp: true\ntheme: 'helmdeck-dark'\n---\n",
			want: true,
		},
		{
			name: "helmdeck-dark on first line of frontmatter",
			md:   "---\ntheme: helmdeck-dark\nmarp: true\n---\n",
			want: true,
		},
		{
			name: "Marp builtin theme is NOT curated",
			md:   "---\nmarp: true\ntheme: gaia\n---\n",
			want: false,
		},
		{
			name: "no frontmatter",
			md:   "# Hi\n",
			want: false,
		},
		{
			name: "helmdeck-dark mentioned in body, not frontmatter",
			md:   "---\nmarp: true\n---\n\n# theme: helmdeck-dark\n",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := markdownReferencesCuratedTheme(c.md); got != c.want {
				t.Errorf("markdownReferencesCuratedTheme = %v, want %v", got, c.want)
			}
		})
	}
}

func TestExtractCuratedThemeName(t *testing.T) {
	cases := map[string]string{
		"---\nmarp: true\ntheme: helmdeck-dark\n---\n":          "helmdeck-dark",
		"---\nmarp: true\ntheme: helmdeck-corporate\n---\n":     "helmdeck-corporate",
		"---\nmarp: true\ntheme: gaia\n---\n":                   "",
		"---\nmarp: true\ntheme: \"helmdeck-dark\"\n---\n":      "helmdeck-dark",
		"# No frontmatter\n":                                    "",
	}
	for in, want := range cases {
		if got := extractCuratedThemeName(in); got != want {
			t.Errorf("extractCuratedThemeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- handler integration: theme upload + --theme-set --------------------

func TestSlidesRender_CuratedTheme_UploadsAndPassesThemeSet(t *testing.T) {
	// Capture every Exec call so we can verify the upload sequence
	// AND the marp argv shape.
	var calls []session.ExecRequest
	ex := &fakeExecutor{
		dispatch: func(req session.ExecRequest) (session.ExecResult, error) {
			calls = append(calls, req)
			if len(req.Cmd) > 0 && req.Cmd[0] == "marp" {
				return session.ExecResult{Stdout: []byte("%PDF fake")}, nil
			}
			return session.ExecResult{ExitCode: 0}, nil
		},
	}
	eng := newSlidesEngine(t, ex)

	md := "---\nmarp: true\ntheme: helmdeck-dark\n---\n\n# Hello\n"
	body, _ := json.Marshal(map[string]string{"markdown": md, "format": "pdf"})
	res, err := eng.Execute(context.Background(), SlidesRender(nil, nil), body)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// We expect: prepare-themes-dir (mkdir), write helmdeck-dark.css,
	// write helmdeck-corporate.css, then marp.
	var marpCall *session.ExecRequest
	sawMkdir, sawDarkWrite, sawCorpWrite := false, false, false
	for i, c := range calls {
		if len(c.Cmd) > 0 && c.Cmd[0] == "marp" {
			marpCall = &calls[i]
		}
		if len(c.Cmd) >= 3 && c.Cmd[0] == "sh" && c.Cmd[1] == "-c" {
			s := c.Cmd[2]
			if strings.Contains(s, "mkdir -p") && strings.Contains(s, slidesThemeDir) {
				sawMkdir = true
			}
			if strings.Contains(s, slidesThemeDir+"/helmdeck-dark.css") {
				sawDarkWrite = true
			}
			if strings.Contains(s, slidesThemeDir+"/helmdeck-corporate.css") {
				sawCorpWrite = true
			}
		}
	}
	if !sawMkdir {
		t.Errorf("expected mkdir for theme dir")
	}
	if !sawDarkWrite {
		t.Errorf("expected helmdeck-dark.css written into theme dir")
	}
	if !sawCorpWrite {
		t.Errorf("expected helmdeck-corporate.css written into theme dir")
	}
	if marpCall == nil {
		t.Fatal("expected a marp invocation")
	}
	// marp argv must include --theme-set <dir>
	foundFlag := false
	for i, a := range marpCall.Cmd {
		if a == "--theme-set" && i+1 < len(marpCall.Cmd) && marpCall.Cmd[i+1] == slidesThemeDir {
			foundFlag = true
		}
	}
	if !foundFlag {
		t.Errorf("marp cmd missing --theme-set %s: %v", slidesThemeDir, marpCall.Cmd)
	}

	// Response should advertise the curated theme used.
	var out struct {
		CuratedThemeUsed string `json:"curated_theme_used"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.CuratedThemeUsed != "helmdeck-dark" {
		t.Errorf("response curated_theme_used = %q, want helmdeck-dark", out.CuratedThemeUsed)
	}
}

func TestSlidesRender_BuiltinTheme_SkipsThemeUpload(t *testing.T) {
	// `theme: gaia` is a Marp builtin — no upload work should happen.
	var calls []session.ExecRequest
	ex := &fakeExecutor{
		dispatch: func(req session.ExecRequest) (session.ExecResult, error) {
			calls = append(calls, req)
			if len(req.Cmd) > 0 && req.Cmd[0] == "marp" {
				return session.ExecResult{Stdout: []byte("%PDF fake")}, nil
			}
			return session.ExecResult{ExitCode: 0}, nil
		},
	}
	eng := newSlidesEngine(t, ex)

	md := "---\nmarp: true\ntheme: gaia\n---\n\n# Hello\n"
	body, _ := json.Marshal(map[string]string{"markdown": md, "format": "pdf"})
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), body)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Only one call expected (the marp call); no mkdir/upload work.
	if len(calls) != 1 {
		t.Errorf("expected 1 exec (marp only), got %d", len(calls))
	}
	for _, a := range calls[len(calls)-1].Cmd {
		if a == "--theme-set" {
			t.Errorf("did not expect --theme-set for builtin theme: %v", calls[0].Cmd)
		}
	}
}

// --- handler integration: warnings in response --------------------------

func TestSlidesRender_ReproducerDeck_EmitsWarning(t *testing.T) {
	// The #202 reproducer: dark blue section bg, no table override.
	md := `---
marp: true
style: |
  section { background: #1e3a8a; color: #ffffff; }
---

# Dark deck
`
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF")}}
	eng := newSlidesEngine(t, ex)

	body, _ := json.Marshal(map[string]string{"markdown": md, "format": "pdf"})
	res, err := eng.Execute(context.Background(), SlidesRender(nil, nil), body)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var out struct {
		Warnings []ContrastWarning `json:"warnings"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Warnings) == 0 {
		t.Fatalf("expected at least one warning in response, got 0")
	}
	gotAntiPattern := false
	for _, w := range out.Warnings {
		if w.Rule == "section-background-without-nested-overrides" {
			gotAntiPattern = true
		}
	}
	if !gotAntiPattern {
		t.Errorf("expected section-background-without-nested-overrides warning, got: %+v", out.Warnings)
	}
}

func TestSlidesRender_CleanDeck_NoWarningsInResponse(t *testing.T) {
	// Plain Marp deck, no custom CSS — `warnings` should be absent
	// (omitted, not just empty) so the response shape stays tight.
	md := "---\nmarp: true\ntheme: gaia\n---\n\n# Hello\n"
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF")}}
	eng := newSlidesEngine(t, ex)

	body, _ := json.Marshal(map[string]string{"markdown": md, "format": "pdf"})
	res, err := eng.Execute(context.Background(), SlidesRender(nil, nil), body)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	if _, present := out["warnings"]; present {
		t.Errorf("clean deck should not include warnings key, got: %v", out["warnings"])
	}
}

// --- embedded-FS smoke -------------------------------------------------

func TestSlidesThemes_EmbeddedFSContainsCuratedThemes(t *testing.T) {
	// Compile-time invariant: the //go:embed directive must include
	// both curated themes. Catch typos at unit-test time, not when an
	// agent calls theme: helmdeck-dark in production.
	for _, name := range curatedThemeFilenames {
		body, err := slidesThemesFS.ReadFile("themes/" + name)
		if err != nil {
			t.Errorf("embedded FS missing themes/%s: %v", name, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("themes/%s is embedded but empty", name)
		}
		// The CSS header /* @theme <name> */ is what Marp uses to
		// resolve `theme: <name>` from frontmatter. Verify it's present
		// so a missing header doesn't silently break theme resolution.
		header := "@theme " + strings.TrimSuffix(name, ".css")
		if !strings.Contains(string(body), header) {
			t.Errorf("themes/%s missing required '%s' header", name, header)
		}
	}
}
