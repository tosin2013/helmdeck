package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/auth"
)

func newIssuer(t *testing.T) *auth.Issuer {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	iss, err := auth.NewIssuer([]byte(secret))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	iss := newIssuer(t)
	tok, err := iss.Issue("user-1", "Tosin", "claude-code", []auth.Scope{auth.ScopeSessions, auth.ScopePacks}, time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := iss.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("Subject = %q, want user-1", claims.Subject)
	}
	if claims.Client != "claude-code" {
		t.Errorf("Client = %q, want claude-code", claims.Client)
	}
	if !claims.Has(auth.ScopeSessions) {
		t.Error("expected ScopeSessions")
	}
	if claims.Has(auth.ScopeVault) {
		t.Error("did not expect ScopeVault")
	}
}

func TestAdminScopeImpliesAll(t *testing.T) {
	iss := newIssuer(t)
	tok, _ := iss.Issue("admin", "", "", []auth.Scope{auth.ScopeAdmin}, time.Hour)
	claims, _ := iss.Verify(tok)
	for _, s := range []auth.Scope{auth.ScopeSessions, auth.ScopePacks, auth.ScopeVault, auth.ScopeMCP, auth.ScopeProviders} {
		if !claims.Has(s) {
			t.Errorf("admin should imply %s", s)
		}
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	iss := newIssuer(t)
	tok, _ := iss.Issue("u", "", "", []auth.Scope{auth.ScopeSessions}, -1*time.Second)
	if _, err := iss.Verify(tok); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestWrongSecretRejected(t *testing.T) {
	iss1 := newIssuer(t)
	iss2 := newIssuer(t)
	tok, _ := iss1.Issue("u", "", "", []auth.Scope{auth.ScopeSessions}, time.Hour)
	if _, err := iss2.Verify(tok); err == nil {
		t.Fatal("expected token signed by other secret to be rejected")
	}
}

func TestSecretTooShort(t *testing.T) {
	if _, err := auth.NewIssuer([]byte("short")); err == nil {
		t.Fatal("expected NewIssuer to reject short secret")
	}
}

func TestMiddlewareProtectsMatchingPaths(t *testing.T) {
	iss := newIssuer(t)
	good, _ := iss.Issue("u", "", "", []auth.Scope{auth.ScopeSessions}, time.Hour)

	mw := auth.Middleware(iss, func(p string) bool { return strings.HasPrefix(p, "/api/v1/") })
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := auth.FromContext(r.Context())
		if c == nil {
			_, _ = w.Write([]byte("ok:")) // open path
			return
		}
		_, _ = w.Write([]byte("ok:" + c.Subject))
	}))

	cases := []struct {
		name      string
		path      string
		authz     string
		wantCode  int
		wantBody  string
		wantWWW   bool
	}{
		{"open path no token", "/healthz", "", http.StatusOK, "ok:", false},
		{"protected no token", "/api/v1/sessions", "", http.StatusUnauthorized, "missing_bearer", true},
		{"protected bad token", "/api/v1/sessions", "Bearer not.a.real.token", http.StatusUnauthorized, "invalid_token", true},
		{"protected good token", "/api/v1/sessions", "Bearer " + good, http.StatusOK, "ok:u", false},
		{"protected wrong scheme", "/api/v1/sessions", "Basic abcdef", http.StatusUnauthorized, "missing_bearer", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d, body = %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantBody) {
				t.Fatalf("body = %q, want substring %q", rr.Body.String(), tc.wantBody)
			}
			if tc.wantWWW && rr.Header().Get("WWW-Authenticate") == "" {
				t.Fatalf("expected WWW-Authenticate header on 401")
			}
		})
	}
}
