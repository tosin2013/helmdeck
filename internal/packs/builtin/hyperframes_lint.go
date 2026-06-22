// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_lint.go — wrap upstream `hyperframes lint --json` as a
// pre-render diagnostic pack. The motivation surfaced during the
// "v0.29.3 render produces 2 frames over 90 seconds" investigation:
// upstream's own `lint` flags multiple issues that explain blank
// renders (most importantly `media_missing_id`: audio without an `id`
// attribute is silent in renders, and `gsap_studio_edit_blocked`:
// manual `window.__timelines["x"] = tl` registration that conflicts
// with the runtime's auto-registration). Catching these BEFORE the
// expensive render call saves wall-clock and surfaces fixable issues
// in a structured shape pipelines / agents can act on.
//
// Architectural shape mirrors av.validate (av_validate.go): wrap an
// external diagnostic tool, parse its JSON output, return structured
// findings, optional strict mode for CI / publish-gate use. Both
// packs are deterministic, both produce a sidecar artifact, both
// default to soft surfacing (findings are the output; the pack only
// errors when something genuinely broke at the transport layer).
//
// Token-savings rationale: the same as av.validate. Every "the video
// rendered blank — why?" diagnostic burns ~3-5K tokens of bash output
// + log analysis. Once this pack is wired in as a pre-step in the
// scaffolded-narrated-video pipeline, the agent reads typed findings
// in ~200 tokens. Upstream CLI takes precedence over custom Go (per
// the standing engineering principle): we shell to `hyperframes lint`
// rather than reimplementing the framework's rule engine.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// HyperframesLint constructs the pack.
//
// No external dispatcher / vault / egress dependency — the lint
// invocation runs entirely inside the sidecar against either a
// downloaded project tarball or a single-file composition_html
// written to a temp project dir.
func HyperframesLint() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.lint",
		Version: "v1",
		Description: "Pre-render validation for hyperframes scaffold projects. Wraps upstream `hyperframes lint --json` to catch render-killing issues — missing media id (audio silent in renders), Google Fonts imports (silent in sandboxed renders), manual __timelines registrations that conflict with the runtime's auto-discovery, composition self-attribute selectors that leak across embedded instances. Findings are typed (code/severity/message/fixHint/snippet/file). By default returns success even with errors — findings ARE the output. Pass `strict:true` to surface error-severity findings as a typed CodeArtifactFailed for CI / publish-gate use. Pairs with `hyperframes.render` (run lint first, then render only if clean) and slots into `builtin.scaffolded-narrated-video` between `attach_audio` and `render`.",
		NeedsSession: true,
		Async:        false,
		// Pin to the hyperframes sidecar image so the `hyperframes lint`
		// CLI is on PATH. Same convention as hyperframes.render
		// (HELMDECK_SIDECAR_HYPERFRAMES env override). Without this
		// pin, the session executor spawns into the default base
		// sidecar which doesn't have the hyperframes CLI installed,
		// and the exec returns exit 127 ("command not found") with no
		// JSON output for the handler to parse. Lint is fast — ~1s
		// for a single file — so 5 min is a generous cap.
		SessionSpec: session.Spec{
			Image:       hyperframesSidecarImage(),
			MemoryLimit: "1g",
			Timeout:     5 * time.Minute,
			CPUProfile:  session.ProfileIO,
		},
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				// Same input shape as hyperframes.render — pass exactly
				// ONE of:
				//   project_artifact_key — tarball from upstream
				//     scaffold/attach_audio/etc. (typical pipeline use)
				//   composition_html — single-file index.html
				//     (one-shot lint of an agent-authored composition)
				"project_artifact_key": "string",
				"composition_html":     "string",
				// Verbose mode surfaces info-level findings (hidden by
				// default in the CLI). Useful for thorough audits;
				// most pipelines stay at default false.
				"verbose": "boolean",
				// Strict mode: any error-severity finding surfaces as
				// CodeArtifactFailed. Default false (soft surface).
				"strict": "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"lint"},
			Properties: map[string]string{
				// Structured lint report: { ok, error_count,
				// warning_count, info_count, files_scanned,
				// findings: [{ code, severity, message, fixHint?,
				// snippet?, file }] }.
				"lint": "object",
				// Sidecar artifact pointing at the raw upstream JSON
				// (empty when artifact store unavailable in test
				// contexts; the inline `lint` field always carries
				// the same data).
				"lint_artifact_key": "string",
			},
		},
		Handler: hyperframesLintHandler(),
	}
}

// hyperframesLintInput mirrors the pack's input schema.
type hyperframesLintInput struct {
	ProjectArtifactKey string `json:"project_artifact_key"`
	CompositionHTML    string `json:"composition_html"`
	Verbose            bool   `json:"verbose"`
	Strict             bool   `json:"strict"`
}

// hyperframesLintFinding mirrors a single entry in the upstream CLI's
// `findings[]` array. Kept as a flat struct so json.Unmarshal accepts
// the CLI's output verbatim with no shape negotiation.
type hyperframesLintFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	FixHint  string `json:"fixHint,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	Selector string `json:"selector,omitempty"`
	File     string `json:"file,omitempty"`
}

// hyperframesLintReport mirrors the top-level JSON the CLI emits with
// --json. The handler unmarshals into this, then re-marshals into the
// pack's output shape (snake_case + a few derived fields).
type hyperframesLintReport struct {
	OK           bool                     `json:"ok"`
	ErrorCount   int                      `json:"errorCount"`
	WarningCount int                      `json:"warningCount"`
	InfoCount    int                      `json:"infoCount"`
	Findings     []hyperframesLintFinding `json:"findings"`
	FilesScanned int                      `json:"filesScanned"`
}

func hyperframesLintHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in hyperframesLintInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// Mutually-exclusive inputs — same contract as
		// hyperframes.render. The handler sets up the project dir via
		// the shared helper either way.
		hasProj := strings.TrimSpace(in.ProjectArtifactKey) != ""
		hasComp := strings.TrimSpace(in.CompositionHTML) != ""
		switch {
		case !hasProj && !hasComp:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "hyperframes.lint requires exactly one of project_artifact_key OR composition_html"}
		case hasProj && hasComp:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "project_artifact_key and composition_html are mutually exclusive — pass exactly one"}
		}
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
				Message: "hyperframes.lint requires a session executor"}
		}

		ec.Report(0, "scaffolding hyperframes project for lint")
		// Reuse the render pack's project-dir setup. Same temp path
		// (hyperframesProjectDir), same tarball extraction logic.
		// Idempotent if invoked twice in the same session (the helper
		// rmrf's first).
		hyperframesRenderInput := hyperframesRenderInput{
			ProjectArtifactKey: in.ProjectArtifactKey,
			CompositionHTML:    in.CompositionHTML,
		}
		if perr := setupHyperframesProjectDir(ctx, ec, hyperframesRenderInput); perr != nil {
			return nil, perr
		}

		args := []string{"hyperframes", "lint", hyperframesProjectDir, "--json"}
		if in.Verbose {
			args = append(args, "--verbose")
		}
		ec.Report(50, "running hyperframes lint")
		execRes, execErr := ec.Exec(ctx, session.ExecRequest{Cmd: args})
		if execErr != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("hyperframes lint exec failed: %v", execErr), Cause: execErr}
		}
		// The CLI prints a telemetry notice to stdout before the JSON
		// payload — strip non-JSON prefix lines. Findings always start
		// with a `{` line; everything before it is decoration.
		jsonBlob := stripNonJSONPrefix(execRes.Stdout)
		if len(jsonBlob) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("hyperframes lint emitted no JSON (exit %d, stderr: %s)",
					execRes.ExitCode, truncStr(strings.TrimSpace(string(execRes.Stderr)), 1024))}
		}
		var report hyperframesLintReport
		if err := json.Unmarshal(jsonBlob, &report); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("parse hyperframes lint JSON: %v", err), Cause: err}
		}

		ec.Report(90, "persisting lint sidecar")
		// Rebuild a normalized JSON payload — snake_case keys for the
		// helmdeck consumer surface, but preserve the upstream camelCase
		// in the findings[] entries (those are quoted by the upstream
		// docs and stable across versions).
		out := map[string]any{
			"lint": map[string]any{
				"ok":            report.OK,
				"error_count":   report.ErrorCount,
				"warning_count": report.WarningCount,
				"info_count":    report.InfoCount,
				"files_scanned": report.FilesScanned,
				"findings":      report.Findings,
			},
		}
		// Persist the raw report as a sidecar so operators can pull
		// the full JSON for offline analysis. Empty when ec.Artifacts
		// is unwired (test contexts); the inline `lint` field always
		// carries the same data, so the sidecar is convenience, not
		// load-bearing.
		if ec.Artifacts != nil {
			sidecarBytes, _ := json.Marshal(out["lint"])
			if art, putErr := ec.Artifacts.Put(ctx, "hyperframes.lint", "lint.json", sidecarBytes, "application/json"); putErr == nil {
				out["lint_artifact_key"] = art.Key
			}
		}

		if in.Strict && report.ErrorCount > 0 {
			// Strict mode surfaces a typed CodeArtifactFailed so CI /
			// publish gates can short-circuit downstream packs. The
			// findings are still in the error chain via the output
			// (the engine surfaces both `error` AND the partial
			// output to the caller).
			marshaled, _ := json.Marshal(out)
			return marshaled, &packs.PackError{Code: packs.CodeArtifactFailed,
				Message: fmt.Sprintf("hyperframes.lint: %d error-severity finding(s) in strict mode (warnings=%d, info=%d). First: %s",
					report.ErrorCount, report.WarningCount, report.InfoCount, firstErrorSummary(report.Findings))}
		}

		ec.Report(100, "lint complete")
		return json.Marshal(out)
	}
}

// firstErrorSummary returns a one-line summary of the first error-
// severity finding for strict-mode error messages. Keeps the typed
// PackError message human-readable without dumping the full report.
func firstErrorSummary(findings []hyperframesLintFinding) string {
	for _, f := range findings {
		if f.Severity == "error" {
			return fmt.Sprintf("[%s] %s", f.Code, f.Message)
		}
	}
	return "<no error-severity finding visible>"
}

// stripNonJSONPrefix removes any non-JSON header lines from the
// upstream CLI's stdout. The telemetry notice and ASCII art are
// emitted before the JSON payload on the very first invocation in
// a session; subsequent invocations may emit only the JSON. The
// search is a single `{` scan — first `{` at column 0 of any line
// starts the JSON object.
func stripNonJSONPrefix(stdout []byte) []byte {
	s := string(stdout)
	idx := strings.Index(s, "\n{")
	if idx == -1 {
		// Possibly the very first byte IS `{` already.
		if strings.HasPrefix(strings.TrimSpace(s), "{") {
			return []byte(strings.TrimSpace(s))
		}
		return nil
	}
	return []byte(strings.TrimSpace(s[idx+1:]))
}
