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
	Packs          []ProjectedPack     `json:"packs"`
	Pipelines      []ProjectedPipeline `json:"pipelines"`
	CommonFindings []CommonFinding     `json:"common_findings,omitempty"`
}

// CommonFinding aggregates one validation-suite finding code across
// the caller's audit history. Ranked by occurrence_count descending,
// capped at DefaultsFindingsTopN. Issue #570.
type CommonFinding struct {
	Code            string `json:"code"`
	Pack            string `json:"pack"`
	Severity        string `json:"severity"`
	OccurrenceCount int    `json:"occurrence_count"`
	LastSeenUnix    int64  `json:"last_seen_unix"`
}

// DefaultsFindingsTopN caps the common_findings list returned in
// projections. 20 is roomy enough to surface the long tail of
// codes a Tier C model typically produces (we saw 3 distinct codes
// in a single empirical lint run), but small enough that an LLM's
// prompt prefix doesn't drown in them.
const DefaultsFindingsTopN = 20

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
	out.CommonFindings = projectCommonFindings(packAudits)
	return out, nil
}

// ProjectDefaults runs the projection against pre-decoded audit slices.
// Used by callers that already hold audit rows (e.g. helmdeck.route
// reads via the per-caller memoryAdapter, not the raw store). For the
// store-backed case use BuildDefaults instead.
func ProjectDefaults(packAudits []PackAudit, pipeAudits []PipelineAudit) Defaults {
	return Defaults{
		Packs:          projectPackEntries(packAudits),
		Pipelines:      projectPipelineEntries(pipeAudits),
		CommonFindings: projectCommonFindings(packAudits),
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

// projectCommonFindings aggregates findings across pack audits by
// `code` — counts occurrences, tracks last_seen, attributes to the
// pack that emitted the finding. Sorted by occurrence_count desc,
// capped at DefaultsFindingsTopN.
//
// Findings come from ANY audit row regardless of outcome — unlike
// LearnInputs (which only learns from successful runs), findings ARE
// the failure signal we want to surface. The LLM should learn to
// AVOID them, so failure-row findings are exactly what we need.
//
// Issue #570 slice 2: this is the data the compose pack's prompt
// template will read in slice 4 to remind the LLM "you've made
// these mistakes N times; don't repeat them."
func projectCommonFindings(audits []PackAudit) []CommonFinding {
	type acc struct {
		pack     string
		severity string
		count    int
		lastSeen int64
	}
	by := map[string]*acc{}
	for _, a := range audits {
		for _, f := range a.Findings {
			if f.Code == "" {
				continue
			}
			cur, ok := by[f.Code]
			if !ok {
				cur = &acc{}
				by[f.Code] = cur
			}
			cur.count++
			// Pack + severity from the MOST RECENT occurrence — old
			// runs may attribute the same code to a different pack
			// over time (rare; mostly stable).
			if a.AtUnix >= cur.lastSeen {
				cur.pack = a.Pack
				cur.severity = f.Severity
				cur.lastSeen = a.AtUnix
			}
		}
	}
	out := make([]CommonFinding, 0, len(by))
	for code, a := range by {
		out = append(out, CommonFinding{
			Code:            code,
			Pack:            a.pack,
			Severity:        a.severity,
			OccurrenceCount: a.count,
			LastSeenUnix:    a.lastSeen,
		})
	}
	// Sort by count desc, then code asc for stable tiebreak.
	sortCommonFindings(out)
	if len(out) > DefaultsFindingsTopN {
		out = out[:DefaultsFindingsTopN]
	}
	return out
}

func sortCommonFindings(s []CommonFinding) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0; j-- {
			if s[j].OccurrenceCount > s[j-1].OccurrenceCount ||
				(s[j].OccurrenceCount == s[j-1].OccurrenceCount && s[j].Code < s[j-1].Code) {
				s[j], s[j-1] = s[j-1], s[j]
				continue
			}
			break
		}
	}
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
