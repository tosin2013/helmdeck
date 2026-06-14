// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_attach_asset.go (#503 Option C, PR 6) — third link in
// the scaffold-based video chain. Splices an A-roll asset (image or
// video) into the scaffold's a-roll slot (default
// `#short_mag_cut_frame` in index.html), uploads the modified project
// as a new project_artifact_key.
//
// Composition with the upstream asset packs:
//
//   image.generate (fal.ai FLUX/SDXL) ─┐
//                                       ├─→ asset_artifact_key
//   stock.search (Pexels)             ─┘                ↓
//                                                       │
//   hyperframes.scaffold ─→ hyperframes.interpolate ─→ hyperframes.attach_asset
//                                                                  ↓
//                                                       hyperframes.render → MP4
//
// The asset bytes are embedded in the project tarball under
// `assets/aroll-<hash>.<ext>` (content-addressed for de-dup if the
// same asset is attached multiple times), and the target div's inner
// content is replaced with an <img> or <video> referencing the
// asset path. <video> elements are emitted with `muted` per upstream's
// AGENTS.md convention ("Videos use `muted` with a separate <audio>
// element for the audio track").
//
// Architectural notes:
//   - Pure-Go in-process tarball manipulation (archive/tar +
//     compress/gzip + existing extractTarball/writeTarball helpers
//     from hyperframes_interpolate.go).
//   - No SessionSpec, no ec.Exec, no dispatcher. Just ec.Artifacts.
//   - URL fetch is intentionally NOT supported in v1 — chain
//     http.fetch upstream if your asset is URL-only.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

const (
	// hyperframesAttachAssetDefaultTargetID matches the canonical
	// a-roll slot id in upstream's scaffolds (visible in the
	// swiss-grid example's index.html as <div id="short_mag_cut_frame">).
	hyperframesAttachAssetDefaultTargetID = "short_mag_cut_frame"
	// 50 MiB cap matches a comfortable single-asset upper bound.
	// Larger A-rolls usually mean the operator wanted a full video,
	// in which case the asset should be encoded shorter, not bigger.
	hyperframesAttachAssetMaxAssetSize = 50 << 20
)

// hyperframesAttachAssetContentTypeMap maps recognized asset
// content-types to their {file extension, kind} pair. The kind drives
// whether the splice emits an <img> or <video> element.
var hyperframesAttachAssetContentTypeMap = map[string]struct {
	Ext  string
	Kind string
}{
	"image/png":     {".png", "image"},
	"image/jpeg":    {".jpg", "image"},
	"image/jpg":     {".jpg", "image"},
	"image/gif":     {".gif", "image"},
	"image/webp":    {".webp", "image"},
	"image/svg+xml": {".svg", "image"},
	"video/mp4":     {".mp4", "video"},
	"video/webm":    {".webm", "video"},
	// QuickTime is uncommon in modern pipelines but accept the
	// MIME type that image.generate / stock.search backends might
	// surface for .mov files.
	"video/quicktime": {".mov", "video"},
}

type hyperframesAttachAssetInput struct {
	ProjectArtifactKey string `json:"project_artifact_key"`
	AssetArtifactKey   string `json:"asset_artifact_key"`
	TargetID           string `json:"target_id"`
}

// HyperframesAttachAsset constructs the pack. No dispatcher dependency
// — just an artifact store (for asset + project download/upload).
// Registered in cmd/control-plane/main.go alongside hyperframes.render
// since both work entirely off the artifact store without LLM gating.
func HyperframesAttachAsset() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.attach_asset",
		Version: "v1",
		Description: "Splice an A-roll asset (image or video) into a hyperframes scaffold project. Takes a `project_artifact_key` (from `hyperframes.scaffold` or `hyperframes.interpolate`) plus an `asset_artifact_key` (from `image.generate`, `stock.search`, or any pack that uploaded an image/video to the artifact store), embeds the asset bytes under `assets/aroll-<hash>.<ext>` in the project tarball, and modifies `index.html` to reference the asset from the target div (default `#short_mag_cut_frame`, matching upstream's canonical a-roll slot). Videos are emitted with `muted` per upstream's AGENTS.md convention. Returns a new `project_artifact_key` ready for `hyperframes.render`. Used as the third optional step in the scaffold-video chain: scaffold → interpolate → attach_asset → render. URL fetch is not supported in v1 — chain `http.fetch` upstream if the asset is URL-only.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"project_artifact_key", "asset_artifact_key"},
			Produces:       []string{"project_artifact_key"},
			IntentKeywords: []string{"attach asset", "add image to video", "splice a-roll", "embed video clip", "add background image"},
			TypicalUse:     "Third (optional) step in a scaffold-based video chain. Chain `hyperframes.scaffold` + `hyperframes.interpolate` first; pre-fetch your A-roll with `image.generate` or `stock.search`; pass the asset key here; then chain `hyperframes.render`. Pipeline `builtin.scaffolded-narrated-video` (PR 7) automates this.",
			Limitations: []string{
				"asset must be in the artifact store; URL fetching not supported in v1 (chain http.fetch upstream)",
				"asset cap 50 MiB",
				"target div must exist in index.html; default '#short_mag_cut_frame' matches upstream's canonical a-roll slot",
				"supported content types: image/{png,jpeg,gif,webp,svg+xml}, video/{mp4,webm,quicktime}",
			},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"project_artifact_key", "asset_artifact_key"},
			Properties: map[string]string{
				"project_artifact_key": "string",
				"asset_artifact_key":   "string",
				"target_id":            "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"project_artifact_key", "asset_kind", "asset_filename", "target_id_used"},
			Properties: map[string]string{
				"project_artifact_key":          "string",
				"original_project_artifact_key": "string",
				"asset_kind":                    "string",
				"asset_filename":                "string",
				"asset_size":                    "number",
				"target_id_used":                "string",
			},
		},
		Handler: hyperframesAttachAssetHandler,
	}
}

func hyperframesAttachAssetHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in hyperframesAttachAssetInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if strings.TrimSpace(in.ProjectArtifactKey) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "project_artifact_key is required (e.g. from hyperframes.scaffold or hyperframes.interpolate)"}
	}
	if strings.TrimSpace(in.AssetArtifactKey) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "asset_artifact_key is required (chain image.generate or stock.search first; URL fetch is a planned follow-up)"}
	}
	if ec.Artifacts == nil {
		return nil, &packs.PackError{Code: packs.CodeInternal,
			Message: "hyperframes.attach_asset requires an artifact store, but none is wired into the ExecutionContext"}
	}

	// Accept either "#foo" or "foo" for target_id; canonicalize to the
	// bare id since that's what HTML id="" attributes carry.
	targetID := strings.TrimPrefix(strings.TrimSpace(in.TargetID), "#")
	if targetID == "" {
		targetID = hyperframesAttachAssetDefaultTargetID
	}

	// 1. Download the asset and classify by content-type.
	ec.Report(10, "downloading asset")
	assetBytes, assetArt, err := ec.Artifacts.Get(ctx, in.AssetArtifactKey)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("asset_artifact_key %q not found in artifact store: %v",
				in.AssetArtifactKey, err), Cause: err}
	}
	if len(assetBytes) == 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("asset_artifact_key %q resolved to empty bytes",
				in.AssetArtifactKey)}
	}
	if len(assetBytes) > hyperframesAttachAssetMaxAssetSize {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("asset exceeds %d MiB cap (got %d bytes); shorten/recompress the asset before attaching",
				hyperframesAttachAssetMaxAssetSize>>20, len(assetBytes))}
	}
	contentType := strings.ToLower(strings.TrimSpace(assetArt.ContentType))
	info, ok := hyperframesAttachAssetContentTypeMap[contentType]
	if !ok {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("unsupported asset content_type %q; supported: image/{png,jpeg,gif,webp,svg+xml}, video/{mp4,webm,quicktime}",
				assetArt.ContentType)}
	}

	// Content-address the asset filename: same bytes → same name,
	// which deduplicates if the operator attaches the same asset
	// twice across different chains.
	h := sha256.Sum256(assetBytes)
	assetFilename := "aroll-" + hex.EncodeToString(h[:6]) + info.Ext
	assetPath := "assets/" + assetFilename

	// 2. Download the project tarball.
	ec.Report(30, "downloading project tarball")
	projectBytes, _, err := ec.Artifacts.Get(ctx, in.ProjectArtifactKey)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("project_artifact_key %q not found in artifact store: %v",
				in.ProjectArtifactKey, err), Cause: err}
	}
	if len(projectBytes) == 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("project_artifact_key %q resolved to empty bytes",
				in.ProjectArtifactKey)}
	}

	// 3. Extract, find index.html, splice in the asset reference.
	ec.Report(50, "splicing asset into index.html")
	files, err := extractTarball(projectBytes)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("decompress/extract project tarball: %v (is the key really a hyperframes project tarball?)", err), Cause: err}
	}
	indexIdx := -1
	for i, f := range files {
		path := strings.TrimPrefix(f.Header.Name, "./")
		if path == "index.html" {
			indexIdx = i
			break
		}
	}
	if indexIdx < 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "project tarball is missing index.html at the root — not a valid hyperframes scaffold"}
	}
	indexContent := string(files[indexIdx].Data)
	newIndex, replaced := spliceAssetIntoTarget(indexContent, targetID, assetPath, info.Kind)
	if !replaced {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("target div id=%q not found in index.html — the scaffold may use a different a-roll slot id. Try the --target_id input or pick a scaffold whose index.html includes <div id=%q>.",
				targetID, targetID)}
	}
	files[indexIdx].Data = []byte(newIndex)
	files[indexIdx].Header.Size = int64(len(newIndex))

	// 4. Append (or replace) the asset file in the tarball.
	// We don't bother de-duping for the test executor here — if the
	// same content-addressed path already exists in the project,
	// upstream's tar reader takes the later entry, which is fine.
	files = append(files, tarFile{
		Header: &tar.Header{
			Name:     assetPath,
			Mode:     0644,
			Size:     int64(len(assetBytes)),
			Typeflag: tar.TypeReg,
		},
		Data: assetBytes,
	})

	// 5. Repackage + upload.
	ec.Report(80, "repackaging project")
	newTarball, err := writeTarball(files)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("repackage tarball: %v", err), Cause: err}
	}
	ec.Report(95, "uploading project")
	art, putErr := ec.Artifacts.Put(ctx, "hyperframes.attach_asset", "with-aroll.tar.gz", newTarball, "application/gzip")
	if putErr != nil {
		return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
			Message: fmt.Sprintf("upload project tarball: %v", putErr), Cause: putErr}
	}

	out := map[string]any{
		"project_artifact_key":          art.Key,
		"original_project_artifact_key": in.ProjectArtifactKey,
		"asset_kind":                    info.Kind,
		"asset_filename":                assetFilename,
		"asset_size":                    len(assetBytes),
		"target_id_used":                targetID,
	}
	return json.Marshal(out)
}

// spliceAssetIntoTarget finds <div id="targetID" …>…</div> in indexHTML
// and replaces its inner content with an <img>/<video> referencing
// assetPath. Returns (newHTML, replaced) — when no matching div was
// found, replaced is false and the original HTML is unchanged.
//
// The regex matches the FIRST occurrence of the target div. Upstream
// scaffolds don't reuse ids (HTML forbids duplicate ids) so this is
// fine; if a scaffold somehow has multiple divs with the same id, the
// first is replaced and the rest are left alone.
func spliceAssetIntoTarget(indexHTML, targetID, assetPath, kind string) (string, bool) {
	re := regexp.MustCompile(fmt.Sprintf(
		`(?s)(<div\s+id\s*=\s*"%s"[^>]*>)(.*?)(</div>)`,
		regexp.QuoteMeta(targetID),
	))
	var newElement string
	switch kind {
	case "image":
		// alt="" is intentional — the a-roll asset is visual, not
		// content; screen-reader-friendly meaning lives in the
		// caption transcript instead.
		newElement = fmt.Sprintf(`<img src="%s" alt="" />`, assetPath)
	case "video":
		// Per upstream AGENTS.md: "Videos use `muted` with a separate
		// `<audio>` element for the audio track." The narration audio
		// (from podcast.generate) lives elsewhere in the composition.
		newElement = fmt.Sprintf(`<video src="%s" muted></video>`, assetPath)
	default:
		// Unsupported kind shouldn't reach here — the handler caps
		// content-type before this is called — but guard anyway so a
		// future content-type-map entry that forgets to pick a kind
		// fails loud instead of silently passing through.
		return indexHTML, false
	}
	replaced := false
	newHTML := re.ReplaceAllStringFunc(indexHTML, func(match string) string {
		sub := re.FindStringSubmatch(match)
		replaced = true
		return sub[1] + newElement + sub[3]
	})
	return newHTML, replaced
}
