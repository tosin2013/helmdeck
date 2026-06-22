// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_inspect.go — wrap upstream `hyperframes inspect --json`
// as a pre-render diagnostic that catches RUNTIME layout issues — text
// overflow, container collapse, element-vs-background contrast, the
// transition-seam overlaps that fire only at tween boundaries. Where
// hyperframes.lint catches STATIC issues by reading source files,
// inspect catches RENDERED issues by sampling the DOM at specified
// (or auto-derived) timestamps and checking each sampled state.
//
// Architectural shape mirrors hyperframes.lint exactly — wraps an
// external diagnostic CLI, returns structured findings, optional
// strict mode for CI / publish gates, soft-surface by default
// (findings ARE the output; the pack only errors when something
// genuinely broke at the transport layer).
//
// Why both lint AND inspect: lint runs in <1s, finds STRUCTURAL
// issues (the wrong audio attribute, the wrong font import) before
// the renderer ever starts. inspect runs in seconds-to-minutes
// (loads the project in headless Chrome, samples N timestamps), and
// finds RENDER-TIME issues (a label that fits in the layout at t=0
// but overflows at t=12.5 when an animation expands its parent).
// They're complementary; both should land before a render is
// authorized in a publish-gated pipeline.

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

// HyperframesInspect constructs the pack.
func HyperframesInspect() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.inspect",
		Version: "v1",
		Description: "Runtime-layout pre-render diagnostic for hyperframes scaffold projects. Wraps upstream `hyperframes inspect --json` to catch text/container overflow, element collapse, and transition-seam overlaps by loading the project in headless Chrome and sampling the DOM at N timestamps (default 9 midpoint samples). Pair with `hyperframes.lint` (catches static issues from source) and `hyperframes.validate` (catches console errors + WCAG contrast). Set `at_transitions:true` to sample every tween start/end boundary — catches transient overlaps that only fire at transition seams. Soft-surface by default; pass `strict:true` to gate downstream packs on a clean inspection result.",
		NeedsSession: true,
		Async:        false,
		// Pin to the hyperframes sidecar image — `hyperframes inspect`
		// loads the composition in headless Chrome, which is bundled
		// with the hyperframes CLI in helmdeck-sidecar-hyperframes.
		// Inspect samples N timestamps in headless Chrome so it's
		// heavier than lint but lighter than render — 10 min is the
		// safe cap for a 720s composition with at_transitions sampling.
		SessionSpec: session.Spec{
			Image:       hyperframesSidecarImage(),
			MemoryLimit: "2g",
			Timeout:     10 * time.Minute,
			CPUProfile:  session.ProfileCompute,
		},
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				// Same input shape as lint + render.
				"project_artifact_key": "string",
				"composition_html":     "string",
				// Sampling controls — passed through to the upstream
				// CLI. Defaults match the CLI's: 9 midpoint samples,
				// tolerance 2 pixels, timeout 5000ms.
				"samples":    "integer",
				"at":         "string", // CSV of timestamps in seconds
				"tolerance":  "integer",
				"timeout":    "integer",
				"max_issues": "integer",
				// at_transitions samples every tween start/end as
				// well — catches the transition-seam overlaps that
				// midpoint sampling alone misses.
				"at_transitions": "boolean",
				// Strict mode: any error-severity issue surfaces as
				// CodeArtifactFailed. Default false (soft surface).
				"strict": "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"inspect"},
			Properties: map[string]string{
				"inspect":              "object",
				"inspect_artifact_key": "string",
			},
		},
		Handler: hyperframesInspectHandler(),
	}
}

type hyperframesInspectInput struct {
	ProjectArtifactKey string `json:"project_artifact_key"`
	CompositionHTML    string `json:"composition_html"`
	Samples            int    `json:"samples"`
	At                 string `json:"at"`
	Tolerance          int    `json:"tolerance"`
	Timeout            int    `json:"timeout"`
	MaxIssues          int    `json:"max_issues"`
	AtTransitions      bool   `json:"at_transitions"`
	Strict             bool   `json:"strict"`
}

// hyperframesInspectIssue mirrors a single entry in the upstream
// `issues[]` array. Kept flat so json.Unmarshal accepts the CLI's
// output verbatim with no shape negotiation. Nested rect/overflow
// objects are preserved as RawMessage so we don't lose any fields
// the CLI may add over time.
type hyperframesInspectIssue struct {
	Code              string          `json:"code"`
	Severity          string          `json:"severity"`
	Time              float64         `json:"time"`
	Selector          string          `json:"selector,omitempty"`
	ContainerSelector string          `json:"containerSelector,omitempty"`
	Text              string          `json:"text,omitempty"`
	Message           string          `json:"message"`
	FixHint           string          `json:"fixHint,omitempty"`
	Rect              json.RawMessage `json:"rect,omitempty"`
	ContainerRect     json.RawMessage `json:"containerRect,omitempty"`
	Overflow          json.RawMessage `json:"overflow,omitempty"`
}

// hyperframesInspectReport mirrors the top-level JSON the CLI emits.
type hyperframesInspectReport struct {
	SchemaVersion   int                       `json:"schemaVersion"`
	Duration        float64                   `json:"duration"`
	Samples         []float64                 `json:"samples"`
	Tolerance       int                       `json:"tolerance"`
	Strict          bool                      `json:"strict"`
	CollapseStatic  bool                      `json:"collapseStatic"`
	OK              bool                      `json:"ok"`
	ErrorCount      int                       `json:"errorCount"`
	WarningCount    int                       `json:"warningCount"`
	InfoCount       int                       `json:"infoCount"`
	IssueCount      int                       `json:"issueCount"`
	TotalIssueCount int                       `json:"totalIssueCount"`
	Truncated       bool                      `json:"truncated"`
	Issues          []hyperframesInspectIssue `json:"issues"`
}

func hyperframesInspectHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in hyperframesInspectInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		hasProj := strings.TrimSpace(in.ProjectArtifactKey) != ""
		hasComp := strings.TrimSpace(in.CompositionHTML) != ""
		switch {
		case !hasProj && !hasComp:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "hyperframes.inspect requires exactly one of project_artifact_key OR composition_html"}
		case hasProj && hasComp:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "project_artifact_key and composition_html are mutually exclusive — pass exactly one"}
		}
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
				Message: "hyperframes.inspect requires a session executor"}
		}

		ec.Report(0, "scaffolding hyperframes project for inspect")
		renderInput := hyperframesRenderInput{
			ProjectArtifactKey: in.ProjectArtifactKey,
			CompositionHTML:    in.CompositionHTML,
		}
		if perr := setupHyperframesProjectDir(ctx, ec, renderInput); perr != nil {
			return nil, perr
		}

		args := []string{"hyperframes", "inspect", hyperframesProjectDir, "--json"}
		if in.Samples > 0 {
			args = append(args, "--samples", strconv.Itoa(in.Samples))
		}
		if strings.TrimSpace(in.At) != "" {
			args = append(args, "--at", in.At)
		}
		if in.Tolerance > 0 {
			args = append(args, "--tolerance", strconv.Itoa(in.Tolerance))
		}
		if in.Timeout > 0 {
			args = append(args, "--timeout", strconv.Itoa(in.Timeout))
		}
		if in.MaxIssues > 0 {
			args = append(args, "--max-issues", strconv.Itoa(in.MaxIssues))
		}
		if in.AtTransitions {
			args = append(args, "--at-transitions")
		}
		ec.Report(50, "running hyperframes inspect (headless render-sample loop)")
		execRes, execErr := ec.Exec(ctx, session.ExecRequest{Cmd: args})
		if execErr != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("hyperframes inspect exec failed: %v", execErr), Cause: execErr}
		}
		jsonBlob := stripNonJSONPrefix(execRes.Stdout)
		if len(jsonBlob) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("hyperframes inspect emitted no JSON (exit %d, stderr: %s)",
					execRes.ExitCode, truncStr(strings.TrimSpace(string(execRes.Stderr)), 1024))}
		}
		var report hyperframesInspectReport
		if err := json.Unmarshal(jsonBlob, &report); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("parse hyperframes inspect JSON: %v", err), Cause: err}
		}

		ec.Report(90, "persisting inspect sidecar")
		out := map[string]any{
			"inspect": map[string]any{
				"ok":                report.OK,
				"duration":          report.Duration,
				"error_count":       report.ErrorCount,
				"warning_count":     report.WarningCount,
				"info_count":        report.InfoCount,
				"issue_count":       report.IssueCount,
				"total_issue_count": report.TotalIssueCount,
				"truncated":         report.Truncated,
				"sample_count":      len(report.Samples),
				"issues":            report.Issues,
			},
		}
		if ec.Artifacts != nil {
			sidecarBytes, _ := json.Marshal(out["inspect"])
			if art, putErr := ec.Artifacts.Put(ctx, "hyperframes.inspect", "inspect.json", sidecarBytes, "application/json"); putErr == nil {
				out["inspect_artifact_key"] = art.Key
			}
		}

		if in.Strict && report.ErrorCount > 0 {
			marshaled, _ := json.Marshal(out)
			return marshaled, &packs.PackError{Code: packs.CodeArtifactFailed,
				Message: fmt.Sprintf("hyperframes.inspect: %d error-severity issue(s) in strict mode (warnings=%d, info=%d). First: %s",
					report.ErrorCount, report.WarningCount, report.InfoCount, firstInspectErrorSummary(report.Issues))}
		}

		ec.Report(100, "inspect complete")
		return json.Marshal(out)
	}
}

func firstInspectErrorSummary(issues []hyperframesInspectIssue) string {
	for _, iss := range issues {
		if iss.Severity == "error" {
			loc := iss.Selector
			if iss.Time > 0 {
				loc = fmt.Sprintf("%s @ t=%.2fs", iss.Selector, iss.Time)
			}
			return fmt.Sprintf("[%s] %s (%s)", iss.Code, iss.Message, loc)
		}
	}
	return "<no error-severity issue visible>"
}
