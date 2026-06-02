// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// mcp_pipelines.go — adapts the pipeline store+runner to the narrow
// mcp.PipelineService interface (ADR 041), so internal/mcp need not
// import internal/pipelines. Methods return marshaled JSON; the MCP
// layer wraps it in a tool-result content block.

import (
	"context"
	"encoding/json"

	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/pipelines"
)

type pipelineServiceAdapter struct {
	store     *pipelines.Store
	runner    *pipelines.Runner
	packExist func(name, version string) bool
}

func (a pipelineServiceAdapter) List(ctx context.Context) (json.RawMessage, error) {
	list, err := a.store.List(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []*pipelines.Pipeline{}
	}
	return json.Marshal(list)
}

func (a pipelineServiceAdapter) Get(ctx context.Context, id string) (json.RawMessage, error) {
	p, err := a.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

func (a pipelineServiceAdapter) Create(ctx context.Context, def json.RawMessage) (json.RawMessage, error) {
	var p pipelines.Pipeline
	if err := json.Unmarshal(def, &p); err != nil {
		return nil, err
	}
	p.ID = "pipe_" + randHex()
	p.Builtin = false
	if err := pipelines.Validate(&p, a.packExist); err != nil {
		return nil, err
	}
	if err := a.store.Create(ctx, &p); err != nil {
		return nil, err
	}
	return json.Marshal(&p)
}

func (a pipelineServiceAdapter) StartRun(ctx context.Context, id string, inputs json.RawMessage) (string, bool, error) {
	return a.runner.StartRun(ctx, id, inputs, mcpCaller(ctx))
}

func (a pipelineServiceAdapter) RunStatus(ctx context.Context, runID string) (json.RawMessage, error) {
	run, err := a.runner.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(run)
}

func (a pipelineServiceAdapter) Rerun(ctx context.Context, runID string) (string, bool, error) {
	return a.runner.Rerun(ctx, runID, mcpCaller(ctx))
}

func (a pipelineServiceAdapter) Cancel(ctx context.Context, runID string) error {
	return a.runner.CancelRun(ctx, runID)
}

// mcpCaller pulls the authenticated subject off the tools/call context
// (the MCP server attaches it via auth.FromContext before dispatch), so
// pipeline runs started over MCP namespace per-caller like REST does.
func mcpCaller(ctx context.Context) string {
	if c := auth.FromContext(ctx); c != nil {
		return c.Subject
	}
	return ""
}

// newPipelineServiceAdapter builds the adapter from Deps, or returns
// (nil-ok=false) when pipelines aren't wired.
func newPipelineServiceAdapter(deps Deps) (pipelineServiceAdapter, bool) {
	if deps.PipelineStore == nil || deps.PipelineRunner == nil {
		return pipelineServiceAdapter{}, false
	}
	packExist := func(name, ver string) bool {
		if deps.PackRegistry == nil {
			return true
		}
		_, err := deps.PackRegistry.Get(name, ver)
		return err == nil
	}
	return pipelineServiceAdapter{store: deps.PipelineStore, runner: deps.PipelineRunner, packExist: packExist}, true
}
