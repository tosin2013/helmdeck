// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// makeFakeScaffoldTarball builds a gzipped tar archive with the given
// {path → content} mapping. Used by the happy-path tests to stub the
// hyperframes-init.sh output without needing a real sidecar +
// hyperframes CLI install.
//
// Paths are written in sorted order so the resulting tar is
// deterministic — consumers (like hyperframes_interpolate_test) script
// dispatcher replies in tar-order, which requires the same ordering
// every run regardless of Go's randomized map iteration.
func makeFakeScaffoldTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		content := files[path]
		hdr := &tar.Header{
			Name:     path,
			Mode:     0644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

// scaffoldExecScript captures and scripts the exec calls for the
// hyperframes.scaffold tests. Matches by command shape — the init
// script invocation and the `cat <tarball>` read-back.
type scaffoldExecScript struct {
	calls []session.ExecRequest
	// scriptExit overrides the init script's exit code. Default 0 (success).
	scriptExit int
	// scriptStderr is what the test returns on the init script call.
	scriptStderr string
	// tarballBytes is what the `cat` call returns as stdout.
	tarballBytes []byte
	// failCat makes the read-back cat fail (simulates sidecar disk error).
	failCat bool
}

func (s *scaffoldExecScript) fn(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	s.calls = append(s.calls, req)
	// hyperframes-init.sh invocation.
	if len(req.Cmd) >= 1 && strings.Contains(req.Cmd[0], "hyperframes-init.sh") {
		if s.scriptExit != 0 {
			return session.ExecResult{ExitCode: s.scriptExit, Stderr: []byte(s.scriptStderr)}, nil
		}
		return session.ExecResult{}, nil
	}
	// `cat <tarball-path>` read-back.
	if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.Contains(req.Cmd[2], hyperframesScaffoldOutputPath) {
		if s.failCat {
			return session.ExecResult{ExitCode: 1, Stderr: []byte("cat: no such file or directory")}, nil
		}
		return session.ExecResult{Stdout: s.tarballBytes}, nil
	}
	return session.ExecResult{}, nil
}

func runScaffold(t *testing.T, exec *scaffoldExecScript, store *packs.MemoryArtifactStore, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesScaffold()
	if store == nil {
		store = packs.NewMemoryArtifactStore()
	}
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Session:   &session.Session{ID: "sess-scaffold"},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:      exec.fn,
		Artifacts: store,
	}
	return pack.Handler(context.Background(), ec)
}

// --- input validation ----------------------------------------------------

func TestHyperframesScaffold_MissingExample_Rejects(t *testing.T) {
	exec := &scaffoldExecScript{}
	_, err := runScaffold(t, exec, nil, `{}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "example is required") {
		t.Errorf("unexpected message: %v", pe.Message)
	}
}

func TestHyperframesScaffold_EmptyExample_Rejects(t *testing.T) {
	exec := &scaffoldExecScript{}
	_, err := runScaffold(t, exec, nil, `{"example":"   "}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
}

func TestHyperframesScaffold_UnknownAspectRatio_Rejects(t *testing.T) {
	exec := &scaffoldExecScript{}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid","aspect_ratio":"21:9"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "aspect_ratio") {
		t.Errorf("error should mention aspect_ratio: %v", pe.Message)
	}
}

// --- happy path ----------------------------------------------------------

func TestHyperframesScaffold_HappyPath(t *testing.T) {
	fakeTar := makeFakeScaffoldTarball(t, map[string]string{
		"index.html":                 "<html><body><div id=\"root\"/></body></html>",
		"compositions/intro.html":    "<template><h1>HYPERFRAMES</h1></template>",
		"compositions/graphics.html": "<template><div>47%</div></template>",
		"compositions/captions.html": "<template><script>const TRANSCRIPT=[];</script></template>",
		"assets/swiss-grid.svg":      "<svg/>",
		"AGENTS.md":                  "# Project",
		"package.json":               "{}",
	})
	exec := &scaffoldExecScript{tarballBytes: fakeTar}
	raw, err := runScaffold(t, exec, nil, `{"example":"swiss-grid"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Exec sequence: init script + cat.
	sawInit := false
	sawCat := false
	for _, c := range exec.calls {
		if len(c.Cmd) >= 1 && strings.Contains(c.Cmd[0], "hyperframes-init.sh") {
			sawInit = true
			// Assert flags propagated correctly.
			joined := strings.Join(c.Cmd, " ")
			if !strings.Contains(joined, "--example=swiss-grid") {
				t.Errorf("expected --example=swiss-grid in cmd: %v", c.Cmd)
			}
			if !strings.Contains(joined, "--resolution=landscape") {
				t.Errorf("expected --resolution=landscape (1080p+16:9 default), got: %v", c.Cmd)
			}
			if !strings.Contains(joined, "--output="+hyperframesScaffoldOutputPath) {
				t.Errorf("expected --output=%s, got: %v", hyperframesScaffoldOutputPath, c.Cmd)
			}
		}
		if len(c.Cmd) >= 3 && c.Cmd[0] == "sh" && c.Cmd[1] == "-c" &&
			strings.Contains(c.Cmd[2], hyperframesScaffoldOutputPath) {
			sawCat = true
		}
	}
	if !sawInit {
		t.Error("expected hyperframes-init.sh invocation")
	}
	if !sawCat {
		t.Error("expected cat <tarball> read-back")
	}

	// Output assertions.
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["project_artifact_key"].(string); !ok {
		t.Errorf("missing project_artifact_key in output: %v", out)
	}
	if out["example_used"] != "swiss-grid" {
		t.Errorf("expected example_used=swiss-grid, got: %v", out["example_used"])
	}
	if out["cli_preset_used"] != "landscape" {
		t.Errorf("expected cli_preset_used=landscape, got: %v", out["cli_preset_used"])
	}
	if w, _ := out["width"].(float64); w != 1920 {
		t.Errorf("expected width=1920, got: %v", out["width"])
	}
	// editable_slots manifest should list the 3 compositions/*.html files.
	slots, ok := out["editable_slots"].(map[string]any)
	if !ok {
		t.Fatalf("missing editable_slots in output: %v", out)
	}
	comps, ok := slots["compositions"].([]any)
	if !ok {
		t.Fatalf("expected compositions to be a slice, got: %T (%v)", slots["compositions"], slots["compositions"])
	}
	if len(comps) != 3 {
		t.Errorf("expected 3 compositions/*.html entries, got %d: %v", len(comps), comps)
	}
}

func TestHyperframesScaffold_VerticalShorts_MapsToPortraitPreset(t *testing.T) {
	fakeTar := makeFakeScaffoldTarball(t, map[string]string{
		"index.html":              "<html/>",
		"compositions/intro.html": "<template/>",
	})
	exec := &scaffoldExecScript{tarballBytes: fakeTar}
	raw, err := runScaffold(t, exec, nil, `{"example":"tiktok-follow","aspect_ratio":"9:16"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if out["cli_preset_used"] != "portrait" {
		t.Errorf("expected cli_preset_used=portrait, got: %v", out["cli_preset_used"])
	}
}

func TestHyperframesScaffold_4kSquare_MapsToSquare4kPreset(t *testing.T) {
	fakeTar := makeFakeScaffoldTarball(t, map[string]string{"index.html": "<html/>"})
	exec := &scaffoldExecScript{tarballBytes: fakeTar}
	raw, err := runScaffold(t, exec, nil, `{"example":"swiss-grid","resolution":"4k","aspect_ratio":"1:1"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if out["cli_preset_used"] != "square-4k" {
		t.Errorf("expected cli_preset_used=square-4k, got: %v", out["cli_preset_used"])
	}
}

// --- error paths ---------------------------------------------------------

func TestHyperframesScaffold_InvalidExample_ExitCode1_IsCallerFix(t *testing.T) {
	exec := &scaffoldExecScript{
		scriptExit:   1,
		scriptStderr: `Failed to scaffold example "nonexistent": Item "nonexistent" not found in registry. Available: warm-grain, play-mode, swiss-grid, vignelli, decision-tree, ...`,
	}
	_, err := runScaffold(t, exec, nil, `{"example":"nonexistent"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput (caller-fixable), got %v", err)
	}
	if !strings.Contains(pe.Message, "not found in registry") {
		t.Errorf("expected upstream registry list to surface in error, got: %v", pe.Message)
	}
}

func TestHyperframesScaffold_ScriptOtherFailure_IsHandlerFailed(t *testing.T) {
	// Exit codes 2-5 are real failures (usage, scaffold malformed,
	// init failed, tar failed) — not caller-fixable.
	exec := &scaffoldExecScript{
		scriptExit:   4,
		scriptStderr: "hyperframes: ENOENT: no such file or directory, /home/helmdeck/.config/hyperframes",
	}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed, got %v", err)
	}
}

func TestHyperframesScaffold_EmptyTarball_IsHandlerFailed(t *testing.T) {
	exec := &scaffoldExecScript{tarballBytes: nil}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed for empty tarball, got %v", err)
	}
	if !strings.Contains(pe.Message, "empty tarball") {
		t.Errorf("expected explicit empty-tarball diagnosis: %v", pe.Message)
	}
}

func TestHyperframesScaffold_CatFailure_IsHandlerFailed(t *testing.T) {
	exec := &scaffoldExecScript{failCat: true}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed for cat failure, got %v", err)
	}
}

// --- enumerateScaffoldedSlots unit tests --------------------------------

func TestEnumerateScaffoldedSlots_CanonicalSwissGridShape(t *testing.T) {
	tar := makeFakeScaffoldTarball(t, map[string]string{
		"index.html":                 "<html/>",
		"compositions/intro.html":    "<template/>",
		"compositions/graphics.html": "<template/>",
		"compositions/captions.html": "<template/>",
		"assets/swiss-grid.svg":      "<svg/>",
		"hyperframes.json":           "{}",
		"meta.json":                  "{}",
		"package.json":               "{}",
		"AGENTS.md":                  "# Project",
		"CLAUDE.md":                  "# Claude",
	})
	slots, err := enumerateScaffoldedSlots(tar)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	comps, _ := slots["compositions"].([]any)
	if len(comps) != 3 {
		t.Errorf("expected 3 compositions, got %d: %v", len(comps), comps)
	}
	others, _ := slots["other_files"].([]any)
	// 7 other paths: index.html + 1 svg + 3 json + 2 md = 7
	if len(others) != 7 {
		t.Errorf("expected 7 other_files, got %d: %v", len(others), others)
	}
}

func TestEnumerateScaffoldedSlots_MalformedTarball_ReturnsError(t *testing.T) {
	_, err := enumerateScaffoldedSlots([]byte("not really gzip"))
	if err == nil {
		t.Fatal("expected error for malformed tarball")
	}
}

func TestEnumerateScaffoldedSlots_LeadingDotSlash_Normalized(t *testing.T) {
	// `tar -czf X -C $SCAFFOLD .` produces paths like `./index.html`
	// per BSD tar conventions on some hosts. Confirm the strip
	// keeps the classification working.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, p := range []string{"./index.html", "./compositions/intro.html"} {
		hdr := &tar.Header{Name: p, Mode: 0644, Size: 0, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz.Close: %v", err)
	}
	slots, err := enumerateScaffoldedSlots(buf.Bytes())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	comps, _ := slots["compositions"].([]any)
	if len(comps) != 1 {
		t.Errorf("expected 1 composition after leading-./ strip, got %d: %v", len(comps), comps)
	}
}

func TestEnumerateScaffoldedSlots_SkipsDirectoryEntries(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// Directory entry (no content)
	if err := tw.WriteHeader(&tar.Header{Name: "compositions/", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatalf("dir header: %v", err)
	}
	// Real file entry
	if err := tw.WriteHeader(&tar.Header{Name: "compositions/intro.html", Mode: 0644, Size: 0, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("file header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz.Close: %v", err)
	}
	slots, err := enumerateScaffoldedSlots(buf.Bytes())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	comps, _ := slots["compositions"].([]any)
	if len(comps) != 1 {
		t.Errorf("expected only the file, not the dir, got: %v", comps)
	}
}

// --- artifact upload integration ----------------------------------------

// --- audio_url passthrough (v0.28.4 fix for silent-video bug) -----------

func TestHyperframesScaffold_AudioURL_Empty_OmitsAudioFlag(t *testing.T) {
	// Default (empty audio_url) — verify --audio is NOT in the init
	// command argv. Closes a regression risk where unconditionally
	// adding --audio= would break callers who don't want narration.
	fakeTar := makeFakeScaffoldTarball(t, map[string]string{"index.html": "<html/>"})
	exec := &scaffoldExecScript{tarballBytes: fakeTar}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid","audio_url":""}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	for _, c := range exec.calls {
		if len(c.Cmd) >= 1 && strings.Contains(c.Cmd[0], "hyperframes-init.sh") {
			joined := strings.Join(c.Cmd, " ")
			if strings.Contains(joined, "--audio=") {
				t.Errorf("--audio should not appear when audio_url is empty, got: %v", c.Cmd)
			}
		}
	}
}

func TestHyperframesScaffold_AudioURL_HappyPath_PassesAudioFlag(t *testing.T) {
	// Spin up a tiny HTTP server returning fake MP3 bytes. Verify the
	// pack fetches the URL, stages the bytes via execWithStdin, and
	// passes --audio=<staged-path> to the init script.
	audioBytes := []byte("ID3 fake-mp3-bytes-for-test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audioBytes)
	}))
	defer srv.Close()

	fakeTar := makeFakeScaffoldTarball(t, map[string]string{"index.html": "<html/>"})
	exec := &scaffoldExecScript{tarballBytes: fakeTar}
	input := `{"example":"swiss-grid","audio_url":"` + srv.URL + `/podcast.mp3"}`
	_, err := runScaffold(t, exec, nil, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Assert: --audio appears in the init call, AND a stdin-write call
	// landed the audio bytes at the conventional staging path.
	sawAudioFlag := false
	sawAudioStage := false
	for _, c := range exec.calls {
		if len(c.Cmd) >= 1 && strings.Contains(c.Cmd[0], "hyperframes-init.sh") {
			joined := strings.Join(c.Cmd, " ")
			if strings.Contains(joined, "--audio="+hyperframesScaffoldAudioPath) {
				sawAudioFlag = true
			}
		}
		// execWithStdin issues `sh -c 'cat > <path>'` + Stdin payload.
		if len(c.Cmd) >= 3 && c.Cmd[0] == "sh" && c.Cmd[1] == "-c" &&
			strings.Contains(c.Cmd[2], hyperframesScaffoldAudioPath) && len(c.Stdin) > 0 {
			if string(c.Stdin) == string(audioBytes) {
				sawAudioStage = true
			}
		}
	}
	if !sawAudioFlag {
		t.Error("expected --audio=<staged-path> in init argv")
	}
	if !sawAudioStage {
		t.Error("expected stdin-write call staging the audio bytes to the sidecar")
	}
}

func TestHyperframesScaffold_AudioURL_404_RejectsInvalidInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	exec := &scaffoldExecScript{}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid","audio_url":"`+srv.URL+`/missing.mp3"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "HTTP 404") {
		t.Errorf("expected message to surface the HTTP status, got: %s", pe.Message)
	}
}

func TestHyperframesScaffold_AudioURL_Empty200_RejectsInvalidInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 200 OK with empty body — a real misconfigured presigned URL
		// occasionally produces this.
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	exec := &scaffoldExecScript{}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid","audio_url":"`+srv.URL+`/empty.mp3"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "empty body") {
		t.Errorf("expected empty-body message, got: %s", pe.Message)
	}
}

func TestHyperframesScaffold_AudioURL_OversizeRejectsInvalidInput(t *testing.T) {
	// Stream 1 byte over the cap.
	tooBig := make([]byte, hyperframesScaffoldMaxAudioSize+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(tooBig)
	}))
	defer srv.Close()
	exec := &scaffoldExecScript{}
	_, err := runScaffold(t, exec, nil, `{"example":"swiss-grid","audio_url":"`+srv.URL+`/huge.mp3"}`)
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "cap") {
		t.Errorf("expected cap message, got: %s", pe.Message)
	}
}

func TestHyperframesScaffold_ArtifactUploadedWithExampleName(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	fakeTar := makeFakeScaffoldTarball(t, map[string]string{"index.html": "<html/>"})
	exec := &scaffoldExecScript{tarballBytes: fakeTar}
	raw, err := runScaffold(t, exec, store, `{"example":"code-snippet-monokai"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	key, _ := out["project_artifact_key"].(string)
	if !strings.Contains(key, "code-snippet-monokai") {
		t.Errorf("expected artifact key to include example name, got: %s", key)
	}
	// Round-trip: ensure the artifact is actually retrievable.
	content, _, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !bytes.Equal(content, fakeTar) {
		t.Error("uploaded artifact content doesn't match the tarball bytes")
	}
}
