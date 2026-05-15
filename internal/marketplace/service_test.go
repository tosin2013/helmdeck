// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package marketplace

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestService_CatalogBeforeRefresh_ReturnsErrNotReady(t *testing.T) {
	s := NewService("https://example.com/", quietLogger())
	idx, meta, err := s.Catalog()
	if err != ErrNotReady {
		t.Errorf("got err=%v, want ErrNotReady", err)
	}
	if idx != nil {
		t.Errorf("expected nil index before refresh, got %+v", idx)
	}
	if meta.Source != "https://example.com/" {
		t.Errorf("meta.Source = %q", meta.Source)
	}
}

func TestService_RefreshPopulatesCatalog(t *testing.T) {
	body := `catalog_version: v1
packs:
  - name: cmd.upper
    version: v1
    path: packs/cmd.upper
    description: Uppercase a string
    author: tosin2013
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	s := NewService(srv.URL, quietLogger())
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	idx, meta, err := s.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(idx.Packs) != 1 {
		t.Errorf("expected 1 pack, got %d", len(idx.Packs))
	}
	if meta.FetchedAt.IsZero() {
		t.Errorf("FetchedAt should be set after a successful refresh")
	}
	if time.Since(meta.FetchedAt) > 5*time.Second {
		t.Errorf("FetchedAt should be very recent, got %v ago", time.Since(meta.FetchedAt))
	}
}

func TestService_RefreshError_PreservesPreviousIndex(t *testing.T) {
	// First fetch succeeds; second fetch returns 500. The cached
	// index should survive — operators should not see an empty
	// marketplace just because the upstream had a momentary blip.
	var serveErrors atomic.Bool
	body := `catalog_version: v1
packs:
  - name: cmd.upper
    version: v1
    path: packs/cmd.upper
    description: Test pack
    author: tester
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveErrors.Load() {
			http.Error(w, "upstream down", 500)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	s := NewService(srv.URL, quietLogger())
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	serveErrors.Store(true)
	err := s.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected second Refresh to error")
	}

	// Cached catalog should still be the first one.
	idx, meta, catErr := s.Catalog()
	if catErr != nil {
		t.Fatalf("Catalog after failed refresh: %v", catErr)
	}
	if len(idx.Packs) != 1 {
		t.Errorf("expected cached catalog to survive, got %d packs", len(idx.Packs))
	}
	if meta.LastError == "" {
		t.Errorf("meta.LastError should reflect the failed refresh")
	}
}

func TestService_Source_IsConfiguredURL(t *testing.T) {
	s := NewService("https://example.com/marketplace", quietLogger())
	if got := s.Source(); got != "https://example.com/marketplace" {
		t.Errorf("Source() = %q", got)
	}
}
