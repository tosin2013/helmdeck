package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/audit"
	"github.com/tosin2013/helmdeck/internal/auth"
)

// statusRecorder captures the response status code so the audit middleware
// can record it without parsing the body.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// auditMiddleware records every request that survives auth into the audit
// log. /healthz and /version are skipped (kubelet probes would otherwise
// drown the table). The actor is read from the JWT claims attached by
// auth.Middleware; unauthenticated open-path requests record an empty actor.
func auditMiddleware(w audit.Writer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if isNoiseEndpoint(r.URL.Path) {
				next.ServeHTTP(rw, r)
				return
			}
			rec := &statusRecorder{ResponseWriter: rw, status: http.StatusOK}
			ctx := auth.WithHolder(r.Context())
			r = r.WithContext(ctx)
			start := time.Now()
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start)

			claims := auth.HolderFromContext(ctx)
			entry := audit.Entry{
				Severity:   severityFor(rec.status),
				EventType:  eventTypeFor(r.URL.Path),
				Method:     r.Method,
				Path:       r.URL.Path,
				StatusCode: rec.status,
				Payload: map[string]any{
					"elapsed_ms": elapsed.Milliseconds(),
					"user_agent": r.UserAgent(),
				},
			}
			if claims != nil {
				entry.ActorSubject = claims.Subject
				entry.ActorClient = claims.Client
			}
			// Best-effort write — never block the response on the audit
			// store. A failed write is logged elsewhere; we don't want to
			// surface storage errors to the API caller.
			_ = w.Write(r.Context(), entry)
		})
	}
}

func isNoiseEndpoint(p string) bool {
	return p == "/healthz" || p == "/version" || p == "/readyz" || p == "/metrics"
}

func severityFor(code int) audit.Severity {
	switch {
	case code >= 500:
		return audit.SeverityError
	case code >= 400:
		return audit.SeverityWarning
	default:
		return audit.SeverityInfo
	}
}

// eventTypeFor classifies a request path into the closed audit vocabulary
// (audit.EventType). Falls back to api_request for anything that doesn't
// match a more specific subsystem.
func eventTypeFor(p string) audit.EventType {
	switch {
	case strings.HasPrefix(p, "/api/v1/sessions"):
		return audit.EventSessionCreate // Terminate is differentiated at the handler when DELETE-only paths land in T109+
	case strings.HasPrefix(p, "/api/v1/packs/"):
		return audit.EventPackCall
	case strings.HasPrefix(p, "/v1/chat/completions"), strings.HasPrefix(p, "/v1/models"):
		return audit.EventLLMCall
	case strings.HasPrefix(p, "/api/v1/mcp/"):
		return audit.EventMCPCall
	case strings.HasPrefix(p, "/api/v1/vault/"):
		return audit.EventVaultRead
	}
	return audit.EventAPIRequest
}
