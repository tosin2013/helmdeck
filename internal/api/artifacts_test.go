// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
