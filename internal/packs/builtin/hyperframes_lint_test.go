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

// hyperframesLintExecScript captures every ec.Exec call and returns
// scripted responses keyed on the command shape. Mirrors the pattern
// in hyperframes_render_test.go's hyperframesExecScript.
type hyperframesLintExecScript struct {
	calls      []session.ExecRequest
	lintStdout []byte // scripted stdout for the `hyperframes lint --json` call
	lintExit   int    // scripted exit code for the lint call
	lintStderr []byte
}

func (h *hyperframesLintExecScript) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	h.calls = append(h.calls, req)
	// Project-dir setup (shared with hyperframes.render).
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.Contains(req.Cmd[2], "mkdir -p") {
		return session.ExecResult{}, nil
	}
	// stdin write of composition_html or tarball.
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
	// `hyperframes lint <dir> --json [--verbose]`
	if len(req.Cmd) >= 2 && req.Cmd[0] == "hyperframes" && req.Cmd[1] == "lint" {
		return session.ExecResult{
			Stdout:   h.lintStdout,
			Stderr:   h.lintStderr,
			ExitCode: h.lintExit,
		}, nil
	}
	return session.ExecResult{}, nil
}

func (h *hyperframesLintExecScript) lintCmdArgs() []string {
	for _, c := range h.calls {
		if len(c.Cmd) >= 2 && c.Cmd[0] == "hyperframes" && c.Cmd[1] == "lint" {
			return c.Cmd
		}
	}
	return nil
}

// runLint invokes the handler with a hand-built ExecutionContext.
func runLint(t *testing.T, exec *hyperframesLintExecScript, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesLint()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-hyperframes-lint"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec.fn,
		Artifacts: packs.NewMemoryArtifactStore(),
	}
	return pack.Handler(context.Background(), ec)
}

// cleanLintJSON is a minimal valid upstream lint JSON output — no
// findings, ok=true. Used by tests that don't care about the
// findings shape, only the input-validation / argv behavior.
var cleanLintJSON = []byte(`{"ok":true,"errorCount":0,"warningCount":0,"infoCount":0,"findings":[],"filesScanned":2}`)

// erroredLintJSON mirrors the real upstream output for a scaffold with
// the media_missing_id error our PR #546 attach_audio injected (before
// the id-fix in the same branch). Used to test strict-mode behavior.
var erroredLintJSON = []byte(`{
  "ok": false,
  "errorCount": 1,
  "warningCount": 2,
  "infoCount": 0,
  "filesScanned": 2,
  "findings": [
    {
      "code": "media_missing_id",
      "severity": "error",
      "message": "<audio> has data-start but no id attribute. The renderer requires id to discover media elements — this audio will be SILENT in renders.",
      "fixHint": "Add a unique id attribute: <audio id=\"my-audio\" ...>",
      "snippet": "<audio src=\"assets/aroll-audio.mp3\" data-start=\"0\">",
      "file": "/tmp/project/index.html"
    },
    {
      "code": "google_fonts_import",
      "severity": "warning",
      "message": "Composition loads fonts from fonts.googleapis.com.",
      "file": "/tmp/project/index.html"
    },
    {
      "code": "composition_self_attribute_selector",
      "severity": "warning",
      "message": "Selector matches the block's own id.",
      "file": "/tmp/project/compositions/decision_tree.html"
    }
  ]
}`)

// --- input validation -----------------------------------------------------

func TestHyperframesLint_MissingBothInputs_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: cleanLintJSON}
	_, err := runLint(t, exec, `{}`)
	if err == nil {
		t.Fatalf("expected error for missing both inputs, got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got: %v", err)
	}
}

func TestHyperframesLint_BothInputs_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: cleanLintJSON}
	_, err := runLint(t, exec, `{"composition_html":"<html/>","project_artifact_key":"x"}`)
	if err == nil {
		t.Fatalf("expected error for mutually-exclusive inputs, got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got: %v", err)
	}
}

// --- happy path -----------------------------------------------------------

func TestHyperframesLint_CleanComposition_ReturnsStructuredOutput(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: cleanLintJSON}
	raw, err := runLint(t, exec, `{"composition_html":"<html><body><div data-composition-id=\"main\"></div></body></html>"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Lint struct {
			OK           bool `json:"ok"`
			ErrorCount   int  `json:"error_count"`
			WarningCount int  `json:"warning_count"`
			InfoCount    int  `json:"info_count"`
			FilesScanned int  `json:"files_scanned"`
		} `json:"lint"`
		LintArtifactKey string `json:"lint_artifact_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Lint.OK {
		t.Errorf("clean composition should have ok=true, got %+v", out.Lint)
	}
	if out.Lint.ErrorCount != 0 || out.Lint.WarningCount != 0 || out.Lint.FilesScanned != 2 {
		t.Errorf("unexpected counts: %+v", out.Lint)
	}
	if out.LintArtifactKey == "" {
		t.Errorf("expected lint_artifact_key sidecar populated, got empty")
	}
}

func TestHyperframesLint_FlaggedComposition_SurfacesFindings(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: erroredLintJSON}
	raw, err := runLint(t, exec, `{"composition_html":"<html><body><div data-composition-id=\"main\"></div></body></html>"}`)
	if err != nil {
		t.Fatalf("default mode should NOT error even with error-severity findings; got: %v", err)
	}
	var out struct {
		Lint struct {
			OK           bool                     `json:"ok"`
			ErrorCount   int                      `json:"error_count"`
			WarningCount int                      `json:"warning_count"`
			Findings     []hyperframesLintFinding `json:"findings"`
		} `json:"lint"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Lint.OK {
		t.Errorf("ok should be false when findings have errors")
	}
	if out.Lint.ErrorCount != 1 || out.Lint.WarningCount != 2 {
		t.Errorf("unexpected counts: errors=%d warnings=%d", out.Lint.ErrorCount, out.Lint.WarningCount)
	}
	// Verify the finding shape preserves CLI keys.
	if len(out.Lint.Findings) != 3 {
		t.Fatalf("expected 3 findings, got %d", len(out.Lint.Findings))
	}
	first := out.Lint.Findings[0]
	if first.Code != "media_missing_id" || first.Severity != "error" {
		t.Errorf("first finding shape mismatch: %+v", first)
	}
	if !strings.Contains(first.Message, "SILENT in renders") {
		t.Errorf("finding message lost: %q", first.Message)
	}
}

// --- strict mode ----------------------------------------------------------

func TestHyperframesLint_StrictMode_ErrorSeverityFails(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: erroredLintJSON}
	_, err := runLint(t, exec, `{"composition_html":"<html/>","strict":true}`)
	if err == nil {
		t.Fatalf("strict mode should error on error-severity findings, got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeArtifactFailed {
		t.Fatalf("expected CodeArtifactFailed, got: %v", err)
	}
	if !strings.Contains(pe.Message, "media_missing_id") {
		t.Errorf("strict-mode error should name the first error code, got: %s", pe.Message)
	}
}

func TestHyperframesLint_StrictMode_CleanLintPasses(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: cleanLintJSON}
	_, err := runLint(t, exec, `{"composition_html":"<html/>","strict":true}`)
	if err != nil {
		t.Fatalf("strict mode with zero errors should succeed, got: %v", err)
	}
}

func TestHyperframesLint_StrictMode_WarningsOnlyPasses(t *testing.T) {
	// Build a report with warnings but no errors.
	warnOnly := []byte(`{"ok":true,"errorCount":0,"warningCount":2,"infoCount":0,"findings":[],"filesScanned":2}`)
	exec := &hyperframesLintExecScript{lintStdout: warnOnly}
	_, err := runLint(t, exec, `{"composition_html":"<html/>","strict":true}`)
	if err != nil {
		t.Fatalf("strict mode should NOT fail on warnings-only, got: %v", err)
	}
}

// --- CLI argv shape -------------------------------------------------------

func TestHyperframesLint_VerboseFlag_AppendsToArgv(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: cleanLintJSON}
	if _, err := runLint(t, exec, `{"composition_html":"<html/>","verbose":true}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	args := exec.lintCmdArgs()
	if args == nil {
		t.Fatalf("no `hyperframes lint` call captured")
	}
	found := false
	for _, a := range args {
		if a == "--verbose" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected --verbose flag, got argv: %v", args)
	}
}

func TestHyperframesLint_DefaultArgv_AlwaysJSON(t *testing.T) {
	exec := &hyperframesLintExecScript{lintStdout: cleanLintJSON}
	if _, err := runLint(t, exec, `{"composition_html":"<html/>"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	args := exec.lintCmdArgs()
	if args == nil {
		t.Fatalf("no `hyperframes lint` call captured")
	}
	foundJSON := false
	for _, a := range args {
		if a == "--json" {
			foundJSON = true
			break
		}
	}
	if !foundJSON {
		t.Errorf("expected --json flag always present, got argv: %v", args)
	}
}

// --- stripNonJSONPrefix ---------------------------------------------------

func TestStripNonJSONPrefix_RemovesTelemetryNotice(t *testing.T) {
	noisy := []byte(`Hyperframes collects anonymous usage data.
Disable: hyperframes telemetry disable

{"ok":true,"errorCount":0,"findings":[]}`)
	cleaned := stripNonJSONPrefix(noisy)
	if !strings.HasPrefix(string(cleaned), `{"ok":true`) {
		t.Errorf("expected stripped output to start with the JSON object, got: %q", string(cleaned))
	}
}

func TestStripNonJSONPrefix_HandlesJSONAtStart(t *testing.T) {
	clean := []byte(`{"ok":true,"errorCount":0,"findings":[]}`)
	out := stripNonJSONPrefix(clean)
	if string(out) != string(clean) {
		t.Errorf("expected pass-through when JSON starts at byte 0, got: %q", string(out))
	}
}

func TestStripNonJSONPrefix_NoJSONReturnsNil(t *testing.T) {
	out := stripNonJSONPrefix([]byte("no json here at all"))
	if out != nil {
		t.Errorf("expected nil when no JSON found, got: %q", string(out))
	}
}

// --- error paths ----------------------------------------------------------

func TestHyperframesLint_NoJSONOutput_HandlerFails(t *testing.T) {
	exec := &hyperframesLintExecScript{
		lintStdout: []byte("internal CLI error"),
		lintStderr: []byte("cannot read project"),
		lintExit:   1,
	}
	_, err := runLint(t, exec, `{"composition_html":"<html/>"}`)
	if err == nil {
		t.Fatalf("expected CodeHandlerFailed when CLI emits no JSON, got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed, got: %v", err)
	}
}

func TestHyperframesLint_MalformedJSON_HandlerFails(t *testing.T) {
	exec := &hyperframesLintExecScript{
		lintStdout: []byte(`{"ok":true,malformed`),
	}
	_, err := runLint(t, exec, `{"composition_html":"<html/>"}`)
	if err == nil {
		t.Fatalf("expected CodeHandlerFailed on malformed JSON, got nil")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed, got: %v", err)
	}
}
