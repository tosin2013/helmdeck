package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/session/fake"
)

func newAuthedTestRouter(t *testing.T) (http.Handler, *auth.Issuer) {
	t.Helper()
	secret, _ := auth.GenerateSecret()
	iss, err := auth.NewIssuer([]byte(secret))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Runtime: fake.New(),
		Issuer:  iss,
	})
	return h, iss
}

func TestAuthRequiredOnAPI(t *testing.T) {
	h, _ := newAuthedTestRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("missing WWW-Authenticate")
	}
}

func TestAuthBypassedOnHealthAndVersion(t *testing.T) {
	h, _ := newAuthedTestRouter(t)
	for _, p := range []string{"/healthz", "/version"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", p, rr.Code)
		}
	}
}

func TestValidTokenAllowsAPI(t *testing.T) {
	h, iss := newAuthedTestRouter(t)
	tok, err := iss.Issue("u", "Tester", "ci", []auth.Scope{auth.ScopeSessions}, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"sessions"`) {
		t.Fatalf("body = %s, want sessions key", rr.Body.String())
	}
}
