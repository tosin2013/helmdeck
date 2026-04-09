// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newSecurityRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// no Issuer => /api/v1/* auth disabled (dev mode)
	})
}

func TestSecuritySnapshot_Defaults(t *testing.T) {
	// Empty env: every optional setting should fall back to defaults.
	t.Setenv("HELMDECK_EGRESS_ALLOWLIST", "")
	t.Setenv("HELMDECK_SECCOMP_PROFILE", "")
	t.Setenv("HELMDECK_PIDS_LIMIT", "")
	t.Setenv("HELMDECK_ADMIN_PASSWORD", "")
	t.Setenv("HELMDECK_ADMIN_USERNAME", "")
	t.Setenv("HELMDECK_JWT_SECRET", "")
	t.Setenv("HELMDECK_OTEL_ENABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	h := newSecurityRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/security", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got securityPolicyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Egress.Allowlist) != 0 {
		t.Errorf("egress.allowlist=%v want empty", got.Egress.Allowlist)
	}
	if len(got.Egress.DefaultBlockList) == 0 {
		t.Errorf("egress.default_block_list is empty; expected the static T508 list")
	}
	if got.Sandbox.SeccompProfile != "" {
		t.Errorf("sandbox.seccomp_profile=%q want empty (docker default)", got.Sandbox.SeccompProfile)
	}
	if got.Sandbox.PidsLimit != 0 {
		t.Errorf("sandbox.pids_limit=%d want 0 (use default)", got.Sandbox.PidsLimit)
	}
	if got.Sandbox.PidsLimitDefault != 1024 {
		t.Errorf("sandbox.pids_limit_default=%d want 1024", got.Sandbox.PidsLimitDefault)
	}
	if got.Auth.AdminLoginEnabled {
		t.Errorf("auth.admin_login_enabled=true with no password set")
	}
	if got.Auth.AdminUsername != "admin" {
		t.Errorf("auth.admin_username=%q want admin", got.Auth.AdminUsername)
	}
	if got.Auth.JWTSecretConfigured {
		t.Errorf("auth.jwt_secret_configured=true with no secret set")
	}
	if got.Telemetry.OTelEnabled {
		t.Errorf("telemetry.otel_enabled=true with no env vars set")
	}
}

func TestSecuritySnapshot_Populated(t *testing.T) {
	t.Setenv("HELMDECK_EGRESS_ALLOWLIST", "10.20.0.0/16, 192.168.50.0/24")
	t.Setenv("HELMDECK_SECCOMP_PROFILE", "/etc/helmdeck/seccomp.json")
	t.Setenv("HELMDECK_PIDS_LIMIT", "512")
	t.Setenv("HELMDECK_ADMIN_PASSWORD", "topsecret")
	t.Setenv("HELMDECK_ADMIN_USERNAME", "operator")
	t.Setenv("HELMDECK_JWT_SECRET", "abc123")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318")

	h := newSecurityRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/security", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var got securityPolicyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Egress.Allowlist) != 2 ||
		got.Egress.Allowlist[0] != "10.20.0.0/16" ||
		got.Egress.Allowlist[1] != "192.168.50.0/24" {
		t.Errorf("egress.allowlist=%v", got.Egress.Allowlist)
	}
	if got.Sandbox.SeccompProfile != "/etc/helmdeck/seccomp.json" {
		t.Errorf("sandbox.seccomp_profile=%q", got.Sandbox.SeccompProfile)
	}
	if got.Sandbox.PidsLimit != 512 {
		t.Errorf("sandbox.pids_limit=%d", got.Sandbox.PidsLimit)
	}
	if !got.Auth.AdminLoginEnabled || got.Auth.AdminUsername != "operator" {
		t.Errorf("auth=%+v", got.Auth)
	}
	if !got.Auth.JWTSecretConfigured {
		t.Errorf("auth.jwt_secret_configured=false")
	}
	if !got.Telemetry.OTelEnabled || got.Telemetry.OTelEndpoint != "http://otel-collector:4318" {
		t.Errorf("telemetry=%+v", got.Telemetry)
	}
}
