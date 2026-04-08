package api

import (
	"net/http"
)

// MinRecommendedBridgeVersion is the oldest helmdeck-mcp release the
// platform still considers fully supported. Bridges older than this
// are still allowed to connect (so an out-of-band update doesn't
// break a working agent the moment the platform ships) but they
// receive a deprecation warning on startup so operators know to
// roll their npm/brew/scoop install forward.
//
// Bump this manually whenever the platform's MCP wire contract
// gains a feature older bridges can't speak. Format is `vMAJOR.MINOR.PATCH`.
const MinRecommendedBridgeVersion = "v0.2.0"

// registerBridgeVersionRoute mounts GET /api/v1/bridge/version. The
// endpoint is intentionally public — the bridge fetches it before
// opening the WebSocket, and that fetch happens before any token
// is presented.
func registerBridgeVersionRoute(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /api/v1/bridge/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"platform_version": deps.Version,
			"min_recommended":  MinRecommendedBridgeVersion,
		})
	})
}
