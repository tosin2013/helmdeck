// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func newArtifactRouter(t *testing.T, store packs.ArtifactStore) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test",
		ArtifactStore: store,
		// no Issuer → /api/v1/* auth disabled (dev mode)
	})
}

// TestArtifactDelete_RemovesAndReturns204 covers the manual-delete path
// (DELETE /api/v1/artifacts/{key...}): the artifact is gone from the
// store afterward and the response is 204.
func TestArtifactDelete_RemovesAndReturns204(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	art, err := store.Put(context.Background(), "content.ground", "grounded.md", []byte("# deck"), "text/markdown")
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	h := newArtifactRouter(t, store)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/artifacts/"+art.Key, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if _, _, err := store.Get(context.Background(), art.Key); err == nil {
		t.Errorf("artifact still present after DELETE")
	}
}

// TestArtifactDelete_UnknownKeyIsIdempotent — Delete is a no-op on an
// unknown key, so a missing artifact still returns 204 rather than 404.
func TestArtifactDelete_UnknownKeyIsIdempotent(t *testing.T) {
	h := newArtifactRouter(t, packs.NewMemoryArtifactStore())

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/artifacts/content.ground/does-not-exist.md", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE unknown key status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
}

// TestArtifactDelete_NoStoreReturns503 — when no artifact store is
// configured, the stub answers DELETE with 503 like the other artifact
// routes (rather than a misleading 204).
func TestArtifactDelete_NoStoreReturns503(t *testing.T) {
	h := newArtifactRouter(t, nil)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/artifacts/content.ground/x.md", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE with no store status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestArtifactDownload_HappyPath — proxy download streams bytes back
// to the browser with correct Content-Type and Content-Disposition.
// Images get inline disposition (so the browser previews them);
// everything else gets attachment.
func TestArtifactDownload_HappyPath(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	png, err := store.Put(context.Background(), "image.generate", "out.png",
		[]byte("\x89PNG\x0d\x0a\x1a\x0afake"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	h := newArtifactRouter(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/download/"+png.Key, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q; want image/png", got)
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(disp, "inline;") {
		t.Errorf("Content-Disposition = %q; want inline for image/*", disp)
	}
	if !strings.Contains(rec.Body.String(), "PNG") {
		t.Errorf("body should contain PNG magic header; got %q", rec.Body.String())
	}
}

// TestArtifactDownload_NonImageAttachment — non-image content-types
// must use attachment disposition so the browser downloads rather
// than tries to render them inline.
func TestArtifactDownload_NonImageAttachment(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	art, err := store.Put(context.Background(), "content.ground", "report.md",
		[]byte("# heading"), "text/markdown")
	if err != nil {
		t.Fatal(err)
	}
	h := newArtifactRouter(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/download/"+art.Key, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d", rec.Code)
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(disp, "attachment;") {
		t.Errorf("Content-Disposition = %q; want attachment for non-image", disp)
	}
	// Filename is the basename of the key (last "/" segment). The
	// store namespaces with `<pack>/<rand>-<name>`, so the basename
	// ends with `-report.md`.
	if !strings.HasSuffix(rec.Header().Get("Content-Disposition"), `-report.md"`) {
		t.Errorf("Content-Disposition should carry basename suffix -report.md; got %q",
			rec.Header().Get("Content-Disposition"))
	}
}

// TestArtifactDownload_NotFound — unknown key → 404 not_found from
// store.Get's error.
func TestArtifactDownload_NotFound(t *testing.T) {
	h := newArtifactRouter(t, packs.NewMemoryArtifactStore())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/download/missing/key.png", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestArtifactList_NoFilter — GET /api/v1/artifacts iterates the
// pack registry and collects every artifact. With one pack and one
// artifact, the response carries that single artifact under a stable
// shape with a proxy URL pointing at the download endpoint.
func TestArtifactList_NoFilter(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	art, err := store.Put(context.Background(), "image.generate", "a.png", []byte("img"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	reg := packs.NewPackRegistry()
	if err := reg.Register(&packs.Pack{Name: "image.generate", Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test",
		ArtifactStore: store,
		PackRegistry:  reg,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Artifacts []struct {
			Key  string `json:"key"`
			URL  string `json:"url"`
			Pack string `json:"pack"`
		} `json:"artifacts"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Artifacts) != 1 {
		t.Fatalf("count=%d artifacts=%+v; want 1", resp.Count, resp.Artifacts)
	}
	if resp.Artifacts[0].Key != art.Key {
		t.Errorf("key = %q; want %q", resp.Artifacts[0].Key, art.Key)
	}
	if resp.Artifacts[0].URL != "/api/v1/artifacts/download/"+art.Key {
		t.Errorf("URL = %q; want proxy URL", resp.Artifacts[0].URL)
	}
	if resp.Artifacts[0].Pack != "image.generate" {
		t.Errorf("pack = %q", resp.Artifacts[0].Pack)
	}
}

// TestArtifactList_PackFilter — ?pack=name short-circuits the registry
// iteration and queries the store for that pack only.
func TestArtifactList_PackFilter(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	if _, err := store.Put(context.Background(), "image.generate", "a.png", []byte("a"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "content.ground", "b.md", []byte("b"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	h := newArtifactRouter(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts?pack=image.generate", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Errorf("count with pack filter = %d; want 1 (only image.generate matches)", resp.Count)
	}
}

// TestArtifactList_LimitParam — ?limit caps the returned items.
// Values out of range (≤0 or >200) fall back to the default 50.
func TestArtifactList_LimitParam(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	for i := 0; i < 5; i++ {
		if _, err := store.Put(context.Background(), "p",
			"f", []byte{byte(i)}, "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}
	reg := packs.NewPackRegistry()
	if err := reg.Register(&packs.Pack{Name: "p", Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test",
		ArtifactStore: store,
		PackRegistry:  reg,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts?limit=2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp struct{ Count int }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Count != 2 {
		t.Errorf("limit=2 gave count=%d", resp.Count)
	}

	// Invalid limit falls back to default (50); we only have 5 so
	// we expect all 5 back. -1 and "abc" both go through the same
	// invalid-→-default branch.
	for _, bad := range []string{"-1", "abc", "0", "999"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts?limit="+bad, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var r struct{ Count int }
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		if r.Count != 5 {
			t.Errorf("limit=%q gave count=%d; want fallback to all 5", bad, r.Count)
		}
	}
}

// TestArtifactList_NoStoreReturns503 — GET /api/v1/artifacts without
// a store returns 503 with artifacts_unavailable.
func TestArtifactList_NoStoreReturns503(t *testing.T) {
	h := newArtifactRouter(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "artifacts_unavailable") {
		t.Errorf("body should mention artifacts_unavailable: %s", rec.Body.String())
	}
}
