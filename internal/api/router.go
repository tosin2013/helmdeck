// Package api wires HTTP handlers for the helmdeck control plane.
//
// This is the T101 skeleton: only /healthz and /version are live.
// Real endpoints arrive in T105 (sessions), T106 (CDP), T107 (auth), and beyond.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// NewRouter returns the top-level HTTP handler for the control plane.
func NewRouter(logger *slog.Logger, version string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": version})
	})

	return logRequests(logger, mux)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}
