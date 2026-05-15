// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_render.go (#200) — HTML/CSS/JS composition → deterministic
// MP4 via Chromium BeginFrame + ffmpeg inside the helmdeck-sidecar-
// hyperframes image.
//
// Pack name: hyperframes.render
//
// Two body modes are supported with no handler branching:
//   - Silent animation: composition_html has no <audio> tag → MP4
//     is video-only.
//   - Pre-mixed audio: composition_html contains an inline <audio src=…>
//     → MP4 carries the audio track. Use this for chained
//     `podcast.generate` → `hyperframes.render` workflows: the podcast
//     pack returns a presigned URL, the agent embeds it as the audio
//     src, and the render pipeline picks it up automatically.
//
// Sizing surface: `resolution` × `aspect_ratio`. The pack maps each
// combination to one of the upstream hyperframes CLI's resolution
// presets (the CLI only accepts named presets, not free-form width/
// height pairs). Compositions MUST be authored at the matching
// aspect ratio in their own CSS — the CLI's --resolution flag is an
// integer-multiple upscale knob, not a dimension setter; mismatches
// surface as a CLI-level error.
//
// Resolution × aspect_ratio mapping to upstream --resolution preset:
//
//   1080p + 16:9 →  landscape      (1920x1080, YouTube)
//   1080p + 9:16 →  portrait       (1080x1920, Shorts/TikTok/Reels)
//   1080p + 1:1  →  square         (1080x1080, Instagram feed)
//   4k    + 16:9 →  landscape-4k   (3840x2160)
//   4k    + 9:16 →  portrait-4k    (2160x3840)
//   4k    + 1:1  →  square-4k      (2160x2160)
//
// Scope: short-form only (≤12 min @ 1080p, 512 MiB artifact cap).
// Long-form streaming defers to a v1.x track (#201).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

const (
	hyperframesMaxVideoSize  = 512 << 20 // 512 MiB artifact cap
	hyperframesDefaultFPS    = 30
	hyperframesDefaultPreset = "high" // CLI accepts: draft | standard | high
	hyperframesProjectDir    = "/tmp/helmdeck-hf"
	hyperframesOutputPath    = "/tmp/helmdeck-hf-out.mp4"
)

// HyperframesRender constructs the pack. Sidecar image override path:
// HELMDECK_SIDECAR_HYPERFRAMES, same convention as the python.run /
// node.run packs.
func HyperframesRender() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.render",
		Version: "v1",
		Description: "Render an HTML/CSS/JS composition into a deterministic MP4 via Chromium BeginFrame + ffmpeg. Sizing is composable: pick a resolution (1080p or 4k) and an aspect_ratio (16:9 standard, 9:16 vertical for Shorts/TikTok, 1:1 square). Silent animations and pre-mixed audio compositions work without a separate code path; chain podcast.generate → hyperframes.render by embedding the podcast's presigned audio URL in your composition's <audio src>. Short-form only (≤12 min, 512 MiB cap).",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"composition_html"},
			Properties: map[string]string{
				"composition_html": "string",
				"resolution":       "string",
				"aspect_ratio":     "string",
				"fps":              "number",
				"quality":          "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"video_artifact_key", "video_size", "width", "height", "fps"},
			Properties: map[string]string{
				"video_artifact_key": "string",
				"video_size":         "number",
				"width":              "number",
				"height":             "number",
				"fps":                "number",
				"aspect_ratio_used":  "string",
				"resolution_used":    "string",
				"cli_preset_used":    "string",
			},
		},
		Handler: hyperframesRenderHandler,
		// Heavy: BeginFrame capture + ffmpeg encode for short-form
		// compositions can run 30s-5min depending on duration, fps,
		// and quality. Async routes the MCP call through the SEP-1686
		// task envelope so the JSON-RPC request returns immediately.
		Async: true,
		// SessionSpec — image pinned to hyperframes sidecar (env override
		// HELMDECK_SIDECAR_HYPERFRAMES). Memory: BeginFrame + Chromium
		// + ffmpeg encode peaks around 3 GiB at 1080p; 4g gives ~30%
		// headroom. Timeout: 60 minutes covers the worst-case 12-minute
		// 4k composition with conservative encode settings; agents that
		// want stricter limits can pass shorter timeouts via the
		// session spec override.
		SessionSpec: session.Spec{
			Image:       hyperframesSidecarImage(),
			MemoryLimit: "4g",
			Timeout:     60 * time.Minute,
		},
	}
}

type hyperframesRenderInput struct {
	CompositionHTML string `json:"composition_html"`
	Resolution      string `json:"resolution"`
	AspectRatio     string `json:"aspect_ratio"`
	FPS             int    `json:"fps"`
	Quality         string `json:"quality"`
}

// resolutionPresetKey is the lookup tuple for mapping
// (resolution × aspect_ratio) → upstream CLI preset name + concrete
// dimensions. Tuples not in the table reject as invalid_input — the
// upstream hyperframes CLI only accepts a closed set of presets, and
// helmdeck deliberately keeps its own input surface aligned with what
// the CLI can actually honor today.
type resolutionPresetKey struct {
	Resolution  string
	AspectRatio string
}

type resolutionPresetValue struct {
	CLIPreset string
	Width     int
	Height    int
}

// hyperframesResolutionMatrix maps each supported (resolution × aspect_ratio)
// combination to the upstream CLI --resolution preset name and the
// resolved pixel dimensions. Compositions must be authored at the
// matching aspect ratio; the CLI rejects mismatches at render time.
//
// What's NOT here (and why):
//   - 720p — upstream CLI has no 720p preset. Compositions can author
//     natively at 720p without --resolution but the pack-side input
//     surface doesn't expose it for v0.13.0; revisit when upstream
//     adds it.
//   - 4:5 portrait — upstream CLI has no 4:5 preset. Same story.
var hyperframesResolutionMatrix = map[resolutionPresetKey]resolutionPresetValue{
	{"1080p", "16:9"}: {"landscape", 1920, 1080},
	{"1080p", "9:16"}: {"portrait", 1080, 1920},
	{"1080p", "1:1"}:  {"square", 1080, 1080},
	{"4k", "16:9"}:    {"landscape-4k", 3840, 2160},
	{"4k", "9:16"}:    {"portrait-4k", 2160, 3840},
	{"4k", "1:1"}:     {"square-4k", 2160, 2160},
}

// resolvePreset maps (resolution, aspect_ratio) to the upstream CLI
// preset plus the concrete dimensions for the output response. Returns
// an error suitable for CodeInvalidInput when either input is unknown
// or the combination isn't in the matrix.
func resolvePreset(resolution, aspectRatio string) (resolutionPresetValue, error) {
	if resolution == "" {
		resolution = "1080p"
	}
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}
	v, ok := hyperframesResolutionMatrix[resolutionPresetKey{resolution, aspectRatio}]
	if !ok {
		return resolutionPresetValue{}, fmt.Errorf(
			"unsupported resolution=%q + aspect_ratio=%q. Supported combinations: "+
				"1080p+16:9 (YouTube), 1080p+9:16 (Shorts/TikTok), 1080p+1:1 (IG feed), "+
				"4k+16:9, 4k+9:16, 4k+1:1",
			resolution, aspectRatio)
	}
	return v, nil
}

func hyperframesRenderHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in hyperframesRenderInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if strings.TrimSpace(in.CompositionHTML) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "composition_html is required"}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "hyperframes.render requires a session executor"}
	}

	resolution := in.Resolution
	if resolution == "" {
		resolution = "1080p"
	}
	aspectRatio := in.AspectRatio
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}
	preset, err := resolvePreset(resolution, aspectRatio)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}

	fps := in.FPS
	if fps <= 0 {
		fps = hyperframesDefaultFPS
	}
	// Pack-side fps cap. The upstream CLI accepts up to 240; we cap
	// at 60 because higher frame rates roughly linearly increase
	// encode cost without an obvious win for the short-form/social
	// content this pack targets. Agents who specifically need 120fps
	// slow-mo can lift this later — file an issue.
	if fps > 60 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("fps %d exceeds cap of 60", fps)}
	}
	quality := in.Quality
	if quality == "" {
		quality = hyperframesDefaultPreset
	}
	// Mirror the upstream CLI's closed set so a typo doesn't make it
	// all the way to the subprocess before erroring.
	if quality != "draft" && quality != "standard" && quality != "high" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("quality %q invalid (allowed: draft, standard, high)", quality)}
	}

	// The hyperframes CLI expects a *project directory* (containing
	// index.html), not a bare HTML file. We write the composition
	// to <projectDir>/index.html and pass the directory as the
	// positional argument. The CLI's resolveProject() will discover
	// index.html as the default composition.
	ec.Report(0, "scaffolding hyperframes project")
	mkdirRes, err := ec.Exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", "mkdir -p " + hyperframesProjectDir},
	})
	if err != nil || mkdirRes.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("create project dir: %v (exit %d)", err, mkdirRes.ExitCode)}
	}
	if _, err := execWithStdin(ctx, ec, hyperframesProjectDir+"/index.html", []byte(in.CompositionHTML)); err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("write composition_html: %v", err), Cause: err}
	}

	// `hyperframes render <project-dir> --resolution <preset> --fps NN
	//  --quality {draft|standard|high} --output /tmp/<...>.mp4`.
	args := []string{
		"hyperframes", "render", hyperframesProjectDir,
		"--resolution", preset.CLIPreset,
		"--fps", fmt.Sprintf("%d", fps),
		"--quality", quality,
		"--output", hyperframesOutputPath,
	}

	ec.Report(10, fmt.Sprintf("rendering %dx%d @ %dfps (preset=%s)", preset.Width, preset.Height, fps, preset.CLIPreset))
	renderRes, err := ec.Exec(ctx, session.ExecRequest{Cmd: args})
	if err != nil || renderRes.ExitCode != 0 {
		stderr := strings.TrimSpace(string(renderRes.Stderr))
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("hyperframes render failed (exit %d): %s",
				renderRes.ExitCode, truncStr(stderr, 4096))}
	}

	ec.Report(90, "reading rendered MP4")
	catRes, err := ec.Exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", "cat " + hyperframesOutputPath},
	})
	if err != nil || catRes.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: "failed to read rendered video"}
	}
	videoBytes := catRes.Stdout
	if len(videoBytes) == 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: "hyperframes produced an empty MP4 (encode silently failed)"}
	}
	// 512 MiB cap (per #200). Enforced BEFORE Put so we don't blow
	// the artifact-store-uploader's buffer on a runaway composition;
	// agents that need longer/larger get pointed at #201 (v1.x
	// streaming track) by the error message.
	if len(videoBytes) > hyperframesMaxVideoSize {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf(
				"rendered video exceeds %d MiB cap (%d bytes). Short-form only in v0.13.0; long-form streaming tracked in #201.",
				hyperframesMaxVideoSize>>20, len(videoBytes))}
	}

	ec.Report(95, "uploading video artifact")
	art, putErr := ec.Artifacts.Put(ctx, "hyperframes.render", "video.mp4", videoBytes, "video/mp4")
	if putErr != nil {
		return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
			Message: fmt.Sprintf("upload video: %v", putErr), Cause: putErr}
	}

	out := map[string]any{
		"video_artifact_key": art.Key,
		"video_size":         len(videoBytes),
		"width":              preset.Width,
		"height":             preset.Height,
		"fps":                fps,
		"aspect_ratio_used":  aspectRatio,
		"resolution_used":    resolution,
		"cli_preset_used":    preset.CLIPreset,
	}
	raw, mErr := json.Marshal(out)
	if mErr != nil {
		return nil, &packs.PackError{Code: packs.CodeInternal, Message: mErr.Error(), Cause: mErr}
	}
	return raw, nil
}

// hyperframesSidecarImage returns the image tag the pack pins via
// SessionSpec.Image. Defaults to the published GHCR image; operators
// override by setting HELMDECK_SIDECAR_HYPERFRAMES (same convention
// as HELMDECK_SIDECAR_PYTHON / HELMDECK_SIDECAR_NODE).
func hyperframesSidecarImage() string {
	if v := os.Getenv("HELMDECK_SIDECAR_HYPERFRAMES"); v != "" {
		return v
	}
	return "ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:latest"
}
