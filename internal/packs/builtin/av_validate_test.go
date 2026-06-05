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

// avValidateExec is a stub executor for av.validate tests. It replays
// canned ExecResults keyed on whether the cmd path is the script. We
// don't actually run av-validate.sh; the unit tests cover the pack's
// JSON parsing + demotion + strict-mode + artifact-store handling.
type avValidateExec struct {
	scriptStdout []byte
	scriptStderr []byte
	scriptExit   int
	scriptErr    error
	calls        []session.ExecRequest
}

func (e *avValidateExec) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	e.calls = append(e.calls, req)
	if e.scriptErr != nil {
		return session.ExecResult{}, e.scriptErr
	}
	if len(req.Cmd) > 0 && strings.HasSuffix(req.Cmd[0], "av-validate.sh") {
		return session.ExecResult{
			Stdout:   e.scriptStdout,
			Stderr:   e.scriptStderr,
			ExitCode: e.scriptExit,
		}, nil
	}
	// stdin-writes for fetched-artifact path (cat > /tmp/...) — succeed silently.
	return session.ExecResult{}, nil
}

// runAVValidate invokes the handler directly. exec routes script
// invocations to the stubbed JSON output.
func runAVValidate(t *testing.T, exec *avValidateExec, input string) (json.RawMessage, error) {
	t.Helper()
	pack := AVValidate()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: packs.NewMemoryArtifactStore(),
		Session:   &session.Session{ID: "sess-av-validate"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec.fn,
	}
	return pack.Handler(context.Background(), ec)
}

// --- validation tests -----------------------------------------------------

func TestAVValidate_Validation_NoInputs(t *testing.T) {
	_, err := runAVValidate(t, &avValidateExec{}, `{}`)
	if err == nil {
		t.Fatal("expected error when no video/audio inputs supplied")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PackError, got %T", err)
	}
	if pe.Code != packs.CodeInvalidInput {
		t.Errorf("code = %s, want CodeInvalidInput", pe.Code)
	}
}

// --- shape passthrough ----------------------------------------------------

const scriptHappyJSON = `{
  "video_path": "/tmp/probe-video.mp4",
  "audio_path": null,
  "captions_path": null,
  "checks": [
    {"name": "mp4:faststart", "severity": "fail", "pass": true, "detail": "moov@36 mdat@568069"},
    {"name": "mp4:codec_pin", "severity": "fail", "pass": true, "detail": "h264 aac 44100"},
    {"name": "audio:rms_sweep", "severity": "fail", "pass": true, "detail": "min RMS -26 dB"},
    {"name": "audio:loudness_lufs", "severity": "warn", "pass": false, "detail": "-24 LUFS outside -14±2"}
  ],
  "passed": 3, "failed": 0, "warnings": 1, "all_passed": true
}`

func TestAVValidate_HappyPath_ParsesAndPersists(t *testing.T) {
	exec := &avValidateExec{scriptStdout: []byte(scriptHappyJSON), scriptExit: 0}
	raw, err := runAVValidate(t, exec, `{"video_path":"/tmp/probe-video.mp4"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Validation struct {
			Checks    []map[string]any `json:"checks"`
			Passed    int              `json:"passed"`
			Failed    int              `json:"failed"`
			Warnings  int              `json:"warnings"`
			AllPassed bool             `json:"all_passed"`
		} `json:"validation"`
		ValidationArtifactKey string `json:"validation_artifact_key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Validation.Checks) != 4 {
		t.Errorf("expected 4 checks, got %d", len(out.Validation.Checks))
	}
	if out.Validation.Passed != 3 || out.Validation.Warnings != 1 || out.Validation.Failed != 0 {
		t.Errorf("counters: passed=%d warnings=%d failed=%d, want 3/1/0",
			out.Validation.Passed, out.Validation.Warnings, out.Validation.Failed)
	}
	if !out.Validation.AllPassed {
		t.Errorf("all_passed should be true (no fail-severity failures)")
	}
	if out.ValidationArtifactKey == "" {
		t.Errorf("validation_artifact_key empty — sidecar artifact must persist")
	}
}

// --- known-issue demotion (the #429 path) --------------------------------

const script429JSON = `{
  "video_path": "/tmp/v.mp4",
  "audio_path": null,
  "captions_path": null,
  "checks": [
    {"name": "mp4:faststart", "severity": "fail", "pass": true, "detail": "ok"},
    {"name": "consistency:audio_video_duration", "severity": "fail", "pass": false,
     "detail": "container=693s audio_content=665s delta=27.9s exceeds 1s tolerance"}
  ],
  "passed": 1, "failed": 1, "warnings": 0, "all_passed": false
}`

// TestAVValidate_NoDemotionsInForce — knownIssueDemotions is empty
// after the #429 fix landed in the same PR as the av-validate.sh
// apad swap. A fail-severity check now surfaces at its natural
// severity (fail), not demoted to warn. This guards against
// accidentally re-adding a demotion entry without filing the
// tracking issue, AND against the demotion mechanism breaking
// silently in a way that lets known bugs through.
func TestAVValidate_NoDemotionsInForce(t *testing.T) {
	exec := &avValidateExec{scriptStdout: []byte(script429JSON), scriptExit: 1}
	raw, err := runAVValidate(t, exec, `{"video_path":"/tmp/v.mp4"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Validation struct {
			Checks    []map[string]any `json:"checks"`
			Failed    int              `json:"failed"`
			Warnings  int              `json:"warnings"`
			AllPassed bool             `json:"all_passed"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Validation.AllPassed {
		t.Errorf("all_passed should be FALSE — no demotion is active, the consistency check should land as fail")
	}
	if out.Validation.Failed != 1 {
		t.Errorf("failed should be 1 (natural severity preserved), got %d", out.Validation.Failed)
	}
	// The consistency check's natural severity is `fail`. Confirm it
	// did NOT get demoted to `warn` AND the detail string does NOT
	// carry a stale #429 reference appended by the demotion path.
	for _, c := range out.Validation.Checks {
		if c["name"] == "consistency:audio_video_duration" {
			if c["severity"] != "fail" {
				t.Errorf("severity should be fail (natural); got %v", c["severity"])
			}
			if strings.Contains(c["detail"].(string), "known issue") {
				t.Errorf("detail should NOT carry a known-issue ref after #429 was fixed; got %q", c["detail"])
			}
		}
	}
}

// --- strict mode -----------------------------------------------------------

const scriptBitstreamFailJSON = `{
  "video_path": "/tmp/v.mp4",
  "audio_path": null,
  "captions_path": null,
  "checks": [
    {"name": "mp4:bitstream_decode", "severity": "fail", "pass": false, "detail": "macroblock corruption"}
  ],
  "passed": 0, "failed": 1, "warnings": 0, "all_passed": false
}`

func TestAVValidate_StrictMode_FailSurfaces(t *testing.T) {
	exec := &avValidateExec{scriptStdout: []byte(scriptBitstreamFailJSON), scriptExit: 1}
	_, err := runAVValidate(t, exec, `{"video_path":"/tmp/v.mp4","strict":true}`)
	if err == nil {
		t.Fatal("strict mode should surface fail-severity failures as a typed error")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PackError, got %T", err)
	}
	if pe.Code != packs.CodeArtifactFailed {
		t.Errorf("code = %s, want CodeArtifactFailed", pe.Code)
	}
	if !strings.Contains(pe.Message, "mp4:bitstream_decode") {
		t.Errorf("strict error should name the failing check; got %q", pe.Message)
	}
}

func TestAVValidate_SoftSurface_NoErrorByDefault(t *testing.T) {
	// Same input as strict, but without strict:true. Pack should
	// succeed (the findings ARE the output).
	exec := &avValidateExec{scriptStdout: []byte(scriptBitstreamFailJSON), scriptExit: 1}
	raw, err := runAVValidate(t, exec, `{"video_path":"/tmp/v.mp4"}`)
	if err != nil {
		t.Fatalf("soft surface: pack should NOT fail on check failures (got %v)", err)
	}
	var out struct {
		Validation struct {
			Failed    int  `json:"failed"`
			AllPassed bool `json:"all_passed"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Validation.Failed != 1 || out.Validation.AllPassed {
		t.Errorf("findings should surface: failed=%d all_passed=%v", out.Validation.Failed, out.Validation.AllPassed)
	}
}

// --- runtime-error vs check-finding distinction --------------------------

func TestAVValidate_ScriptExit2_SurfacesAsRuntimeError(t *testing.T) {
	// Exit 2 = usage error or missing dependency. That's a real
	// runtime failure (the validation didn't run), distinct from
	// "validation ran and reported issues" (any non-2 exit with
	// parseable JSON).
	exec := &avValidateExec{
		scriptStderr: []byte("ffprobe: command not found"),
		scriptExit:   2,
	}
	_, err := runAVValidate(t, exec, `{"video_path":"/tmp/v.mp4"}`)
	if err == nil {
		t.Fatal("exit 2 should surface as a typed error")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PackError, got %T", err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("code = %s, want CodeHandlerFailed", pe.Code)
	}
	if !strings.Contains(pe.Message, "ffprobe") {
		t.Errorf("error should surface the stderr; got %q", pe.Message)
	}
}

// --- argv construction ----------------------------------------------------

func TestAVValidate_ArgvWiring(t *testing.T) {
	exec := &avValidateExec{scriptStdout: []byte(scriptHappyJSON), scriptExit: 0}
	_, err := runAVValidate(t, exec, `{
		"video_path": "/tmp/v.mp4",
		"audio_path": "/tmp/a.mp3",
		"captions_path": "/tmp/c.srt",
		"ebur128_target": -16,
		"skip_checks": "video:black_runs,audio:silence_runs"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	// Find the script invocation in exec.calls.
	var argv []string
	for _, c := range exec.calls {
		if len(c.Cmd) > 0 && strings.HasSuffix(c.Cmd[0], "av-validate.sh") {
			argv = c.Cmd
			break
		}
	}
	if argv == nil {
		t.Fatal("script invocation not found in exec.calls")
	}
	want := []string{"--video", "/tmp/v.mp4", "--audio", "/tmp/a.mp3", "--captions", "/tmp/c.srt",
		"--ebur128-target", "-16", "--skip-checks", "video:black_runs,audio:silence_runs"}
	for _, w := range want {
		found := false
		for _, a := range argv {
			if a == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected argv to contain %q; got %v", w, argv)
		}
	}
	// --json is mandatory.
	jsonFlag := false
	for _, a := range argv {
		if a == "--json" {
			jsonFlag = true
			break
		}
	}
	if !jsonFlag {
		t.Errorf("--json flag must always be passed; got %v", argv)
	}
}
