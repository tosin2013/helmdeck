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
// Sizing surface: `resolution` (the shorter side, in pixels) × `aspect_ratio`
// (16:9 landscape, 9:16 vertical for Shorts/TikTok/Reels, 1:1 square for
// Instagram feed, 4:5 portrait for Instagram feed-portrait). Resolves
// to a width × height pair fed to the hyperframes-cli viewport AND
// ffmpeg's scale filter so the composition renders pixel-perfect at
// the chosen output size.
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
	hyperframesDefaultPreset = "high"
)

// HyperframesRender constructs the pack. Sidecar image override path:
// HELMDECK_SIDECAR_HYPERFRAMES, same convention as the python.run /
// node.run packs.
func HyperframesRender() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.render",
		Version: "v1",
		Description: "Render an HTML/CSS/JS composition into a deterministic MP4 via Chromium BeginFrame + ffmpeg. Sizing is composable: pick a resolution (720p/1080p/4k) and an aspect_ratio (16:9 standard, 9:16 vertical for Shorts/TikTok, 1:1 square, 4:5 portrait). Silent animations and pre-mixed audio compositions work without a separate code path; chain podcast.generate → hyperframes.render by embedding the podcast's presigned audio URL in your composition's <audio src>. Short-form only (≤12 min, 512 MiB cap).",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"composition_html"},
			Properties: map[string]string{
				"composition_html": "string",
				"resolution":       "string",
				"aspect_ratio":     "string",
				"fps":              "number",
				"quality":          "string",
				"duration_s":       "number",
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
				"duration_s":         "number",
				"aspect_ratio_used":  "string",
				"resolution_used":    "string",
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
	CompositionHTML string  `json:"composition_html"`
	Resolution      string  `json:"resolution"`
	AspectRatio     string  `json:"aspect_ratio"`
	FPS             int     `json:"fps"`
	Quality         string  `json:"quality"`
	DurationS       float64 `json:"duration_s"`
}

// shorterSidePixels maps the resolution preset to a pixel count for
// the shorter dimension. The longer dimension is computed from the
// aspect ratio so a 1080p+9:16 composition becomes 1080×1920 (Shorts)
// and 1080p+16:9 becomes 1920×1080 (YouTube standard).
var shorterSidePixels = map[string]int{
	"720p":  720,
	"1080p": 1080,
	"4k":    2160,
}

// resolveDimensions converts (resolution, aspect_ratio) into a concrete
// width × height pair. Returns an error suitable for surfacing as a
// CodeInvalidInput PackError when either input is unknown.
//
// Convention: `resolution` controls the shorter dimension. For
// landscape (16:9), that's height; for portrait (9:16, 4:5), that's
// width; for square (1:1), both are equal. This matches what
// platform-aware operators expect — "1080p Shorts" means 1080×1920,
// not 1920×1080.
func resolveDimensions(resolution, aspectRatio string) (width, height int, err error) {
	if resolution == "" {
		resolution = "1080p"
	}
	short, ok := shorterSidePixels[resolution]
	if !ok {
		return 0, 0, fmt.Errorf("unknown resolution %q (allowed: 720p, 1080p, 4k)", resolution)
	}
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}
	switch aspectRatio {
	case "16:9":
		// Landscape: short side = height; width = height * 16/9.
		return short * 16 / 9, short, nil
	case "9:16":
		// Portrait (Shorts/TikTok/Reels): short side = width;
		// height = width * 16/9.
		return short, short * 16 / 9, nil
	case "1:1":
		// Square (Instagram feed): both sides equal.
		return short, short, nil
	case "4:5":
		// Portrait (Instagram feed-portrait): short side = width;
		// height = width * 5/4.
		return short, short * 5 / 4, nil
	default:
		return 0, 0, fmt.Errorf("unknown aspect_ratio %q (allowed: 16:9, 9:16, 1:1, 4:5)", aspectRatio)
	}
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
	width, height, dimErr := resolveDimensions(resolution, aspectRatio)
	if dimErr != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: dimErr.Error(), Cause: dimErr}
	}

	fps := in.FPS
	if fps <= 0 {
		fps = hyperframesDefaultFPS
	}
	if fps > 60 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("fps %d exceeds cap of 60", fps)}
	}
	quality := in.Quality
	if quality == "" {
		quality = hyperframesDefaultPreset
	}

	ec.Report(0, "writing composition to sidecar")
	if _, err := execWithStdin(ctx, ec, "/tmp/composition.html", []byte(in.CompositionHTML)); err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("write composition_html: %v", err), Cause: err}
	}

	// hyperframes-cli flags are passed positionally; --duration is
	// optional (when omitted, the producer infers from the composition's
	// own animation length via document.animationend hooks).
	args := []string{
		"hyperframes", "render",
		"--width", fmt.Sprintf("%d", width),
		"--height", fmt.Sprintf("%d", height),
		"--fps", fmt.Sprintf("%d", fps),
		"--quality", quality,
		"-o", "/tmp/video.mp4",
	}
	if in.DurationS > 0 {
		args = append(args, "--duration", fmt.Sprintf("%.3f", in.DurationS))
	}
	args = append(args, "/tmp/composition.html")

	ec.Report(10, fmt.Sprintf("rendering %dx%d @ %dfps", width, height, fps))
	renderRes, err := ec.Exec(ctx, session.ExecRequest{Cmd: args})
	if err != nil || renderRes.ExitCode != 0 {
		stderr := strings.TrimSpace(string(renderRes.Stderr))
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("hyperframes render failed (exit %d): %s",
				renderRes.ExitCode, truncStr(stderr, 4096))}
	}

	ec.Report(90, "reading rendered MP4")
	catRes, err := ec.Exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", "cat /tmp/video.mp4"},
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
		"width":              width,
		"height":             height,
		"fps":                fps,
		"aspect_ratio_used":  aspectRatio,
		"resolution_used":    resolution,
	}
	if in.DurationS > 0 {
		out["duration_s"] = in.DurationS
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
