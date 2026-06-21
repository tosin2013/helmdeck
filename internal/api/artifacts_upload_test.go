// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// artifacts_upload_test.go — tests for POST /api/v1/artifacts/upload.
// Covers the operator-facing artifact upload surface that closes the
// "I have an MP3 on my laptop, I want a narrated video" UX gap.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// buildUploadRequest builds a multipart/form-data POST that mirrors a
// browser's drag-drop upload. fieldName matches the handler's lookup;
// declaredContentType is what the browser would set on the part header
// (browsers usually populate this from the file's MIME type).
func buildUploadRequest(t *testing.T, fieldName, filename, declaredContentType string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	hdr := make(map[string][]string)
	hdr["Content-Disposition"] = []string{`form-data; name="` + fieldName + `"; filename="` + filename + `"`}
	if declaredContentType != "" {
		hdr["Content-Type"] = []string{declaredContentType}
	}
	part, err := w.CreatePart(hdr)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// --- happy path -----------------------------------------------------------

func TestArtifactUpload_AcceptsMP3_AndReturnsKey(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	h := newArtifactRouter(t, store)

	// Synthetic "MP3" — just bytes; the handler doesn't transcode,
	// it persists what the operator uploads.
	content := []byte("ID3\x03\x00\x00\x00\x00\x00\x00stub mp3 content here")
	req := buildUploadRequest(t, "file", "my-narration.mp3", "audio/mpeg", content)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ArtifactKey string `json:"artifact_key"`
		URL         string `json:"url"`
		Size        int64  `json:"size"`
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ArtifactKey == "" {
		t.Errorf("expected artifact_key populated, got empty; body=%s", rec.Body.String())
	}
	if !strings.HasPrefix(out.ArtifactKey, "operator-uploads/") {
		t.Errorf("artifact_key should be under operator-uploads/ namespace, got: %s", out.ArtifactKey)
	}
	if out.ContentType != "audio/mpeg" {
		t.Errorf("content_type should be passed through from upload, got: %s", out.ContentType)
	}
	if out.Size != int64(len(content)) {
		t.Errorf("size mismatch: got %d, want %d", out.Size, len(content))
	}
	if out.Filename != "my-narration.mp3" {
		t.Errorf("filename should be preserved, got: %s", out.Filename)
	}
	// Verify the upload actually landed in the store.
	got, art, err := store.Get(context.Background(), out.ArtifactKey)
	if err != nil {
		t.Fatalf("artifact not in store after upload: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("stored content doesn't match upload")
	}
	if art.URL == "" {
		t.Errorf("stored artifact has empty URL — store should expose signed URL")
	}
}

// --- content-type inference -----------------------------------------------

func TestArtifactUpload_NoContentType_InfersFromExtension(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	h := newArtifactRouter(t, store)

	// Browser didn't set Content-Type on the part. Handler should
	// fall back to mime.TypeByExtension on the filename.
	req := buildUploadRequest(t, "file", "track.mp3", "", []byte("stub mp3 bytes"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ContentType string `json:"content_type"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	// mime.TypeByExtension for .mp3 returns audio/mpeg on stdlib Go.
	if !strings.HasPrefix(out.ContentType, "audio/") {
		t.Errorf("expected inferred audio/* content-type for .mp3 filename, got: %q", out.ContentType)
	}
}

// --- error: missing file field --------------------------------------------

func TestArtifactUpload_MissingFileField_Returns400(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	h := newArtifactRouter(t, store)

	// Build a multipart form with no `file` field — just another field.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("notes", "wrong-field-name")
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// --- error: not a multipart request ---------------------------------------

func TestArtifactUpload_PlainJSON_Returns400(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	h := newArtifactRouter(t, store)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts/upload",
		strings.NewReader(`{"file":"not-multipart"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// --- error: no artifact store ---------------------------------------------

func TestArtifactUpload_NoStore_Returns503(t *testing.T) {
	h := newArtifactRouter(t, nil)

	req := buildUploadRequest(t, "file", "x.mp3", "audio/mpeg", []byte("stub"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// --- filename sanitization ------------------------------------------------

func TestSanitizeUploadFilename(t *testing.T) {
	cases := []struct {
		name     string
		in, want string
	}{
		{name: "passes_normal", in: "narration.mp3", want: "narration.mp3"},
		{name: "preserves_spaces", in: "My Audio.mp3", want: "My Audio.mp3"},
		{name: "strips_path_prefix", in: "/home/user/audio.mp3", want: "audio.mp3"},
		{name: "strips_control_chars", in: "audio\x00.mp3", want: "audio.mp3"},
		{name: "empty_returns_upload", in: "", want: "upload"},
		{name: "just_separator", in: "/", want: "upload"},
		{name: "only_dots", in: ".", want: "upload"},
		{name: "long_truncated", in: strings.Repeat("a", 250) + ".mp3", want: strings.Repeat("a", 200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeUploadFilename(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeUploadFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
