// Package api wires HTTP handlers for the helmdeck control plane.
//
// T101 shipped /healthz and /version. T105 adds the /api/v1/sessions
// surface backed by a [session.Runtime].
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/audit"
	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/session"
)

// Deps bundles the runtime dependencies the router needs. Passing them as
// a struct (rather than positional args) keeps the router constructor
// stable as new subsystems land in later phases.
type Deps struct {
	Logger  *slog.Logger
	Version string
	Runtime session.Runtime // optional; nil disables /api/v1/sessions
	Issuer  *auth.Issuer    // optional; nil disables /api/v1/* JWT enforcement (dev mode)
	Audit   audit.Writer    // optional; nil uses audit.Discard
}

// IsProtectedPath returns true for paths the auth middleware must guard.
// Exported so tests and the control plane can share the same predicate.
func IsProtectedPath(p string) bool {
	return strings.HasPrefix(p, "/api/v1/") || strings.HasPrefix(p, "/v1/")
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

	registerSessionRoutes(mux, deps)

	if deps.Audit == nil {
		deps.Audit = audit.Discard{}
	}

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
