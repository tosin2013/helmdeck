// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// my_plans.go — helmdeck://my-plans MCP resource (ADR 050 PR #3).
//
// Projects the caller's plan_history audit category (written by
// helmdeck.plan in ADR 049) into a structured view of past
// decompositions. Operators and agents read this resource to:
//
//   - See which intent shapes the planner has handled most often
//     (frequency by intent_sha, top tools per group)
//   - Audit the planner's behavior — when an intent appears repeatedly
//     and produces a stable decomposition, that's a "learned plan"
//     the agent can prefer over re-running the planner
//   - Inform future PR #4 LLM-filter escalation: when lexical
//     retrieval is ambiguous, the planner's own history is a prior
//     on which tools to consider
//
// Wire shape:
//
//   {
//     "scope":      "caller=<id>",
//     "fetched_at": "...",
//     "groups": [
//       {
//         "intent_sha": "a1b2c3d4e5f60718",
//         "count":      4,
//         "complexity": "pack-chain",          // most-frequent classification for this sha
//         "top_tools":  ["helmdeck.memory_store", "helmdeck.image_generate"],
//         "last_unix":  1717209600,
//         "models":     ["openrouter/openrouter/free"]
//       }
//     ],
//     "note": "..."   // when empty or memory disabled
//   }
//
// Privacy / scope: rows are namespaced per caller (JWT subject), same
// as every other memory surface. The projection deliberately does NOT
// surface the rewritten_prompt or step args — those would echo user
// content. We surface only the intent_sha (opaque hash) plus the
// pre-aggregated tool/model frequencies. Audit categories
// (pack_history, pipeline_history) are unchanged by this resource.

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// myPlansMaxGroups bounds the number of intent groups the projection
// surfaces. Keeps the resource compact for callers with deep
// histories; a separate per-group view would be a follow-up if
// operators ask for it.
const myPlansMaxGroups = 25

// myPlansTopToolsPerGroup bounds the tools-per-group list. We don't
// echo every step's tool name; the top 5 most-used tools across a
// group's plans is enough signal for routing decisions.
const myPlansTopToolsPerGroup = 5

// MyPlans is the wire shape of the projection.
type MyPlans struct {
	Scope     string         `json:"scope"`
	FetchedAt string         `json:"fetched_at"`
	Groups    []MyPlansGroup `json:"groups"`
	Note      string         `json:"note,omitempty"`
}

// MyPlansGroup is one intent-sha cohort's aggregated view.
type MyPlansGroup struct {
	IntentSHA  string   `json:"intent_sha"`
	Count      int      `json:"count"`
	Complexity string   `json:"complexity,omitempty"`
	TopTools   []string `json:"top_tools,omitempty"`
	LastUnix   int64    `json:"last_unix,omitempty"`
	Models     []string `json:"models,omitempty"`
}

func (s *PackServer) buildMyPlans(ctx context.Context, caller string) (MyPlans, *rpcError) {
	out := MyPlans{
		Scope:     "caller=" + caller,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Groups:    []MyPlansGroup{},
	}
	var store memory.MemoryStore
	if s.engine != nil {
		store = s.engine.MemoryStore()
	}
	if store == nil {
		out.Note = "memory layer disabled — no plan history is being captured. Pin HELMDECK_MEMORY_KEY and restart to enable."
		return out, nil
	}
	entries, err := store.List(ctx, caller, packs.AuditKeyPrefixPlan)
	if err != nil {
		return out, &rpcError{Code: -32603, Message: "list plan_history: " + err.Error()}
	}
	if len(entries) == 0 {
		out.Note = "no plan history yet for this caller — call helmdeck.plan a few times and revisit."
		return out, nil
	}
	out.Groups = aggregatePlanGroups(entries)
	return out, nil
}

// aggregatePlanGroups reduces a flat list of plan_history entries
// into per-intent_sha cohorts. Group ordering is by Count desc (most-
// used first), then LastUnix desc (most recent), then IntentSHA asc
// (deterministic tie-break for tests).
func aggregatePlanGroups(entries []memory.Entry) []MyPlansGroup {
	// Per-sha accumulators. We touch each entry once, then sort + cap
	// the final groups slice — O(N) over audit rows.
	type acc struct {
		count       int
		complexity  map[string]int
		toolCounts  map[string]int
		modelCounts map[string]int
		lastUnix    int64
	}
	bySHA := map[string]*acc{}

	for _, e := range entries {
		var audit packs.PlanAudit
		if err := json.Unmarshal(e.Value, &audit); err != nil {
			// Audit row that doesn't decode is treated as missing —
			// the projection should never fail because a single row
			// is corrupt.
			continue
		}
		if audit.IntentSHA == "" {
			continue
		}
		a, ok := bySHA[audit.IntentSHA]
		if !ok {
			a = &acc{
				complexity:  map[string]int{},
				toolCounts:  map[string]int{},
				modelCounts: map[string]int{},
			}
			bySHA[audit.IntentSHA] = a
		}
		a.count++
		if audit.Complexity != "" {
			a.complexity[audit.Complexity]++
		}
		if audit.Model != "" {
			a.modelCounts[audit.Model]++
		}
		if audit.AtUnix > a.lastUnix {
			a.lastUnix = audit.AtUnix
		}
		for _, step := range audit.Steps {
			if step.Tool != "" && step.Tool != "unknown" {
				a.toolCounts[step.Tool]++
			}
		}
	}

	out := make([]MyPlansGroup, 0, len(bySHA))
	for sha, a := range bySHA {
		out = append(out, MyPlansGroup{
			IntentSHA:  sha,
			Count:      a.count,
			Complexity: topKey(a.complexity),
			TopTools:   topKeys(a.toolCounts, myPlansTopToolsPerGroup),
			LastUnix:   a.lastUnix,
			Models:     topKeys(a.modelCounts, 3),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].LastUnix != out[j].LastUnix {
			return out[i].LastUnix > out[j].LastUnix
		}
		return out[i].IntentSHA < out[j].IntentSHA
	})
	if len(out) > myPlansMaxGroups {
		out = out[:myPlansMaxGroups]
	}
	return out
}

// topKey returns the highest-count key in the map, breaking ties
// alphabetically for determinism. Empty when the map is empty.
func topKey(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	bestKey := ""
	bestCount := -1
	for k, v := range m {
		if v > bestCount || (v == bestCount && k < bestKey) {
			bestKey = k
			bestCount = v
		}
	}
	return bestKey
}

// topKeys returns up to n keys sorted by count desc (tie-breaking
// alphabetically). Used for top_tools and models lists.
func topKeys(m map[string]int, n int) []string {
	if len(m) == 0 || n <= 0 {
		return nil
	}
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, 0, len(all))
	for _, kv := range all {
		out = append(out, kv.k)
	}
	return out
}
