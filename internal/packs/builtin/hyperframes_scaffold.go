// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_scaffold.go (#503 Path B, PR 4) — first link in the
// scaffold-based video pipeline. Runs `hyperframes init --example=<x>`
// inside the helmdeck-sidecar-hyperframes container, packages the
// resulting project directory as a gzipped tarball, uploads it to the
// artifact store, and returns the artifact key + an editable-slot
// manifest.
//
// The output's `project_artifact_key` chains forward through the rest
// of the scaffold-mode video pipeline:
//   hyperframes.scaffold  → scaffolded but generic text/visuals
//   hyperframes.interpolate → LLM rewrites the visible text content
//   hyperframes.attach_asset → splices an A-roll image / video
//   hyperframes.render    → produces the final MP4
//
// Why a separate scaffold pack (vs. folding into compose): see
// CONTRIBUTING.md §"Prefer the upstream CLI" and issue #503's
// Option-C decision note. Compose's existing freeform mode (operator
// writes raw HTML, LLM generates BODY/STYLES/TIMELINE) stays untouched
// for callers who want full control; the scaffold family is the
// upstream-CLI-driven alternative for Tier C reliability + visual
// polish without authoring HTML from scratch.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

const (
	// hyperframesScaffoldOutputPath is the in-sidecar path the init
	// script writes the gzipped tarball to. Read back via `cat` into
	// res.Stdout, then uploaded to the artifact store. Stable path so
	// multiple invocations don't accumulate temp files; the script's
	// upstream cleanup handles removal between calls.
	hyperframesScaffoldOutputPath = "/tmp/helmdeck-hf-scaffold.tar.gz"
	// hyperframesScaffoldMaxEntries caps the tar enumeration to
	// protect against pathological archives. Real hyperframes
	// scaffolds are 10-30 files; 1024 is comfortable headroom without
	// allowing a runaway archive to lock up the manifest pass.
	hyperframesScaffoldMaxEntries = 1024
)

type hyperframesScaffoldInput struct {
	Example     string `json:"example"`
	Resolution  string `json:"resolution"`
	AspectRatio string `json:"aspect_ratio"`
}

// HyperframesScaffold constructs the pack. SessionSpec pins the
// helmdeck-sidecar-hyperframes image (which ships Node 22 +
// hyperframes@0.6.97 + the init script per PR #506 / #507).
//
// Sizing: scaffolding is I/O light — `npx hyperframes init` typically
// completes in 10-30s. 1 GiB and a 5-minute timeout cover the
// pathological case where the init step pulls a larger example with
// rich assets.
func HyperframesScaffold() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.scaffold",
		Version: "v1",
		Description: "Scaffold a hyperframes composition from one of the upstream framework's 140+ pre-built examples (`swiss-grid`, `decision-tree`, `code-snippet-dark-modern`, `nyt-graph`, `kinetic-type`, `vignelli`, `warm-grain`, `caption-pill-karaoke`, …). Runs `hyperframes init --example=<name>` inside the dedicated sidecar, packages the scaffolded project directory as a gzipped tarball, and uploads it to the artifact store. Pair with `hyperframes.interpolate` to fill in the visible text content, then `hyperframes.attach_asset` to splice in an A-roll image/video (from `image.generate` or `stock.search`), then `hyperframes.render` to produce the MP4. All four packs compose individually or via the `builtin.scaffolded-narrated-video` pipeline.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"example", "resolution", "aspect_ratio"},
			Produces:       []string{"project_artifact_key"},
			IntentKeywords: []string{"scaffold a video", "use a hyperframes example", "start from a template", "begin with swiss-grid", "begin with decision-tree"},
			TypicalUse:     "First step in a scaffolded video pipeline. Outputs a project_artifact_key that hyperframes.interpolate (text rewriting), hyperframes.attach_asset (A-roll image), and hyperframes.render (MP4) consume in sequence.",
			Limitations: []string{
				"requires an upstream example name — run with an invalid example to see the full registry list in the error message",
				"does not modify the scaffold's content (use hyperframes.interpolate for that)",
				"does not add an A-roll image (use hyperframes.attach_asset)",
			},
		},
		NeedsSession: true,
		Async:        true,
		SessionSpec: session.Spec{
			Image:       hyperframesSidecarImage(),
			MemoryLimit: "1g",
			Timeout:     5 * time.Minute,
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"example"},
			Properties: map[string]string{
				"example":      "string",
				"resolution":   "string",
				"aspect_ratio": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"project_artifact_key", "example_used", "cli_preset_used", "width", "height"},
			Properties: map[string]string{
				"project_artifact_key": "string",
				"example_used":         "string",
				"cli_preset_used":      "string",
				"width":                "number",
				"height":               "number",
				"aspect_ratio_used":    "string",
				"resolution_used":      "string",
				// editable_slots: manifest naming the files in the
				// scaffold that hyperframes.interpolate can rewrite.
				// Shape: { compositions: [{path, size}, ...], other_files: [...] }
				"editable_slots": "object",
			},
		},
		Handler: hyperframesScaffoldHandler,
	}
}

func hyperframesScaffoldHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in hyperframesScaffoldInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if strings.TrimSpace(in.Example) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "example is required (e.g. swiss-grid, decision-tree, code-snippet-dark-modern, kinetic-type). Run with an invalid name to see the full upstream registry list in the script's stderr."}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
			Message: "hyperframes.scaffold requires a session executor"}
	}
	if ec.Artifacts == nil {
		return nil, &packs.PackError{Code: packs.CodeInternal,
			Message: "hyperframes.scaffold requires an artifact store, but none is wired into the ExecutionContext"}
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

	ec.Report(10, "running hyperframes init --example="+in.Example)
	initRes, err := ec.Exec(ctx, session.ExecRequest{
		Cmd: []string{
			"/usr/local/bin/hyperframes-init.sh",
			"--example=" + in.Example,
			"--resolution=" + preset.CLIPreset,
			"--output=" + hyperframesScaffoldOutputPath,
		},
	})
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("hyperframes-init.sh: %v", err), Cause: err}
	}
	if initRes.ExitCode != 0 {
		stderr := strings.TrimSpace(string(initRes.Stderr))
		// Script's documented exit codes (see scripts/hyperframes-init.sh):
		//   1 — invalid --example (registry list on stderr)
		//   2 — usage / missing dependency
		//   3 — scaffold malformed (no index.html)
		//   4 — init itself failed (network, telemetry consent, internal)
		//   5 — tarball creation failed
		// Exit 1 is the only one the CALLER can fix by retrying with a
		// valid name; everything else is a real failure inside the
		// sidecar or upstream.
		code := packs.CodeHandlerFailed
		if initRes.ExitCode == 1 {
			code = packs.CodeInvalidInput
		}
		return nil, &packs.PackError{Code: code,
			Message: fmt.Sprintf("hyperframes init failed (exit %d): %s", initRes.ExitCode, truncStr(stderr, 4096))}
	}

	ec.Report(60, "reading scaffolded project tarball")
	catRes, err := ec.Exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", "cat " + hyperframesScaffoldOutputPath},
	})
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("read scaffold tarball: %v", err), Cause: err}
	}
	if catRes.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("read scaffold tarball: exit %d", catRes.ExitCode)}
	}
	tarballBytes := catRes.Stdout
	if len(tarballBytes) == 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: "hyperframes-init.sh produced an empty tarball (this is a pack/script bug — please file an issue)"}
	}

	ec.Report(85, "enumerating editable slots")
	slots, slotErr := enumerateScaffoldedSlots(tarballBytes)
	if slotErr != nil {
		// Soft-degrade: still upload the artifact, but log + emit an
		// `enumeration_error` field. The interpolate pack can fall
		// back to inspecting the tarball itself if the manifest is
		// missing detail; we don't want a manifest hiccup to block
		// the pipeline.
		if ec.Logger != nil {
			ec.Logger.Warn("scaffold slot enumeration failed", "err", slotErr)
		}
		slots = map[string]any{
			"compositions":      []any{},
			"other_files":       []any{},
			"enumeration_error": slotErr.Error(),
		}
	}

	ec.Report(95, "uploading scaffold artifact")
	art, putErr := ec.Artifacts.Put(ctx, "hyperframes.scaffold",
		in.Example+"-scaffold.tar.gz", tarballBytes, "application/gzip")
	if putErr != nil {
		return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
			Message: fmt.Sprintf("upload scaffold tarball: %v", putErr), Cause: putErr}
	}

	out := map[string]any{
		"project_artifact_key": art.Key,
		"example_used":         in.Example,
		"cli_preset_used":      preset.CLIPreset,
		"width":                preset.Width,
		"height":               preset.Height,
		"aspect_ratio_used":    aspectRatio,
		"resolution_used":      resolution,
		"editable_slots":       slots,
	}
	return json.Marshal(out)
}

// enumerateScaffoldedSlots parses the gzipped tarball and returns a
// summary of the editable text content slots in the scaffold. For
// hyperframes-init scaffolds the canonical structure is:
//
//   - index.html             — root composition (structural, generally not edited)
//   - compositions/*.html    — sub-compositions with editable text content
//   - assets/*               — media (replaced via hyperframes.attach_asset)
//   - meta.json / hyperframes.json / package.json — project metadata
//   - AGENTS.md / CLAUDE.md  — upstream's agent instructions
//
// We focus on compositions/*.html because that's where the interpolate
// pack does its work (titles in intro.html, stats in graphics.html,
// caption transcripts in captions.html). Other paths are reported in
// `other_files` so the interpolate pack can spot a scaffold whose
// shape differs from expectations.
func enumerateScaffoldedSlots(tarballBytes []byte) (map[string]any, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarballBytes))
	if err != nil {
		return nil, fmt.Errorf("gzip open: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	compositions := []any{}
	others := []any{}
	for i := 0; i < hyperframesScaffoldMaxEntries; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar next: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // skip dirs, links, character devices, etc.
		}
		path := strings.TrimPrefix(hdr.Name, "./")
		if strings.HasPrefix(path, "compositions/") && strings.HasSuffix(path, ".html") {
			compositions = append(compositions, map[string]any{
				"path": path,
				"size": hdr.Size,
			})
		} else {
			others = append(others, path)
		}
	}
	return map[string]any{
		"compositions": compositions,
		"other_files":  others,
	}, nil
}
