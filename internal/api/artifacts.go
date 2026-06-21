// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// artifacts.go (T613, ADR 032) — GET /api/v1/artifacts lists recent
// artifacts from the in-memory S3 index with freshly-generated
// signed URLs. Operators use this in the Management UI's Artifact
// Explorer panel to browse, preview, and download what their agents
// produced.

import (
	"io"
	"mime"
	"net/http"
	"path/filepath"
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

	// DELETE /api/v1/artifacts/{key...} — remove a single artifact
	// from the store. The TTL janitor already deletes by age; this is
	// the operator-facing manual delete (Artifact Explorer trash
	// button). Delete is idempotent — an unknown key is a no-op — so a
	// missing artifact still returns 204. Key extraction mirrors the
	// download route above.
	mux.HandleFunc("DELETE /api/v1/artifacts/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/")
		if key == "" {
			writeError(w, http.StatusBadRequest, "missing_key", "artifact key required")
			return
		}
		if err := store.Delete(r.Context(), key); err != nil {
			writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
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

	// POST /api/v1/artifacts/upload — operator-facing upload surface.
	// Accepts multipart/form-data with a `file` field; persists the
	// bytes under the "operator-uploads" namespace and returns the
	// generated artifact key. The operator pastes the key into BYO-
	// audio pipeline calls (e.g. builtin.byo-audio-narrated-video's
	// audio_artifact_key input). Cap matches the largest media a
	// helmdeck pack typically handles — hyperframes.attach_audio's
	// 50 MiB audio cap is the load-bearing reference. We allow up to
	// 100 MiB here to cover the rare large-MP4 or long-form audio
	// case; oversize uploads return 413.
	mux.HandleFunc("POST /api/v1/artifacts/upload", func(w http.ResponseWriter, r *http.Request) {
		// 100 MiB cap. ParseMultipartForm reads UP TO this limit into
		// memory, then spills to /tmp. With a small control-plane
		// container, the spill-to-disk behavior is correct for
		// large media.
		const maxBytes = 100 << 20 // 100 MiB
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MiB in-memory
			writeError(w, http.StatusBadRequest, "parse_failed",
				"could not parse multipart form: "+err.Error())
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing_file",
				"form field `file` is required")
			return
		}
		defer file.Close()
		if header.Size > maxBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large",
				"file exceeds 100 MiB cap")
			return
		}
		content, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed",
				"could not read upload body: "+err.Error())
			return
		}
		// Content-type detection: prefer client's declared
		// Content-Type, then fall back to mime.TypeByExtension on the
		// filename, then to http.DetectContentType on the first 512
		// bytes. The pack that consumes the artifact (e.g.
		// hyperframes.compose for audio) reads this to enforce its
		// own type whitelist; we just need to fill it correctly.
		contentType := header.Header.Get("Content-Type")
		if contentType == "" {
			if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(header.Filename), ".")); ext != "" {
				if mimeType := mime.TypeByExtension("." + ext); mimeType != "" {
					contentType = mimeType
				}
			}
		}
		if contentType == "" && len(content) > 0 {
			head := content
			if len(head) > 512 {
				head = head[:512]
			}
			contentType = http.DetectContentType(head)
		}
		filename := sanitizeUploadFilename(header.Filename)
		art, err := store.Put(r.Context(), "operator-uploads", filename, content, contentType)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "put_failed",
				"could not persist artifact: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"artifact_key": art.Key,
			"url":          "/api/v1/artifacts/download/" + art.Key,
			"size":         art.Size,
			"content_type": art.ContentType,
			"filename":     filename,
		})
	})
}

// sanitizeUploadFilename strips path separators and non-printable
// characters from a multipart upload's filename. The resulting name
// becomes part of the artifact key, so we keep it short, safe to
// embed in S3 keys, and informative enough for an operator to
// recognize their upload in the artifact list.
//
// We deliberately keep spaces (operators upload "My Audio.mp3"
// regularly) — the S3 store URL-encodes the key on PUT.
func sanitizeUploadFilename(raw string) string {
	// http.MultipartReader can hand us paths from a browser that
	// includes a directory prefix on some platforms.
	name := filepath.Base(raw)
	if name == "" || name == "." || name == "/" {
		return "upload"
	}
	// Strip control characters; preserve everything else.
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	cleaned := strings.TrimSpace(b.String())
	if cleaned == "" {
		return "upload"
	}
	// Cap at 200 chars so the resulting key (pack/uuid-filename) fits
	// comfortably in any S3-key cap.
	if len(cleaned) > 200 {
		cleaned = cleaned[:200]
	}
	return cleaned
}
