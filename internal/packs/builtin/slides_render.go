package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
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
func SlidesRender() *packs.Pack {
	return &packs.Pack{
		Name:        "slides.render",
		Version:     "v1",
		Description: "Render a Marp markdown deck to PDF, PPTX, or HTML.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"markdown"},
			Properties: map[string]string{
				"markdown": "string",
				"format":   "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"format", "artifact_key", "size"},
			Properties: map[string]string{
				"format":       "string",
				"artifact_key": "string",
				"size":         "number",
			},
		},
		Handler: slidesRenderHandler,
	}
}

type slidesInput struct {
	Markdown string `json:"markdown"`
	Format   string `json:"format"`
}

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

func slidesRenderHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
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
		Cmd: []string{
			"marp",
			"--stdin",
			"--allow-local-files",
			flag,
			"-o", "-",
		},
		Stdin: []byte(in.Markdown),
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
	return json.Marshal(map[string]any{
		"format":       ext,
		"artifact_key": art.Key,
		"size":         art.Size,
	})
}
