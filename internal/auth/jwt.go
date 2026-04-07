// Package auth implements JWT bearer authentication for the helmdeck
// control plane. It issues short-lived signed tokens with a closed scope
// vocabulary (packs:*, sessions:*, providers:*, mcp:*, vault:*, admin)
// and exposes a net/http middleware that gates the /api/v1/* surface.
//
// See ADR 010 (security baseline) and PRD §7 (JWT bearer header on every
// management endpoint).
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	issuer = "helmdeck"

	// HeaderAuthorization is the header the middleware reads.
	HeaderAuthorization = "Authorization"

	// DefaultTTL is used when Issuer.Issue is called with ttl <= 0.
	DefaultTTL = 24 * time.Hour
)

// Scope is a closed set of authorization scopes. Tokens carry one or more.
type Scope string

const (
	ScopeAdmin     Scope = "admin"
	ScopeSessions  Scope = "sessions:*"
	ScopePacks     Scope = "packs:*"
	ScopeProviders Scope = "providers:*"
	ScopeMCP       Scope = "mcp:*"
	ScopeVault     Scope = "vault:*"
)

// Claims is the JWT body helmdeck issues and verifies.
type Claims struct {
	Subject string   `json:"sub"`
	Name    string   `json:"name,omitempty"`
	Client  string   `json:"client,omitempty"` // claude-code, claude-desktop, openclaw, gemini-cli, ...
	Scopes  []string `json:"scopes,omitempty"`
	jwt.RegisteredClaims
}

// Has reports whether the claim set carries the requested scope. The
// admin scope satisfies any check.
func (c *Claims) Has(s Scope) bool {
	for _, sc := range c.Scopes {
		if sc == string(ScopeAdmin) || sc == string(s) {
			return true
		}
	}
	return false
}

// Issuer mints and verifies tokens with a single shared HMAC secret.
type Issuer struct {
	secret []byte
}

// NewIssuer constructs an Issuer. The secret must be at least 32 bytes;
// shorter secrets are rejected to avoid weak HMAC keys.
func NewIssuer(secret []byte) (*Issuer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("auth: secret must be ≥32 bytes, got %d", len(secret))
	}
	return &Issuer{secret: secret}, nil
}

// GenerateSecret returns a fresh 32-byte random secret as a hex string.
// Used by control-plane startup when no HELMDECK_JWT_SECRET is set.
func GenerateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Issue mints a signed JWT for the given subject and scopes. ttl ≤ 0
// falls back to DefaultTTL.
func (i *Issuer) Issue(subject, name, client string, scopes []Scope, ttl time.Duration) (string, error) {
	if ttl == 0 {
		ttl = DefaultTTL
	}
	now := time.Now().UTC()
	scopeStrs := make([]string, len(scopes))
	for i, s := range scopes {
		scopeStrs[i] = string(s)
	}
	claims := Claims{
		Subject: subject,
		Name:    name,
		Client:  client,
		Scopes:  scopeStrs,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(i.secret)
}

// Verify parses and validates a signed token. The returned Claims are
// safe to read; the token's signature, expiry, and issuer have all been
// checked.
func (i *Issuer) Verify(raw string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(raw, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return i.secret, nil
	}, jwt.WithIssuer(issuer), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	c, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("auth: invalid token")
	}
	return c, nil
}

// contextKey is unexported so callers can only retrieve the claims via
// FromContext.
type contextKey struct{}

// FromContext returns the verified claims attached by Middleware, or nil
// if the request was unauthenticated (in which case the request never
// reached a protected handler anyway).
func FromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(contextKey{}).(*Claims)
	return c
}

// Middleware returns an http.Handler middleware that requires a valid
// bearer token on requests whose path is matched by protectedPath. Paths
// not matched by the predicate are passed through unchanged so /healthz
// and /version remain reachable for kubelet probes and ops dashboards.
func Middleware(iss *Issuer, protectedPath func(path string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !protectedPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			raw := bearerToken(r.Header.Get(HeaderAuthorization))
			if raw == "" {
				writeUnauthorized(w, "missing_bearer", "Authorization: Bearer <token> required")
				return
			}
			claims, err := iss.Verify(raw)
			if err != nil {
				writeUnauthorized(w, "invalid_token", err.Error())
				return
			}
			ctx := context.WithValue(r.Context(), contextKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(h string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func writeUnauthorized(w http.ResponseWriter, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="helmdeck"`)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + code + `","message":"` + escapeJSON(msg) + `"}` + "\n"))
}

func escapeJSON(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(s)
}
