// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// fakeHyperframesMP4 is the stub stdout returned by `cat /tmp/...`
// in the test executor. Real MP4s are ~1-10 MiB; tests just need a
// non-empty byte slice to validate the upload + size codepath.
var fakeHyperframesMP4 = []byte("fake-hyperframes-mp4-video-bytes")

// hyperframesExecScript is the recordingExecutor pattern: captures
// every ec.Exec call, returns scripted responses by inspecting the
// command shape, and lets tests assert on the exact CLI arguments
// the pack sent to the sidecar.
type hyperframesExecScript struct {
	calls          []session.ExecRequest
	failRender     bool
	emptyMP4       bool
	oversizeMP4    bool
	customMP4Bytes []byte
}

func (h *hyperframesExecScript) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	h.calls = append(h.calls, req)

	// `mkdir -p <project-dir>` scaffold step.
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.Contains(req.Cmd[2], "mkdir -p") {
		return session.ExecResult{}, nil
	}
	// stdin write: composition_html → <project-dir>/index.html
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.Contains(req.Cmd[2], "cat >") && len(req.Stdin) > 0 {
		return session.ExecResult{}, nil
	}
	// `hyperframes render ...` invocation
	if len(req.Cmd) >= 2 && req.Cmd[0] == "hyperframes" && req.Cmd[1] == "render" {
		if h.failRender {
			return session.ExecResult{ExitCode: 1, Stderr: []byte("simulated render failure")}, nil
		}
		return session.ExecResult{}, nil
	}
	// readback: `cat <output-path>`
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.Contains(req.Cmd[2], "cat /tmp/helmdeck-hf-out.mp4") {
		switch {
		case h.emptyMP4:
			return session.ExecResult{Stdout: nil}, nil
		case h.oversizeMP4:
			return session.ExecResult{Stdout: bytes.Repeat([]byte("X"), hyperframesMaxVideoSize+1)}, nil
		case h.customMP4Bytes != nil:
			return session.ExecResult{Stdout: h.customMP4Bytes}, nil
		default:
			return session.ExecResult{Stdout: fakeHyperframesMP4}, nil
		}
	}
	return session.ExecResult{}, nil
}

// renderCmdArgs returns the argv of the first `hyperframes render`
// invocation captured by the script, or nil if none was seen.
func (h *hyperframesExecScript) renderCmdArgs() []string {
	for _, c := range h.calls {
		if len(c.Cmd) >= 2 && c.Cmd[0] == "hyperframes" && c.Cmd[1] == "render" {
			return c.Cmd
		}
	}
	return nil
}

// runHyperframes calls the handler directly with a hand-built
// ExecutionContext (same pattern as runNarrate in slides_narrate_test.go).
func runHyperframes(t *testing.T, exec *hyperframesExecScript, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesRender()
	artifacts := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-hyperframes"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec.fn,
		Artifacts: artifacts,
	}
	return pack.Handler(context.Background(), ec)
}

// argValue returns the value following `--flag` in argv, or empty
// if the flag isn't present.
func argValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// --- input validation ----------------------------------------------------

func TestHyperframesRender_MissingComposition_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesExecScript{}
	_, err := runHyperframes(t, exec, `{}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
}

func TestHyperframesRender_UnknownResolution_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesExecScript{}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "resolution":"8k"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "8k") {
		t.Errorf("error message should mention the unknown value, got: %s", pe.Message)
	}
}

func TestHyperframesRender_UnknownAspectRatio_RejectsInvalidInput(t *testing.T) {
	exec := &hyperframesExecScript{}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "aspect_ratio":"21:9"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
}

// 720p compositions can be authored natively but the pack-side input
// surface doesn't expose 720p as a v0.13.0 value — upstream CLI has
// no 720p preset. Drop here ensures we don't silently accept a value
// the CLI will reject downstream.
func TestHyperframesRender_720p_RejectsAsUnsupportedTuple(t *testing.T) {
	exec := &hyperframesExecScript{}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "resolution":"720p", "aspect_ratio":"16:9"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for 720p (no CLI preset), got %v", err)
	}
}

// 4:5 portrait is requested by some agents (Instagram feed-portrait)
// but upstream CLI doesn't ship a 4:5 preset today. Reject loud so
// callers know to fall back to 1:1 or 9:16.
func TestHyperframesRender_4to5_RejectsAsUnsupportedTuple(t *testing.T) {
	exec := &hyperframesExecScript{}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "resolution":"1080p", "aspect_ratio":"4:5"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for 4:5 (no upstream preset), got %v", err)
	}
}

func TestHyperframesRender_FPSCapEnforced(t *testing.T) {
	exec := &hyperframesExecScript{}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "fps":120}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for fps>60, got %v", err)
	}
}

func TestHyperframesRender_InvalidQuality_Rejects(t *testing.T) {
	exec := &hyperframesExecScript{}
	// CLI accepts only draft|standard|high. "medium" looks plausible
	// but isn't valid upstream — catch it here before the subprocess.
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "quality":"medium"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for quality=medium, got %v", err)
	}
}

// --- resolution-preset matrix --------------------------------------------

func TestResolvePreset_AllSupportedTuples(t *testing.T) {
	cases := []struct {
		res, ar              string
		wantPreset           string
		wantWidth, wantHight int
	}{
		// Landscape (YouTube standard).
		{"1080p", "16:9", "landscape", 1920, 1080},
		{"4k", "16:9", "landscape-4k", 3840, 2160},
		// Portrait (Shorts/TikTok/Reels).
		{"1080p", "9:16", "portrait", 1080, 1920},
		{"4k", "9:16", "portrait-4k", 2160, 3840},
		// Square (Instagram feed).
		{"1080p", "1:1", "square", 1080, 1080},
		{"4k", "1:1", "square-4k", 2160, 2160},
	}
	for _, c := range cases {
		got, err := resolvePreset(c.res, c.ar)
		if err != nil {
			t.Errorf("%s + %s: unexpected error: %v", c.res, c.ar, err)
			continue
		}
		if got.CLIPreset != c.wantPreset {
			t.Errorf("%s + %s: CLI preset = %q, want %q", c.res, c.ar, got.CLIPreset, c.wantPreset)
		}
		if got.Width != c.wantWidth || got.Height != c.wantHight {
			t.Errorf("%s + %s: dims = %dx%d, want %dx%d",
				c.res, c.ar, got.Width, got.Height, c.wantWidth, c.wantHight)
		}
	}
}

func TestResolvePreset_Defaults(t *testing.T) {
	// Empty resolution → 1080p; empty aspect_ratio → 16:9.
	v, err := resolvePreset("", "")
	if err != nil {
		t.Fatal(err)
	}
	if v.CLIPreset != "landscape" || v.Width != 1920 || v.Height != 1080 {
		t.Errorf("defaults = %+v, want landscape 1920x1080", v)
	}
}

// --- happy paths --------------------------------------------------------

func TestHyperframesRender_DefaultSizing_LandsAsYouTubeStandard(t *testing.T) {
	exec := &hyperframesExecScript{}
	raw, err := runHyperframes(t, exec, `{"composition_html":"<html><body><h1>Hello</h1></body></html>"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Width          int    `json:"width"`
		Height         int    `json:"height"`
		FPS            int    `json:"fps"`
		AspectUsed     string `json:"aspect_ratio_used"`
		ResolutionUsed string `json:"resolution_used"`
		CLIPresetUsed  string `json:"cli_preset_used"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Width != 1920 || out.Height != 1080 {
		t.Errorf("got %dx%d, want 1920x1080 (default 1080p + 16:9)", out.Width, out.Height)
	}
	if out.FPS != 30 {
		t.Errorf("default fps = %d, want 30", out.FPS)
	}
	if out.AspectUsed != "16:9" || out.ResolutionUsed != "1080p" {
		t.Errorf("response should echo defaults: got aspect=%s resolution=%s", out.AspectUsed, out.ResolutionUsed)
	}
	if out.CLIPresetUsed != "landscape" {
		t.Errorf("cli_preset_used = %q, want 'landscape'", out.CLIPresetUsed)
	}

	// CLI received the right argv shape.
	argv := exec.renderCmdArgs()
	if argv == nil {
		t.Fatal("no `hyperframes render` invocation captured")
	}
	if r := argValue(argv, "--resolution"); r != "landscape" {
		t.Errorf("--resolution sent as %q, want landscape", r)
	}
	if q := argValue(argv, "--quality"); q != "high" {
		t.Errorf("--quality sent as %q, want high (pack default)", q)
	}
	if o := argValue(argv, "--output"); o != hyperframesOutputPath {
		t.Errorf("--output sent as %q, want %s", o, hyperframesOutputPath)
	}
	// Positional project-dir argument is at argv[2].
	if len(argv) < 3 || argv[2] != hyperframesProjectDir {
		t.Errorf("expected positional project-dir argv[2]=%s, got argv=%v", hyperframesProjectDir, argv)
	}
}

func TestHyperframesRender_VerticalShorts_MapsToPortraitPreset(t *testing.T) {
	exec := &hyperframesExecScript{}
	raw, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "resolution":"1080p", "aspect_ratio":"9:16"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Width         int    `json:"width"`
		Height        int    `json:"height"`
		CLIPresetUsed string `json:"cli_preset_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Width != 1080 || out.Height != 1920 {
		t.Errorf("Shorts/TikTok dims = %dx%d, want 1080x1920", out.Width, out.Height)
	}
	if out.CLIPresetUsed != "portrait" {
		t.Errorf("cli_preset_used = %q, want portrait", out.CLIPresetUsed)
	}
	argv := exec.renderCmdArgs()
	if r := argValue(argv, "--resolution"); r != "portrait" {
		t.Errorf("CLI got --resolution %q, want portrait", r)
	}
}

func TestHyperframesRender_4kSquare_MapsToSquare4kPreset(t *testing.T) {
	exec := &hyperframesExecScript{}
	raw, err := runHyperframes(t, exec, `{"composition_html":"<html></html>", "resolution":"4k", "aspect_ratio":"1:1"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Width, Height int
		CLIPresetUsed string `json:"cli_preset_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Width != 2160 || out.Height != 2160 {
		t.Errorf("4k square dims = %dx%d, want 2160x2160", out.Width, out.Height)
	}
	if out.CLIPresetUsed != "square-4k" {
		t.Errorf("cli_preset_used = %q, want square-4k", out.CLIPresetUsed)
	}
}

func TestHyperframesRender_FPSAndQuality_PassedToCLI(t *testing.T) {
	exec := &hyperframesExecScript{}
	_, err := runHyperframes(t, exec, `{
		"composition_html":"<html></html>",
		"fps": 60,
		"quality": "draft"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	argv := exec.renderCmdArgs()
	if f := argValue(argv, "--fps"); f != "60" {
		t.Errorf("--fps sent as %q, want 60", f)
	}
	if q := argValue(argv, "--quality"); q != "draft" {
		t.Errorf("--quality sent as %q, want draft", q)
	}
}

// --- output / artifact path ---------------------------------------------

func TestHyperframesRender_UploadsArtifact(t *testing.T) {
	exec := &hyperframesExecScript{}
	raw, err := runHyperframes(t, exec, `{"composition_html":"<html></html>"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		VideoArtifactKey string `json:"video_artifact_key"`
		VideoSize        int    `json:"video_size"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.VideoArtifactKey == "" {
		t.Error("video_artifact_key empty")
	}
	if out.VideoSize != len(fakeHyperframesMP4) {
		t.Errorf("video_size = %d, want %d", out.VideoSize, len(fakeHyperframesMP4))
	}
}

// --- failure modes ------------------------------------------------------

func TestHyperframesRender_RenderFailure_SurfacesStderr(t *testing.T) {
	exec := &hyperframesExecScript{failRender: true}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed, got %v", err)
	}
	if !strings.Contains(pe.Message, "simulated render failure") {
		t.Errorf("expected stderr propagation, got: %s", pe.Message)
	}
}

func TestHyperframesRender_EmptyMP4_RejectsAsHandlerFailed(t *testing.T) {
	exec := &hyperframesExecScript{emptyMP4: true}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed for empty MP4, got %v", err)
	}
}

// #200 acceptance: oversize rejection MUST happen BEFORE artifact upload
// so a runaway composition can't blow the artifact-store buffer.
func TestHyperframesRender_OversizeMP4_RejectedBeforeUpload(t *testing.T) {
	exec := &hyperframesExecScript{oversizeMP4: true}
	_, err := runHyperframes(t, exec, `{"composition_html":"<html></html>"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed for oversize, got %v", err)
	}
	if !strings.Contains(pe.Message, "512 MiB cap") {
		t.Errorf("error message should explain the cap, got: %s", pe.Message)
	}
	if !strings.Contains(pe.Message, "#201") {
		t.Errorf("error message should point at #201 for long-form, got: %s", pe.Message)
	}
}

// --- sidecar image override --------------------------------------------

func TestHyperframesSidecarImage_DefaultIsGHCR(t *testing.T) {
	t.Setenv("HELMDECK_SIDECAR_HYPERFRAMES", "")
	if got := hyperframesSidecarImage(); got != "ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:latest" {
		t.Errorf("default = %q, want the published GHCR tag", got)
	}
}

func TestHyperframesSidecarImage_EnvOverride(t *testing.T) {
	t.Setenv("HELMDECK_SIDECAR_HYPERFRAMES", "registry.example/custom-hf:v1")
	if got := hyperframesSidecarImage(); got != "registry.example/custom-hf:v1" {
		t.Errorf("env override not honored, got %q", got)
	}
}

// --- pack registration shape -------------------------------------------

func TestHyperframesRender_IsAsync(t *testing.T) {
	if !HyperframesRender().Async {
		t.Error("hyperframes.render must be Async (heavy work, async-task envelope)")
	}
}

func TestHyperframesRender_NeedsSession(t *testing.T) {
	p := HyperframesRender()
	if !p.NeedsSession {
		t.Error("hyperframes.render must declare NeedsSession (it runs in the hyperframes sidecar)")
	}
	if p.SessionSpec.Image == "" {
		t.Error("SessionSpec.Image must be set so the engine picks the right sidecar")
	}
}
