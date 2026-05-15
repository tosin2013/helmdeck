package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// SlidesRender is the third reference pack referenced by ADR 014. It
// renders a Marp markdown deck into PDF, PPTX, or HTML inside the
// session container — Marp + a working Chromium are part of the
// sidecar image (T104), so the pack is "render this markdown" and
// nothing else.
//
// Why exec, not CDP: Marp is a CLI that drives Chromium internally.
// Trying to script it through DevTools Protocol would mean
// re-implementing Marp's slide-splitter, theme loader, and PDF
// engine in Go. The pack just shells out to `marp`.
//
// Input shape:
//
//	{
//	  "markdown": "# Slide 1\n---\n# Slide 2",
//	  "format":   "pdf"  // one of: pdf, pptx, html (default pdf)
//	}
//
// Output shape:
//
//	{
//	  "format":       "pdf",
//	  "artifact_key": "slides.render/<rand>-deck.pdf",
//	  "size":         123456
//	}
func SlidesRender(v *vault.Store, eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:        "slides.render",
		Version:     "v1",
		Description: "Render a Marp markdown deck to PDF, PPTX, or HTML. Supports ```mermaid``` fenced blocks (rendered via mmdc) and optional hero image (generated via fal.ai when `hero_image_prompt` is set).",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"markdown"},
			Properties: map[string]string{
				"markdown":          "string",
				"format":            "string",
				"mermaid":           "boolean",
				"hero_image_prompt": "string",
				"hero_image_model":  "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"format", "artifact_key", "size"},
			Properties: map[string]string{
				"format":                "string",
				"artifact_key":          "string",
				"size":                  "number",
				"hero_image_model_used": "string",
				// #202: contrast lint surfaces palette anti-patterns
				// as warnings (informational, not errors). Empty/absent
				// when the deck looks clean.
				"warnings":          "array",
				"curated_theme_used": "string",
			},
		},
		Handler: slidesRenderHandler(v, eg),
	}
}

type slidesInput struct {
	Markdown string `json:"markdown"`
	Format   string `json:"format"`
	// Mermaid controls whether ```mermaid fences are pre-rendered to
	// inline SVG via mmdc before Marp sees the deck. Default true.
	// Use a *bool so an absent field is "default on" and an explicit
	// false opts out for decks that don't need mermaid (saves a few
	// hundred ms of mmdc startup).
	Mermaid *bool `json:"mermaid,omitempty"`
	// HeroImagePrompt, when non-empty, triggers image.generate via the
	// shared RunImageGen helper (#146). The generated PNG is base64-
	// inlined as an <img data:image/png;base64,...> before slide 1 so
	// every output format (PDF/PPTX/HTML) renders the hero without
	// Marp needing to fetch URLs at render time.
	HeroImagePrompt string `json:"hero_image_prompt"`
	// HeroImageModel overrides the default fal.ai model used for
	// hero generation. Empty → fal-ai/flux/schnell (fastest/cheapest).
	HeroImageModel string `json:"hero_image_model"`
}

// mermaidFenceRe matches ```mermaid…``` fenced blocks (single-line or
// multi-line). The (?s) flag lets `.` match newlines. The body is
// captured non-greedily so adjacent fences don't merge.
var mermaidFenceRe = regexp.MustCompile("(?s)```mermaid\\s*\\n(.*?)\\n```")

// formatExtension returns the marp output flag, file extension, and
// MIME type for a requested format. Centralised so the validation
// path and the artifact-write path can't drift.
func formatSpec(format string) (marpFlag, ext, mime string, err error) {
	switch format {
	case "", "pdf":
		return "--pdf", "pdf", "application/pdf", nil
	case "pptx":
		return "--pptx", "pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
	case "html":
		return "--html", "html", "text/html", nil
	default:
		return "", "", "", fmt.Errorf("unsupported format %q (want pdf, pptx, or html)", format)
	}
}

func slidesRenderHandler(v *vault.Store, eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in slidesInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if in.Markdown == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "markdown must not be empty"}
		}
		flag, ext, mime, err := formatSpec(in.Format)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if ec.Exec == nil {
			// The engine guarantees Exec is non-nil only when a session
			// executor was wired. Surfacing this as session_unavailable
			// is the honest answer — the runtime is up but the bridge
			// to in-container tooling is missing.
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
		}

		markdown := in.Markdown

		// Hero image — when prompt is set, call RunImageGen via the
		// shared #146 helper, fetch the artifact bytes, base64-inline
		// before slide 1 so PDF/PPTX/HTML all render the hero without
		// Marp needing to resolve URLs at render time. Failures bubble
		// up; an unreachable fal.ai shouldn't silently produce a
		// header-less deck.
		heroModelUsed := ""
		if strings.TrimSpace(in.HeroImagePrompt) != "" {
			heroModel := in.HeroImageModel
			if heroModel == "" {
				heroModel = imageGenDefaultModel
			}
			heroRes, perr := RunImageGen(ctx, ec, v, eg, ImageGenRequest{
				Prompt: in.HeroImagePrompt,
				Model:  heroModel,
			})
			if perr != nil {
				return nil, perr
			}
			heroModelUsed = heroRes.ModelUsed
			// Fetch the artifact bytes and base64-inline. We could
			// alternately reference a URL, but inline keeps Marp from
			// needing network access inside the sidecar to render the
			// hero — important for the PDF path where Chromium fetches
			// images during rasterisation.
			imgBytes, _, gerr := ec.Artifacts.Get(ctx, heroRes.ArtifactKeys[0])
			if gerr != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("read hero image artifact: %v", gerr), Cause: gerr}
			}
			dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)
			heroBlock := fmt.Sprintf("<img src=\"%s\" alt=\"hero\" class=\"hero-image\" />\n\n---\n\n", dataURI)
			// Prepend BEFORE any frontmatter check: Marp swallows the
			// first --- as the slide separator only if frontmatter is
			// absent. The safest insertion is right after the closing
			// `---` of the frontmatter block (if present) — match
			// against the second `---\n` from start. If no frontmatter,
			// prepend the hero block as a new first slide.
			if idx := frontmatterEndIndex(markdown); idx > 0 {
				markdown = markdown[:idx] + "\n" + heroBlock + markdown[idx:]
			} else {
				markdown = heroBlock + markdown
			}
		}

		// Mermaid pre-processing — substitute ```mermaid fences with
		// inline-SVG <img> data-URIs so PDF/PPTX/HTML outputs all show
		// the diagrams. Caller can opt out with mermaid:false.
		mermaidOn := in.Mermaid == nil || *in.Mermaid
		if mermaidOn {
			rewritten, perr := preprocessMermaidFences(ctx, ec.Exec, markdown)
			if perr != nil {
				return nil, perr
			}
			markdown = rewritten
		}

		// #202 Option C: when the deck's frontmatter declares one of
		// our curated themes, upload the embedded CSS files to the
		// sidecar so marp can resolve `theme: helmdeck-dark` (etc).
		// Skip the upload work for built-in Marp themes — operators
		// using `theme: gaia` shouldn't pay for it on every call.
		curatedThemeUsed := ""
		marpArgs := []string{
			"marp",
			"--stdin",
			"--allow-local-files",
			flag,
			"-o", "-",
		}
		if markdownReferencesCuratedTheme(markdown) {
			themeDir, terr := uploadCuratedThemes(ctx, ec)
			if terr != nil {
				return nil, terr
			}
			marpArgs = append(marpArgs, "--theme-set", themeDir)
			curatedThemeUsed = extractCuratedThemeName(markdown)
		}

		// Marp reads markdown from stdin when given `-` as the input
		// path. We use `--stdin -o -` so the binary output streams back
		// over our captured stdout — no temp files inside the container,
		// no path management. The format flag (pdf/pptx/html) selects
		// the output codec.
		//
		// `--allow-local-files` is required for any deck that references
		// local images; harmless when the markdown has none. We do NOT
		// pass `--input-dir` so Marp can't escape the stdin sandbox.
		res, err := ec.Exec(ctx, session.ExecRequest{
			Cmd:   marpArgs,
			Stdin: []byte(markdown),
		})
		if err != nil {
			return nil, fmt.Errorf("exec marp: %w", err)
		}
		if res.ExitCode != 0 {
			// Surface marp's stderr verbatim — its messages are useful to
			// pack authors and don't carry secrets. Truncate hard-coded
			// to keep error envelopes small.
			stderr := string(res.Stderr)
			if len(stderr) > 1024 {
				stderr = stderr[:1024] + "...(truncated)"
			}
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("marp exit %d: %s", res.ExitCode, stderr),
			}
		}
		if len(res.Stdout) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "marp produced empty output"}
		}

		art, err := ec.Artifacts.Put(ctx, ec.Pack.Name, "deck."+ext, res.Stdout, mime)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeArtifactFailed, Message: err.Error(), Cause: err}
		}
		out := map[string]any{
			"format":       ext,
			"artifact_key": art.Key,
			"size":         art.Size,
		}
		if heroModelUsed != "" {
			out["hero_image_model_used"] = heroModelUsed
		}
		if curatedThemeUsed != "" {
			out["curated_theme_used"] = curatedThemeUsed
		}
		// #202 Option B: contrast lint runs on the input markdown
		// (NOT the mermaid-rewritten copy) so warnings reference the
		// CSS the author actually wrote. Lint is best-effort — a
		// parse failure cannot prevent a successful render.
		if w := LintContrast(in.Markdown); len(w) > 0 {
			out["warnings"] = w
		}
		return json.Marshal(out)
	}
}

// extractCuratedThemeName returns the helmdeck-* theme name from the
// markdown's frontmatter `theme:` value, or empty if none of our
// curated themes is referenced. Used to populate the response's
// `curated_theme_used` field so callers can confirm which theme
// actually applied (rather than guessing from frontmatter parsing
// on their end).
func extractCuratedThemeName(md string) string {
	end := frontmatterEndIndex(md)
	if end <= 0 {
		return ""
	}
	fm := md[:end]
	for _, name := range curatedThemeNames() {
		needles := []string{
			"\ntheme: " + name + "\n",
			"\ntheme: \"" + name + "\"\n",
			"\ntheme: '" + name + "'\n",
		}
		for _, n := range needles {
			if strings.Contains(fm, n) {
				return name
			}
		}
		if strings.HasPrefix(fm, "---\ntheme: "+name+"\n") ||
			strings.HasPrefix(fm, "---\r\ntheme: "+name+"\r\n") {
			return name
		}
	}
	return ""
}

// frontmatterEndIndex returns the byte index of the start of the line
// AFTER the closing `---\n` of a Marp frontmatter block. Returns 0 if
// the markdown does not begin with `---\n` (i.e., no frontmatter).
// Used by the hero-image inserter to land the hero block AFTER the
// frontmatter directives, not before them.
func frontmatterEndIndex(md string) int {
	if !strings.HasPrefix(md, "---\n") {
		return 0
	}
	rest := md[4:]
	if i := strings.Index(rest, "\n---\n"); i >= 0 {
		return 4 + i + len("\n---\n")
	}
	return 0
}

// preprocessMermaidFences finds every ```mermaid…``` block in the
// markdown, renders each to SVG via mmdc inside the session container,
// and substitutes the fence with an inline-SVG <img> data-URI. The
// result is plain Marp markdown that PDF/PPTX/HTML all render with
// diagrams in place.
//
// mmdc is single-shot per diagram; we use a small sh -c wrapper to
// write stdin to a temp .mmd, run mmdc, and cat the resulting .svg.
// One session exec per diagram. mmdc bootstrap is ~500ms so a deck with
// many diagrams accumulates; future work could batch via a single
// stdin-multi-doc wrapper script. For most technical decks (1–4
// diagrams) the cost is acceptable.
//
// On mmdc failure the function returns the diagram's source verbatim
// in the error (truncated) so authors can spot syntax problems without
// having to re-run with mmdc locally.
func preprocessMermaidFences(ctx context.Context, exec func(context.Context, session.ExecRequest) (session.ExecResult, error), md string) (string, *packs.PackError) {
	matches := mermaidFenceRe.FindAllStringSubmatchIndex(md, -1)
	if len(matches) == 0 {
		return md, nil
	}
	// Walk matches in order, building the rewritten string. Indices
	// from FindAllStringSubmatchIndex are pairs: [start, end, g1Start, g1End].
	var b strings.Builder
	cursor := 0
	for i, m := range matches {
		fenceStart, fenceEnd := m[0], m[1]
		diagStart, diagEnd := m[2], m[3]
		b.WriteString(md[cursor:fenceStart])
		diagram := md[diagStart:diagEnd]
		svg, perr := mmdcRender(ctx, exec, diagram, i)
		if perr != nil {
			return "", perr
		}
		// Inline-SVG <img> via data URI. We base64 the SVG rather than
		// URL-encoding it — Marp's HTML renderer chokes on % signs in
		// raw-data URLs more often than on base64.
		dataURI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg))
		b.WriteString(`<img src="`)
		b.WriteString(dataURI)
		b.WriteString(`" alt="mermaid diagram" class="mermaid-svg" />`)
		cursor = fenceEnd
	}
	b.WriteString(md[cursor:])
	return b.String(), nil
}

// mmdcRender executes mmdc inside the session container with the given
// mermaid source on stdin and returns the SVG bytes from stdout.
func mmdcRender(ctx context.Context, exec func(context.Context, session.ExecRequest) (session.ExecResult, error), diagram string, idx int) (string, *packs.PackError) {
	// One-shot shell pipeline: read stdin to tmpdir, run mmdc, cat svg,
	// clean up. mmdc's puppeteer needs --no-sandbox in a container; the
	// sidecar ships /etc/mmdc/puppeteer-config.json (see
	// deploy/docker/sidecar.Dockerfile) that sets it. -q silences progress
	// chatter on stdout so we get clean SVG.
	script := `set -e
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
cat > "$T/in.mmd"
mmdc -i "$T/in.mmd" -o "$T/out.svg" -p /etc/mmdc/puppeteer-config.json -q >&2
cat "$T/out.svg"`
	res, err := exec(ctx, session.ExecRequest{
		Cmd:   []string{"sh", "-c", script},
		Stdin: []byte(diagram),
	})
	if err != nil {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("exec mmdc (diagram %d): %v", idx, err), Cause: err}
	}
	if res.ExitCode != 0 {
		stderr := string(res.Stderr)
		if len(stderr) > 512 {
			stderr = stderr[:512] + "...(truncated)"
		}
		preview := diagram
		if len(preview) > 256 {
			preview = preview[:256] + "...(truncated)"
		}
		return "", &packs.PackError{
			Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("mmdc exit %d on diagram %d: %s\n--- diagram source ---\n%s",
				res.ExitCode, idx, stderr, preview),
		}
	}
	if len(res.Stdout) == 0 {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("mmdc produced empty SVG on diagram %d", idx)}
	}
	return string(res.Stdout), nil
}
