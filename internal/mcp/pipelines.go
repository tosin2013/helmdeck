// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// pipelines.go — MCP surface for pipelines (ADR 041). The helmdeck__pipeline-*
// tools are non-pack tools intercepted before the registry lookup, exactly
// like pack.start/status/result. They let any connected agent (OpenClaw,
// Gemini CLI, Claude Code) list/create/run pipelines conversationally.
// Runs are async, so a separate run-status tool is exposed for polling.

import (
	"context"
	"encoding/json"
)

// PipelineService is the narrow surface PackServer needs. The production
// adapter lives in internal/api and wraps the real pipeline store+runner;
// returning marshaled JSON keeps internal/mcp from importing
// internal/pipelines (cyclic-import-safe, MCP owns the wire shape).
type PipelineService interface {
	List(ctx context.Context) (json.RawMessage, error)
	Get(ctx context.Context, id string) (json.RawMessage, error)
	Create(ctx context.Context, def json.RawMessage) (json.RawMessage, error)
	// StartRun returns the run id, plus coalesced=true when the request was
	// deduped onto an already in-flight identical run instead of starting
	// a new one. Callers polling pipeline-run-status see the same status
	// progression either way.
	StartRun(ctx context.Context, id string, inputs json.RawMessage) (runID string, coalesced bool, err error)
	RunStatus(ctx context.Context, runID string) (json.RawMessage, error)
	Rerun(ctx context.Context, runID string) (newRunID string, coalesced bool, err error)
	Cancel(ctx context.Context, runID string) error
}

// WithPipelines wires the pipeline service so the helmdeck__pipeline-*
// tools appear in tools/list and dispatch in tools/call. Omitted ⇒ tools
// absent.
func WithPipelines(p PipelineService) PackServerOption {
	return func(s *PackServer) { s.pipelines = p }
}

// pipelineTools returns the MCP tools backing pipelines, or nil when no
// pipeline service is wired.
//
// Tool names are BARE (`pipeline-run`, not `helmdeck__pipeline-run`) — exactly
// like pack tools, which are advertised as `pack.Name` (server.go) and let the
// MCP client namespace them with the server name. Baking the `helmdeck__`
// prefix in here made namespacing clients double-prefix the tool to
// `helmdeck__helmdeck__pipeline-run`, so the documented `helmdeck__pipeline-run`
// (UI copy-prompt, SKILL.md, prompt templates) was unreachable. Keep these bare;
// the resolved client-facing name is `helmdeck__pipeline-*` (what the docs say).
func (s *PackServer) pipelineTools() []Tool {
	if s.pipelines == nil {
		return nil
	}
	idSchema := mustJSON(map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"required":   []string{"id"},
	})
	return []Tool{
		{
			Name:        "pipeline-list",
			Description: "List all helmdeck pipelines (built-in starters + user-created) with their steps and status. A pipeline is a saved, ordered sequence of pack calls with ${{ steps.X.output.field }} templating between steps.",
			InputSchema: mustJSON(map[string]any{"type": "object", "properties": map[string]any{}}),
		},
		{
			Name:        "pipeline-get",
			Description: "Get one pipeline's full definition by id.",
			InputSchema: idSchema,
		},
		{
			Name:        "pipeline-create",
			Description: "Create a new pipeline from an ordered list of steps. Each step is {id, pack, input}; a step's input may reference an earlier step via ${{ steps.<id>.output.<field> }} or a run input via ${{ inputs.<name> }}. Discover valid chat-model IDs via the helmdeck://models resource, and voice/image-model IDs via helmdeck://voices and helmdeck://image-models, before setting a `model` or referencing podcast/image packs.",
			InputSchema: mustJSON(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"steps":       map[string]any{"type": "array", "description": "Ordered [{id, pack, input}] step list."},
				},
				"required": []string{"name", "steps"},
			}),
		},
		{
			Name:        "pipeline-run",
			Description: "Start a pipeline run (async) and return a run_id immediately. Pass `inputs` for the pipeline's ${{ inputs.* }} references. Then poll helmdeck__pipeline-run-status. SINGLE-FLIGHT: if an identical run is already in-flight (same caller, pipeline id, and inputs), the response returns that existing run_id with `coalesced: true` instead of starting a duplicate — this prevents the failure mode where a tool-call timeout causes the LLM to re-fire the same long-running pipeline and OOM both. Don't treat `coalesced: true` as an error; just poll the returned run_id as usual.",
			InputSchema: mustJSON(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "string"},
					"inputs": map[string]any{"type": "object", "description": "Values for the pipeline's ${{ inputs.* }} references."},
				},
				"required": []string{"id"},
			}),
		},
		{
			Name:        "pipeline-run-status",
			Description: "Get the status of a pipeline run by run_id: overall status (pending|running|succeeded|failed) plus per-step outputs/errors. A failed run includes failure_class (caller_fixable|pack_bug|transient|state_changed) and a failure_reason saying what to do — fix the input, re-run, or file a helmdeck issue. Poll every few seconds until terminal.",
			InputSchema: mustJSON(map[string]any{
				"type":       "object",
				"properties": map[string]any{"run_id": map[string]any{"type": "string"}},
				"required":   []string{"run_id"},
			}),
		},
		{
			Name:        "pipeline-rerun",
			Description: "Re-run an existing run from the top with the same pipeline + inputs (the CI/CD 'retry this job' affordance). Use after fixing a caller_fixable failure, or to retry a transient one. Returns a new run_id, OR — if an identical run is already in-flight — that existing run_id with `coalesced: true` (same single-flight guarantee as pipeline-run).",
			InputSchema: mustJSON(map[string]any{
				"type":       "object",
				"properties": map[string]any{"run_id": map[string]any{"type": "string"}},
				"required":   []string{"run_id"},
			}),
		},
		{
			Name:        "pipeline-cancel",
			Description: "Hard-stop a running or pending pipeline run by run_id. Force-removes the run's session container(s) so an in-flight render frees CPU within ~1-2s. Already-terminal runs return an error. Partial outputs from the in-flight step are discarded.",
			InputSchema: mustJSON(map[string]any{
				"type":       "object",
				"properties": map[string]any{"run_id": map[string]any{"type": "string"}},
				"required":   []string{"run_id"},
			}),
		},
	}
}

// dispatchPipelineTool handles the helmdeck__pipeline-* tools. Returns
// (result, true) when handled; (nil, false) to fall through to the pack
// path (so an unknown tool still yields -32601).
func (s *PackServer) dispatchPipelineTool(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, bool) {
	if s.pipelines == nil {
		return nil, false
	}
	switch name {
	case "pipeline-list":
		out, err := s.pipelines.List(ctx)
		return jsonOrErr(out, err), true
	case "pipeline-get":
		var a struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(arguments, &a); err != nil || a.ID == "" {
			return errorToolResult("invalid_input", "helmdeck__pipeline-get: id is required"), true
		}
		out, err := s.pipelines.Get(ctx, a.ID)
		return jsonOrErr(out, err), true
	case "pipeline-create":
		out, err := s.pipelines.Create(ctx, arguments)
		return jsonOrErr(out, err), true
	case "pipeline-run":
		var a struct {
			ID     string          `json:"id"`
			Inputs json.RawMessage `json:"inputs"`
		}
		if err := json.Unmarshal(arguments, &a); err != nil || a.ID == "" {
			return errorToolResult("invalid_input", "helmdeck__pipeline-run: id is required"), true
		}
		runID, coalesced, err := s.pipelines.StartRun(ctx, a.ID, a.Inputs)
		if err != nil {
			return errorToolResult("pipeline_run_failed", err.Error()), true
		}
		body, _ := json.Marshal(map[string]any{"run_id": runID, "pipeline_id": a.ID, "status": "pending", "coalesced": coalesced})
		return okToolResult(body), true
	case "pipeline-run-status":
		var a struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(arguments, &a); err != nil || a.RunID == "" {
			return errorToolResult("invalid_input", "helmdeck__pipeline-run-status: run_id is required"), true
		}
		out, err := s.pipelines.RunStatus(ctx, a.RunID)
		return jsonOrErr(out, err), true
	case "pipeline-rerun":
		var a struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(arguments, &a); err != nil || a.RunID == "" {
			return errorToolResult("invalid_input", "helmdeck__pipeline-rerun: run_id is required"), true
		}
		runID, coalesced, err := s.pipelines.Rerun(ctx, a.RunID)
		if err != nil {
			return errorToolResult("pipeline_run_failed", err.Error()), true
		}
		body, _ := json.Marshal(map[string]any{"run_id": runID, "status": "pending", "coalesced": coalesced})
		return okToolResult(body), true
	case "pipeline-cancel":
		var a struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(arguments, &a); err != nil || a.RunID == "" {
			return errorToolResult("invalid_input", "helmdeck__pipeline-cancel: run_id is required"), true
		}
		if err := s.pipelines.Cancel(ctx, a.RunID); err != nil {
			return errorToolResult("pipeline_cancel_failed", err.Error()), true
		}
		body, _ := json.Marshal(map[string]string{"run_id": a.RunID, "status": "cancelled"})
		return okToolResult(body), true
	}
	return nil, false
}

func jsonOrErr(body json.RawMessage, err error) map[string]any {
	if err != nil {
		return errorToolResult("pipeline_error", err.Error())
	}
	return okToolResult(body)
}

func okToolResult(body json.RawMessage) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": false,
	}
}
