package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestRouter(t *testing.T, version string) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: version,
	})
}

func TestHealthz(t *testing.T) {
	h := newTestRouter(t, "test")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %q, want status:ok", rr.Body.String())
	}
}

func TestVersion(t *testing.T) {
	h := newTestRouter(t, "v1.2.3")
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"version":"v1.2.3"`) {
		t.Fatalf("body = %q, want version:v1.2.3", rr.Body.String())
	}
}

func TestSessionsRouteWithoutRuntimeReturns503(t *testing.T) {
	h := newTestRouter(t, "test")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "runtime_unavailable") {
		t.Fatalf("body = %q, want runtime_unavailable", rr.Body.String())
	}
}
