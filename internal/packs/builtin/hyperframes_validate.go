// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_validate.go — wrap upstream `hyperframes validate --json`
// as a pre-render diagnostic that catches console errors during a real
// headless-Chrome load + (by default) WCAG contrast issues across
// timeline samples. Complements hyperframes.lint (static issues) and
// hyperframes.inspect (layout sampling) — this pack is the
// runtime-failure detector: it boots the project in Chrome, watches
// the DevTools console, and reports anything that the browser itself
// flagged as a real error.
//
// Common findings:
//   - CORS errors (an asset URL the renderer can't fetch — silent
//     blank media in the MP4)
//   - net::ERR_FAILED for any external resource (Google Fonts,
//     external video, missing local file)
//   - JS runtime errors (the composition's script threw and the
//     timeline never registered → blank canvas)
//   - WCAG AA contrast failures across sampled timestamps
//
// Architectural shape mirrors hyperframes.lint + hyperframes.inspect:
// same setup helper, same JSON-parse + strict-mode contract,
// soft-surface by default.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// HyperframesValidate constructs the pack.
func HyperframesValidate() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.validate",
		Version: "v1",
		Description: "Runtime-error pre-render diagnostic for hyperframes scaffold projects. Wraps upstream `hyperframes validate --json` — loads the composition in headless Chrome and reports DevTools console errors (CORS failures, missing assets, JS exceptions) plus (by default) WCAG AA contrast issues across sampled timestamps. Catches the failure modes that lint can't see (static-source-only) and inspect doesn't surface (layout-only): a composition script that throws on load, a CORS-blocked video URL that produces a blank media element, an external font fetch the sandbox blocks. Soft-surface by default; pass `strict:true` to gate downstream packs on a clean validation. Final third of the pre-render validation suite alongside `hyperframes.lint` (static) and `hyperframes.inspect` (layout).",
		NeedsSession: true,
		Async:        false,
		// Pin to the hyperframes sidecar image — `hyperframes validate`
		// loads the composition in headless Chrome to capture console
		// errors and run the WCAG contrast audit. Same image as
		// render/lint/inspect — bundles the CLI + headless Chrome.
		SessionSpec: session.Spec{
			Image:       hyperframesSidecarImage(),
			MemoryLimit: "2g",
			Timeout:     5 * time.Minute,
			CPUProfile:  session.ProfileCompute,
		},
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				"project_artifact_key": "string",
				"composition_html":     "string",
				// Default true (matches CLI default). Set false to
				// skip the WCAG contrast audit — useful when the
				// composition is intentionally low-contrast (cinema
				// titles, motion-graphics intro frames) and the
				// noise outweighs the signal.
				"contrast": "boolean",
				// CLI default is 5000ms; passed through verbatim.
				"timeout": "integer",
				// Strict mode: any error-level console message
				// surfaces as CodeArtifactFailed. Contrast failures
				// do NOT trigger strict — they're a separate audit
				// dimension. Default false.
				"strict": "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"validate"},
			Properties: map[string]string{
				"validate":              "object",
				"validate_artifact_key": "string",
			},
		},
		Handler: hyperframesValidateHandler(),
	}
}

type hyperframesValidateInput struct {
	ProjectArtifactKey string `json:"project_artifact_key"`
	CompositionHTML    string `json:"composition_html"`
	// Pointer so we can distinguish unset (default-true) from
	// explicit-false (skip contrast).
	Contrast *bool `json:"contrast"`
	Timeout  int   `json:"timeout"`
	Strict   bool  `json:"strict"`
}

// hyperframesValidateConsoleEntry mirrors a single entry in the
// upstream `errors[]` or `warnings[]` array.
type hyperframesValidateConsoleEntry struct {
	Level string `json:"level"`
	Text  string `json:"text"`
	URL   string `json:"url,omitempty"`
}

// hyperframesValidateContrastEntry mirrors a single entry in the
// upstream `contrast[]` array. Per-sample-per-text-element row.
type hyperframesValidateContrastEntry struct {
	Time     float64 `json:"time"`
	Selector string  `json:"selector,omitempty"`
	Text     string  `json:"text,omitempty"`
	Ratio    float64 `json:"ratio"`
	WCAGAA   bool    `json:"wcagAA"`
	Large    bool    `json:"large"`
	FG       string  `json:"fg,omitempty"`
	BG       string  `json:"bg,omitempty"`
}

// hyperframesValidateReport mirrors the top-level JSON.
type hyperframesValidateReport struct {
	OK       bool                                `json:"ok"`
	Errors   []hyperframesValidateConsoleEntry   `json:"errors"`
	Warnings []hyperframesValidateConsoleEntry   `json:"warnings"`
	Contrast []hyperframesValidateContrastEntry  `json:"contrast"`
}

func hyperframesValidateHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in hyperframesValidateInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		hasProj := strings.TrimSpace(in.ProjectArtifactKey) != ""
		hasComp := strings.TrimSpace(in.CompositionHTML) != ""
		switch {
		case !hasProj && !hasComp:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "hyperframes.validate requires exactly one of project_artifact_key OR composition_html"}
		case hasProj && hasComp:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "project_artifact_key and composition_html are mutually exclusive — pass exactly one"}
		}
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
				Message: "hyperframes.validate requires a session executor"}
		}

		ec.Report(0, "scaffolding hyperframes project for validate")
		renderInput := hyperframesRenderInput{
			ProjectArtifactKey: in.ProjectArtifactKey,
			CompositionHTML:    in.CompositionHTML,
		}
		if perr := setupHyperframesProjectDir(ctx, ec, renderInput); perr != nil {
			return nil, perr
		}

		args := []string{"hyperframes", "validate", hyperframesProjectDir, "--json"}
		if in.Contrast != nil && !*in.Contrast {
			args = append(args, "--no-contrast")
		}
		if in.Timeout > 0 {
			args = append(args, "--timeout", strconv.Itoa(in.Timeout))
		}
		ec.Report(50, "running hyperframes validate (headless Chrome load + console audit)")
		execRes, execErr := ec.Exec(ctx, session.ExecRequest{Cmd: args})
		if execErr != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("hyperframes validate exec failed: %v", execErr), Cause: execErr}
		}
		jsonBlob := stripNonJSONPrefix(execRes.Stdout)
		if len(jsonBlob) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("hyperframes validate emitted no JSON (exit %d, stderr: %s)",
					execRes.ExitCode, truncStr(strings.TrimSpace(string(execRes.Stderr)), 1024))}
		}
		var report hyperframesValidateReport
		if err := json.Unmarshal(jsonBlob, &report); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("parse hyperframes validate JSON: %v", err), Cause: err}
		}

		// Derive a WCAG-failure count for operator-facing surfaces.
		// The CLI emits every text-element-per-sample row; we
		// summarize "how many rows failed AA?" without re-deriving
		// the raw rows downstream.
		contrastFailures := 0
		for _, c := range report.Contrast {
			if !c.WCAGAA {
				contrastFailures++
			}
		}

		ec.Report(90, "persisting validate sidecar")
		out := map[string]any{
			"validate": map[string]any{
				"ok":                  report.OK,
				"error_count":         len(report.Errors),
				"warning_count":       len(report.Warnings),
				"contrast_sample_count": len(report.Contrast),
				"contrast_failure_count": contrastFailures,
				"errors":              report.Errors,
				"warnings":            report.Warnings,
				"contrast":            report.Contrast,
			},
		}
		if ec.Artifacts != nil {
			sidecarBytes, _ := json.Marshal(out["validate"])
			if art, putErr := ec.Artifacts.Put(ctx, "hyperframes.validate", "validate.json", sidecarBytes, "application/json"); putErr == nil {
				out["validate_artifact_key"] = art.Key
			}
		}

		if in.Strict && len(report.Errors) > 0 {
			marshaled, _ := json.Marshal(out)
			return marshaled, &packs.PackError{Code: packs.CodeArtifactFailed,
				Message: fmt.Sprintf("hyperframes.validate: %d console error(s) in strict mode. First: %s",
					len(report.Errors), firstValidateErrorSummary(report.Errors))}
		}

		ec.Report(100, "validate complete")
		return json.Marshal(out)
	}
}

func firstValidateErrorSummary(errors []hyperframesValidateConsoleEntry) string {
	if len(errors) == 0 {
		return "<no console error visible>"
	}
	e := errors[0]
	txt := e.Text
	if len(txt) > 200 {
		txt = txt[:200] + "..."
	}
	return fmt.Sprintf("[%s] %s", e.Level, txt)
}
