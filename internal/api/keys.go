package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/keystore"
)

// registerKeyRoutes mounts /api/v1/providers/keys CRUD + rotate + test.
// When deps.Keys is nil the routes return 503 so misconfiguration is
// loud rather than the router silently 404'ing — same pattern as the
// browser and gateway routes.
func registerKeyRoutes(mux *http.ServeMux, deps Deps) {
	if deps.Keys == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "keystore_unavailable", "provider key store not configured")
		}
		mux.HandleFunc("/api/v1/providers/keys", stub)
		mux.HandleFunc("/api/v1/providers/keys/", stub)
		return
	}
	ks := deps.Keys

	mux.HandleFunc("GET /api/v1/providers/keys", func(w http.ResponseWriter, r *http.Request) {
		recs, err := ks.List(r.Context(), r.URL.Query().Get("provider"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		// Always emit a JSON array even when empty so clients don't
		// have to special-case `null`.
		if recs == nil {
			recs = []keystore.Record{}
		}
		writeJSON(w, http.StatusOK, recs)
	})

	mux.HandleFunc("POST /api/v1/providers/keys", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Provider string `json:"provider"`
			Label    string `json:"label"`
			Key      string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		rec, err := ks.Create(r.Context(), req.Provider, req.Label, req.Key)
		if err != nil {
			switch {
			case errors.Is(err, keystore.ErrDuplicate):
				writeError(w, http.StatusConflict, "duplicate", err.Error())
			default:
				writeError(w, http.StatusBadRequest, "create_failed", err.Error())
			}
			return
		}
		// T202a hot reload: re-hydrate the gateway registry so the
		// new key is live for /v1/chat/completions immediately. We
		// log but never fail the HTTP response on rehydrate error —
		// the keystore mutation itself succeeded.
		if deps.RehydrateGateway != nil {
			if err := deps.RehydrateGateway(); err != nil {
				deps.Logger.Warn("rehydrate gateway after key create failed", "err", err)
			}
		}
		writeJSON(w, http.StatusCreated, rec)
	})

	// Subroutes under /api/v1/providers/keys/{id}[/rotate|/test] use a
	// single handler because net/http's path patterns can't express the
	// ".../{id}" + ".../{id}/rotate" disambiguation cleanly without
	// listing every verb explicitly.
	mux.HandleFunc("/api/v1/providers/keys/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/providers/keys/")
		parts := strings.Split(path, "/")
		if len(parts) == 0 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not_found", "missing id")
			return
		}
		id := parts[0]

		// /{id}
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				rec, err := ks.Get(r.Context(), id)
				if errors.Is(err, keystore.ErrNotFound) {
					writeError(w, http.StatusNotFound, "not_found", err.Error())
					return
				}
				if err != nil {
					writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
					return
				}
				writeJSON(w, http.StatusOK, rec)
			case http.MethodDelete:
				if err := ks.Delete(r.Context(), id); err != nil {
					if errors.Is(err, keystore.ErrNotFound) {
						writeError(w, http.StatusNotFound, "not_found", err.Error())
						return
					}
					writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
					return
				}
				if deps.RehydrateGateway != nil {
					if err := deps.RehydrateGateway(); err != nil {
						deps.Logger.Warn("rehydrate gateway after key delete failed", "err", err)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
			}
			return
		}

		// /{id}/rotate or /{id}/test
		if len(parts) == 2 && r.Method == http.MethodPost {
			switch parts[1] {
			case "rotate":
				var req struct {
					Key string `json:"key"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
					return
				}
				rec, err := ks.Rotate(r.Context(), id, req.Key)
				if errors.Is(err, keystore.ErrNotFound) {
					writeError(w, http.StatusNotFound, "not_found", err.Error())
					return
				}
				if err != nil {
					writeError(w, http.StatusBadRequest, "rotate_failed", err.Error())
					return
				}
				if deps.RehydrateGateway != nil {
					if err := deps.RehydrateGateway(); err != nil {
						deps.Logger.Warn("rehydrate gateway after key rotate failed", "err", err)
					}
				}
				writeJSON(w, http.StatusOK, rec)
				return
			case "test":
				rec, err := ks.Get(r.Context(), id)
				if errors.Is(err, keystore.ErrNotFound) {
					writeError(w, http.StatusNotFound, "not_found", err.Error())
					return
				}
				if err != nil {
					writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
					return
				}
				plaintext, err := ks.Decrypt(r.Context(), id)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "decrypt_failed", err.Error())
					return
				}
				tester := deps.KeyTester
				if tester == nil {
					tester = keystore.TestProviderKey
				}
				if err := tester(r.Context(), nil, rec.Provider, plaintext); err != nil {
					writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
		}
		writeError(w, http.StatusNotFound, "not_found", "unknown route")
	})
}
