// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// my_defaults.go — helmdeck://my-defaults projection over per-caller audit
// memory (ADR 047 PR #2).
//
// The aggregation logic moved to internal/packs/defaults.go in PR #3 so
// the helmdeck.route meta-pack can reuse it without an internal/mcp
// import cycle. This file is now a thin wrapper that adds MCP wire-shape
// concerns (scope label, fetched_at, explanatory note for empty states)
// on top of the engine-neutral packs.Defaults projection.

import (
	"context"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// MyDefaults is the wire shape of the projection.
type MyDefaults struct {
	Scope     string                `json:"scope"`
	FetchedAt string                `json:"fetched_at"`
	Packs     []MyDefaultsPackEntry `json:"packs"`
	Pipelines []MyDefaultsPipeEntry `json:"pipelines"`
	Note      string                `json:"note,omitempty"`
}

// MyDefaultsPackEntry mirrors packs.ProjectedPack 1:1 for the wire.
type MyDefaultsPackEntry struct {
	ID           string            `json:"id"`
	Calls        int               `json:"calls"`
	LastUsedUnix int64             `json:"last_used_unix"`
	CommonInputs map[string]string `json:"common_inputs,omitempty"`
}

// MyDefaultsPipeEntry mirrors packs.ProjectedPipeline 1:1 for the wire.
type MyDefaultsPipeEntry struct {
	ID           string            `json:"id"`
	Runs         int               `json:"runs"`
	LastUsedUnix int64             `json:"last_used_unix"`
	CommonInputs map[string]string `json:"common_inputs,omitempty"`
}

func (s *PackServer) buildMyDefaults(ctx context.Context, caller string) (MyDefaults, *rpcError) {
	out := MyDefaults{
		Scope:     "caller=" + caller,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Packs:     []MyDefaultsPackEntry{},
		Pipelines: []MyDefaultsPipeEntry{},
	}
	var store memory.MemoryStore
	if s.engine != nil {
		store = s.engine.MemoryStore()
	}
	if store == nil {
		out.Note = "memory store not configured; no learned defaults available"
		return out, nil
	}

	def, err := packs.BuildDefaults(ctx, store, caller)
	if err != nil {
		return out, &rpcError{Code: -32603, Message: "my-defaults: " + err.Error()}
	}

	for _, p := range def.Packs {
		out.Packs = append(out.Packs, MyDefaultsPackEntry{
			ID:           p.ID,
			Calls:        p.Calls,
			LastUsedUnix: p.LastUsedUnix,
			CommonInputs: p.CommonInputs,
		})
	}
	for _, p := range def.Pipelines {
		out.Pipelines = append(out.Pipelines, MyDefaultsPipeEntry{
			ID:           p.ID,
			Runs:         p.Runs,
			LastUsedUnix: p.LastUsedUnix,
			CommonInputs: p.CommonInputs,
		})
	}

	if len(out.Packs) == 0 && len(out.Pipelines) == 0 {
		out.Note = "no audit history yet; defaults will fill in as packs/pipelines run under this caller"
	}
	return out, nil
}
