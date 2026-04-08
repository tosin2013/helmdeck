// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newWebRouter constructs a router with no API deps wired so the
// SPA fallback / web embed routes are the only thing handling
// non-API paths.
func newWebRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
}

func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestWebRoute_ServesIndexAtRoot(t *testing.T) {
	h := newWebRouter(t)
	rr := doGet(t, h, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	// The placeholder index.html is what's bundled in the source
	// tree before `make web-build` runs. Either the placeholder or
	// the real built bundle should be served — both contain the
	// word "Helmdeck" in the title.
	if !strings.Contains(body, "Helmdeck") && !strings.Contains(body, "helmdeck") {
		t.Errorf("body should contain Helmdeck branding: %s", body[:min(200, len(body))])
	}
}

func TestWebRoute_SPAFallbackToIndex(t *testing.T) {
	// react-router-dom client routes like /sessions, /vault should
	// fall back to index.html so the JS router can take over.
	h := newWebRouter(t)
	for _, path := range []string{"/sessions", "/vault", "/audit", "/packs/some-pack"} {
		rr := doGet(t, h, path)
		if rr.Code != http.StatusOK {
			t.Errorf("path %s: status=%d (should fallback to 200 index.html)", path, rr.Code)
		}
	}
}

func TestWebRoute_DoesNotShadowAPI(t *testing.T) {
	// /api/* paths must NOT be served by the SPA fallback — they
	// have to fall through to the real API routes (which return
	// 404 in this test because none are registered, but the point
	// is the body should not be HTML).
	h := newWebRouter(t)
	rr := doGet(t, h, "/api/v1/sessions")
	body := rr.Body.String()
	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "<!DOCTYPE html>") {
		t.Errorf("API path %s served HTML — SPA fallback shadowed the API: %s",
			"/api/v1/sessions", body[:min(200, len(body))])
	}
}

func TestWebRoute_HealthzBypassesSPA(t *testing.T) {
	h := newWebRouter(t)
	rr := doGet(t, h, "/healthz")
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz should return 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "ok") {
		t.Errorf("healthz body wrong: %s", rr.Body.String())
	}
}

func TestIsAPIPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/", false},
		{"/sessions", false},
		{"/vault", false},
		{"/api/v1/sessions", true},
		{"/api/v1/auth/login", true},
		{"/v1/chat/completions", true},
		{"/a2a/v1/tasks", true},
		{"/.well-known/agent.json", true},
		{"/healthz", true},
		{"/version", true},
		{"/static/main.js", false},
	}
	for _, tc := range cases {
		got := isAPIPath(tc.path)
		if got != tc.want {
			t.Errorf("isAPIPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
