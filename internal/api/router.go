// Package api wires HTTP handlers for the helmdeck control plane.
//
// T101 shipped /healthz and /version. T105 adds the /api/v1/sessions
// surface backed by a [session.Runtime].
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/audit"

	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/inject"
	"github.com/tosin2013/helmdeck/internal/keystore"
	"github.com/tosin2013/helmdeck/internal/mcp"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// KeyTester pings a provider to verify a freshly-decrypted key works.
// Injected into Deps so tests can stub the network call.
type KeyTester func(ctx context.Context, client *http.Client, provider, apiKey string) error

// Deps bundles the runtime dependencies the router needs. Passing them as
// a struct (rather than positional args) keeps the router constructor
// stable as new subsystems land in later phases.
type Deps struct {
	Logger     *slog.Logger
	Version    string
	Runtime    session.Runtime  // optional; nil disables /api/v1/sessions
	Issuer     *auth.Issuer     // optional; nil disables /api/v1/* JWT enforcement (dev mode)
	Audit      audit.Writer     // optional; nil uses audit.Discard
	CDPFactory CDPClientFactory  // optional; nil disables /api/v1/browser/*
	Executor   session.Executor // optional; nil disables /api/v1/desktop/*
	Gateway          *gateway.Registry // optional; nil disables /v1/* AI facade
	DB               *sql.DB           // optional; nil disables /api/v1/providers/stats and any other endpoint that needs raw SQL
	GatewayChain     *gateway.Chain    // optional; when set, /v1/* dispatches via the chain
	// RehydrateGateway re-runs gateway.HydrateFromKeystore so a key
	// added/rotated/deleted via /api/v1/providers/keys takes effect
	// without a control-plane restart (T202a hot reload). nil = no-op.
	RehydrateGateway func() error
	Keys         *keystore.Store  // optional; nil disables /api/v1/providers/keys
	KeyTester    KeyTester        // optional; defaults to keystore.TestProviderKey
	PackRegistry *packs.Registry // optional; nil disables /api/v1/packs
	PackEngine   *packs.Engine   // optional; nil disables /api/v1/packs dispatch
	MCPRegistry  *mcp.Registry   // optional; nil disables /api/v1/mcp/servers
	Vault        *vault.Store    // optional; nil disables /api/v1/vault/*
	Injector     *inject.Injector // optional; nil disables vault injection on /api/v1/browser/navigate
}

// IsProtectedPath returns true for paths the auth middleware must guard.
// Exported so tests and the control plane can share the same predicate.
func IsProtectedPath(p string) bool {
	// /.well-known/agent.json (T212), /api/v1/bridge/version (T304),
	// and /api/v1/auth/login (T601) are intentionally public — all
	// three are endpoints the client hits before it has a token.
	// /a2a/v1/* (T213) IS protected because task execution costs
	// real resources.
	if p == "/api/v1/bridge/version" || p == "/api/v1/auth/login" {
		return false
	}
	return strings.HasPrefix(p, "/api/v1/") || strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/a2a/v1/")
}

// NewRouter returns the top-level HTTP handler for the control plane.
func NewRouter(deps Deps) http.Handler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": deps.Version})
	})

	// Plumb the Discard fallback BEFORE register calls so handler
	// closures (e.g. registerAuditRoutes) capture a non-nil Audit
	// writer. Deps is passed by value into each registerX function;
	// mutating deps.Audit after registration would not propagate.
	if deps.Audit == nil {
		deps.Audit = audit.Discard{}
	}

	registerSessionRoutes(mux, deps)
	registerBrowserRoutes(mux, deps)
	registerDesktopRoutes(mux, deps)
	registerDesktopVNCRoute(mux, deps)
	registerVisionRoutes(mux, deps)
	registerVaultRoutes(mux, deps)
	registerAuthLoginRoute(mux, deps)
	// SPA web UI mount is registered LAST so its catch-all "GET /"
	// route doesn't shadow more specific API routes registered
	// above. net/http's ServeMux uses longest-prefix matching but
	// the explicit ordering keeps the intent obvious in code review.
	registerWebRoute(mux, deps)
	registerGatewayRoutes(mux, deps)
	registerKeyRoutes(mux, deps)
	registerPackRoutes(mux, deps)
	registerA2ARoutes(mux, deps)
	registerMCPRoutes(mux, deps)
	registerMCPServerRoute(mux, deps)
	registerBridgeVersionRoute(mux, deps)
	registerConnectRoutes(mux, deps)
	registerAuditRoutes(mux, deps)
	registerSecurityRoutes(mux, deps)
	registerProviderStatsRoutes(mux, deps)

	var handler http.Handler = mux
	// Innermost: auth attaches claims (or rejects with 401).
	if deps.Issuer != nil {
		handler = auth.Middleware(deps.Issuer, IsProtectedPath)(handler)
	}
	// Outer: audit sees the final response code, including auth-rejected
	// requests, so failed-auth attempts are part of the security trail.
	// Successful requests still carry claims because auth runs first and
	// the recorded handler chain populates the context before audit reads it.
	handler = auditMiddleware(deps.Audit)(handler)
	return logRequests(deps.Logger, handler)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, errCode, message string) {
	writeJSON(w, code, map[string]string{
		"error":   errCode,
		"message": message,
	})
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
