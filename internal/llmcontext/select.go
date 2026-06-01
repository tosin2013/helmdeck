// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package llmcontext

// select.go — Select() is the public entry point all LLM-backed
// packs call to size their catalog projection to the model's budget
// (ADR 050 PR #3).
//
// Select internally cascades through escalating stages — Tier A
// pass-through, then metadata compaction (PR #1), then lexical
// pre-filter + TopK truncation (PR #3), with PR #4 adding a two-pass
// LLM filter as the final escalation. The cascade is internal:
// callers see one function call, one return value, and one Trim
// record naming which stages fired.
//
// Why a cascade and not a single pass: empirically each stage is
// effective for a different failure mode. Compaction (PR #1) handles
// "model can read the catalog but the metadata is too verbose";
// lexical retrieval (PR #3) handles "even minimal metadata exceeds
// the model's working set"; the future LLM filter (PR #4) handles
// "lexical signal is too ambiguous to pick top-N." The cheap stages
// run first; expensive stages only fire when cheap ones can't decide.
//
// This is standard production RAG architecture (dense retrieval +
// cross-encoder re-ranker, HyDE + answer model) applied to tool
// selection inside helmdeck's known catalog schema instead of
// open-text QA.

// SelectMaxEntriesTierC is the default top-N cap when lexical retrieval
// has to truncate the catalog because metadata compaction wasn't enough.
// Picked empirically: 12 entries gives a free model enough context to
// catch the few packs an intent typically needs while staying well
// under the 14KB irreducible floor that breaks the model otherwise.
const SelectMaxEntriesTierC = 12

// SelectMaxEntriesTierB is the same cap for Tier B mid-tier models.
// More generous because Tier B handles larger contexts well; we
// truncate only when compaction alone can't reach budget.
const SelectMaxEntriesTierB = 25

// Select returns the catalog projection sized to fit the model's
// budget, plus a Trim record describing which cascade stages fired.
// The returned catalog is always non-empty unless the input was —
// even an aggressive truncation keeps at least the top-N matches so
// the LLM has something to plan with.
//
// Cascade order:
//  1. Tier A → no work, return catalog unchanged.
//  2. CompactCatalog (PR #1) — strip metadata in priority order.
//  3. If still over budget: LexicalRank + TopK truncation (PR #3).
//     Default top-N depends on tier; pass an explicit cap by
//     pre-marshaling the catalog and trimming separately if needed.
//
// PR #4 will add stage 4 (two-pass LLM filter) gated by a future
// Budget.AllowsLLMFilter flag. Until then, lexical retrieval is the
// last escalation.
func Select(rg RoutingGuide, intent string, budget Budget) (RoutingGuide, Trim) {
	// Stage 1: Tier A pass-through. No stages fire, Trim is empty.
	if budget.MaxCatalogBytes <= 0 {
		out, trim := CompactCatalog(rg, budget)
		return out, trim
	}

	// Stage 2: metadata compaction. CompactCatalog returns the
	// trimmed projection plus a Trim record naming each metadata
	// field it stripped.
	compacted, trim := CompactCatalog(rg, budget)

	// If compaction alone met the budget, we're done. CompactCatalog
	// appends a "still_over_budget(...)" marker to trim.Dropped when
	// it couldn't fit — that's our signal to escalate.
	if !overBudget(trim) {
		return compacted, trim
	}

	// Stage 3: lexical retrieval + top-N truncation. Score every
	// entry against the intent, keep the top-N. Top-N cap depends on
	// the tier; Tier C cuts more aggressively than Tier B.
	maxEntries := SelectMaxEntriesTierC
	if budget.Tier == TierB {
		maxEntries = SelectMaxEntriesTierB
	}

	ranked := LexicalRank(compacted, intent)
	selected := topNRoutingGuide(ranked, maxEntries)
	trim.Dropped = append(trim.Dropped, "lexical.top_n")

	// Append a confidence marker so downstream callers (the PR #4
	// LLM-filter escalation in plan.go and route.go) can decide
	// whether to spend the extra round-trip. When lexical produced a
	// confident top pick, the planning call has a strong signal and
	// the filter pass would only add latency; when ambiguous, the
	// filter pass earns its cost.
	if ShouldEscalateToFilter(ranked, 3) {
		trim.Dropped = append(trim.Dropped, "lexical.low_confidence")
	}

	// Remeasure: the after-bytes in Trim was set by CompactCatalog
	// to the post-compaction size. We update it to reflect the final
	// post-truncation size so operators see the real shipping number.
	if b, err := jsonMarshalForSize(selected); err == nil {
		trim.AfterBytes = b
	}

	return selected, trim
}

// overBudget returns true when CompactCatalog appended a marker
// indicating it ran every trim step but the catalog was still too
// big. Used by Select to decide whether to escalate to stage 3.
func overBudget(t Trim) bool {
	for _, d := range t.Dropped {
		// CompactCatalog uses the prefix "still_over_budget(" — we
		// match the prefix because the byte numbers follow inside
		// the parentheses.
		if len(d) >= len("still_over_budget(") && d[:len("still_over_budget(")] == "still_over_budget(" {
			return true
		}
	}
	return false
}

// topNRoutingGuide reconstructs a RoutingGuide containing only the
// top-N entries from a ranked list. Preserves the kind ordering
// (packs grouped, then pipelines grouped) so the marshaled output
// keeps the same shape humans and the LLM expect.
func topNRoutingGuide(ranked []Scored, n int) RoutingGuide {
	out := RoutingGuide{Packs: []Pack{}, Pipelines: []Pipeline{}}
	count := 0
	for _, s := range ranked {
		if count >= n {
			break
		}
		switch s.Kind {
		case "pack":
			if s.Pack != nil {
				out.Packs = append(out.Packs, *s.Pack)
				count++
			}
		case "pipeline":
			if s.Pipeline != nil {
				out.Pipelines = append(out.Pipelines, *s.Pipeline)
				count++
			}
		}
	}
	return out
}

// jsonMarshalForSize wraps json.Marshal returning just the byte
// count. Pure helper; named for clarity at the call site.
func jsonMarshalForSize(v interface{}) (int, error) {
	b, err := jsonMarshal(v)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}
