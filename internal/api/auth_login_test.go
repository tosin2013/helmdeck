// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/auth"
)

// newLoginRouter constructs a router with a real auth.Issuer so the
// login endpoint can mint tokens. Tests reset the package-level
// AuthAdminPassword var via t.Cleanup so they don't leak state into
// each other.
func newLoginRouter(t *testing.T, password string) http.Handler {
	t.Helper()
	prev := AuthAdminPassword
	AuthAdminPassword = password
	t.Cleanup(func() { AuthAdminPassword = prev })

	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	issuer, err := auth.NewIssuer(secret)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Issuer:  issuer,
	})
}

func doLogin(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestAuthLogin_HappyPath(t *testing.T) {
	h := newLoginRouter(t, "hunter2")
	rr := doLogin(t, h, `{"username":"admin","password":"hunter2"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp loginResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" || resp.Subject != "admin" {
		t.Errorf("response shape wrong: %+v", resp)
	}
	if resp.ExpiresAt.IsZero() {
		t.Error("expires_at not set")
	}
}

func TestAuthLogin_WrongPassword(t *testing.T) {
	h := newLoginRouter(t, "hunter2")
	rr := doLogin(t, h, `{"username":"admin","password":"wrong"}`)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid_credentials") {
		t.Errorf("expected typed error code: %s", rr.Body.String())
	}
}

func TestAuthLogin_WrongUsername(t *testing.T) {
	h := newLoginRouter(t, "hunter2")
	rr := doLogin(t, h, `{"username":"root","password":"hunter2"}`)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthLogin_CustomUsername(t *testing.T) {
	prev := AuthAdminUsername
	AuthAdminUsername = "tosin"
	t.Cleanup(func() { AuthAdminUsername = prev })

	h := newLoginRouter(t, "hunter2")
	rr := doLogin(t, h, `{"username":"tosin","password":"hunter2"}`)
	if rr.Code != http.StatusOK {
		t.Errorf("custom username should work: %d", rr.Code)
	}
}

func TestAuthLogin_DisabledWhenPasswordEmpty(t *testing.T) {
	h := newLoginRouter(t, "")
	rr := doLogin(t, h, `{"username":"admin","password":"anything"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when password unset, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "login_disabled") {
		t.Errorf("expected login_disabled code: %s", rr.Body.String())
	}
}

func TestAuthLogin_DisabledWhenIssuerNil(t *testing.T) {
	prev := AuthAdminPassword
	AuthAdminPassword = "hunter2"
	t.Cleanup(func() { AuthAdminPassword = prev })

	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// no Issuer
	})
	rr := doLogin(t, h, `{"username":"admin","password":"hunter2"}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when issuer nil, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "auth_disabled") {
		t.Errorf("expected auth_disabled code: %s", rr.Body.String())
	}
}

func TestAuthLogin_MissingFields(t *testing.T) {
	h := newLoginRouter(t, "hunter2")
	cases := []string{
		`{}`,
		`{"username":"admin"}`,
		`{"password":"hunter2"}`,
		`{"username":"","password":""}`,
	}
	for _, body := range cases {
		rr := doLogin(t, h, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %q, got %d", body, rr.Code)
		}
	}
}

func TestAuthLogin_BadJSON(t *testing.T) {
	h := newLoginRouter(t, "hunter2")
	rr := doLogin(t, h, `{not json}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed JSON, got %d", rr.Code)
	}
}

func TestAuthLogin_PathIsPublic(t *testing.T) {
	// IsProtectedPath must NOT include /api/v1/auth/login — the
	// login form has no token to present.
	if IsProtectedPath("/api/v1/auth/login") {
		t.Error("/api/v1/auth/login must be unauthenticated")
	}
	if !IsProtectedPath("/api/v1/sessions") {
		t.Error("/api/v1/sessions must remain protected")
	}
}
