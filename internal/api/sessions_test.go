package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/session/fake"
)

func newTestRouterWithFake(t *testing.T) (http.Handler, *fake.Runtime) {
	t.Helper()
	rt := fake.New()
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Runtime: rt,
		// Issuer left nil so existing handler tests don't need a token.
	})
	return h, rt
}

func TestSessionLifecycle(t *testing.T) {
	h, _ := newTestRouterWithFake(t)

	// Create
	body := bytes.NewBufferString(`{"label":"smoke","memory_limit":"512m","timeout_seconds":120}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var created sessionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create response missing id")
	}
	if created.Spec.Label != "smoke" {
		t.Fatalf("create label = %q, want smoke", created.Spec.Label)
	}
	if created.Spec.TimeoutSeconds != 120 {
		t.Fatalf("create timeout_seconds = %d, want 120", created.Spec.TimeoutSeconds)
	}

	// Get
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+created.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d", rr.Code)
	}

	// List
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), created.ID) {
		t.Fatalf("list missing created id, body = %s", rr.Body.String())
	}

	// Logs
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+created.ID+"/logs", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("logs status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "fake log") {
		t.Fatalf("logs body unexpected: %s", rr.Body.String())
	}

	// Delete
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+created.ID, nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rr.Code)
	}

	// Get after delete → 404
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+created.ID, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get-after-delete status = %d", rr.Code)
	}
}

func TestCreateBadJSON(t *testing.T) {
	h, _ := newTestRouterWithFake(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestGetUnknownSession(t *testing.T) {
	h, _ := newTestRouterWithFake(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/does-not-exist", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
