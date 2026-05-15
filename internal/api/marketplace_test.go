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

	"github.com/tosin2013/helmdeck/internal/marketplace"
)

// stubMarketplaceUpstream serves a canned index.yaml the marketplace
// service fetches. Returns the same body for every request so the
// handler can call refresh repeatedly during a single test.
func stubMarketplaceUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newMarketplaceRouter(t *testing.T, svc *marketplace.Service) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:     "test",
		Marketplace: svc,
		// no Issuer → /api/v1/* auth disabled (dev mode)
	})
}

func TestMarketplaceCatalog_NotReady_Returns503(t *testing.T) {
	// Service constructed but Refresh never called.
	svc := marketplace.NewService("https://example.com/", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := newMarketplaceRouter(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/catalog", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not_ready") {
		t.Errorf("body should mention not-ready: %s", w.Body.String())
	}
}

func TestMarketplaceCatalog_AfterRefresh_ReturnsIndex(t *testing.T) {
	body := `catalog_version: v1
packs:
  - name: cmd.upper
    version: v1
    path: packs/cmd.upper
    description: Uppercase a string
    author: tosin2013
    category: developer-tools
    tags: [example]
  - name: cmd.lower
    version: v1
    path: packs/cmd.lower
    description: Lowercase a string
    author: tester
`
	upstream := stubMarketplaceUpstream(t, body)
	svc := marketplace.NewService(upstream.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := newMarketplaceRouter(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/catalog", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Index struct {
			Packs []marketplace.IndexEntry `json:"packs"`
		} `json:"index"`
		Meta marketplace.CatalogMeta `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Index.Packs) != 2 {
		t.Errorf("got %d packs, want 2", len(resp.Index.Packs))
	}
	if resp.Meta.Source != upstream.URL {
		t.Errorf("meta.source = %q", resp.Meta.Source)
	}
}

func TestMarketplaceRefresh_FetchesAndReturnsIndex(t *testing.T) {
	body := `catalog_version: v1
packs:
  - name: cmd.upper
    version: v1
    path: packs/cmd.upper
    description: Uppercase
    author: tester
`
	upstream := stubMarketplaceUpstream(t, body)
	// Construct the service WITHOUT calling Refresh — the endpoint
	// should populate the cache on first hit.
	svc := marketplace.NewService(upstream.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := newMarketplaceRouter(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/refresh", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	// Catalog endpoint should now return the index.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/catalog", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("post-refresh GET status = %d", w2.Code)
	}
}

func TestMarketplaceRefresh_UpstreamFailure_Returns502(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", 500)
	}))
	defer upstream.Close()

	svc := marketplace.NewService(upstream.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := newMarketplaceRouter(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/marketplace/refresh", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (bad gateway = upstream failed)", w.Code)
	}
}

func TestMarketplaceCatalog_NilService_Returns503Disabled(t *testing.T) {
	// Deps.Marketplace == nil → handler returns 503 with
	// marketplace_disabled code so operators know the surface is off.
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// Marketplace deliberately nil
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/catalog", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "marketplace_disabled") {
		t.Errorf("body should mention marketplace_disabled: %s", w.Body.String())
	}
}
