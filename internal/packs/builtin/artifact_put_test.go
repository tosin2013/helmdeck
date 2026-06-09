// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// runArtifactPut invokes the handler with a memory-backed artifact
// store. Returns the parsed output AND the store so tests can
// cross-check the stored bytes against what the handler reported.
func runArtifactPut(t *testing.T, input string) (map[string]any, *packs.MemoryArtifactStore, error) {
	t.Helper()
	pack := ArtifactPut()
	store := packs.NewMemoryArtifactStore()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		return nil, store, err
	}
	var out map[string]any
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("unmarshal pack output: %v", uerr)
	}
	if verr := pack.OutputSchema.Validate(raw); verr != nil {
		t.Fatalf("output failed declared schema: %v", verr)
	}
	return out, store, nil
}

func TestArtifactPut_HappyPath_BlogKind(t *testing.T) {
	out, store, err := runArtifactPut(t, `{
		"content": "# Hello\n\nA blog post.",
		"kind": "blog"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	key, _ := out["artifact_key"].(string)
	if key == "" {
		t.Fatal("expected non-empty artifact_key")
	}
	if !strings.HasPrefix(key, "artifact.put/") {
		t.Errorf("artifact_key = %q, want prefix artifact.put/", key)
	}
	if got := out["content_type"]; got != "text/markdown" {
		t.Errorf("content_type = %v, want text/markdown", got)
	}
	if got := out["filename"]; got != "content.md" {
		t.Errorf("filename = %v, want content.md", got)
	}
	if got, _ := out["size"].(float64); int(got) != len("# Hello\n\nA blog post.") {
		t.Errorf("size = %v, want %d", got, len("# Hello\n\nA blog post."))
	}

	body, art, gerr := store.Get(context.Background(), key)
	if gerr != nil {
		t.Fatalf("store.Get: %v", gerr)
	}
	if string(body) != "# Hello\n\nA blog post." {
		t.Errorf("stored body = %q, want original markdown", string(body))
	}
	if art.ContentType != "text/markdown" {
		t.Errorf("stored content_type = %q", art.ContentType)
	}
}

func TestArtifactPut_KindDefaults(t *testing.T) {
	cases := []struct {
		kind            string
		wantContentType string
		wantFilename    string
	}{
		{"markdown", "text/markdown", "content.md"},
		{"transcript", "text/plain", "transcript.txt"},
		{"summary", "text/markdown", "summary.md"},
		{"json", "application/json", "content.json"},
		{"text", "text/plain", "content.txt"},
		{"html", "text/html", "content.html"},
		{"csv", "text/csv", "content.csv"},
		{"binary", "application/octet-stream", "content.bin"},
		// Unknown kind falls back to text defaults.
		{"unknown-kind", "text/plain", "content.txt"},
		// Empty kind falls back to text defaults.
		{"", "text/plain", "content.txt"},
		// Case-insensitive.
		{"BLOG", "text/markdown", "content.md"},
		// Surrounding whitespace tolerated.
		{"  json  ", "application/json", "content.json"},
	}
	for _, tc := range cases {
		t.Run("kind="+tc.kind, func(t *testing.T) {
			input := `{"content": "x", "kind": ` + strconvQuote(tc.kind) + `}`
			out, _, err := runArtifactPut(t, input)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if got := out["content_type"]; got != tc.wantContentType {
				t.Errorf("content_type = %v, want %v", got, tc.wantContentType)
			}
			if got := out["filename"]; got != tc.wantFilename {
				t.Errorf("filename = %v, want %v", got, tc.wantFilename)
			}
		})
	}
}

func TestArtifactPut_ExplicitContentTypeOverridesKind(t *testing.T) {
	out, _, err := runArtifactPut(t, `{
		"content": "{\"k\":1}",
		"kind": "blog",
		"content_type": "application/json"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := out["content_type"]; got != "application/json" {
		t.Errorf("content_type = %v, want application/json (explicit override)", got)
	}
}

func TestArtifactPut_ExplicitFilenameOverridesKind(t *testing.T) {
	out, _, err := runArtifactPut(t, `{
		"content": "x",
		"kind": "blog",
		"filename": "my-post.md"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := out["filename"]; got != "my-post.md" {
		t.Errorf("filename = %v, want my-post.md (explicit override)", got)
	}
}

func TestArtifactPut_CustomNamespace(t *testing.T) {
	out, _, err := runArtifactPut(t, `{
		"content": "x",
		"kind": "blog",
		"namespace": "blog.publish"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	key, _ := out["artifact_key"].(string)
	if !strings.HasPrefix(key, "blog.publish/") {
		t.Errorf("artifact_key = %q, want prefix blog.publish/", key)
	}
	if got := out["namespace"]; got != "blog.publish" {
		t.Errorf("namespace = %v, want blog.publish", got)
	}
}

func TestArtifactPut_Base64Encoding(t *testing.T) {
	raw := []byte{0x00, 0x01, 0xfe, 0xff, 'h', 'i'}
	encoded := base64.StdEncoding.EncodeToString(raw)
	out, store, err := runArtifactPut(t, `{
		"content": `+strconvQuote(encoded)+`,
		"kind": "binary",
		"encoding": "base64"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	key, _ := out["artifact_key"].(string)
	body, _, _ := store.Get(context.Background(), key)
	if string(body) != string(raw) {
		t.Errorf("decoded body mismatch: got %v, want %v", body, raw)
	}
}

func TestArtifactPut_FilenameSanitization(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/absolute/path.md", "absolute/path.md"},
		{"../../etc/passwd", "etc/passwd"},
		{"a/./b/../c.md", "a/c.md"},
		{".", "content.txt"},
		{"..", "content.txt"},
		{"", "content.md"}, // empty → kind default (blog → content.md)
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			input := `{"content":"x","kind":"blog","filename":` + strconvQuote(tc.input) + `}`
			out, _, err := runArtifactPut(t, input)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if got := out["filename"]; got != tc.want {
				t.Errorf("filename = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestArtifactPut_ErrorPaths(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		store    packs.ArtifactStore
		wantCode packs.ErrorCode
	}{
		{
			name:     "missing content",
			input:    `{"kind":"blog"}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "empty content",
			input:    `{"content":"","kind":"blog"}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "no artifact store wired",
			input:    `{"content":"x"}`,
			store:    nil,
			wantCode: packs.CodeArtifactFailed,
		},
		{
			name:     "bad base64",
			input:    `{"content":"not-base64!!!", "encoding":"base64"}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "unsupported encoding",
			input:    `{"content":"x", "encoding":"rot13"}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "malformed json input",
			input:    `{bad}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := ArtifactPut()
			ec := &packs.ExecutionContext{
				Pack:      pack,
				Input:     json.RawMessage(tc.input),
				Artifacts: tc.store,
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			_, err := pack.Handler(context.Background(), ec)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var pe *packs.PackError
			if !errors.As(err, &pe) {
				t.Fatalf("expected PackError, got %T: %v", err, err)
			}
			if pe.Code != tc.wantCode {
				t.Errorf("code = %s, want %s", pe.Code, tc.wantCode)
			}
		})
	}
}

func TestArtifactPut_StoreFailureReturnsArtifactFailed(t *testing.T) {
	pack := ArtifactPut()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(`{"content":"x","kind":"blog"}`),
		Artifacts: failingStore{},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	_, err := pack.Handler(context.Background(), ec)
	if err == nil {
		t.Fatal("expected error from failing store")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PackError, got %T", err)
	}
	if pe.Code != packs.CodeArtifactFailed {
		t.Errorf("code = %s, want CodeArtifactFailed", pe.Code)
	}
}

// failingStore returns an error from Put so we can exercise the
// CodeArtifactFailed branch without making MemoryArtifactStore fail.
type failingStore struct{}

func (failingStore) Put(_ context.Context, _, _ string, _ []byte, _ string) (packs.Artifact, error) {
	return packs.Artifact{}, errors.New("backend offline")
}
func (failingStore) ListForPack(_ context.Context, _ string) ([]packs.Artifact, error) {
	return nil, nil
}
func (failingStore) Get(_ context.Context, _ string) ([]byte, packs.Artifact, error) {
	return nil, packs.Artifact{}, errors.New("not found")
}
func (failingStore) ListAll(_ context.Context) ([]packs.Artifact, error) { return nil, nil }
func (failingStore) Delete(_ context.Context, _ string) error            { return nil }

// strconvQuote produces a JSON-safe quoted string for inline test
// fixtures without pulling in fmt for one use site.
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
