package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/mcp"
)

// registerMCPRoutes mounts the /api/v1/mcp/servers CRUD surface and
// the /api/v1/mcp/servers/{id}/manifest fetch endpoint. The shape
// mirrors the provider keys handler from T203 because operators
// expect the same patterns across resource types.
func registerMCPRoutes(mux *http.ServeMux, deps Deps) {
	if deps.MCPRegistry == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registry not configured")
		}
		mux.HandleFunc("/api/v1/mcp/servers", stub)
		mux.HandleFunc("/api/v1/mcp/servers/", stub)
		return
	}
	reg := deps.MCPRegistry

	mux.HandleFunc("GET /api/v1/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		servers, err := reg.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		if servers == nil {
			servers = []*mcp.Server{}
		}
		writeJSON(w, http.StatusOK, servers)
	})

	mux.HandleFunc("POST /api/v1/mcp/servers", func(w http.ResponseWriter, r *http.Request) {
		var in mcp.CreateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		s, err := reg.Create(r.Context(), in)
		if err != nil {
			switch {
			case errors.Is(err, mcp.ErrDuplicateName):
				writeError(w, http.StatusConflict, "duplicate", err.Error())
			default:
				writeError(w, http.StatusBadRequest, "create_failed", err.Error())
			}
			return
		}
		writeJSON(w, http.StatusCreated, s)
	})

	mux.HandleFunc("/api/v1/mcp/servers/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/mcp/servers/")
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
				s, err := reg.Get(r.Context(), id)
				if errors.Is(err, mcp.ErrServerNotFound) {
					writeError(w, http.StatusNotFound, "not_found", err.Error())
					return
				}
				if err != nil {
					writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
					return
				}
				writeJSON(w, http.StatusOK, s)
			case http.MethodPut:
				var in mcp.CreateInput
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
					return
				}
				s, err := reg.Update(r.Context(), id, in)
				if errors.Is(err, mcp.ErrServerNotFound) {
					writeError(w, http.StatusNotFound, "not_found", err.Error())
					return
				}
				if err != nil {
					writeError(w, http.StatusBadRequest, "update_failed", err.Error())
					return
				}
				writeJSON(w, http.StatusOK, s)
			case http.MethodDelete:
				if err := reg.Delete(r.Context(), id); err != nil {
					if errors.Is(err, mcp.ErrServerNotFound) {
						writeError(w, http.StatusNotFound, "not_found", err.Error())
						return
					}
					writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
					return
				}
				w.WriteHeader(http.StatusNoContent)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
			}
			return
		}

		// /{id}/manifest — GET serves cache, POST forces refresh
		if len(parts) == 2 && parts[1] == "manifest" {
			force := r.Method == http.MethodPost
			if r.Method != http.MethodGet && r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
				return
			}
			m, err := reg.Manifest(r.Context(), id, force)
			if errors.Is(err, mcp.ErrServerNotFound) {
				writeError(w, http.StatusNotFound, "not_found", err.Error())
				return
			}
			if err != nil {
				writeError(w, http.StatusBadGateway, "manifest_failed", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, m)
			return
		}

		writeError(w, http.StatusNotFound, "not_found", "unknown route")
	})
}
