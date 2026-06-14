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

// runAttachAsset drives the pack handler against a pre-seeded artifact
// store. No dispatcher, no session executor — the pack is pure-Go
// in-process.
func runAttachAsset(t *testing.T, store *packs.MemoryArtifactStore, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesAttachAsset()
	if store == nil {
		store = packs.NewMemoryArtifactStore()
	}
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-attach"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Artifacts: store,
	}
	return pack.Handler(context.Background(), ec)
}

// seedProjectWithIndex puts a fake project tarball into the store
// whose index.html includes <div id="short_mag_cut_frame"> by default
// (overridable via the divID arg).
func seedProjectWithIndex(t *testing.T, store *packs.MemoryArtifactStore, divID string) string {
	t.Helper()
	indexHTML := `<!doctype html><html><body>
<div id="master-root" data-composition-id="master">
  <div id="` + divID + `">

  </div>
</div>
</body></html>`
	tar := makeFakeScaffoldTarball(t, map[string]string{
		"index.html":              indexHTML,
		"compositions/intro.html": "<template/>",
		"hyperframes.json":        "{}",
	})
	art, err := store.Put(context.Background(), "hyperframes.scaffold", "scaffold.tar.gz", tar, "application/gzip")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return art.Key
}

// seedAsset puts an asset into the store and returns the key.
func seedAsset(t *testing.T, store *packs.MemoryArtifactStore, name string, content []byte, contentType string) string {
	t.Helper()
	art, err := store.Put(context.Background(), "image.generate", name, content, contentType)
	if err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	return art.Key
}

// --- input validation ----------------------------------------------------

func TestHyperframesAttachAsset_MissingProjectKey_Rejects(t *testing.T) {
	_, err := runAttachAsset(t, nil, `{"asset_artifact_key":"x"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "project_artifact_key is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_MissingAssetKey_Rejects(t *testing.T) {
	_, err := runAttachAsset(t, nil, `{"project_artifact_key":"x"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "asset_artifact_key is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_AssetNotInStore_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	_, err := runAttachAsset(t, store, `{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"missing/x.png"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "asset_artifact_key") || !strings.Contains(pe.Message, "not found in artifact store") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_ProjectNotInStore_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	assetKey := seedAsset(t, store, "x.png", []byte("png-bytes"), "image/png")
	_, err := runAttachAsset(t, store,
		`{"project_artifact_key":"missing/p.tar.gz","asset_artifact_key":"`+assetKey+`"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "project_artifact_key") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_EmptyAssetBytes_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	assetKey := seedAsset(t, store, "empty.png", []byte{}, "image/png")
	_, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "empty") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_AssetTooLarge_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	// One byte over the 50 MiB cap.
	big := make([]byte, hyperframesAttachAssetMaxAssetSize+1)
	assetKey := seedAsset(t, store, "huge.mp4", big, "video/mp4")
	_, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "cap") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_UnsupportedContentType_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	assetKey := seedAsset(t, store, "x.exe", []byte("not-a-media-file"), "application/octet-stream")
	_, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "unsupported asset content_type") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_ProjectMissingIndex_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	// Project tar with no index.html at root.
	badTar := makeFakeScaffoldTarball(t, map[string]string{
		"compositions/intro.html": "<template/>",
	})
	projArt, _ := store.Put(context.Background(), "test", "no-index.tar.gz", badTar, "application/gzip")
	assetKey := seedAsset(t, store, "x.png", []byte("png-bytes"), "image/png")
	_, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projArt.Key+`","asset_artifact_key":"`+assetKey+`"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "missing index.html") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAsset_TargetDivNotInIndex_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	// Seed a project whose index.html does NOT have the default
	// target id.
	projectKey := seedProjectWithIndex(t, store, "different-div-id")
	assetKey := seedAsset(t, store, "x.png", []byte("png-bytes"), "image/png")
	// Use the DEFAULT target id, which doesn't exist in this project.
	_, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "not found in index.html") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

// --- happy paths ---------------------------------------------------------

func TestHyperframesAttachAsset_Image_HappyPath(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	pngBytes := []byte("\x89PNG\r\n\x1a\n-fake-png-bytes")
	assetKey := seedAsset(t, store, "scene.png", pngBytes, "image/png")

	raw, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["asset_kind"] != "image" {
		t.Errorf("expected asset_kind=image, got %v", out["asset_kind"])
	}
	if out["target_id_used"] != "short_mag_cut_frame" {
		t.Errorf("expected default target_id, got %v", out["target_id_used"])
	}
	filename, _ := out["asset_filename"].(string)
	if !strings.HasPrefix(filename, "aroll-") || !strings.HasSuffix(filename, ".png") {
		t.Errorf("expected aroll-<hash>.png filename, got %v", filename)
	}
	// Verify the modified project: index.html should have <img src="assets/aroll-..." />
	// AND the asset file should exist in the tarball.
	newKey, _ := out["project_artifact_key"].(string)
	if newKey == "" {
		t.Fatal("missing new project_artifact_key")
	}
	if newKey == projectKey {
		t.Error("expected NEW project_artifact_key, got same as input")
	}
	content, _, err := store.Get(context.Background(), newKey)
	if err != nil {
		t.Fatalf("get new project: %v", err)
	}
	files, err := extractTarball(content)
	if err != nil {
		t.Fatalf("extract new project: %v", err)
	}
	var sawIndex, sawAsset bool
	for _, f := range files {
		path := strings.TrimPrefix(f.Header.Name, "./")
		switch {
		case path == "index.html":
			if strings.Contains(string(f.Data), `<img src="assets/`+filename+`" alt="" />`) {
				sawIndex = true
			}
		case path == "assets/"+filename:
			if bytes.Equal(f.Data, pngBytes) {
				sawAsset = true
			}
		}
	}
	if !sawIndex {
		t.Error("index.html should contain <img> referencing the asset")
	}
	if !sawAsset {
		t.Error("tarball should contain the asset file at assets/<filename>")
	}
}

func TestHyperframesAttachAsset_Video_EmitsMuted(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	mp4Bytes := []byte("fake-mp4-bytes")
	assetKey := seedAsset(t, store, "scene.mp4", mp4Bytes, "video/mp4")

	raw, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["asset_kind"] != "video" {
		t.Errorf("expected asset_kind=video, got %v", out["asset_kind"])
	}
	// Pull the modified project and verify the video element has `muted`.
	newKey, _ := out["project_artifact_key"].(string)
	content, _, err := store.Get(context.Background(), newKey)
	if err != nil {
		t.Fatalf("get new project: %v", err)
	}
	files, err := extractTarball(content)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, f := range files {
		if strings.TrimPrefix(f.Header.Name, "./") == "index.html" {
			s := string(f.Data)
			if !strings.Contains(s, `<video src="assets/aroll-`) {
				t.Errorf("expected <video src=> in index.html, got: %s", s)
			}
			if !strings.Contains(s, ` muted`) {
				t.Errorf("expected muted attribute per upstream AGENTS.md convention, got: %s", s)
			}
		}
	}
}

func TestHyperframesAttachAsset_CustomTargetID(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "bg-image-slot")
	assetKey := seedAsset(t, store, "x.jpg", []byte("jpg-bytes"), "image/jpeg")

	raw, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`","target_id":"bg-image-slot"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["target_id_used"] != "bg-image-slot" {
		t.Errorf("expected custom target_id, got %v", out["target_id_used"])
	}
}

func TestHyperframesAttachAsset_HashPrefixAcceptedOnTargetID(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithIndex(t, store, "bg-slot")
	assetKey := seedAsset(t, store, "x.png", []byte("png"), "image/png")

	raw, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey+`","asset_artifact_key":"`+assetKey+`","target_id":"#bg-slot"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Stored as bare id without the leading #.
	if out["target_id_used"] != "bg-slot" {
		t.Errorf("expected bare id, got %v", out["target_id_used"])
	}
}

func TestHyperframesAttachAsset_ContentAddressedFilename(t *testing.T) {
	// Same asset bytes → same filename, regardless of input asset_artifact_key.
	store := packs.NewMemoryArtifactStore()
	projectKey1 := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	projectKey2 := seedProjectWithIndex(t, store, "short_mag_cut_frame")
	identicalBytes := []byte("identical-png-bytes-for-dedup")
	assetKey1 := seedAsset(t, store, "a.png", identicalBytes, "image/png")
	assetKey2 := seedAsset(t, store, "b.png", identicalBytes, "image/png")

	raw1, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey1+`","asset_artifact_key":"`+assetKey1+`"}`)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	raw2, err := runAttachAsset(t, store,
		`{"project_artifact_key":"`+projectKey2+`","asset_artifact_key":"`+assetKey2+`"}`)
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	var out1, out2 map[string]any
	if err := json.Unmarshal(raw1, &out1); err != nil {
		t.Fatalf("unmarshal 1: %v", err)
	}
	if err := json.Unmarshal(raw2, &out2); err != nil {
		t.Fatalf("unmarshal 2: %v", err)
	}
	if out1["asset_filename"] != out2["asset_filename"] {
		t.Errorf("expected identical asset_filename for identical bytes, got %v vs %v",
			out1["asset_filename"], out2["asset_filename"])
	}
}

// --- spliceAssetIntoTarget unit -----------------------------------------

func TestSpliceAssetIntoTarget_ReplacesImage(t *testing.T) {
	html := `<div id="slot" class="x"><img src="placeholder.png"/></div>`
	got, ok := spliceAssetIntoTarget(html, "slot", "assets/new.png", "image")
	if !ok {
		t.Fatal("expected replacement")
	}
	if !strings.Contains(got, `<img src="assets/new.png" alt="" />`) {
		t.Errorf("expected new img element, got: %s", got)
	}
}

func TestSpliceAssetIntoTarget_VideoEmitsMuted(t *testing.T) {
	html := `<div id="slot"></div>`
	got, ok := spliceAssetIntoTarget(html, "slot", "assets/new.mp4", "video")
	if !ok {
		t.Fatal("expected replacement")
	}
	if !strings.Contains(got, `<video src="assets/new.mp4" muted></video>`) {
		t.Errorf("expected muted video element, got: %s", got)
	}
}

func TestSpliceAssetIntoTarget_NoMatch_ReturnsFalse(t *testing.T) {
	html := `<div id="other">content</div>`
	got, ok := spliceAssetIntoTarget(html, "slot", "assets/x.png", "image")
	if ok {
		t.Error("expected no replacement")
	}
	if got != html {
		t.Error("html should be unchanged")
	}
}

func TestSpliceAssetIntoTarget_PreservesDivAttributes(t *testing.T) {
	html := `<div id="slot" class="overlay" data-foo="bar">old</div>`
	got, ok := spliceAssetIntoTarget(html, "slot", "assets/x.png", "image")
	if !ok {
		t.Fatal("expected replacement")
	}
	if !strings.Contains(got, `<div id="slot" class="overlay" data-foo="bar">`) {
		t.Errorf("attributes lost: %s", got)
	}
}
