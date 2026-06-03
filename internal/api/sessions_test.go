package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/session"
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

// TestSessions_UnavailableWhenNoRuntime — both list and per-id paths
// return 503 with runtime_unavailable when Deps.Runtime is nil. The
// router registers two stub routes so neither shape 404s.
func TestSessions_UnavailableWhenNoRuntime(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	for _, path := range []string{"/api/v1/sessions", "/api/v1/sessions/any-id"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "runtime_unavailable") {
			t.Errorf("%s body should mention runtime_unavailable: %s", path, rr.Body.String())
		}
	}
}

// TestSessions_LogsUnknownSession — logs on an unknown id returns 404.
func TestSessions_LogsUnknownSession(t *testing.T) {
	h, _ := newTestRouterWithFake(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/missing/logs", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestSessions_DeleteEvictsCDPClient — terminating a session also
// evicts the CDP client cache so the next handle on the same id
// doesn't reuse a now-dead browser connection.
func TestSessions_DeleteEvictsCDPClient(t *testing.T) {
	rt := fake.New()
	s, _ := rt.Create(context.Background(), session.Spec{Image: "browser:1"})
	cdpFactory := &stubCDPFactory{}
	h := NewRouter(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		Runtime:    rt,
		CDPFactory: cdpFactory,
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+s.ID, nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rr.Code)
	}
	if len(cdpFactory.evicted) != 1 || cdpFactory.evicted[0] != s.ID {
		t.Errorf("expected Evict(%s) once, got %+v", s.ID, cdpFactory.evicted)
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
