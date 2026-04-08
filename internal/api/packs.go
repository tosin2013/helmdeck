package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// registerPackRoutes mounts /api/v1/packs (list) and the dispatch
// surface /api/v1/packs/{name}[/v{n}].
//
// The path parsing is hand-rolled (rather than relying on net/http's
// 1.22 wildcards) because we have two overlapping shapes:
//
//	/api/v1/packs/{name}
//	/api/v1/packs/{name}/{version}
//
// and net/http patterns can't disambiguate the version segment from
// "is this still part of the name" without listing every verb. One
// shared handler keeps the routing logic in one place.
func registerPackRoutes(mux *http.ServeMux, deps Deps) {
	if deps.PackRegistry == nil || deps.PackEngine == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "packs_unavailable", "pack engine not configured")
		}
		mux.HandleFunc("/api/v1/packs", stub)
		mux.HandleFunc("/api/v1/packs/", stub)
		return
	}
	reg := deps.PackRegistry
	eng := deps.PackEngine

	mux.HandleFunc("GET /api/v1/packs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, reg.List())
	})

	mux.HandleFunc("/api/v1/packs/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/packs/")
		parts := strings.Split(path, "/")
		if parts[0] == "" {
			writeError(w, http.StatusNotFound, "not_found", "missing pack name")
			return
		}
		name := parts[0]
		version := ""
		if len(parts) >= 2 {
			version = parts[1]
		}

		switch r.Method {
		case http.MethodGet:
			pack, err := reg.Get(name, version)
			if err != nil {
				writeError(w, http.StatusNotFound, "not_found", err.Error())
				return
			}
			info := packs.PackInfo{
				Name:        pack.Name,
				Description: pack.Description,
				Versions:    []string{pack.Version},
				Latest:      pack.Version,
			}
			writeJSON(w, http.StatusOK, info)
		case http.MethodPost:
			pack, err := reg.Get(name, version)
			if err != nil {
				writeError(w, http.StatusNotFound, "not_found", err.Error())
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeError(w, http.StatusBadRequest, "read_failed", err.Error())
				return
			}
			// Empty body is allowed — packs can declare an empty input
			// schema. JSON decode happens inside the pack's schema
			// validator, so we don't pre-parse here.
			input := json.RawMessage(body)
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			res, perr := eng.Execute(r.Context(), pack, input)
			if perr != nil {
				writePackError(w, perr)
				return
			}
			writeJSON(w, http.StatusOK, res)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		}
	})
}

// writePackError maps a packs.PackError to an HTTP status. The
// closed-set codes from T206 give us a clean switch — REST clients
// see the same code in JSON, so they can branch on Code without
// scraping messages.
func writePackError(w http.ResponseWriter, err error) {
	var perr *packs.PackError
	if !errors.As(err, &perr) {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	status := http.StatusInternalServerError
	switch perr.Code {
	case packs.CodeInvalidInput:
		status = http.StatusBadRequest
	case packs.CodeInvalidOutput:
		status = http.StatusBadGateway
	case packs.CodeSessionUnavailable:
		status = http.StatusServiceUnavailable
	case packs.CodeArtifactFailed:
		status = http.StatusBadGateway
	case packs.CodeTimeout:
		status = http.StatusGatewayTimeout
	case packs.CodeHandlerFailed:
		status = http.StatusBadGateway
	case packs.CodeInternal:
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]any{
		"error":   string(perr.Code),
		"message": perr.Message,
	})
}
