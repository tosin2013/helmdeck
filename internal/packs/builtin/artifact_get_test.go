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

func runArtifactGet(t *testing.T, store packs.ArtifactStore, input string) (map[string]any, error) {
	t.Helper()
	pack := ArtifactGet()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		return nil, err
	}
	if verr := pack.OutputSchema.Validate(raw); verr != nil {
		t.Fatalf("output failed declared schema: %v", verr)
	}
	var out map[string]any
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("unmarshal output: %v", uerr)
	}
	return out, nil
}

func seedArtifact(t *testing.T, store *packs.MemoryArtifactStore, ns, name, contentType string, body []byte) string {
	t.Helper()
	art, err := store.Put(context.Background(), ns, name, body, contentType)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return art.Key
}

func TestArtifactGet_TextReturnsUTF8(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	key := seedArtifact(t, store, "blog.publish", "post.md", "text/markdown", []byte("# Hello"))

	out, err := runArtifactGet(t, store, `{"artifact_key":`+strconvQuote(key)+`}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := out["encoding"]; got != "utf-8" {
		t.Errorf("encoding = %v, want utf-8", got)
	}
	if got := out["content"]; got != "# Hello" {
		t.Errorf("content = %v, want # Hello", got)
	}
	if got := out["content_type"]; got != "text/markdown" {
		t.Errorf("content_type = %v", got)
	}
	if got := out["filename"]; got != "post.md" {
		t.Errorf("filename = %v, want post.md", got)
	}
	if got := out["namespace"]; got != "blog.publish" {
		t.Errorf("namespace = %v, want blog.publish", got)
	}
}

func TestArtifactGet_BinaryReturnsBase64(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	raw := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG magic
	key := seedArtifact(t, store, "image.generate", "out.png", "image/png", raw)

	out, err := runArtifactGet(t, store, `{"artifact_key":`+strconvQuote(key)+`}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := out["encoding"]; got != "base64" {
		t.Errorf("encoding = %v, want base64 for image/png", got)
	}
	contentStr, _ := out["content"].(string)
	decoded, derr := base64.StdEncoding.DecodeString(contentStr)
	if derr != nil {
		t.Fatalf("decode base64: %v", derr)
	}
	if string(decoded) != string(raw) {
		t.Errorf("decoded body mismatch")
	}
}

func TestArtifactGet_TextContentTypeMatrix(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	textTypes := []string{
		"text/plain", "text/markdown", "text/html", "text/csv",
		"application/json", "application/yaml", "application/xml",
		"application/ld+json", "application/svg+xml",
	}
	for _, ct := range textTypes {
		t.Run(ct, func(t *testing.T) {
			key := seedArtifact(t, store, "test", "f", ct, []byte("hello"))
			out, err := runArtifactGet(t, store, `{"artifact_key":`+strconvQuote(key)+`}`)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if out["encoding"] != "utf-8" {
				t.Errorf("content_type=%s: encoding=%v, want utf-8", ct, out["encoding"])
			}
		})
	}
}

func TestArtifactGet_BinaryContentTypeMatrix(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	binaryTypes := []string{
		"image/png", "image/jpeg", "audio/mpeg", "video/mp4",
		"application/octet-stream", "application/pdf", "",
	}
	for _, ct := range binaryTypes {
		t.Run(ct, func(t *testing.T) {
			key := seedArtifact(t, store, "test", "f", ct, []byte{0x00, 0xff})
			out, err := runArtifactGet(t, store, `{"artifact_key":`+strconvQuote(key)+`}`)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if out["encoding"] != "base64" {
				t.Errorf("content_type=%q: encoding=%v, want base64", ct, out["encoding"])
			}
		})
	}
}

func TestArtifactGet_ForceEncoding(t *testing.T) {
	store := packs.NewMemoryArtifactStore()

	// Force base64 on text content.
	textKey := seedArtifact(t, store, "test", "f.md", "text/markdown", []byte("hi"))
	out, err := runArtifactGet(t, store, `{"artifact_key":`+strconvQuote(textKey)+`, "encoding":"base64"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out["encoding"] != "base64" {
		t.Errorf("forced base64: got encoding=%v", out["encoding"])
	}

	// Force utf-8 on a content_type that would default to base64
	// (caller asserts they know the bytes are readable).
	binKey := seedArtifact(t, store, "test", "raw", "application/octet-stream", []byte("plain text"))
	out, err = runArtifactGet(t, store, `{"artifact_key":`+strconvQuote(binKey)+`, "encoding":"utf-8"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if out["encoding"] != "utf-8" {
		t.Errorf("forced utf-8: got encoding=%v", out["encoding"])
	}
	if out["content"] != "plain text" {
		t.Errorf("content = %v", out["content"])
	}
}

func TestArtifactGet_ErrorPaths(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		store    packs.ArtifactStore
		wantCode packs.ErrorCode
	}{
		{
			name:     "missing artifact_key",
			input:    `{}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "empty artifact_key",
			input:    `{"artifact_key":""}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "whitespace artifact_key",
			input:    `{"artifact_key":"   "}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "no store wired",
			input:    `{"artifact_key":"x/y"}`,
			store:    nil,
			wantCode: packs.CodeArtifactFailed,
		},
		{
			name:     "key not found",
			input:    `{"artifact_key":"missing/key"}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeArtifactFailed,
		},
		{
			name:     "malformed json",
			input:    `{bad`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := ArtifactGet()
			ec := &packs.ExecutionContext{
				Pack:      pack,
				Input:     json.RawMessage(tc.input),
				Artifacts: tc.store,
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			_, err := pack.Handler(context.Background(), ec)
			if err == nil {
				t.Fatal("expected error")
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

func TestArtifactGet_SplitArtifactKey(t *testing.T) {
	cases := []struct {
		key          string
		wantFilename string
		wantNS       string
	}{
		{"blog.publish/abc123-post.md", "post.md", "blog.publish"},
		{"av.validate/def456-validation.json", "validation.json", "av.validate"},
		{"no-namespace-just-this", "no-namespace-just-this", ""},
		{"namespace/nodashhere", "nodashhere", "namespace"}, // no dash → whole rest is filename
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			filename, ns := splitArtifactKey(tc.key)
			if filename != tc.wantFilename {
				t.Errorf("filename = %q, want %q", filename, tc.wantFilename)
			}
			if ns != tc.wantNS {
				t.Errorf("namespace = %q, want %q", ns, tc.wantNS)
			}
		})
	}
}

func TestArtifactGet_RoundTripWithArtifactPut(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	// 1. Put.
	putPack := ArtifactPut()
	putEC := &packs.ExecutionContext{
		Pack:      putPack,
		Input:     json.RawMessage(`{"content":"# Round trip\n\nbody.","kind":"blog","filename":"rt.md"}`),
		Artifacts: store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	putRaw, err := putPack.Handler(context.Background(), putEC)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	var putOut map[string]any
	json.Unmarshal(putRaw, &putOut)
	key := putOut["artifact_key"].(string)

	// 2. Get.
	out, err := runArtifactGet(t, store, `{"artifact_key":`+strconvQuote(key)+`}`)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := out["content"]; got != "# Round trip\n\nbody." {
		t.Errorf("round-tripped content = %q", got)
	}
	if !strings.HasSuffix(out["filename"].(string), "rt.md") {
		t.Errorf("filename = %v, want suffix rt.md", out["filename"])
	}
}
