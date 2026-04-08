// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/auth"
)

// AuthAdminPassword is the password the login endpoint accepts. Set
// from the HELMDECK_ADMIN_PASSWORD env var by cmd/control-plane;
// empty string disables the login endpoint entirely (returns 503).
//
// This is intentionally a package-level variable rather than a field
// on Deps so the login handler doesn't carry plaintext through the
// router constructor signature. The control plane sets it once at
// startup and never touches it again.
var AuthAdminPassword string

// AuthAdminUsername defaults to "admin"; operators can override
// via HELMDECK_ADMIN_USERNAME if they prefer a different login.
var AuthAdminUsername = "admin"

// AuthLoginTokenTTL is how long the JWT minted at login lives.
// 12 hours matches the typical operator workday — long enough that
// the user doesn't get logged out mid-session, short enough that a
// stolen token expires within a day.
var AuthLoginTokenTTL = 12 * time.Hour

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string    `json:"token"`
	Subject   string    `json:"subject"`
	ExpiresAt time.Time `json:"expires_at"`
}

// registerAuthLoginRoute mounts POST /api/v1/auth/login. The route
// is intentionally OUTSIDE the JWT-protected prefix list (see
// IsProtectedPath) because the login form has no token to present
// — it's the path by which a token is acquired.
//
// When deps.Issuer is nil (dev mode with auth disabled) or
// AuthAdminPassword is empty, the endpoint returns 503 with a
// pointer at the env var operators need to set.
func registerAuthLoginRoute(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if deps.Issuer == nil {
			writeError(w, http.StatusServiceUnavailable, "auth_disabled",
				"JWT auth is not configured (HELMDECK_JWT_SECRET unset)")
			return
		}
		if AuthAdminPassword == "" {
			writeError(w, http.StatusServiceUnavailable, "login_disabled",
				"set HELMDECK_ADMIN_PASSWORD on the control plane to enable the login endpoint")
			return
		}

		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if strings.TrimSpace(req.Username) == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "missing_credentials",
				"username and password are required")
			return
		}

		// Constant-time compare on both fields so a timing attacker
		// can't enumerate valid usernames or guess passwords by
		// length. We compare against the expected username too in
		// case operators set a non-default HELMDECK_ADMIN_USERNAME.
		userOK := subtle.ConstantTimeCompare(
			[]byte(req.Username), []byte(AuthAdminUsername),
		) == 1
		passOK := subtle.ConstantTimeCompare(
			[]byte(req.Password), []byte(AuthAdminPassword),
		) == 1
		if !userOK || !passOK {
			writeError(w, http.StatusUnauthorized, "invalid_credentials",
				"username or password is incorrect")
			return
		}

		expires := time.Now().Add(AuthLoginTokenTTL)
		token, err := deps.Issuer.Issue(
			req.Username,                  // subject
			req.Username,                  // name
			"ui",                          // client
			[]auth.Scope{auth.ScopeAdmin}, // scopes
			AuthLoginTokenTTL,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "token_mint_failed", err.Error())
			return
		}

		writeJSON(w, http.StatusOK, loginResponse{
			Token:     token,
			Subject:   req.Username,
			ExpiresAt: expires,
		})
	})
}
