// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"log/slog"
	"io"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// runInterpolate drives the pack with a scripted dispatcher + seeded
// artifact store. The seeded tarball IS the input project; the pack
// fetches it via ec.Artifacts.Get(project_artifact_key).
func runInterpolate(t *testing.T, disp *scriptedDispatcherWT, store *packs.MemoryArtifactStore, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesInterpolate(disp)
	if store == nil {
		store = packs.NewMemoryArtifactStore()
	}
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-interpolate"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Artifacts: store,
	}
	return pack.Handler(context.Background(), ec)
}

// seedProject puts a fake project tarball into the store and returns
// the key the test will pass as project_artifact_key.
func seedProject(t *testing.T, store *packs.MemoryArtifactStore, files map[string]string) string {
	t.Helper()
	tar := makeFakeScaffoldTarball(t, files)
	art, err := store.Put(context.Background(), "hyperframes.scaffold", "test-scaffold.tar.gz", tar, "application/gzip")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return art.Key
}

// --- input validation ----------------------------------------------------

func TestHyperframesInterpolate_MissingProjectKey_Rejects(t *testing.T) {
	disp := &scriptedDispatcherWT{}
	_, err := runInterpolate(t, disp, nil, `{"description":"x","model":"m"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "project_artifact_key is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesInterpolate_MissingDescription_Rejects(t *testing.T) {
	disp := &scriptedDispatcherWT{}
	_, err := runInterpolate(t, disp, nil, `{"project_artifact_key":"x","model":"m"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "description is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesInterpolate_MissingModel_Rejects(t *testing.T) {
	disp := &scriptedDispatcherWT{}
	_, err := runInterpolate(t, disp, nil, `{"project_artifact_key":"x","description":"d"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
}

func TestHyperframesInterpolate_ProjectKeyNotInStore_Rejects(t *testing.T) {
	disp := &scriptedDispatcherWT{}
	_, err := runInterpolate(t, disp, nil,
		`{"project_artifact_key":"missing/nope.tar.gz","description":"d","model":"m"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "not found in artifact store") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesInterpolate_MalformedTarball_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	art, err := store.Put(context.Background(), "test", "bad.tar.gz", []byte("not gzip"), "application/gzip")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	disp := &scriptedDispatcherWT{}
	_, err = runInterpolate(t, disp, store,
		`{"project_artifact_key":"`+art.Key+`","description":"d","model":"m"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
}

// --- classifyCompositionFile unit ----------------------------------------

func TestClassifyCompositionFile_DetectsTranscript(t *testing.T) {
	js := `(function () { const TRANSCRIPT = [{ text: "a", start: 0, end: 0.1 }]; })();`
	if got := classifyCompositionFile(js); got != compositionKindTranscript {
		t.Errorf("expected js_transcript, got %v", got)
	}
}

func TestClassifyCompositionFile_DetectsTextSlots(t *testing.T) {
	html := `<div><h1 class="title">A</h1><h2 class="subtitle">B</h2></div>`
	if got := classifyCompositionFile(html); got != compositionKindTextSlots {
		t.Errorf("expected html_text_slots, got %v", got)
	}
}

func TestClassifyCompositionFile_StatBlocks_DetectedAsTextSlots(t *testing.T) {
	html := `<div class="stat-value">47%</div><div class="stat-label">FOO</div>`
	if got := classifyCompositionFile(html); got != compositionKindTextSlots {
		t.Errorf("expected html_text_slots, got %v", got)
	}
}

func TestClassifyCompositionFile_Unknown(t *testing.T) {
	html := `<div><p>plain paragraph</p></div>`
	if got := classifyCompositionFile(html); got != compositionKindUnknown {
		t.Errorf("expected unknown_shape, got %v", got)
	}
}

func TestClassifyCompositionFile_TranscriptWinsWhenBothPresent(t *testing.T) {
	mixed := `<h1>Title</h1><script>const TRANSCRIPT = [{text:"a",start:0,end:0.1}];</script>`
	if got := classifyCompositionFile(mixed); got != compositionKindTranscript {
		t.Errorf("expected js_transcript precedence, got %v", got)
	}
}

// --- extractTextSlots / spliceTextSlots roundtrip ------------------------

func TestExtractTextSlots_PicksH1H2StatValueStatLabel(t *testing.T) {
	html := `<h1 class="title">HYPERFRAMES</h1>
<h2 class="subtitle">THE SURVEY FINDINGS</h2>
<div class="stat-value">47%</div>
<div class="stat-label">NEED MOTION GRAPHICS</div>`
	got := extractTextSlots(html)
	want := []string{"HYPERFRAMES", "THE SURVEY FINDINGS", "47%", "NEED MOTION GRAPHICS"}
	if len(got) != len(want) {
		t.Fatalf("expected %d slots, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("slot %d: got %q want %q", i, got[i], w)
		}
	}
}

func TestSpliceTextSlots_ReplacesPreservingAttributes(t *testing.T) {
	html := `<h1 class="title">HYPERFRAMES</h1><h2 class="subtitle">THE SURVEY</h2>`
	got := spliceTextSlots(html, []string{"EBPF OBSERVABILITY", "KERNEL TRACE FLOW"})
	if !strings.Contains(got, `<h1 class="title">EBPF OBSERVABILITY</h1>`) {
		t.Errorf("h1 replacement failed: %q", got)
	}
	if !strings.Contains(got, `<h2 class="subtitle">KERNEL TRACE FLOW</h2>`) {
		t.Errorf("h2 replacement failed: %q", got)
	}
}

func TestSpliceTextSlots_MissingReplacementsKeepOriginals(t *testing.T) {
	html := `<h1>A</h1><h2>B</h2>`
	got := spliceTextSlots(html, []string{"X"}) // only one replacement for two slots
	if !strings.Contains(got, `<h1>X</h1>`) {
		t.Errorf("first slot should be replaced: %q", got)
	}
	if !strings.Contains(got, `<h2>B</h2>`) {
		t.Errorf("second slot should be kept: %q", got)
	}
}

// --- parseNumberedSlots unit ---------------------------------------------

func TestParseNumberedSlots_StrictFormat(t *testing.T) {
	got := parseNumberedSlots("1: alpha\n2: beta\n3: gamma", 3)
	want := []string{"alpha", "beta", "gamma"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("slot %d: got %q want %q", i, got[i], w)
		}
	}
}

func TestParseNumberedSlots_OutOfOrder(t *testing.T) {
	got := parseNumberedSlots("3: gamma\n1: alpha\n2: beta", 3)
	if got[0] != "alpha" || got[2] != "gamma" {
		t.Errorf("expected indexed placement, got %v", got)
	}
}

func TestParseNumberedSlots_ExtraIgnored_MissingZero(t *testing.T) {
	got := parseNumberedSlots("1: a\n5: x", 3)
	if got[0] != "a" {
		t.Errorf("slot 0 should be 'a', got %q", got[0])
	}
	if got[1] != "" || got[2] != "" {
		t.Errorf("slots 1-2 should be empty, got %v", got)
	}
}

// --- parseTranscriptArray unit -------------------------------------------

func TestParseTranscriptArray_StrictJSON(t *testing.T) {
	raw := `[{"text":"hello","start":0.0,"end":0.3},{"text":"world","start":0.3,"end":0.6}]`
	got, err := parseTranscriptArray(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 || got[0].Text != "hello" || got[1].End != 0.6 {
		t.Errorf("unexpected parse: %v", got)
	}
}

func TestParseTranscriptArray_JSStyleKeys(t *testing.T) {
	raw := `[{text:"foo",start:0.0,end:0.1},{text:"bar",start:0.1,end:0.2}]`
	got, err := parseTranscriptArray(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d", len(got))
	}
}

func TestParseTranscriptArray_Empty_Rejects(t *testing.T) {
	if _, err := parseTranscriptArray(`[]`); err == nil {
		t.Errorf("expected error for empty array")
	}
	if _, err := parseTranscriptArray(`garbage`); err == nil {
		t.Errorf("expected error for garbage")
	}
}

// --- tarball roundtrip ---------------------------------------------------

func TestExtractWriteTarball_Roundtrip(t *testing.T) {
	original := makeFakeScaffoldTarball(t, map[string]string{
		"index.html":              "<html/>",
		"compositions/intro.html": "<template/>",
	})
	files, err := extractTarball(original)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	repacked, err := writeTarball(files)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	files2, err := extractTarball(repacked)
	if err != nil {
		t.Fatalf("round-trip extract: %v", err)
	}
	if len(files2) != 2 {
		t.Errorf("round-trip lost files: %d", len(files2))
	}
}

// --- happy path multi-file rewrite --------------------------------------

func TestHyperframesInterpolate_HappyPath_RewritesBothFiles(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	key := seedProject(t, store, map[string]string{
		"index.html":                 "<html/>",
		"compositions/intro.html":    `<h1 class="title">HYPERFRAMES</h1><h2 class="subtitle">SURVEY</h2>`,
		"compositions/captions.html": `<script>const TRANSCRIPT = [{ text: "We", start: 0.1, end: 0.3 }];</script>`,
		"assets/swiss-grid.svg":      "<svg/>",
	})
	// Scripted replies in handler-call order. makeFakeScaffoldTarball
	// writes paths sorted lexicographically, so captions.html comes
	// BEFORE intro.html in the tar — the dispatcher gets the
	// transcript-shape reply first, then the text-slot reply.
	disp := &scriptedDispatcherWT{
		replies: []string{
			// compositions/captions.html → fresh transcript (strict JSON)
			`[{"text":"eBPF","start":0.0,"end":0.3},{"text":"traces","start":0.3,"end":0.6}]`,
			// compositions/intro.html → text-slot rewrite (2 slots)
			"1: EBPF OBSERVABILITY\n2: KERNEL TRACE FLOW",
		},
	}
	raw, err := runInterpolate(t, disp, store, `{
		"project_artifact_key":"`+key+`",
		"description":"eBPF tracepoint observability for kernel rootkit detection",
		"model":"openrouter/openai/gpt-oss-120b:free",
		"duration_seconds":15
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	newKey, _ := out["project_artifact_key"].(string)
	if newKey == "" {
		t.Fatal("missing new project_artifact_key")
	}
	if newKey == key {
		t.Error("expected NEW project_artifact_key, got same as input")
	}
	// files_rewritten should list both compositions.
	rewritten, _ := out["files_rewritten"].([]any)
	if len(rewritten) != 2 {
		t.Errorf("expected 2 files_rewritten, got %d: %v", len(rewritten), rewritten)
	}
	// Round-trip: download the new tarball and verify the text changed.
	content, _, err := store.Get(context.Background(), newKey)
	if err != nil {
		t.Fatalf("get new artifact: %v", err)
	}
	files, err := extractTarball(content)
	if err != nil {
		t.Fatalf("extract new artifact: %v", err)
	}
	var sawIntroEdit, sawCaptionsEdit bool
	for _, f := range files {
		switch strings.TrimPrefix(f.Header.Name, "./") {
		case "compositions/intro.html":
			if strings.Contains(string(f.Data), "EBPF OBSERVABILITY") &&
				strings.Contains(string(f.Data), "KERNEL TRACE FLOW") {
				sawIntroEdit = true
			}
		case "compositions/captions.html":
			s := string(f.Data)
			if strings.Contains(s, `"eBPF"`) && strings.Contains(s, `"traces"`) {
				sawCaptionsEdit = true
			}
		}
	}
	if !sawIntroEdit {
		t.Error("intro.html should contain the new title/subtitle")
	}
	if !sawCaptionsEdit {
		t.Error("captions.html should contain the regenerated transcript")
	}
}

func TestHyperframesInterpolate_NoRecognizedShape_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	key := seedProject(t, store, map[string]string{
		"index.html":                "<html/>",
		"compositions/weird.html":   `<div><p>just a paragraph</p></div>`,
		"compositions/another.html": `<section><span>plain text</span></section>`,
	})
	disp := &scriptedDispatcherWT{} // no calls expected
	_, err := runInterpolate(t, disp, store, `{
		"project_artifact_key":"`+key+`",
		"description":"x","model":"m"
	}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "no files in the scaffold matched") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesInterpolate_EmptyArtifact_Rejects(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	art, err := store.Put(context.Background(), "test", "empty.tar.gz", []byte{}, "application/gzip")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	disp := &scriptedDispatcherWT{}
	_, err = runInterpolate(t, disp, store,
		`{"project_artifact_key":"`+art.Key+`","description":"d","model":"m"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for empty, got %v", err)
	}
}
