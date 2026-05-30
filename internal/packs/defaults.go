package packs

// defaults.go — caller-defaults projection over audit memory (ADR 047
// PR #2 surface, reused by PR #3's helmdeck.route meta-pack).
//
// Logic was originally written in internal/mcp/my_defaults.go for the
// helmdeck://my-defaults resource. PR #3 needs the same projection
// inside the route handler — a Go pack can't import internal/mcp
// without an import cycle, and we'd rather not duplicate the sort +
// most-common picker. So the projection lives here, in the same
// package as PackAudit / PipelineAudit, and the MCP resource is now a
// thin wrapper that adds wire-shape concerns (scope label, note).

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// DefaultsTopN caps how many distinct packs/pipelines surface in a
// projection. The most-used N (by count) are kept.
const DefaultsTopN = 12

// ProjectedPack is one pack's per-caller usage summary.
type ProjectedPack struct {
	ID           string            `json:"id"`
	Calls        int               `json:"calls"`
	LastUsedUnix int64             `json:"last_used_unix"`
	CommonInputs map[string]string `json:"common_inputs,omitempty"`
}

// ProjectedPipeline is one pipeline's per-caller usage summary.
type ProjectedPipeline struct {
	ID           string            `json:"id"`
	Runs         int               `json:"runs"`
	LastUsedUnix int64             `json:"last_used_unix"`
	CommonInputs map[string]string `json:"common_inputs,omitempty"`
}

// Defaults is the combined projection. Empty slices when no history.
type Defaults struct {
	Packs     []ProjectedPack     `json:"packs"`
	Pipelines []ProjectedPipeline `json:"pipelines"`
}

// BuildDefaults reads the caller's pack_history/* + pipeline_history/*
// entries from the store and aggregates them into the projection.
// Returns a zero-value Defaults (non-nil slices) when the caller has
// no audit history yet — never errors on "empty," only on a real
// backend failure.
func BuildDefaults(ctx context.Context, store memory.MemoryStore, caller string) (Defaults, error) {
	out := Defaults{
		Packs:     []ProjectedPack{},
		Pipelines: []ProjectedPipeline{},
	}
	if store == nil {
		return out, nil
	}

	packEntries, err := store.List(ctx, caller, AuditKeyPrefixPack)
	if err != nil {
		return out, err
	}
	pipeEntries, err := store.List(ctx, caller, AuditKeyPrefixPipeline)
	if err != nil {
		return out, err
	}

	packAudits := make([]PackAudit, 0, len(packEntries))
	for _, e := range packEntries {
		var a PackAudit
		if err := json.Unmarshal(e.Value, &a); err != nil {
			continue
		}
		packAudits = append(packAudits, a)
	}
	pipeAudits := make([]PipelineAudit, 0, len(pipeEntries))
	for _, e := range pipeEntries {
		var a PipelineAudit
		if err := json.Unmarshal(e.Value, &a); err != nil {
			continue
		}
		pipeAudits = append(pipeAudits, a)
	}

	out.Packs = projectPackEntries(packAudits)
	out.Pipelines = projectPipelineEntries(pipeAudits)
	return out, nil
}

// ProjectDefaults runs the projection against pre-decoded audit slices.
// Used by callers that already hold audit rows (e.g. helmdeck.route
// reads via the per-caller memoryAdapter, not the raw store). For the
// store-backed case use BuildDefaults instead.
func ProjectDefaults(packAudits []PackAudit, pipeAudits []PipelineAudit) Defaults {
	return Defaults{
		Packs:     projectPackEntries(packAudits),
		Pipelines: projectPipelineEntries(pipeAudits),
	}
}

// projectPackEntries groups successful audit rows by pack, picks the
// most-common value per learnable field, ranks by call count, caps at
// DefaultsTopN. Failures are excluded — only successful runs teach
// defaults (a caller-fixable failure with persona="executive" should
// NOT pin executive as a learned default).
func projectPackEntries(audits []PackAudit) []ProjectedPack {
	type acc struct {
		calls    int
		lastUsed int64
		inputs   map[string]map[string]int
	}
	by := map[string]*acc{}
	for _, a := range audits {
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
	out := make([]ProjectedPack, 0, len(by))
	for id, g := range by {
		out = append(out, ProjectedPack{
			ID:           id,
			Calls:        g.calls,
			LastUsedUnix: g.lastUsed,
			CommonInputs: pickMostCommon(g.inputs),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].LastUsedUnix > out[j].LastUsedUnix
	})
	if len(out) > DefaultsTopN {
		out = out[:DefaultsTopN]
	}
	return out
}

func projectPipelineEntries(audits []PipelineAudit) []ProjectedPipeline {
	type acc struct {
		runs     int
		lastUsed int64
		inputs   map[string]map[string]int
	}
	by := map[string]*acc{}
	for _, a := range audits {
		// Pipeline runs report "succeeded" via Run.Status; pack-level
		// stays "ok". Accept both so this is portable across either
		// audit type.
		if a.Outcome != "succeeded" && a.Outcome != "ok" {
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
	out := make([]ProjectedPipeline, 0, len(by))
	for id, g := range by {
		out = append(out, ProjectedPipeline{
			ID:           id,
			Runs:         g.runs,
			LastUsedUnix: g.lastUsed,
			CommonInputs: pickMostCommon(g.inputs),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].LastUsedUnix > out[j].LastUsedUnix
	})
	if len(out) > DefaultsTopN {
		out = out[:DefaultsTopN]
	}
	return out
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
