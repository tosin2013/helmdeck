// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// pipelines.go — REST surface for pipelines as a first-class resource
// (ADR 041). Mirrors registerPackRoutes: a collection route plus one
// prefix handler that hand-parses /{id}, /{id}/run, /{id}/runs[/{runId}].
// Auth auto-applies via IsProtectedPath (/api/v1/*). Runs are async:
// POST /run returns 202 + run_id; clients poll GET /runs/{runId}.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/pipelines"
)

// pipelineCaller extracts the authenticated subject from the request so
// pipeline runs namespace per-caller resources (e.g. repo.fetch's
// persistent clone dir) the same way single-pack calls do. Empty when
// unauthenticated/auth-disabled → packs treat it as "unknown".
func pipelineCaller(r *http.Request) string {
	if c := auth.FromContext(r.Context()); c != nil {
		return c.Subject
	}
	return ""
}

func registerPipelineRoutes(mux *http.ServeMux, deps Deps) {
	if deps.PipelineStore == nil || deps.PipelineRunner == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "pipelines_unavailable", "pipeline engine not configured")
		}
		mux.HandleFunc("/api/v1/pipelines", stub)
		mux.HandleFunc("/api/v1/pipelines/", stub)
		return
	}
	store := deps.PipelineStore
	runner := deps.PipelineRunner
	packExists := func(name, ver string) bool {
		if deps.PackRegistry == nil {
			return true
		}
		_, err := deps.PackRegistry.Get(name, ver)
		return err == nil
	}

	mux.HandleFunc("GET /api/v1/pipelines", func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		if list == nil {
			list = []*pipelines.Pipeline{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("POST /api/v1/pipelines", func(w http.ResponseWriter, r *http.Request) {
		var p pipelines.Pipeline
		if err := decodeJSON(r, &p); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_pipeline", err.Error())
			return
		}
		// Server owns identity + the builtin flag; clients can't forge them.
		p.ID = "pipe_" + randHex()
		p.Builtin = false
		if err := pipelines.Validate(&p, packExists); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_pipeline", err.Error())
			return
		}
		if err := store.Create(r.Context(), &p); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, &p)
	})

	// Cross-pipeline recent runs — one poll for the Management UI to show
	// which pipelines have an active run. Distinct path (not under
	// /pipelines/) so it doesn't collide with the {id} prefix parser below.
	mux.HandleFunc("GET /api/v1/pipeline-runs", func(w http.ResponseWriter, r *http.Request) {
		handleListAllRuns(w, r, store)
	})

	mux.HandleFunc("/api/v1/pipelines/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/pipelines/")
		parts := strings.Split(path, "/")
		id := parts[0]
		if id == "" {
			writeError(w, http.StatusNotFound, "not_found", "missing pipeline id")
			return
		}

		// Sub-resources: /{id}/run and /{id}/runs[/{runId}].
		if len(parts) >= 2 {
			switch parts[1] {
			case "run":
				if r.Method != http.MethodPost {
					writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
					return
				}
				handlePipelineRun(w, r, runner, id)
				return
			case "runs":
				// POST /{id}/runs/{runId}/rerun — start a fresh run from
				// an existing one (same pipeline + inputs).
				if len(parts) >= 4 && parts[3] == "rerun" {
					if r.Method != http.MethodPost {
						writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
						return
					}
					handlePipelineRerun(w, r, runner, parts[2])
					return
				}
				// POST /{id}/runs/{runId}/cancel — hard-stop a running
				// or pending run (kills its session containers).
				if len(parts) >= 4 && parts[3] == "cancel" {
					if r.Method != http.MethodPost {
						writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
						return
					}
					handlePipelineCancel(w, r, runner, parts[2])
					return
				}
				if r.Method != http.MethodGet {
					writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
					return
				}
				if len(parts) >= 3 && parts[2] != "" {
					handleGetRun(w, r, runner, parts[2])
					return
				}
				handleListRuns(w, r, store, id)
				return
			default:
				writeError(w, http.StatusNotFound, "not_found", "unknown sub-resource")
				return
			}
		}

		// /{id} CRUD.
		switch r.Method {
		case http.MethodGet:
			p, err := store.Get(r.Context(), id)
			if err != nil {
				writePipelineNotFound(w, err)
				return
			}
			writeJSON(w, http.StatusOK, p)
		case http.MethodPut:
			existing, err := store.Get(r.Context(), id)
			if err != nil {
				writePipelineNotFound(w, err)
				return
			}
			if existing.Builtin {
				writeError(w, http.StatusConflict, "builtin_readonly", "built-in pipelines are read-only; clone it instead")
				return
			}
			var p pipelines.Pipeline
			if err := decodeJSON(r, &p); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_pipeline", err.Error())
				return
			}
			p.ID = id
			if err := pipelines.Validate(&p, packExists); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_pipeline", err.Error())
				return
			}
			if err := store.Update(r.Context(), &p); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, &p)
		case http.MethodDelete:
			existing, err := store.Get(r.Context(), id)
			if err != nil {
				writePipelineNotFound(w, err)
				return
			}
			if existing.Builtin {
				writeError(w, http.StatusConflict, "builtin_readonly", "built-in pipelines are read-only")
				return
			}
			if err := store.Delete(r.Context(), id); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", r.Method)
		}
	})
}

func handlePipelineRun(w http.ResponseWriter, r *http.Request, runner *pipelines.Runner, id string) {
	var body struct {
		Inputs json.RawMessage `json:"inputs"`
	}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	runID, coalesced, err := runner.StartRun(r.Context(), id, body.Inputs, pipelineCaller(r))
	if err != nil {
		if errors.Is(err, pipelines.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "pipeline not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_pipeline", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id": runID, "pipeline_id": id, "status": string(pipelines.RunPending),
		"coalesced": coalesced,
	})
}

// handlePipelineRerun starts a fresh run from an existing run (same
// pipeline + inputs). Distinct from a resume — every step runs again.
// Also coalesces onto an identical in-flight run (same single-flight
// guarantee as POST /run).
func handlePipelineRerun(w http.ResponseWriter, r *http.Request, runner *pipelines.Runner, runID string) {
	newRunID, coalesced, err := runner.Rerun(r.Context(), runID, pipelineCaller(r))
	if err != nil {
		if errors.Is(err, pipelines.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "run or pipeline not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_pipeline", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id": newRunID, "status": string(pipelines.RunPending),
		"coalesced": coalesced,
	})
}

// handlePipelineCancel hard-stops a running (or pending) run: marks the
// cancel intent, fires the run ctx-cancel, and force-removes its session
// containers via the helmdeck.run_id label. Returns 200 on success — the
// run goroutine flips to "cancelled" within ~1-2s (single-writer).
// Already-terminal runs return 409 not_cancellable.
func handlePipelineCancel(w http.ResponseWriter, r *http.Request, runner *pipelines.Runner, runID string) {
	if err := runner.CancelRun(r.Context(), runID); err != nil {
		if errors.Is(err, pipelines.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		// "already <status> and cannot be cancelled" — distinguish via
		// the error string. CancelRun is the only source of that text.
		if strings.Contains(err.Error(), "cannot be cancelled") {
			writeError(w, http.StatusConflict, "not_cancellable", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id": runID, "status": string(pipelines.RunCancelled),
	})
}

func handleGetRun(w http.ResponseWriter, r *http.Request, runner *pipelines.Runner, runID string) {
	run, err := runner.GetRun(r.Context(), runID)
	if err != nil {
		writePipelineNotFound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func handleListRuns(w http.ResponseWriter, r *http.Request, store *pipelines.Store, id string) {
	runs, err := store.ListRuns(r.Context(), id, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if runs == nil {
		runs = []*pipelines.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// handleListAllRuns returns recent runs across every pipeline so the UI can
// show which ones are active without polling each pipeline individually.
func handleListAllRuns(w http.ResponseWriter, r *http.Request, store *pipelines.Store) {
	runs, err := store.ListAllRuns(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if runs == nil {
		runs = []*pipelines.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func writePipelineNotFound(w http.ResponseWriter, err error) {
	if errors.Is(err, pipelines.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "pipeline or run not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", err.Error())
}

func decodeJSON(r *http.Request, v any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func randHex() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
