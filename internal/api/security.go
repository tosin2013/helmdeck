// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

// securityPolicyResponse is the JSON shape returned by GET /api/v1/security.
// It is a snapshot of the security-relevant configuration the control plane
// applied at startup. Env vars are read at request time, but the underlying
// guards/runtime were configured once at boot — the snapshot is therefore
// "what is currently in effect" only as long as the env vars haven't been
// edited mid-process. Today the control plane re-reads them on restart, so
// the snapshot is honest for any in-flight session.
type securityPolicyResponse struct {
	Egress   egressPolicy   `json:"egress"`
	Sandbox  sandboxPolicy  `json:"sandbox"`
	Auth     authPolicy     `json:"auth"`
	Telemetry telemetryPolicy `json:"telemetry"`
}

type egressPolicy struct {
	// Allowlist is the parsed CIDR list from HELMDECK_EGRESS_ALLOWLIST.
	// Empty means "block list only".
	Allowlist []string `json:"allowlist"`
	// DefaultBlockList is the static set of CIDRs the egress guard
	// blocks unless an entry is explicitly allowlisted. Operators
	// almost never want sessions reaching these from a sandbox.
	DefaultBlockList []string `json:"default_block_list"`
	// Description is a one-liner the panel renders so operators
	// understand what the guard does without diving into ADRs.
	Description string `json:"description"`
}

type sandboxPolicy struct {
	// SeccompProfile is HELMDECK_SECCOMP_PROFILE — empty string means
	// "use docker's curated default profile". A non-empty value is the
	// host path to a custom JSON profile that overrides the default.
	SeccompProfile string `json:"seccomp_profile"`
	// PidsLimit is the per-session process cap. 0 means "use the
	// hard-coded default of 1024" (defaultPidsLimit in
	// internal/session/docker/runtime.go).
	PidsLimit int64 `json:"pids_limit"`
	// PidsLimitDefault is the hard-coded baseline so the UI can show
	// "1024 (default)" instead of just "1024".
	PidsLimitDefault int64 `json:"pids_limit_default"`
	// Description is a one-liner.
	Description string `json:"description"`
}

type authPolicy struct {
	// AdminLoginEnabled is true when HELMDECK_ADMIN_PASSWORD is set
	// (the Management UI login endpoint accepts credentials). When
	// false the endpoint returns 503 and the only path to a JWT is
	// the CLI -mint-token flag.
	AdminLoginEnabled bool `json:"admin_login_enabled"`
	// AdminUsername is HELMDECK_ADMIN_USERNAME or "admin" by default.
	AdminUsername string `json:"admin_username"`
	// JWTSecretConfigured is true when HELMDECK_JWT_SECRET is set.
	// False means the control plane is running with an ephemeral
	// signing key that vanishes on restart, invalidating every minted
	// token — almost certainly a misconfiguration in production.
	JWTSecretConfigured bool `json:"jwt_secret_configured"`
}

type telemetryPolicy struct {
	// OTelEnabled is true when HELMDECK_OTEL_ENABLED=true OR
	// OTEL_EXPORTER_OTLP_ENDPOINT is set (telemetry.Init's gate).
	OTelEnabled bool `json:"otel_enabled"`
	// OTelEndpoint is the configured collector endpoint, or empty.
	OTelEndpoint string `json:"otel_endpoint"`
}

// defaultEgressBlockList mirrors the static block list set in
// internal/security/egress.go's New() function. It is duplicated
// here so the API surface is decoupled from the package internals
// — if egress.go grows new defaults, this list (and the panel's
// "what's blocked by default" tooltip) needs a matching update.
var defaultEgressBlockList = []string{
	"169.254.169.254/32",
	"169.254.0.0/16",
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"0.0.0.0/8",
	"224.0.0.0/4",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
}

// defaultPidsLimit mirrors internal/session/docker/runtime.go's
// constant of the same name. Same decoupling rationale as the
// egress block list above.
const defaultPidsLimit int64 = 1024

// registerSecurityRoutes mounts GET /api/v1/security (T609). The
// Security Policies panel reads this endpoint to render the current
// sandbox baseline, egress allowlist, and auth posture.
func registerSecurityRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /api/v1/security", func(w http.ResponseWriter, r *http.Request) {
		resp := securityPolicyResponse{
			Egress: egressPolicy{
				Allowlist:        parseAllowlist(os.Getenv("HELMDECK_EGRESS_ALLOWLIST")),
				DefaultBlockList: defaultEgressBlockList,
				Description: "Application-layer SSRF defense. Every pack handler that " +
					"makes an outbound HTTP call goes through the egress guard, which " +
					"resolves the target host and rejects it if it lands in the default " +
					"block list (cloud metadata, RFC 1918, loopback, multicast). The " +
					"allowlist exempts CIDRs operators legitimately need to reach.",
			},
			Sandbox: sandboxPolicy{
				SeccompProfile:   os.Getenv("HELMDECK_SECCOMP_PROFILE"),
				PidsLimit:        parsePidsLimit(os.Getenv("HELMDECK_PIDS_LIMIT")),
				PidsLimitDefault: defaultPidsLimit,
				Description: "T509 sandbox baseline. Every browser-sidecar session " +
					"container runs as nonroot, drops every Linux capability, has its " +
					"process count capped, and applies a seccomp filter (docker's " +
					"curated default unless an operator points HELMDECK_SECCOMP_PROFILE " +
					"at a custom JSON file).",
			},
			Auth: authPolicy{
				AdminLoginEnabled:   os.Getenv("HELMDECK_ADMIN_PASSWORD") != "",
				AdminUsername:       envOr("HELMDECK_ADMIN_USERNAME", "admin"),
				JWTSecretConfigured: os.Getenv("HELMDECK_JWT_SECRET") != "",
			},
			Telemetry: telemetryPolicy{
				OTelEnabled: os.Getenv("HELMDECK_OTEL_ENABLED") == "true" ||
					os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "",
				OTelEndpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			},
		}
		writeJSON(w, http.StatusOK, resp)
	})
}

func parseAllowlist(raw string) []string {
	if raw == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parsePidsLimit(raw string) int64 {
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
