// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// my_defaults.go — helmdeck://my-defaults projection over per-caller audit
// memory (ADR 047 PR #2).
//
// Reads pack_history/* and pipeline_history/* entries the audit hook in
// internal/packs/audit.go wrote under the caller's bare namespace,
// aggregates frequency + most-common input values, and returns:
//
//	{
//	  "scope": "caller=<id>",
//	  "fetched_at": "...",
//	  "packs":      [{ id, calls, last_used_unix, common_inputs: {persona: "technical", ...} }, ...],
//	  "pipelines":  [{ id, runs,  last_used_unix, common_inputs: {...} }, ...]
//	}
//
// When the caller has no audit history (fresh user, just-cleared) the
// arrays are empty — agents should treat empty as "no learned defaults,
// ask the user".

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// myDefaultsTopN caps how many distinct packs/pipelines surface in the
// projection. The most-used N are kept by call count.
const myDefaultsTopN = 12

// MyDefaults is the wire shape of the projection.
type MyDefaults struct {
	Scope     string                `json:"scope"`
	FetchedAt string                `json:"fetched_at"`
	Packs     []MyDefaultsPackEntry `json:"packs"`
	Pipelines []MyDefaultsPipeEntry `json:"pipelines"`
	Note      string                `json:"note,omitempty"`
}

// MyDefaultsPackEntry summarises one pack's per-caller usage.
type MyDefaultsPackEntry struct {
	ID           string            `json:"id"`
	Calls        int               `json:"calls"`
	LastUsedUnix int64             `json:"last_used_unix"`
	CommonInputs map[string]string `json:"common_inputs,omitempty"`
}

// MyDefaultsPipeEntry summarises one pipeline's per-caller usage.
type MyDefaultsPipeEntry struct {
	ID           string            `json:"id"`
	Runs         int               `json:"runs"`
	LastUsedUnix int64             `json:"last_used_unix"`
	CommonInputs map[string]string `json:"common_inputs,omitempty"`
}

// buildMyDefaults projects the caller's audit history into the
// MyDefaults shape. Returns an empty (but non-nil) projection when
// the store is nil or empty.
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

	packAudits, err := readPackAudits(ctx, store, caller)
	if err != nil {
		return out, &rpcError{Code: -32603, Message: "my-defaults: read pack audits: " + err.Error()}
	}
	pipeAudits, err := readPipelineAudits(ctx, store, caller)
	if err != nil {
		return out, &rpcError{Code: -32603, Message: "my-defaults: read pipeline audits: " + err.Error()}
	}

	out.Packs = projectPackEntries(packAudits)
	out.Pipelines = projectPipelineEntries(pipeAudits)
	if len(out.Packs) == 0 && len(out.Pipelines) == 0 {
		out.Note = "no audit history yet; defaults will fill in as packs/pipelines run under this caller"
	}
	return out, nil
}

func readPackAudits(ctx context.Context, store memory.MemoryStore, caller string) ([]packs.PackAudit, error) {
	entries, err := store.List(ctx, caller, packs.AuditKeyPrefixPack)
	if err != nil {
		return nil, err
	}
	out := make([]packs.PackAudit, 0, len(entries))
	for _, e := range entries {
		var a packs.PackAudit
		if err := json.Unmarshal(e.Value, &a); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func readPipelineAudits(ctx context.Context, store memory.MemoryStore, caller string) ([]packs.PipelineAudit, error) {
	entries, err := store.List(ctx, caller, packs.AuditKeyPrefixPipeline)
	if err != nil {
		return nil, err
	}
	out := make([]packs.PipelineAudit, 0, len(entries))
	for _, e := range entries {
		var a packs.PipelineAudit
		if err := json.Unmarshal(e.Value, &a); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// projectPackEntries groups audits by pack ID, picks the most-common
// value per learnable input field across each group, and returns the
// top-N most-used packs.
func projectPackEntries(audits []packs.PackAudit) []MyDefaultsPackEntry {
	type acc struct {
		calls    int
		lastUsed int64
		inputs   map[string]map[string]int
	}
	by := map[string]*acc{}
	for _, a := range audits {
		// Only learn from successful runs — failures with caller_fixable
		// inputs would otherwise teach the wrong defaults.
		if a.Outcome != "ok" {
			continue
		}
		g := by[a.Pack]
		if g == nil {
			g = &acc{inputs: map[string]map[string]int{}}
			by[a.Pack] = g
		}
		g.calls++
		if a.AtUnix > g.lastUsed {
			g.lastUsed = a.AtUnix
		}
		for k, v := range a.LearnInputs {
			if g.inputs[k] == nil {
				g.inputs[k] = map[string]int{}
			}
			g.inputs[k][v]++
		}
	}
	out := make([]MyDefaultsPackEntry, 0, len(by))
	for id, g := range by {
		out = append(out, MyDefaultsPackEntry{
			ID:           id,
			Calls:        g.calls,
			LastUsedUnix: g.lastUsed,
			CommonInputs: pickMostCommon(g.inputs),
		})
	}
	return sortAndCapPacks(out)
}

// projectPipelineEntries mirrors projectPackEntries for pipeline audits.
func projectPipelineEntries(audits []packs.PipelineAudit) []MyDefaultsPipeEntry {
	type acc struct {
		runs     int
		lastUsed int64
		inputs   map[string]map[string]int
	}
	by := map[string]*acc{}
	for _, a := range audits {
		if !strings.EqualFold(a.Outcome, "succeeded") && a.Outcome != "ok" {
			continue
		}
		g := by[a.Pipeline]
		if g == nil {
			g = &acc{inputs: map[string]map[string]int{}}
			by[a.Pipeline] = g
		}
		g.runs++
		if a.AtUnix > g.lastUsed {
			g.lastUsed = a.AtUnix
		}
		for k, v := range a.LearnInputs {
			if g.inputs[k] == nil {
				g.inputs[k] = map[string]int{}
			}
			g.inputs[k][v]++
		}
	}
	out := make([]MyDefaultsPipeEntry, 0, len(by))
	for id, g := range by {
		out = append(out, MyDefaultsPipeEntry{
			ID:           id,
			Runs:         g.runs,
			LastUsedUnix: g.lastUsed,
			CommonInputs: pickMostCommon(g.inputs),
		})
	}
	return sortAndCapPipelines(out)
}

func pickMostCommon(counts map[string]map[string]int) map[string]string {
	if len(counts) == 0 {
		return nil
	}
	out := map[string]string{}
	for field, values := range counts {
		var bestVal string
		bestN := 0
		for v, n := range values {
			if n > bestN || (n == bestN && v < bestVal) {
				bestVal = v
				bestN = n
			}
		}
		out[field] = bestVal
	}
	return out
}

func sortAndCapPacks(s []MyDefaultsPackEntry) []MyDefaultsPackEntry {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Calls != s[j].Calls {
			return s[i].Calls > s[j].Calls
		}
		return s[i].LastUsedUnix > s[j].LastUsedUnix
	})
	if len(s) > myDefaultsTopN {
		s = s[:myDefaultsTopN]
	}
	return s
}

func sortAndCapPipelines(s []MyDefaultsPipeEntry) []MyDefaultsPipeEntry {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Runs != s[j].Runs {
			return s[i].Runs > s[j].Runs
		}
		return s[i].LastUsedUnix > s[j].LastUsedUnix
	})
	if len(s) > myDefaultsTopN {
		s = s[:myDefaultsTopN]
	}
	return s
}
