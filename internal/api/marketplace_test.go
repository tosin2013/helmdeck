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
	"github.com/tosin2013/helmdeck/internal/packs"
)

// marketplaceTestRegistry returns the *packs.Registry the Installer
// hot-loads into. Tests that don't actually install never look at it;
// it just has to exist for NewInstaller's signature.
func marketplaceTestRegistry() *packs.Registry {
	return packs.NewPackRegistry()
}

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

// TestMarketplacePackDetail_NilService_Returns503 — the per-pack
// detail handler must respect the same on/off semantics as the
// catalog: nil service → 503 with marketplace_disabled.
func TestMarketplacePackDetail_NilService_Returns503(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/packs/cmd.upper", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestMarketplacePackDetail_NotInCatalog_Returns404 — service is wired
// and the catalog is loaded, but the requested pack name is not in it.
// The handler must surface a 404 with pack_not_in_catalog (operators
// shouldn't get a generic 500 for a typo in the URL).
func TestMarketplacePackDetail_NotInCatalog_Returns404(t *testing.T) {
	body := `catalog_version: v1
packs:
  - name: cmd.upper
    version: v1
    path: packs/cmd.upper
    description: Uppercase a string
    author: tosin2013
`
	upstream := stubMarketplaceUpstream(t, body)
	svc := marketplace.NewService(upstream.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := newMarketplaceRouter(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/packs/does-not-exist", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "pack_not_in_catalog") {
		t.Errorf("body should mention pack_not_in_catalog: %s", w.Body.String())
	}
}

// TestMarketplacePackDetail_NotReady_Returns503 — service wired but
// Refresh never called → Catalog() returns ErrNotReady which the
// handler translates to 503 marketplace_not_ready.
func TestMarketplacePackDetail_NotReady_Returns503(t *testing.T) {
	svc := marketplace.NewService("https://example.com/", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := newMarketplaceRouter(t, svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/packs/foo", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not_ready") {
		t.Errorf("body should mention not_ready: %s", w.Body.String())
	}
}

// TestMarketplaceInstall_NilInstaller_Returns503 — install routes are
// 503 with marketplace_install_disabled when MarketplaceInstaller is
// unwired (the most common deployment shape, where packs come from
// the in-tree builtin set only).
func TestMarketplaceInstall_NilInstaller_Returns503(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	cases := []struct {
		name, method, path, body string
	}{
		{"install", http.MethodPost, "/api/v1/marketplace/install", `{"name":"cmd.upper"}`},
		{"uninstall", http.MethodPost, "/api/v1/marketplace/uninstall", `{"name":"cmd.upper"}`},
		{"installed", http.MethodGet, "/api/v1/marketplace/installed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", w.Code)
			}
			if !strings.Contains(w.Body.String(), "marketplace_install_disabled") {
				t.Errorf("body should mention marketplace_install_disabled: %s", w.Body.String())
			}
		})
	}
}

// TestMarketplaceInstall_MissingName_Returns400 — wire an Installer
// (via the same scaffold internal/marketplace tests use) so the route
// passes the disabled check, then send a body with empty name. The
// handler must return 400 invalid_input.
func TestMarketplaceInstall_MissingName_Returns400(t *testing.T) {
	body := `catalog_version: v1
packs: []
`
	upstream := stubMarketplaceUpstream(t, body)
	svc := marketplace.NewService(upstream.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Use a real (but empty) Installer — install dir is a tempdir, no
	// packs are scaffolded so an actual install attempt would fail with
	// ErrPackNotInCatalog, but we never get there because the handler
	// rejects empty name first.
	reg := marketplaceTestRegistry()
	inst := marketplace.NewInstaller(svc, reg, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := NewRouter(Deps{
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:              "test",
		Marketplace:          svc,
		MarketplaceInstaller: inst,
	})

	cases := []struct{ name, path, body string }{
		{"install", "/api/v1/marketplace/install", `{"name":""}`},
		{"uninstall", "/api/v1/marketplace/uninstall", `{"name":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "invalid_input") {
				t.Errorf("body should mention invalid_input: %s", w.Body.String())
			}
		})
	}
}

// TestMarketplaceInstall_BadJSON_Returns400 — malformed body must be
// rejected with invalid_input, not a 500.
func TestMarketplaceInstall_BadJSON_Returns400(t *testing.T) {
	svc := marketplace.NewService("https://example.com", slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := marketplaceTestRegistry()
	inst := marketplace.NewInstaller(svc, reg, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := NewRouter(Deps{
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:              "test",
		Marketplace:          svc,
		MarketplaceInstaller: inst,
	})
	for _, path := range []string{"/api/v1/marketplace/install", "/api/v1/marketplace/uninstall"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{not-json`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
	}
}

// TestMarketplaceInstalled_EmptyList — Installed() returns nothing
// fresh, but the handler must still emit a well-shaped JSON object
// (not null) so the UI can render an empty state.
func TestMarketplaceInstalled_EmptyList(t *testing.T) {
	svc := marketplace.NewService("https://example.com", slog.New(slog.NewTextHandler(io.Discard, nil)))
	reg := marketplaceTestRegistry()
	inst := marketplace.NewInstaller(svc, reg, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := NewRouter(Deps{
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:              "test",
		Marketplace:          svc,
		MarketplaceInstaller: inst,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/marketplace/installed", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Installed []marketplace.InstalledPack `json:"installed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Installed) != 0 {
		t.Errorf("Installed = %+v; want empty", resp.Installed)
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
