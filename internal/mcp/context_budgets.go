// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// context_budgets.go — helmdeck://context-budgets MCP resource (ADR
// 050 PR #2).
//
// Surfaces the per-model prompt budgets that llmcontext applies when
// LLM-backed packs (helmdeck.plan, helmdeck.route, future LLM-backed
// packs) compact their catalog projection. Operators can audit which
// model gets which tier + budget without grepping source; agents can
// read this resource to understand why a given plan was made under a
// slim catalog and decide whether to escalate to a stronger model.
//
// The projection is a stable read of the budgets table at request
// time — it has no per-caller state, no memory dependency, and never
// fails. Always listed; the ADR's contract is that budgets are public
// engine policy.
//
// Wire shape:
//
//   {
//     "fetched_at": "2026-06-01T...",
//     "fallback":   { "model": "<TIER_C_DEFAULT>", "input_tokens": 16000, ... },
//     "budgets":    [
//        { "model": "anthropic/claude-haiku-", "input_tokens": 200000, "output_tokens": 4000, "max_catalog_bytes": 0, "tier": "A" },
//        ...
//     ],
//     "policy":     "..."  // operator-readable explanation of how lookup works
//   }

import (
	"context"
	"time"

	"github.com/tosin2013/helmdeck/internal/llmcontext"
)

// ContextBudgets is the wire shape of the projection.
type ContextBudgets struct {
	FetchedAt string               `json:"fetched_at"`
	Fallback  ContextBudgetEntry   `json:"fallback"`
	Budgets   []ContextBudgetEntry `json:"budgets"`
	Policy    string               `json:"policy"`
}

// ContextBudgetEntry is one row of the budgets table. The
// snake_case JSON keys match the rest of helmdeck's resource
// surface so agents reading multiple resources don't have to
// switch conventions.
type ContextBudgetEntry struct {
	Model           string `json:"model"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	MaxCatalogBytes int    `json:"max_catalog_bytes"`
	Tier            string `json:"tier"`
}

// buildContextBudgets projects the llmcontext budgets table into the
// resource shape. No memory dependency, no caller scoping — budgets
// are global engine policy, identical for every caller. Method on
// PackServer for symmetry with the other resource builders; the
// receiver isn't used today but lets us add per-caller overrides in a
// follow-up without breaking the call site in server.go.
func (s *PackServer) buildContextBudgets(_ context.Context) (ContextBudgets, *rpcError) {
	all := llmcontext.AllBudgets()
	out := ContextBudgets{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Fallback:  toBudgetEntry(llmcontext.TierCFallback()),
		Budgets:   make([]ContextBudgetEntry, 0, len(all)),
		Policy:    "BudgetFor(model) returns the matching table entry: exact match first, then longest-prefix wins. Unknown models inherit the `fallback` profile (Tier C). MaxCatalogBytes=0 disables compaction (Tier A frontier models); otherwise CompactCatalog trims metadata in deterministic priority order until the marshaled catalog fits. Pipeline `metadata.supersedes`, pack names, and pipeline ids are never trimmed — they anchor the dispatch graph. Edit `internal/llmcontext/budgets.go` to add a model or change a tier; budgets are operator policy, not auto-detected from provider APIs.",
	}
	for _, b := range all {
		out.Budgets = append(out.Budgets, toBudgetEntry(b))
	}
	return out, nil
}

func toBudgetEntry(b llmcontext.Budget) ContextBudgetEntry {
	return ContextBudgetEntry{
		Model:           b.Model,
		InputTokens:     b.InputTokens,
		OutputTokens:    b.OutputTokens,
		MaxCatalogBytes: b.MaxCatalogBytes,
		Tier:            string(b.Tier),
	}
}
