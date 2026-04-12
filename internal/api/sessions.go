package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/tosin2013/helmdeck/internal/session"
)

// createSessionRequest is the JSON body accepted by POST /api/v1/sessions.
type createSessionRequest struct {
	Label          string            `json:"label,omitempty"`
	Image          string            `json:"image,omitempty"`
	MemoryLimit    string            `json:"memory_limit,omitempty"`
	SHMSize        string            `json:"shm_size,omitempty"`
	CPULimit       float64           `json:"cpu_limit,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxTasks       int               `json:"max_tasks,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

// sessionResponse is the JSON shape every session-returning endpoint emits.
type sessionResponse struct {
	ID          string         `json:"id"`
	ContainerID string         `json:"container_id"`
	Status      session.Status `json:"status"`
	CDPEndpoint string         `json:"cdp_endpoint,omitempty"`
	// PlaywrightMCPEndpoint is the per-session SSE URL of the bundled
	// @playwright/mcp server (T807a / ADR 035). Omitted when the sidecar
	// was built without Playwright MCP or the operator disabled it.
	PlaywrightMCPEndpoint string    `json:"playwright_mcp_endpoint,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	Spec                  specView  `json:"spec"`
}

type specView struct {
	Label          string            `json:"label,omitempty"`
	Image          string            `json:"image,omitempty"`
	MemoryLimit    string            `json:"memory_limit,omitempty"`
	SHMSize        string            `json:"shm_size,omitempty"`
	CPULimit       float64           `json:"cpu_limit,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxTasks       int               `json:"max_tasks,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
}

func toResponse(s *session.Session) sessionResponse {
	return sessionResponse{
		ID:                    s.ID,
		ContainerID:           s.ContainerID,
		Status:                s.Status,
		CDPEndpoint:           s.CDPEndpoint,
		PlaywrightMCPEndpoint: s.PlaywrightMCPEndpoint,
		CreatedAt:             s.CreatedAt,
		Spec: specView{
			Label:          s.Spec.Label,
			Image:          s.Spec.Image,
			MemoryLimit:    s.Spec.MemoryLimit,
			SHMSize:        s.Spec.SHMSize,
			CPULimit:       s.Spec.CPULimit,
			TimeoutSeconds: int(s.Spec.Timeout / time.Second),
			MaxTasks:       s.Spec.MaxTasks,
			Env:            s.Spec.Env,
		},
	}
}

func registerSessionRoutes(mux *http.ServeMux, deps Deps) {
	if deps.Runtime == nil {
		// Register a single placeholder so callers see a clear 503 instead
		// of a confusing 404 when the runtime isn't wired (dev mode).
		mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "session runtime not configured")
		})
		mux.HandleFunc("/api/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "session runtime not configured")
		})
		return
	}

	rt := deps.Runtime

	mux.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req createSessionRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
				return
			}
		}
		spec := session.Spec{
			Label:       req.Label,
			Image:       req.Image,
			MemoryLimit: req.MemoryLimit,
			SHMSize:     req.SHMSize,
			CPULimit:    req.CPULimit,
			Timeout:     time.Duration(req.TimeoutSeconds) * time.Second,
			MaxTasks:    req.MaxTasks,
			Env:         req.Env,
		}
		s, err := rt.Create(r.Context(), spec)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, toResponse(s))
	})

	mux.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		list, err := rt.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		out := make([]sessionResponse, 0, len(list))
		for _, s := range list {
			out = append(out, toResponse(s))
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
	})

	mux.HandleFunc("GET /api/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s, err := rt.Get(r.Context(), id)
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, toResponse(s))
	})

	mux.HandleFunc("DELETE /api/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Tear down any cached chromedp client first so we don't leak
		// browser-side resources after the container disappears.
		if deps.CDPFactory != nil {
			deps.CDPFactory.Evict(id)
		}
		if err := rt.Terminate(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "terminate_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/v1/sessions/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		rc, err := rt.Logs(r.Context(), id)
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "logs_failed", err.Error())
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, rc)
	})
}
