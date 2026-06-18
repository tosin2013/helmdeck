// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

type hyperframesInspectExecScript struct {
	calls         []session.ExecRequest
	inspectStdout []byte
	inspectStderr []byte
	inspectExit   int
}

func (h *hyperframesInspectExecScript) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	h.calls = append(h.calls, req)
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.Contains(req.Cmd[2], "mkdir -p") {
		return session.ExecResult{}, nil
	}
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.Contains(req.Cmd[2], "cat >") && len(req.Stdin) > 0 {
		return session.ExecResult{}, nil
	}
	if len(req.Cmd) >= 1 && req.Cmd[0] == "tar" {
		return session.ExecResult{}, nil
	}
	if len(req.Cmd) >= 2 && req.Cmd[0] == "test" && req.Cmd[1] == "-f" {
		return session.ExecResult{}, nil
	}
	if len(req.Cmd) >= 2 && req.Cmd[0] == "hyperframes" && req.Cmd[1] == "inspect" {
		return session.ExecResult{
			Stdout:   h.inspectStdout,
			Stderr:   h.inspectStderr,
			ExitCode: h.inspectExit,
		}, nil
	}
	return session.ExecResult{}, nil
}

func (h *hyperframesInspectExecScript) inspectCmdArgs() []string {
	for _, c := range h.calls {
		if len(c.Cmd) >= 2 && c.Cmd[0] == "hyperframes" && c.Cmd[1] == "inspect" {
			return c.Cmd
		}
	}
	return nil
}

func runInspect(t *testing.T, exec *hyperframesInspectExecScript, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesInspect()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-hf-inspect"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec.fn,
		Artifacts: packs.NewMemoryArtifactStore(),
	}
	return pack.Handler(context.Background(), ec)
}

var cleanInspectJSON = []byte(`{"schemaVersion":1,"duration":15,"samples":[0,5,10],"tolerance":2,"strict":false,"collapseStatic":true,"ok":true,"errorCount":0,"warningCount":0,"infoCount":0,"issueCount":0,"totalIssueCount":0,"truncated":false,"issues":[]}`)

var flaggedInspectJSON = []byte(`{
  "schemaVersion": 1,
  "duration": 15,
  "samples": [0,5,10,12.5,14.9],
  "tolerance": 2,
  "ok": false,
  "errorCount": 1,
  "warningCount": 1,
  "infoCount": 0,
  "issueCount": 2,
  "totalIssueCount": 2,
  "truncated": false,
  "issues": [
    {
      "code": "text_box_overflow",
      "severity": "error",
      "time": 12.5,
      "selector": "div.wes-text-bottom",
      "containerSelector": "div.wes-container",
      "text": "and render.",
      "message": "Text extends outside its nearest visual/container box.",
      "fixHint": "Text is 259px x 55"
    },
    {
      "code": "transition_overlap",
      "severity": "warning",
      "time": 10.0,
      "selector": ".hero",
      "message": "Sibling clips overlap at transition seam."
    }
  ]
}`)

// --- input validation -----------------------------------------------------

func TestHyperframesInspect_MissingBothInputs_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: cleanInspectJSON}
	_, err := runInspect(t, exec, `{}`)
	if err == nil {
		t.Fatalf("expected error for missing both inputs")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got: %v", err)
	}
}

func TestHyperframesInspect_BothInputs_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: cleanInspectJSON}
	_, err := runInspect(t, exec, `{"composition_html":"<html/>","project_artifact_key":"x"}`)
	if err == nil {
		t.Fatalf("expected error for mutually-exclusive inputs")
	}
}

// --- happy path -----------------------------------------------------------

func TestHyperframesInspect_CleanComposition_ReturnsStructuredOutput(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: cleanInspectJSON}
	raw, err := runInspect(t, exec, `{"composition_html":"<html/>"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Inspect struct {
			OK           bool                     `json:"ok"`
			Duration     float64                  `json:"duration"`
			ErrorCount   int                      `json:"error_count"`
			WarningCount int                      `json:"warning_count"`
			SampleCount  int                      `json:"sample_count"`
			Issues       []map[string]interface{} `json:"issues"`
		} `json:"inspect"`
		InspectArtifactKey string `json:"inspect_artifact_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Inspect.OK || out.Inspect.SampleCount != 3 || out.Inspect.Duration != 15 {
		t.Errorf("unexpected report: %+v", out.Inspect)
	}
	if out.InspectArtifactKey == "" {
		t.Errorf("expected inspect_artifact_key sidecar populated")
	}
}

func TestHyperframesInspect_FlaggedComposition_SurfacesIssues(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: flaggedInspectJSON}
	raw, err := runInspect(t, exec, `{"composition_html":"<html/>"}`)
	if err != nil {
		t.Fatalf("default mode should NOT error even with error-severity issues; got: %v", err)
	}
	var out struct {
		Inspect struct {
			ErrorCount int                       `json:"error_count"`
			Issues     []hyperframesInspectIssue `json:"issues"`
		} `json:"inspect"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Inspect.ErrorCount != 1 || len(out.Inspect.Issues) != 2 {
		t.Errorf("unexpected counts: errors=%d issues=%d", out.Inspect.ErrorCount, len(out.Inspect.Issues))
	}
	if out.Inspect.Issues[0].Code != "text_box_overflow" {
		t.Errorf("first issue code unexpected: %q", out.Inspect.Issues[0].Code)
	}
}

// --- strict mode ----------------------------------------------------------

func TestHyperframesInspect_StrictMode_ErrorSeverityFails(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: flaggedInspectJSON}
	_, err := runInspect(t, exec, `{"composition_html":"<html/>","strict":true}`)
	if err == nil {
		t.Fatalf("strict mode should error on error-severity issues")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeArtifactFailed {
		t.Fatalf("expected CodeArtifactFailed, got: %v", err)
	}
	if !strings.Contains(pe.Message, "text_box_overflow") {
		t.Errorf("strict-mode error should name the first error code: %s", pe.Message)
	}
	if !strings.Contains(pe.Message, "t=12.50s") {
		t.Errorf("strict-mode error should include the timestamp: %s", pe.Message)
	}
}

func TestHyperframesInspect_StrictMode_CleanPasses(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: cleanInspectJSON}
	_, err := runInspect(t, exec, `{"composition_html":"<html/>","strict":true}`)
	if err != nil {
		t.Fatalf("strict mode with zero errors should succeed, got: %v", err)
	}
}

// --- CLI argv shape -------------------------------------------------------

func TestHyperframesInspect_AtTransitionsFlag_Threads(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: cleanInspectJSON}
	if _, err := runInspect(t, exec, `{"composition_html":"<html/>","at_transitions":true}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	args := exec.inspectCmdArgs()
	found := false
	for _, a := range args {
		if a == "--at-transitions" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --at-transitions flag, got argv: %v", args)
	}
}

func TestHyperframesInspect_SamplesAndAt_Threads(t *testing.T) {
	exec := &hyperframesInspectExecScript{inspectStdout: cleanInspectJSON}
	if _, err := runInspect(t, exec, `{"composition_html":"<html/>","samples":15,"at":"1.5,4,7.25","tolerance":3}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	args := exec.inspectCmdArgs()
	if argValue(args, "--samples") != "15" {
		t.Errorf("expected --samples 15, got argv: %v", args)
	}
	if argValue(args, "--at") != "1.5,4,7.25" {
		t.Errorf("expected --at threading, got argv: %v", args)
	}
	if argValue(args, "--tolerance") != "3" {
		t.Errorf("expected --tolerance 3, got argv: %v", args)
	}
}

// --- error paths ----------------------------------------------------------

func TestHyperframesInspect_NoJSONOutput_HandlerFails(t *testing.T) {
	exec := &hyperframesInspectExecScript{
		inspectStdout: []byte("internal CLI error"),
		inspectStderr: []byte("cannot launch chrome"),
		inspectExit:   1,
	}
	_, err := runInspect(t, exec, `{"composition_html":"<html/>"}`)
	if err == nil {
		t.Fatalf("expected handler failure when CLI emits no JSON")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed, got: %v", err)
	}
}
