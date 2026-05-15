// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"net/http"

	"github.com/tosin2013/helmdeck/internal/marketplace"
)

// registerMarketplaceRoutes mounts the read-only catalog surface
// from T810 (#28). Operators query the catalog via the Management
// UI's Marketplace panel; the same endpoints back the CLI's
// `helmdeck pack list` (T812).
//
// Routes:
//   GET  /api/v1/marketplace/catalog   — current catalog snapshot
//   POST /api/v1/marketplace/refresh   — clear cache + re-fetch
//
// When deps.Marketplace is nil (e.g. compose deployments where the
// operator hasn't set HELMDECK_MARKETPLACE_URL and the default is
// disabled), both endpoints return 503 with a clear message. We
// register them unconditionally so an enabling-by-config path doesn't
// require a binary restart — flipping the env var on a future deploy
// activates them.
func registerMarketplaceRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /api/v1/marketplace/catalog", func(w http.ResponseWriter, r *http.Request) {
		if deps.Marketplace == nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_disabled",
				"marketplace is not configured. Set HELMDECK_MARKETPLACE_URL or remove HELMDECK_MARKETPLACE_DISABLE to enable.")
			return
		}
		idx, meta, err := deps.Marketplace.Catalog()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_not_ready",
				err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"index": idx,
			"meta":  meta,
		})
	})

	mux.HandleFunc("POST /api/v1/marketplace/refresh", func(w http.ResponseWriter, r *http.Request) {
		if deps.Marketplace == nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_disabled",
				"marketplace is not configured. Set HELMDECK_MARKETPLACE_URL or remove HELMDECK_MARKETPLACE_DISABLE to enable.")
			return
		}
		if err := deps.Marketplace.Refresh(r.Context()); err != nil {
			// Refresh failure isn't fatal — the cached catalog (if any)
			// is preserved. Return 502 (bad gateway) so operators
			// recognize the upstream issue rather than mistaking it
			// for a server-side bug.
			writeError(w, http.StatusBadGateway, "marketplace_fetch_failed",
				err.Error())
			return
		}
		idx, meta, _ := deps.Marketplace.Catalog()
		writeJSON(w, http.StatusOK, map[string]any{
			"index": idx,
			"meta":  meta,
		})
	})
}

// Compile-time assertion that the dep field's type alias resolves to
// the marketplace package's Service. If a future refactor renames the
// type, this errors at build time rather than at the first 503.
var _ *marketplace.Service = (*marketplace.Service)(nil)
