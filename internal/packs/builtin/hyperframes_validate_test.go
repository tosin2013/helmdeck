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

type hyperframesValidateExecScript struct {
	calls          []session.ExecRequest
	validateStdout []byte
	validateStderr []byte
	validateExit   int
}

func (h *hyperframesValidateExecScript) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
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
	if len(req.Cmd) >= 2 && req.Cmd[0] == "hyperframes" && req.Cmd[1] == "validate" {
		return session.ExecResult{
			Stdout:   h.validateStdout,
			Stderr:   h.validateStderr,
			ExitCode: h.validateExit,
		}, nil
	}
	return session.ExecResult{}, nil
}

func (h *hyperframesValidateExecScript) validateCmdArgs() []string {
	for _, c := range h.calls {
		if len(c.Cmd) >= 2 && c.Cmd[0] == "hyperframes" && c.Cmd[1] == "validate" {
			return c.Cmd
		}
	}
	return nil
}

func runValidate(t *testing.T, exec *hyperframesValidateExecScript, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesValidate()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-hf-validate"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec.fn,
		Artifacts: packs.NewMemoryArtifactStore(),
	}
	return pack.Handler(context.Background(), ec)
}

var cleanValidateJSON = []byte(`{"ok":true,"errors":[],"warnings":[],"contrast":[{"time":1.5,"selector":"h1","text":"Hello","ratio":19.75,"wcagAA":true,"large":true,"fg":"rgb(255,255,255)","bg":"rgb(10,10,15)"}]}`)

var flaggedValidateJSON = []byte(`{
  "ok": false,
  "errors": [
    {
      "level": "error",
      "text": "Access to video at 'https://example.com/x.mp4' from origin 'http://127.0.0.1:1234' has been blocked by CORS policy",
      "url": "http://127.0.0.1:1234/"
    },
    {
      "level": "error",
      "text": "Failed to load resource: net::ERR_FAILED",
      "url": "https://example.com/x.mp4"
    }
  ],
  "warnings": [
    {"level": "warning", "text": "deprecated API used"}
  ],
  "contrast": [
    {"time": 1.5, "selector": ".bad", "text": "low", "ratio": 2.1, "wcagAA": false, "large": false, "fg": "rgb(200,200,200)", "bg": "rgb(220,220,220)"},
    {"time": 1.5, "selector": ".good", "text": "high", "ratio": 18, "wcagAA": true, "large": true, "fg": "rgb(0,0,0)", "bg": "rgb(255,255,255)"}
  ]
}`)

// --- input validation -----------------------------------------------------

func TestHyperframesValidate_MissingBothInputs_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: cleanValidateJSON}
	_, err := runValidate(t, exec, `{}`)
	if err == nil {
		t.Fatalf("expected error for missing both inputs")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got: %v", err)
	}
}

func TestHyperframesValidate_BothInputs_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: cleanValidateJSON}
	_, err := runValidate(t, exec, `{"composition_html":"<html/>","project_artifact_key":"x"}`)
	if err == nil {
		t.Fatalf("expected error for mutually-exclusive inputs")
	}
}

// --- happy path -----------------------------------------------------------

func TestHyperframesValidate_CleanComposition_ReturnsStructuredOutput(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: cleanValidateJSON}
	raw, err := runValidate(t, exec, `{"composition_html":"<html/>"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Validate struct {
			OK                   bool `json:"ok"`
			ErrorCount           int  `json:"error_count"`
			WarningCount         int  `json:"warning_count"`
			ContrastSampleCount  int  `json:"contrast_sample_count"`
			ContrastFailureCount int  `json:"contrast_failure_count"`
		} `json:"validate"`
		ValidateArtifactKey string `json:"validate_artifact_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Validate.OK || out.Validate.ErrorCount != 0 || out.Validate.ContrastSampleCount != 1 {
		t.Errorf("unexpected report: %+v", out.Validate)
	}
	if out.Validate.ContrastFailureCount != 0 {
		t.Errorf("clean fixture has wcagAA=true; expected zero contrast failures, got %d", out.Validate.ContrastFailureCount)
	}
	if out.ValidateArtifactKey == "" {
		t.Errorf("expected validate_artifact_key sidecar populated")
	}
}

func TestHyperframesValidate_FlaggedComposition_SurfacesAllDimensions(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: flaggedValidateJSON}
	raw, err := runValidate(t, exec, `{"composition_html":"<html/>"}`)
	if err != nil {
		t.Fatalf("default mode should NOT error even with console errors; got: %v", err)
	}
	var out struct {
		Validate struct {
			OK                   bool                                `json:"ok"`
			ErrorCount           int                                 `json:"error_count"`
			WarningCount         int                                 `json:"warning_count"`
			ContrastSampleCount  int                                 `json:"contrast_sample_count"`
			ContrastFailureCount int                                 `json:"contrast_failure_count"`
			Errors               []hyperframesValidateConsoleEntry   `json:"errors"`
			Contrast             []hyperframesValidateContrastEntry  `json:"contrast"`
		} `json:"validate"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Validate.OK {
		t.Errorf("ok should be false")
	}
	if out.Validate.ErrorCount != 2 || out.Validate.WarningCount != 1 {
		t.Errorf("unexpected counts: errors=%d warnings=%d", out.Validate.ErrorCount, out.Validate.WarningCount)
	}
	if out.Validate.ContrastSampleCount != 2 || out.Validate.ContrastFailureCount != 1 {
		t.Errorf("expected 2 contrast rows / 1 failure; got %d / %d",
			out.Validate.ContrastSampleCount, out.Validate.ContrastFailureCount)
	}
	if !strings.Contains(out.Validate.Errors[0].Text, "CORS policy") {
		t.Errorf("first error text mismatch: %q", out.Validate.Errors[0].Text)
	}
}

// --- strict mode ----------------------------------------------------------

func TestHyperframesValidate_StrictMode_ConsoleErrorsFails(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: flaggedValidateJSON}
	_, err := runValidate(t, exec, `{"composition_html":"<html/>","strict":true}`)
	if err == nil {
		t.Fatalf("strict mode should error on console errors")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeArtifactFailed {
		t.Fatalf("expected CodeArtifactFailed, got: %v", err)
	}
	if !strings.Contains(pe.Message, "CORS") {
		t.Errorf("strict error should include first console error text: %s", pe.Message)
	}
}

func TestHyperframesValidate_StrictMode_ContrastFailuresAlonePass(t *testing.T) {
	// Strict mode is about console errors, not contrast. A clean
	// console with contrast failures should still pass strict.
	contrastOnlyFail := []byte(`{"ok":true,"errors":[],"warnings":[],"contrast":[{"time":1.5,"selector":".bad","ratio":2.1,"wcagAA":false}]}`)
	exec := &hyperframesValidateExecScript{validateStdout: contrastOnlyFail}
	_, err := runValidate(t, exec, `{"composition_html":"<html/>","strict":true}`)
	if err != nil {
		t.Fatalf("strict mode should NOT fail on contrast-only failures, got: %v", err)
	}
}

// --- CLI argv shape -------------------------------------------------------

func TestHyperframesValidate_NoContrast_AppendsFlag(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: cleanValidateJSON}
	if _, err := runValidate(t, exec, `{"composition_html":"<html/>","contrast":false}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	args := exec.validateCmdArgs()
	found := false
	for _, a := range args {
		if a == "--no-contrast" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --no-contrast flag, got argv: %v", args)
	}
}

func TestHyperframesValidate_DefaultContrast_NoFlag(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: cleanValidateJSON}
	// Don't pass contrast at all → CLI default (true) → no flag added.
	if _, err := runValidate(t, exec, `{"composition_html":"<html/>"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	args := exec.validateCmdArgs()
	for _, a := range args {
		if a == "--no-contrast" || a == "--contrast" {
			t.Errorf("expected no contrast flag when input omits it, got argv: %v", args)
		}
	}
}

func TestHyperframesValidate_ContrastTrueExplicit_NoFlag(t *testing.T) {
	exec := &hyperframesValidateExecScript{validateStdout: cleanValidateJSON}
	if _, err := runValidate(t, exec, `{"composition_html":"<html/>","contrast":true}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	args := exec.validateCmdArgs()
	for _, a := range args {
		if a == "--no-contrast" {
			t.Errorf("explicit contrast:true should NOT append --no-contrast, got argv: %v", args)
		}
	}
}

// --- error paths ----------------------------------------------------------

func TestHyperframesValidate_NoJSONOutput_HandlerFails(t *testing.T) {
	exec := &hyperframesValidateExecScript{
		validateStdout: []byte("internal error"),
		validateStderr: []byte("cannot launch chrome"),
		validateExit:   1,
	}
	_, err := runValidate(t, exec, `{"composition_html":"<html/>"}`)
	if err == nil {
		t.Fatalf("expected handler failure when CLI emits no JSON")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed, got: %v", err)
	}
}
