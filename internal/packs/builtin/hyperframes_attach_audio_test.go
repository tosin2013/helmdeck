// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// runAttachAudio drives the pack handler against a pre-seeded artifact
// store. Mirrors runAttachAsset.
func runAttachAudio(t *testing.T, store *packs.MemoryArtifactStore, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesAttachAudio()
	if store == nil {
		store = packs.NewMemoryArtifactStore()
	}
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-attach-audio"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Artifacts: store,
	}
	return pack.Handler(context.Background(), ec)
}

// seedProjectWithRootDiv puts a fake project tarball into the store
// whose index.html has a root composition div with data-composition-id
// = "main" and a data-duration. rootDuration is the initial value;
// attach_audio's default update_root_duration:true should rewrite it.
func seedProjectWithRootDiv(t *testing.T, store *packs.MemoryArtifactStore, rootDuration string) string {
	t.Helper()
	indexHTML := `<!doctype html><html><body>
<div id="root" data-composition-id="main" data-start="0" data-duration="` + rootDuration + `" data-width="1920" data-height="1080">
  <h1>Placeholder content</h1>
</div>
</body></html>`
	tar := makeFakeScaffoldTarball(t, map[string]string{
		"index.html":              indexHTML,
		"compositions/intro.html": "<template/>",
		"hyperframes.json":        "{}",
	})
	art, err := store.Put(context.Background(), "hyperframes.interpolate", "interpolated.tar.gz", tar, "application/gzip")
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return art.Key
}

// seedAudio puts an audio asset into the store and returns the key.
func seedAudio(t *testing.T, store *packs.MemoryArtifactStore, name string, content []byte, contentType string) string {
	t.Helper()
	art, err := store.Put(context.Background(), "podcast.generate", name, content, contentType)
	if err != nil {
		t.Fatalf("seed audio: %v", err)
	}
	return art.Key
}

// --- input validation ----------------------------------------------------

func TestHyperframesAttachAudio_MissingProjectKey_Rejects(t *testing.T) {
	_, err := runAttachAudio(t, nil, `{"audio_artifact_key":"x","duration_seconds":60}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "project_artifact_key is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_MissingAudioKey_Rejects(t *testing.T) {
	_, err := runAttachAudio(t, nil, `{"project_artifact_key":"x","duration_seconds":60}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "audio_artifact_key is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_MissingDurationSeconds_Rejects(t *testing.T) {
	_, err := runAttachAudio(t, nil, `{"project_artifact_key":"x","audio_artifact_key":"y"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "duration_seconds is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_NegativeDuration_Rejects(t *testing.T) {
	_, err := runAttachAudio(t, nil, `{"project_artifact_key":"x","audio_artifact_key":"y","duration_seconds":-5}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "duration_seconds is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_AudioNotInStore_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	_, err := runAttachAudio(t, store, `{"project_artifact_key":"`+projectKey+`","audio_artifact_key":"missing/x.mp3","duration_seconds":60}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "audio_artifact_key") || !strings.Contains(pe.Message, "not found in artifact store") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_ProjectNotInStore_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	audioKey := seedAudio(t, store, "narration.mp3", []byte("ID3 stub"), "audio/mpeg")
	_, err := runAttachAudio(t, store,
		`{"project_artifact_key":"missing/p.tar.gz","audio_artifact_key":"`+audioKey+`","duration_seconds":60}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "project_artifact_key") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_EmptyAudio_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	audioKey := seedAudio(t, store, "empty.mp3", []byte{}, "audio/mpeg")
	_, err := runAttachAudio(t, store,
		`{"project_artifact_key":"`+projectKey+`","audio_artifact_key":"`+audioKey+`","duration_seconds":60}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "empty") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_AudioTooLarge_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	// One byte over the 50 MiB cap.
	big := make([]byte, hyperframesAttachAudioMaxAssetSize+1)
	audioKey := seedAudio(t, store, "huge.mp3", big, "audio/mpeg")
	_, err := runAttachAudio(t, store,
		`{"project_artifact_key":"`+projectKey+`","audio_artifact_key":"`+audioKey+`","duration_seconds":60}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "MiB cap") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_UnsupportedContentType_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	audioKey := seedAudio(t, store, "weird.ogg", []byte("OggS stub"), "audio/ogg")
	_, err := runAttachAudio(t, store,
		`{"project_artifact_key":"`+projectKey+`","audio_artifact_key":"`+audioKey+`","duration_seconds":60}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "unsupported audio content_type") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

// --- happy paths ---------------------------------------------------------

func TestHyperframesAttachAudio_HappyPath_MP3(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	audioKey := seedAudio(t, store, "narration.mp3", []byte("ID3 stub bytes"), "audio/mpeg")

	raw, err := runAttachAudio(t, store, fmt.Sprintf(
		`{"project_artifact_key":%q,"audio_artifact_key":%q,"duration_seconds":96.339592}`,
		projectKey, audioKey))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ProjectArtifactKey  string  `json:"project_artifact_key"`
		AudioFilename       string  `json:"audio_filename"`
		AudioSize           int     `json:"audio_size"`
		DurationSecondsUsed float64 `json:"duration_seconds_used"`
		RootDurationUpdated bool    `json:"root_duration_updated"`
		TrackIndexUsed      int     `json:"track_index_used"`
		VolumeUsed          float64 `json:"volume_used"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(out.AudioFilename, "aroll-audio-") || !strings.HasSuffix(out.AudioFilename, ".mp3") {
		t.Errorf("audio_filename = %q, want aroll-audio-*.mp3", out.AudioFilename)
	}
	if out.AudioSize != 14 { // len("ID3 stub bytes")
		t.Errorf("audio_size = %d, want 14", out.AudioSize)
	}
	if out.DurationSecondsUsed != 96.339592 {
		t.Errorf("duration_seconds_used = %v, want 96.339592", out.DurationSecondsUsed)
	}
	if !out.RootDurationUpdated {
		t.Error("root_duration_updated should default to true")
	}
	if out.TrackIndexUsed != 9 {
		t.Errorf("track_index_used = %d, want 9 (default)", out.TrackIndexUsed)
	}
	if out.VolumeUsed != 1.0 {
		t.Errorf("volume_used = %v, want 1.0 (default)", out.VolumeUsed)
	}

	// Inspect the produced tarball: should have a new index.html with
	// the <audio> element + rewritten data-duration AND the audio file.
	tar, _, err := store.Get(context.Background(), out.ProjectArtifactKey)
	if err != nil {
		t.Fatalf("get output tarball: %v", err)
	}
	files, err := extractTarball(tar)
	if err != nil {
		t.Fatalf("extract output tarball: %v", err)
	}
	var indexContent string
	foundAudio := false
	for _, f := range files {
		switch strings.TrimPrefix(f.Header.Name, "./") {
		case "index.html":
			indexContent = string(f.Data)
		case "assets/" + out.AudioFilename:
			foundAudio = true
			if string(f.Data) != "ID3 stub bytes" {
				t.Errorf("audio bytes mismatch: %q", string(f.Data))
			}
		}
	}
	if !foundAudio {
		t.Errorf("audio file %q missing from output tarball", "assets/"+out.AudioFilename)
	}
	if !strings.Contains(indexContent, `<audio src="assets/`+out.AudioFilename+`"`) {
		t.Errorf("index.html missing <audio> element: %s", indexContent)
	}
	if !strings.Contains(indexContent, `data-duration="96.339592"`) {
		t.Errorf("root div data-duration not rewritten to 96.339592: %s", indexContent)
	}
	if !strings.Contains(indexContent, `data-track-index="9"`) {
		t.Errorf("audio data-track-index missing: %s", indexContent)
	}
}

func TestHyperframesAttachAudio_UpdateRootDurationFalse_DoesNotRewriteRoot(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	audioKey := seedAudio(t, store, "narration.mp3", []byte("ID3 stub"), "audio/mpeg")

	raw, err := runAttachAudio(t, store, fmt.Sprintf(
		`{"project_artifact_key":%q,"audio_artifact_key":%q,"duration_seconds":60,"update_root_duration":false}`,
		projectKey, audioKey))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ProjectArtifactKey  string `json:"project_artifact_key"`
		RootDurationUpdated bool   `json:"root_duration_updated"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.RootDurationUpdated {
		t.Error("root_duration_updated should be false when update_root_duration:false")
	}

	tar, _, _ := store.Get(context.Background(), out.ProjectArtifactKey)
	files, _ := extractTarball(tar)
	var indexContent string
	for _, f := range files {
		if strings.TrimPrefix(f.Header.Name, "./") == "index.html" {
			indexContent = string(f.Data)
		}
	}
	// Original root duration was "15" — should remain unchanged.
	if !strings.Contains(indexContent, `data-duration="15"`) {
		t.Errorf("root div data-duration should be preserved when update_root_duration:false: %s", indexContent)
	}
	// The audio element should still be present.
	if !strings.Contains(indexContent, `<audio`) {
		t.Error("audio element missing")
	}
}

func TestHyperframesAttachAudio_CustomVolumeTrackIndex(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	audioKey := seedAudio(t, store, "narration.mp3", []byte("ID3 stub"), "audio/mpeg")

	raw, err := runAttachAudio(t, store, fmt.Sprintf(
		`{"project_artifact_key":%q,"audio_artifact_key":%q,"duration_seconds":60,"volume":0.7,"track_index":12}`,
		projectKey, audioKey))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ProjectArtifactKey string  `json:"project_artifact_key"`
		TrackIndexUsed     int     `json:"track_index_used"`
		VolumeUsed         float64 `json:"volume_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.TrackIndexUsed != 12 {
		t.Errorf("track_index_used = %d, want 12", out.TrackIndexUsed)
	}
	if out.VolumeUsed != 0.7 {
		t.Errorf("volume_used = %v, want 0.7", out.VolumeUsed)
	}
	tar, _, _ := store.Get(context.Background(), out.ProjectArtifactKey)
	files, _ := extractTarball(tar)
	var idx string
	for _, f := range files {
		if strings.TrimPrefix(f.Header.Name, "./") == "index.html" {
			idx = string(f.Data)
		}
	}
	if !strings.Contains(idx, `data-volume="0.7"`) {
		t.Errorf("audio data-volume not 0.7: %s", idx)
	}
	if !strings.Contains(idx, `data-track-index="12"`) {
		t.Errorf("audio data-track-index not 12: %s", idx)
	}
}

func TestHyperframesAttachAudio_NoRootDiv_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	// Project tarball with NO root composition div — should reject.
	tar := makeFakeScaffoldTarball(t, map[string]string{
		"index.html": `<!doctype html><html><body><div id="root">no composition-id</div></body></html>`,
	})
	art, _ := store.Put(context.Background(), "hyperframes.scaffold", "scaffold.tar.gz", tar, "application/gzip")
	audioKey := seedAudio(t, store, "n.mp3", []byte("ID3"), "audio/mpeg")

	_, err := runAttachAudio(t, store, fmt.Sprintf(
		`{"project_artifact_key":%q,"audio_artifact_key":%q,"duration_seconds":60}`,
		art.Key, audioKey))
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "root composition div not found") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_MissingIndexHTML_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	tar := makeFakeScaffoldTarball(t, map[string]string{
		"compositions/intro.html": "<template/>",
	})
	art, _ := store.Put(context.Background(), "hyperframes.scaffold", "scaffold.tar.gz", tar, "application/gzip")
	audioKey := seedAudio(t, store, "n.mp3", []byte("ID3"), "audio/mpeg")

	_, err := runAttachAudio(t, store, fmt.Sprintf(
		`{"project_artifact_key":%q,"audio_artifact_key":%q,"duration_seconds":60}`,
		art.Key, audioKey))
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "missing index.html") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesAttachAudio_ContentAddressedDedup(t *testing.T) {
	// Same bytes → same filename. Second attach with the same audio
	// produces the same aroll-audio-<hash>.<ext> name, supporting
	// deduplication across chains.
	store := packs.NewMemoryArtifactStore()
	projectKey := seedProjectWithRootDiv(t, store, "15")
	audioKey1 := seedAudio(t, store, "a.mp3", []byte("identical bytes"), "audio/mpeg")
	audioKey2 := seedAudio(t, store, "b.mp3", []byte("identical bytes"), "audio/mpeg")

	raw1, _ := runAttachAudio(t, store, fmt.Sprintf(
		`{"project_artifact_key":%q,"audio_artifact_key":%q,"duration_seconds":60}`, projectKey, audioKey1))
	raw2, _ := runAttachAudio(t, store, fmt.Sprintf(
		`{"project_artifact_key":%q,"audio_artifact_key":%q,"duration_seconds":60}`, projectKey, audioKey2))

	var o1, o2 struct {
		AudioFilename string `json:"audio_filename"`
	}
	_ = json.Unmarshal(raw1, &o1)
	_ = json.Unmarshal(raw2, &o2)
	if o1.AudioFilename != o2.AudioFilename {
		t.Errorf("same bytes -> different filenames: %q vs %q", o1.AudioFilename, o2.AudioFilename)
	}
}

// --- splice helpers (regex-level) ---------------------------------------

func TestSpliceAudioIntoRoot_FindsDataCompositionIDMain(t *testing.T) {
	html := `<div id="root" data-composition-id="main" data-duration="15"><h1>X</h1></div>`
	audio := `<audio src="assets/x.mp3"></audio>`
	out, ok := spliceAudioIntoRoot(html, audio)
	if !ok {
		t.Fatal("expected splice to succeed")
	}
	if !strings.Contains(out, audio) {
		t.Errorf("audio element missing in output: %s", out)
	}
	// The audio should appear after the opening tag and before <h1>.
	idxAudio := strings.Index(out, audio)
	idxH1 := strings.Index(out, "<h1>")
	if idxAudio >= idxH1 {
		t.Errorf("audio should be inserted before <h1>: %s", out)
	}
}

func TestSpliceAudioIntoRoot_NoMatch_ReturnsFalse(t *testing.T) {
	html := `<div id="root">no composition id</div>`
	out, ok := spliceAudioIntoRoot(html, `<audio/>`)
	if ok {
		t.Error("expected splice to return false on no match")
	}
	if out != html {
		t.Errorf("html should be unchanged when match fails: %q -> %q", html, out)
	}
}

func TestSpliceAudioIntoRoot_AttributeOrderDoesNotMatter(t *testing.T) {
	// data-composition-id can appear at any position in the attribute list.
	html := `<div data-start="0" data-duration="15" data-composition-id="main" data-width="1920"><span/></div>`
	out, ok := spliceAudioIntoRoot(html, `<audio src="x"/>`)
	if !ok {
		t.Fatal("expected splice to succeed regardless of attribute order")
	}
	if !strings.Contains(out, `<audio src="x"/>`) {
		t.Errorf("audio missing: %s", out)
	}
}

func TestUpdateRootDataDuration_RewritesValue(t *testing.T) {
	html := `<div id="root" data-composition-id="main" data-duration="15">x</div>`
	out, ok := updateRootDataDuration(html, 96.5)
	if !ok {
		t.Fatal("expected update to succeed")
	}
	if !strings.Contains(out, `data-duration="96.5"`) {
		t.Errorf("data-duration not rewritten: %s", out)
	}
	if strings.Contains(out, `data-duration="15"`) {
		t.Errorf("original data-duration value still present: %s", out)
	}
}

func TestUpdateRootDataDuration_OnlyRewritesRootNotChildClips(t *testing.T) {
	// A composition typically has many class="clip" elements with
	// their own data-duration. Only the root's should change.
	html := `<div id="root" data-composition-id="main" data-duration="15"><div class="clip" data-duration="5"></div></div>`
	out, ok := updateRootDataDuration(html, 60)
	if !ok {
		t.Fatal("expected update to succeed")
	}
	if !strings.Contains(out, `data-duration="60"`) {
		t.Errorf("root data-duration not rewritten to 60: %s", out)
	}
	if !strings.Contains(out, `<div class="clip" data-duration="5">`) {
		t.Errorf("child clip's data-duration='5' must NOT be rewritten: %s", out)
	}
}

func TestUpdateRootDataDuration_DataDurationAfterCompositionID(t *testing.T) {
	// data-duration can appear AFTER data-composition-id in the attribute list.
	html := `<div data-composition-id="main" data-start="0" data-duration="15">x</div>`
	out, ok := updateRootDataDuration(html, 99)
	if !ok {
		t.Fatal("expected update to succeed (data-duration after composition-id)")
	}
	if !strings.Contains(out, `data-duration="99"`) {
		t.Errorf("data-duration not rewritten: %s", out)
	}
}

// --- Child composition stretching (issue #521 follow-up) ----------------

// TestUpdateRootDataDuration_StretchesMatchingChildComposition — the
// empirical repro from the v0.29.2 retest. Root and one child
// composition both had data-duration="15"; my regex extended root to
// 97.9s but the child stayed at 15s, blanking the canvas after 15s.
// The fix: any data-composition-id-bearing div whose duration MATCHED
// the root's original should be stretched alongside the root.
func TestUpdateRootDataDuration_StretchesMatchingChildComposition(t *testing.T) {
	html := `<div id="root" data-composition-id="main" data-start="0" data-duration="15"><div id="dt-comp" data-composition-id="decision-tree" data-composition-src="compositions/decision_tree.html" data-start="0" data-duration="15"></div></div>`
	out, ok := updateRootDataDuration(html, 97.906939)
	if !ok {
		t.Fatal("expected update to succeed")
	}
	if !strings.Contains(out, `data-composition-id="main" data-start="0" data-duration="97.906939"`) {
		t.Errorf("root not rewritten: %s", out)
	}
	if !strings.Contains(out, `data-composition-id="decision-tree"`) || !strings.Contains(out, `data-composition-src="compositions/decision_tree.html" data-start="0" data-duration="97.906939"`) {
		t.Errorf("child composition not stretched to match root: %s", out)
	}
	// The original "15" should NOT survive anywhere on a div with
	// data-composition-id (we rewrote both).
	if strings.Contains(out, `data-duration="15"`) {
		t.Errorf("data-duration=\"15\" should be fully gone after rewrite: %s", out)
	}
}

// TestUpdateRootDataDuration_LeavesDivergentChildAlone — operator-
// deliberate divergence is preserved. If a child composition has a
// duration that DOESN'T match the root's original, the child was
// intentionally set differently and shouldn't be stretched.
func TestUpdateRootDataDuration_LeavesDivergentChildAlone(t *testing.T) {
	// Root duration=30, child duration=5 (intentional 5-sec intro).
	html := `<div id="root" data-composition-id="main" data-duration="30"><div data-composition-id="intro" data-composition-src="intro.html" data-duration="5"></div></div>`
	out, ok := updateRootDataDuration(html, 90)
	if !ok {
		t.Fatal("expected update to succeed")
	}
	// Root rewritten 30 → 90.
	if !strings.Contains(out, `data-composition-id="main" data-duration="90"`) {
		t.Errorf("root not rewritten to 90: %s", out)
	}
	// Child stays at 5 (different from root's original 30).
	if !strings.Contains(out, `data-composition-id="intro" data-composition-src="intro.html" data-duration="5"`) {
		t.Errorf("child with divergent duration should be preserved: %s", out)
	}
}

// TestUpdateRootDataDuration_DecisionTreeScaffoldShape — end-to-end
// against the exact HTML shape from the v0.29.2 retest's failing run
// (run_6f6cb0ea40a94dd1). Confirms the empirical bug is fixed and
// guards against regression as the upstream scaffold conventions
// evolve.
func TestUpdateRootDataDuration_DecisionTreeScaffoldShape(t *testing.T) {
	html := `<!doctype html><html><body>
<div
  id="root"
  data-composition-id="main"
  data-start="0"
  data-duration="15"
  data-width="1920"
  data-height="1080"
>
  <div
    id="decision-tree-comp"
    data-composition-id="decision-tree"
    data-composition-src="compositions/decision_tree.html"
    data-start="0"
    data-duration="15"
    data-track-index="0"
    data-width="1920"
    data-height="1080"
  ></div>
</div>
</body></html>`
	out, ok := updateRootDataDuration(html, 97.906939)
	if !ok {
		t.Fatal("expected update to succeed against decision-tree scaffold shape")
	}
	// Both data-duration="15" occurrences should have been replaced.
	count15 := strings.Count(out, `data-duration="15"`)
	if count15 != 0 {
		t.Errorf("data-duration=\"15\" still present (%d times) — child composition not stretched:\n%s", count15, out)
	}
	count97 := strings.Count(out, `data-duration="97.906939"`)
	if count97 != 2 {
		t.Errorf("expected 2 occurrences of data-duration=\"97.906939\" (root + child), got %d:\n%s", count97, out)
	}
}

// TestUpdateRootDataDuration_StillSkipsChildClips — regression guard
// for the original "don't touch class=clip" semantics. A class=clip
// element whose data-duration HAPPENS to equal root's must still be
// left alone (no data-composition-id anchor).
func TestUpdateRootDataDuration_StillSkipsChildClips(t *testing.T) {
	html := `<div id="root" data-composition-id="main" data-duration="15"><div class="clip" data-duration="15"></div></div>`
	out, ok := updateRootDataDuration(html, 60)
	if !ok {
		t.Fatal("expected update to succeed")
	}
	if !strings.Contains(out, `data-composition-id="main" data-duration="60"`) {
		t.Errorf("root not rewritten: %s", out)
	}
	// class="clip" lacks data-composition-id, so it MUST keep data-duration=15.
	if !strings.Contains(out, `<div class="clip" data-duration="15">`) {
		t.Errorf("class=\"clip\" data-duration must not be rewritten: %s", out)
	}
}
