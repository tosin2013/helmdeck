// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// slides_themes.go (#202 Option C) — curated Marp themes shipped
// alongside slides.render so agents can pick a known-WCAG-AA palette
// by name (`theme: helmdeck-dark`, `theme: helmdeck-corporate`)
// instead of authoring custom CSS that frequently produces
// unreadable color combinations.
//
// The theme CSS files live in assets/marp-themes/ and are embedded
// into the control-plane binary at build time. On each slides.render
// invocation, the pack writes them to a temp dir inside the sidecar
// and passes `--theme-set <dir>` to marp; the frontmatter's `theme:`
// value selects which embedded theme actually applies.
//
// The themes themselves are hand-tuned for WCAG AA: every element
// that nests inside `section` (h*, p, a, strong, em, table, th, td,
// code, pre, blockquote, ul/ol, li, hr) has explicit foreground +
// background declarations, so the #202 reproducer (changed `section`
// background leaves default light-themed tables glaring through) is
// structurally impossible against these themes.

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// slidesThemesFS embeds the curated theme CSS files. The themes live
// in this package's `themes/` subdirectory because Go's //go:embed
// directive only walks DOWN from the package source file, never up
// into a sibling repo-rooted assets/ tree.
//
//go:embed themes/helmdeck-dark.css themes/helmdeck-corporate.css
var slidesThemesFS embed.FS

// slidesThemeDir is the path inside the sidecar where uploaded
// themes land. marp reads it via `--theme-set <dir>`.
const slidesThemeDir = "/tmp/helmdeck-marp-themes"

// curatedThemeFilenames is the canonical list of helmdeck-shipped
// theme CSS files. New themes added to assets/marp-themes/ must be
// appended here AND to the //go:embed directive above.
var curatedThemeFilenames = []string{
	"helmdeck-dark.css",
	"helmdeck-corporate.css",
}

// curatedThemeNames returns the `@theme` names declared in the
// embedded CSS files, in the same order as curatedThemeFilenames.
// Used for input validation + doc generation.
func curatedThemeNames() []string {
	return []string{"helmdeck-dark", "helmdeck-corporate"}
}

// uploadCuratedThemes copies the embedded theme CSS files into the
// session container's slidesThemeDir and returns the directory path
// to pass to `marp --theme-set`. The dir is recreated fresh on each
// call so a stale half-uploaded file from a previous error can't
// confuse marp's theme registry.
//
// Side-effect-free for callers: the function performs all sidecar
// I/O via the provided executor; nothing on the control-plane side
// touches disk.
func uploadCuratedThemes(ctx context.Context, ec *packs.ExecutionContext) (string, *packs.PackError) {
	// Recreate the dir so a previous-run half-write can't leave a
	// truncated CSS file behind for marp to choke on.
	mkdirRes, err := ec.Exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", "rm -rf " + slidesThemeDir + " && mkdir -p " + slidesThemeDir},
	})
	if err != nil || mkdirRes.ExitCode != 0 {
		return "", &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("prepare theme dir: %v (exit %d)", err, mkdirRes.ExitCode),
		}
	}

	for _, name := range curatedThemeFilenames {
		body, rerr := slidesThemesFS.ReadFile("themes/" + name)
		if rerr != nil {
			// Build-time invariant — if //go:embed compiled, the file
			// is present. Surfacing as Internal because there's no
			// recovery path the agent can take.
			return "", &packs.PackError{Code: packs.CodeInternal,
				Message: fmt.Sprintf("embedded theme %s missing at runtime: %v", name, rerr)}
		}
		target := slidesThemeDir + "/" + name
		if _, werr := execWithStdin(ctx, ec, target, body); werr != nil {
			return "", &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("upload theme %s: %v", name, werr)}
		}
	}
	return slidesThemeDir, nil
}

// markdownReferencesCuratedTheme returns true if the markdown's
// frontmatter declares a `theme:` value that matches one of our
// curated themes. The check is permissive — leading whitespace,
// quoted values, and CRLF line endings all parse correctly.
//
// When this returns false, slides_render.go skips the theme-upload
// path entirely — operators using only Marp built-ins shouldn't pay
// for the upload work on every call.
func markdownReferencesCuratedTheme(md string) bool {
	// Walk the first frontmatter block only — Marp's `theme:` directive
	// must live in YAML frontmatter, not inline.
	if !strings.HasPrefix(md, "---\n") && !strings.HasPrefix(md, "---\r\n") {
		return false
	}
	end := frontmatterEndIndex(md)
	if end <= 0 {
		return false
	}
	fm := md[:end]
	for _, name := range curatedThemeNames() {
		// Match `theme: <name>` at start-of-line, allowing single or
		// double quotes. We don't pull in a full YAML parser for one
		// directive; the substring check is robust enough for Marp's
		// flat frontmatter shape.
		needles := []string{
			"\ntheme: " + name + "\n",
			"\ntheme: \"" + name + "\"\n",
			"\ntheme: '" + name + "'\n",
		}
		for _, n := range needles {
			if strings.Contains(fm, n) {
				return true
			}
		}
		// Also accept the value on the first line (no leading newline).
		if strings.HasPrefix(fm, "---\ntheme: "+name+"\n") ||
			strings.HasPrefix(fm, "---\r\ntheme: "+name+"\r\n") {
			return true
		}
	}
	return false
}
