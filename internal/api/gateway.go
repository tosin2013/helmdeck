package api

import (
	"net/http"

	"github.com/tosin2013/helmdeck/internal/gateway"
)

// registerGatewayRoutes mounts the OpenAI-compatible /v1/models and
// /v1/chat/completions endpoints. When no registry is configured we
// surface 503 on both paths so misconfiguration is loud rather than the
// router silently 404'ing — same pattern as registerBrowserRoutes.
func registerGatewayRoutes(mux *http.ServeMux, deps Deps) {
	if deps.Gateway == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "gateway_unavailable", "AI gateway not configured")
		}
		mux.HandleFunc("GET /v1/models", stub)
		mux.HandleFunc("POST /v1/chat/completions", stub)
		return
	}
	// Prefer the chain (with fallback rules) over the bare registry when
	// both are configured. The chain falls through to the registry for
	// any model that has no rule installed, so this is safe even when
	// rules are sparse.
	var dispatcher gateway.Dispatcher = deps.Gateway
	if deps.GatewayChain != nil {
		dispatcher = deps.GatewayChain
	}
	h := gateway.Handler(dispatcher)
	mux.Handle("GET /v1/models", h)
	mux.Handle("POST /v1/chat/completions", h)
}
