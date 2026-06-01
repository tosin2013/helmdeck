package llmcontext

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSelect_TierAPassThrough — frontier-tier budgets bypass the
// whole cascade. Catalog returns unchanged, no trim record entries
// describing stage activity.
func TestSelect_TierAPassThrough(t *testing.T) {
	rg := makeBigCatalog(10, 5)
	out, trim := Select(rg, "any intent here", Budget{MaxCatalogBytes: 0, Tier: TierA})
	if len(out.Packs) != 10 || len(out.Pipelines) != 5 {
		t.Errorf("Tier A should pass through unchanged; got packs=%d pipelines=%d", len(out.Packs), len(out.Pipelines))
	}
	if len(trim.Dropped) != 0 {
		t.Errorf("Tier A should fire no stages; got dropped=%v", trim.Dropped)
	}
}

// TestSelect_CompactOnlyWhenSufficient — Tier B/C with a catalog
// that fits AFTER compaction stops at stage 2; no lexical truncation
// fires. The trim record names compaction steps but NOT lexical.top_n.
func TestSelect_CompactOnlyWhenSufficient(t *testing.T) {
	// Small catalog (3 packs) that's already small enough to fit a
	// 5000-byte cap after just a few metadata strips.
	rg := makeBigCatalog(3, 1)
	_, trim := Select(rg, "intent", Budget{MaxCatalogBytes: 5000, Tier: TierC})
	for _, d := range trim.Dropped {
		if d == "lexical.top_n" {
			t.Errorf("lexical.top_n should not fire when compaction alone was sufficient; trim=%v", trim.Dropped)
		}
	}
}

// TestSelect_LexicalEscalation — when compaction can't bring the
// catalog under budget, Select escalates to lexical retrieval +
// top-N. The trim record reports lexical.top_n; the output catalog
// is bounded by SelectMaxEntriesTierC.
func TestSelect_LexicalEscalation(t *testing.T) {
	rg := makeBigCatalog(40, 15) // big enough that compaction won't fit 10000-byte cap
	out, trim := Select(rg, "rewrite a blog for audience using markdown", Budget{MaxCatalogBytes: 10000, Tier: TierC})
	sawLexical := false
	for _, d := range trim.Dropped {
		if d == "lexical.top_n" {
			sawLexical = true
		}
	}
	if !sawLexical {
		t.Fatalf("expected lexical.top_n in trim.Dropped; got %v", trim.Dropped)
	}
	totalEntries := len(out.Packs) + len(out.Pipelines)
	if totalEntries > SelectMaxEntriesTierC {
		t.Errorf("Tier C should cap entries at %d; got %d", SelectMaxEntriesTierC, totalEntries)
	}
	if totalEntries == 0 {
		t.Errorf("Select should never return zero entries when input was non-empty")
	}
}

// TestSelect_PreservesSupersedesUnderLexical — even when lexical
// truncation kicks in, the pipeline with supersedes overlap MUST be
// in the top-N when its supersedes targets a pack the user named.
// Regression guard for rule P2 / R2 at the cascade layer.
func TestSelect_PreservesSupersedesUnderLexical(t *testing.T) {
	// Build a catalog where exactly one pipeline supersedes a
	// specifically-named pack. The intent mentions that pack by
	// name, so the pipeline should rank above the bare pack AND
	// survive the top-N cut.
	rg := makeBigCatalog(50, 1) // 50 noise packs that don't match
	// Inject the targeted pipeline with supersedes link.
	pipeMeta := map[string]interface{}{
		"accepts":    []string{"markdown"},
		"produces":   []string{"blog_markdown"},
		"supersedes": []string{"important.pack.target"},
	}
	pipeJSON, _ := json.Marshal(pipeMeta)
	rg.Pipelines = append(rg.Pipelines, Pipeline{
		ID:          "builtin.target-pipeline",
		Name:        "Target pipeline",
		Description: "supersedes important.pack.target",
		Metadata:    pipeJSON,
	})
	out, trim := Select(rg, "use important.pack.target to do work", Budget{MaxCatalogBytes: 8000, Tier: TierC})
	sawLexical := false
	for _, d := range trim.Dropped {
		if d == "lexical.top_n" {
			sawLexical = true
		}
	}
	if !sawLexical {
		t.Fatalf("test fixture should trigger lexical truncation; got %v", trim.Dropped)
	}
	// builtin.target-pipeline must be in the survivors.
	found := false
	for _, p := range out.Pipelines {
		if p.ID == "builtin.target-pipeline" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("supersedes pipeline must survive top-N cut; got pipelines=%v", out.Pipelines)
	}
}

// TestSelect_OverBudgetMarker_NotForwarded — when compaction can fit
// the budget by metadata strip alone, Select returns trim WITHOUT
// the "still_over_budget" marker (which only appears when
// CompactCatalog ran every step and the catalog was still too big).
// Asserts the cascade short-circuits properly.
func TestSelect_OverBudgetMarker_NotForwarded(t *testing.T) {
	rg := makeBigCatalog(5, 2)
	_, trim := Select(rg, "intent", Budget{MaxCatalogBytes: 8000, Tier: TierC})
	for _, d := range trim.Dropped {
		if strings.HasPrefix(d, "still_over_budget") {
			t.Errorf("still_over_budget marker should not surface when compaction sufficed; got %v", trim.Dropped)
		}
	}
}
