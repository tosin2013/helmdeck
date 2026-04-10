// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// artifacts.go (T613, ADR 032) — GET /api/v1/artifacts lists recent
// artifacts from the in-memory S3 index with freshly-generated
// signed URLs. Operators use this in the Management UI's Artifact
// Explorer panel to browse, preview, and download what their agents
// produced.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func registerArtifactRoutes(mux *http.ServeMux, deps Deps) {
	if deps.ArtifactStore == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "artifacts_unavailable",
				"S3 artifact store not configured (set HELMDECK_S3_ENDPOINT)")
		}
		mux.HandleFunc("/api/v1/artifacts", stub)
		mux.HandleFunc("/api/v1/artifacts/", stub)
		return
	}
	store := deps.ArtifactStore

	// GET /api/v1/artifacts/{key...} — proxy download. The signed
	// S3 URLs use internal Docker DNS (garage:3900) which the
	// operator's browser can't resolve. This endpoint fetches the
	// artifact from the store and streams it to the browser so the
	// browser only needs to reach the control plane.
	mux.HandleFunc("GET /api/v1/artifacts/download/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/download/")
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing_key", "artifact key required")
			return
		}
		data, art, err := store.Get(r.Context(), key)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		ct := art.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		// Inline for images (browser preview), attachment for everything else
		disp := "inline"
		if !strings.HasPrefix(ct, "image/") {
			disp = "attachment"
		}
		filename := key
		if idx := strings.LastIndex(key, "/"); idx >= 0 {
			filename = key[idx+1:]
		}
		w.Header().Set("Content-Disposition", disp+"; filename=\""+filename+"\"")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	mux.HandleFunc("GET /api/v1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		packFilter := strings.TrimSpace(r.URL.Query().Get("pack"))
		limitStr := r.URL.Query().Get("limit")
		limit := 50
		if limitStr != "" {
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}

		// List all packs from the registry to gather artifact keys.
		// The S3 store's in-memory index is keyed by pack name —
		// iterate every pack that has artifacts.
		var all []packs.Artifact
		if packFilter != "" {
			arts, err := store.ListForPack(r.Context(), packFilter)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
				return
			}
			all = arts
		} else {
			// No pack filter — list from every pack. The S3 store
			// only exposes ListForPack (not ListAll), so we iterate
			// the pack registry for names. This is fine for the
			// current scale; a dedicated ListAll method is a future
			// optimisation.
			if deps.PackRegistry != nil {
				for _, info := range deps.PackRegistry.List() {
					arts, err := store.ListForPack(r.Context(), info.Name)
					if err != nil {
						continue
					}
					all = append(all, arts...)
				}
			}
		}

		// Sort newest-first (reverse order of append, since the
		// index is append-only and roughly chronological).
		// For a proper sort we'd need to compare CreatedAt but
		// reversing the slice is correct for the append-only path.
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}

		// Apply limit.
		if len(all) > limit {
			all = all[:limit]
		}

		type artifactResponse struct {
			Key         string `json:"key"`
			URL         string `json:"url"`
			Size        int64  `json:"size"`
			ContentType string `json:"content_type"`
			CreatedAt   string `json:"created_at"`
			Pack        string `json:"pack"`
		}

		items := make([]artifactResponse, 0, len(all))
		for _, a := range all {
			// Use the proxy URL so the browser can reach the artifact
			// without resolving internal Docker DNS (garage:3900).
			// The proxy endpoint at /api/v1/artifacts/download/{key}
			// fetches from the S3 store and streams to the browser.
			proxyURL := "/api/v1/artifacts/download/" + a.Key
			items = append(items, artifactResponse{
				Key:         a.Key,
				URL:         proxyURL,
				Size:        a.Size,
				ContentType: a.ContentType,
				CreatedAt:   a.CreatedAt.Format("2006-01-02T15:04:05Z"),
				Pack:        a.Pack,
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"artifacts": items,
			"count":     len(items),
		})
	})
}
