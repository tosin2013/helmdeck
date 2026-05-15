// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"encoding/json"
	"errors"
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

	// GET /api/v1/marketplace/packs/{name} — pack detail (T813).
	// Returns the full helmdeck-pack.yaml manifest for one pack so the
	// UI's detail card can render input/output schemas, examples, and
	// the trust block. Fetched on demand (not pre-loaded with the
	// catalog) because the manifest can be larger than the catalog
	// entry and most packs are never opened.
	mux.HandleFunc("GET /api/v1/marketplace/packs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if deps.Marketplace == nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_disabled",
				"marketplace is not configured. Set HELMDECK_MARKETPLACE_URL or remove HELMDECK_MARKETPLACE_DISABLE to enable.")
			return
		}
		name := r.PathValue("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "invalid_input", "pack name is required in the URL path")
			return
		}
		idx, _, err := deps.Marketplace.Catalog()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_not_ready", err.Error())
			return
		}
		var entry *marketplace.IndexEntry
		for i := range idx.Packs {
			if idx.Packs[i].Name == name {
				entry = &idx.Packs[i]
				break
			}
		}
		if entry == nil {
			writeError(w, http.StatusNotFound, "pack_not_in_catalog",
				"no pack named "+name+" in the catalog. Did you POST /api/v1/marketplace/refresh recently?")
			return
		}
		manifest, err := marketplace.LoadManifest(r.Context(), deps.Marketplace.Source(), entry.Path)
		if err != nil {
			writeError(w, http.StatusBadGateway, "manifest_fetch_failed",
				"failed to fetch pack manifest: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entry":    entry,
			"manifest": manifest,
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

	// --- install / uninstall / list-installed (T812 / #30) --------------

	mux.HandleFunc("POST /api/v1/marketplace/install", func(w http.ResponseWriter, r *http.Request) {
		if deps.MarketplaceInstaller == nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_install_disabled",
				"marketplace install is not configured. Requires HELMDECK_PACKS_DIR to be set.")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "invalid_input",
				"`name` is required")
			return
		}
		result, err := deps.MarketplaceInstaller.Install(r.Context(), body.Name)
		if err != nil {
			if errors.Is(err, marketplace.ErrPackNotInCatalog) {
				writeError(w, http.StatusNotFound, "pack_not_in_catalog", err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "install_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	mux.HandleFunc("POST /api/v1/marketplace/uninstall", func(w http.ResponseWriter, r *http.Request) {
		if deps.MarketplaceInstaller == nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_install_disabled",
				"marketplace install is not configured. Requires HELMDECK_PACKS_DIR to be set.")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "invalid_input",
				"`name` is required")
			return
		}
		if err := deps.MarketplaceInstaller.Uninstall(r.Context(), body.Name); err != nil {
			if errors.Is(err, marketplace.ErrPackNotInstalled) {
				writeError(w, http.StatusNotFound, "pack_not_installed", err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "uninstall_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "uninstalled", "name": body.Name})
	})

	mux.HandleFunc("GET /api/v1/marketplace/installed", func(w http.ResponseWriter, r *http.Request) {
		if deps.MarketplaceInstaller == nil {
			writeError(w, http.StatusServiceUnavailable, "marketplace_install_disabled",
				"marketplace install is not configured. Requires HELMDECK_PACKS_DIR to be set.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"installed": deps.MarketplaceInstaller.Installed(),
		})
	})
}

// Compile-time assertion that the dep field's type alias resolves to
// the marketplace package's Service. If a future refactor renames the
// type, this errors at build time rather than at the first 503.
var _ *marketplace.Service = (*marketplace.Service)(nil)
