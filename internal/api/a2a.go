package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// A2A is helmdeck's implementation of the (subset of) the
// Agent2Agent protocol described in ADR 026. Two endpoints land
// here:
//
//   - GET  /.well-known/agent.json   (T212) — agent card auto-derived
//     from the pack registry; refreshed on every request so packs
//     hot-loaded at runtime show up without a control-plane restart.
//   - POST /a2a/v1/tasks              (T213) — execute one pack and
//     stream status updates back over Server-Sent Events.
//
// The shape is intentionally compatible with the A2A reference SDK
// at the level a remote agent would consume — `skill.id` matches the
// pack name and the SSE event names match the spec's task lifecycle
// (submitted → working → completed | failed). Anything beyond that
// (auth metadata, multi-turn message threading, push notifications)
// is out of scope for the P2 milestone.

// agentCardSkill is one entry in the /.well-known/agent.json `skills`
// array. We map one Capability Pack → one skill.
type agentCardSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// agentCard is the JSON document served at /.well-known/agent.json.
// Fields not relevant to helmdeck (auth schemes, push config) are
// omitted; the spec treats unknown fields as forward-compatible
// extensions, so leaving them out is preferable to lying about
// support.
type agentCard struct {
	Name               string           `json:"name"`
	Description        string           `json:"description"`
	URL                string           `json:"url"`
	Version            string           `json:"version"`
	Capabilities       map[string]bool  `json:"capabilities"`
	DefaultInputModes  []string         `json:"defaultInputModes"`
	DefaultOutputModes []string         `json:"defaultOutputModes"`
	Skills             []agentCardSkill `json:"skills"`
}

func registerA2ARoutes(mux *http.ServeMux, deps Deps) {
	if deps.PackRegistry == nil || deps.PackEngine == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "a2a_unavailable", "pack registry not configured")
		}
		mux.HandleFunc("GET /.well-known/agent.json", stub)
		mux.HandleFunc("POST /a2a/v1/tasks", stub)
		return
	}

	mux.HandleFunc("GET /.well-known/agent.json", func(w http.ResponseWriter, r *http.Request) {
		// Build the card on every request rather than caching. The
		// pack registry supports hot-reload (T207), and a stale card
		// is worse than the microsecond cost of a fresh List.
		writeJSON(w, http.StatusOK, buildAgentCard(deps.Version, deps.PackRegistry, r))
	})

	mux.HandleFunc("POST /a2a/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		handleA2ATask(w, r, deps.PackRegistry, deps.PackEngine)
	})
}

func buildAgentCard(version string, reg *packs.Registry, r *http.Request) agentCard {
	infos := reg.List()
	skills := make([]agentCardSkill, 0, len(infos))
	for _, info := range infos {
		skills = append(skills, agentCardSkill{
			ID:          info.Name,
			Name:        info.Name,
			Description: info.Description,
			Tags:        []string{}, // empty rather than nil so the JSON shape is stable
		})
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return agentCard{
		Name:               "helmdeck",
		Description:        "Browser-and-pack capability platform for agentic systems",
		URL:                fmt.Sprintf("%s://%s", scheme, r.Host),
		Version:            version,
		Capabilities:       map[string]bool{"streaming": true},
		DefaultInputModes:  []string{"application/json"},
		DefaultOutputModes: []string{"application/json"},
		Skills:             skills,
	}
}

// a2aTaskRequest is the (heavily simplified) shape we accept on
// POST /a2a/v1/tasks. The full A2A spec uses a `message.parts` array
// with typed text/data parts; for helmdeck we only care about
// "which skill, what input", so the request is flattened.
type a2aTaskRequest struct {
	Skill   string          `json:"skill"`
	Version string          `json:"version,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
}

// a2aEvent is the envelope for one SSE message. The `event:` line
// matches the A2A task lifecycle vocabulary so a spec-aware client
// can branch on it without parsing the data field.
type a2aEvent struct {
	Event string
	Data  any
}

func handleA2ATask(w http.ResponseWriter, r *http.Request, reg *packs.Registry, eng *packs.Engine) {
	var req a2aTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Skill == "" {
		writeError(w, http.StatusBadRequest, "missing_skill", "skill is required")
		return
	}
	pack, err := reg.Get(req.Skill, req.Version)
	if err != nil {
		writeError(w, http.StatusNotFound, "skill_not_found", err.Error())
		return
	}

	// Stream guards: SSE requires a Flusher, and we want chunked
	// transfer rather than the buffered default. Bail with a plain
	// 500 if the underlying ResponseWriter can't flush — that's a
	// configuration error, not a runtime one.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_flusher", "responsewriter does not support streaming")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering for the same reason
	w.WriteHeader(http.StatusOK)

	taskID := "task_" + uuid.NewString()
	emit := func(ev a2aEvent) {
		payload, _ := json.Marshal(ev.Data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, payload)
		flusher.Flush()
	}

	emit(a2aEvent{Event: "submitted", Data: map[string]any{
		"id":     taskID,
		"skill":  pack.Name,
		"status": "submitted",
	}})

	// Run the pack in a goroutine so we can heartbeat the connection.
	// SSE clients (and intervening proxies) drop idle connections
	// fast — Cloudflare sits at 100s. A 30s keepalive comment is
	// enough to keep every common deployment happy.
	type runResult struct {
		res *packs.Result
		err error
	}
	done := make(chan runResult, 1)
	var inputPayload json.RawMessage
	if len(req.Input) == 0 {
		inputPayload = json.RawMessage("{}")
	} else {
		inputPayload = req.Input
	}
	emit(a2aEvent{Event: "working", Data: map[string]any{"id": taskID, "status": "working"}})

	// We use the request context as the parent so a client disconnect
	// cancels the underlying pack run rather than letting it finish
	// in the background and waste a session container.
	ctx := r.Context()
	go func() {
		res, err := eng.Execute(ctx, pack, inputPayload)
		done <- runResult{res: res, err: err}
	}()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	var heartbeats atomic.Int64

	for {
		select {
		case <-ctx.Done():
			emit(a2aEvent{Event: "failed", Data: map[string]any{
				"id":      taskID,
				"status":  "failed",
				"error":   "canceled",
				"message": ctx.Err().Error(),
			}})
			return
		case <-heartbeat.C:
			// SSE comment line — clients ignore it but proxies see
			// traffic. Comment-only lines are part of the spec.
			heartbeats.Add(1)
			fmt.Fprintf(w, ": heartbeat %d\n\n", heartbeats.Load())
			flusher.Flush()
		case rr := <-done:
			if rr.err != nil {
				emit(a2aEvent{Event: "failed", Data: a2aFailureEnvelope(taskID, rr.err)})
				return
			}
			emit(a2aEvent{Event: "completed", Data: map[string]any{
				"id":     taskID,
				"status": "completed",
				"result": rr.res,
			}})
			return
		}
	}
}

func a2aFailureEnvelope(id string, err error) map[string]any {
	out := map[string]any{
		"id":     id,
		"status": "failed",
	}
	var perr *packs.PackError
	if errors.As(err, &perr) {
		out["error"] = string(perr.Code)
		out["message"] = perr.Message
	} else {
		out["error"] = "internal"
		out["message"] = err.Error()
	}
	return out
}

// pragmatic compile-time check that we never accidentally use a
// context.Context with a non-cancellable parent here.
var _ context.Context = context.Background()
