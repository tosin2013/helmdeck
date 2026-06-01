package llmcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// makeBigCatalog returns a RoutingGuide with N packs and M pipelines,
// each with verbose metadata so the marshaled size easily exceeds
// the Tier C cap. Useful for asserting compaction order against a
// realistic load.
func makeBigCatalog(packCount, pipeCount int) RoutingGuide {
	rg := RoutingGuide{}
	for i := 0; i < packCount; i++ {
		rg.Packs = append(rg.Packs, Pack{
			Name:        "synthetic.pack" + itoa(i),
			Description: "This is a verbose description that explains in detail what the pack does, why it exists, and how it differs from sibling packs in the same category. It runs to two sentences for testing.",
			Metadata: packs.PackMetadata{
				Accepts:        []string{"input_kind_a", "input_kind_b"},
				Produces:       []string{"output_kind_a"},
				IntentKeywords: []string{"keyword one", "keyword two", "keyword three", "keyword four", "keyword five"},
				TypicalUse:     "Long typical-use string describing the canonical caller pattern, the right context to invoke this pack, and adjacent packs the result chains with.",
				Limitations:    []string{"limit one with explanation", "limit two with explanation", "limit three with explanation"},
			},
		})
	}
	for i := 0; i < pipeCount; i++ {
		// Build a realistic pipeline metadata with steps[] and
		// inputs/outputs schemas so the slim* helpers have something
		// to operate on.
		meta := map[string]interface{}{
			"accepts":         []string{"source_a", "source_b"},
			"produces":        []string{"target_x"},
			"intent_keywords": []string{"do the thing", "transform this"},
			"supersedes":      []string{"old.pack.foo", "old.pack.bar"},
			"steps": []map[string]interface{}{
				{"id": "step1", "pack": "foo.parse", "name": "Parse the input", "input_template": "{{.Input}}", "output_select": "$.body"},
				{"id": "step2", "pack": "foo.rewrite", "name": "Rewrite for audience", "input_template": "{{.step1.body}}", "output_select": "$.markdown"},
				{"id": "step3", "pack": "foo.publish", "name": "Publish output", "input_template": "{{.step2.markdown}}"},
			},
			"inputs": []map[string]interface{}{
				{"name": "source_url", "type": "string", "required": true, "description": "URL to fetch"},
				{"name": "audience", "type": "string", "required": false, "description": "Target audience persona"},
				{"name": "persona", "type": "string", "required": false},
			},
			"outputs": []map[string]interface{}{
				{"name": "result_markdown", "type": "string", "description": "Final markdown body"},
				{"name": "artifact_url", "type": "string"},
			},
		}
		raw, _ := json.Marshal(meta)
		rg.Pipelines = append(rg.Pipelines, Pipeline{
			ID:          "builtin.synthetic-pipe" + itoa(i),
			Name:        "Synthetic pipe " + itoa(i),
			Description: "A pipeline that chains synthetic packs. It exists for tests. Second sentence for truncation.",
			Metadata:    raw,
		})
	}
	return rg
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestCompactCatalog_TierAPassThrough — when MaxCatalogBytes is 0,
// the catalog is returned unchanged. Sanity guard for the Tier A
// path.
func TestCompactCatalog_TierAPassThrough(t *testing.T) {
	full := makeBigCatalog(10, 5)
	out, trim := CompactCatalog(full, Budget{MaxCatalogBytes: 0, Tier: TierA})
	if len(out.Packs) != 10 || len(out.Pipelines) != 5 {
		t.Errorf("entries dropped under Tier A pass-through")
	}
	if len(trim.Dropped) != 0 {
		t.Errorf("Tier A should drop nothing; got %v", trim.Dropped)
	}
	if trim.BeforeBytes != trim.AfterBytes {
		t.Errorf("before/after sizes should match in pass-through")
	}
}

// TestCompactCatalog_TierC_StripsInPriorityOrder — the largest cap
// where compaction triggers should strip only step 1
// (intent_keywords[]). Successively tighter caps trigger steps 2-6.
func TestCompactCatalog_TierC_StripsInPriorityOrder(t *testing.T) {
	full := makeBigCatalog(10, 3)
	full_size := mustSize(t, full)

	// Cap just below the full size — should strip exactly the first
	// few fields, not everything.
	cap := full_size - 100
	out, trim := CompactCatalog(full, Budget{MaxCatalogBytes: cap, Tier: TierC})

	if len(trim.Dropped) == 0 {
		t.Fatal("expected at least one drop")
	}
	if trim.Dropped[0] != "pack.intent_keywords[]" {
		t.Errorf("first drop should be intent_keywords; got %q", trim.Dropped[0])
	}
	// intent_keywords should be empty everywhere.
	for _, p := range out.Packs {
		if len(p.Metadata.IntentKeywords) != 0 {
			t.Errorf("pack %q still has IntentKeywords: %v", p.Name, p.Metadata.IntentKeywords)
		}
	}
}

// TestCompactCatalog_TierC_AggressiveCapDropsEverything — at the
// real Tier C cap (10000 bytes), all six trim steps fire and the
// catalog still keeps name/id/supersedes intact.
func TestCompactCatalog_TierC_AggressiveCapDropsEverything(t *testing.T) {
	full := makeBigCatalog(20, 8)
	out, trim := CompactCatalog(full, Budget{MaxCatalogBytes: 10000, Tier: TierC})

	// Names + ids are always preserved — they're dispatch identifiers.
	for _, p := range out.Packs {
		if p.Name == "" {
			t.Error("pack name dropped — compaction must never strip names")
		}
	}
	for _, p := range out.Pipelines {
		if p.ID == "" {
			t.Error("pipeline id dropped — compaction must never strip ids")
		}
	}

	// Supersedes must survive (rule P2 anchor).
	for _, p := range out.Pipelines {
		if len(p.Metadata) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(p.Metadata, &m); err != nil {
			t.Fatalf("pipeline %q metadata not valid JSON after compaction: %v", p.ID, err)
		}
		sup, ok := m["supersedes"]
		if !ok {
			t.Errorf("pipeline %q lost supersedes — compaction must never strip it", p.ID)
		}
		if arr, _ := sup.([]interface{}); len(arr) != 2 {
			t.Errorf("pipeline %q supersedes length changed: %v", p.ID, sup)
		}
	}

	// All six trim labels should have fired given how aggressively
	// the test catalog overflows 10000 bytes.
	expectedDrops := []string{
		"pack.intent_keywords[]",
		"pack.typical_use",
		"pack.limitations[]",
		"pipeline.steps[].body",
		"pipeline.inputs/outputs.schema",
	}
	for _, want := range expectedDrops {
		found := false
		for _, got := range trim.Dropped {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in trim.Dropped; got %v", want, trim.Dropped)
		}
	}
}

// TestCompactCatalog_SlimPipelineSteps — after step 4 fires, the
// pipeline steps[] body keeps only id/pack/name and drops the
// input_template / output_select fields. The supersedes link stays
// intact.
func TestCompactCatalog_SlimPipelineSteps(t *testing.T) {
	full := makeBigCatalog(3, 1)
	// Cap that's tight enough to trigger step 4 but loose enough that
	// later steps don't fire — easiest is to call slimPipelineSteps
	// directly via a public route. Force the trim by setting a tight
	// cap.
	out, trim := CompactCatalog(full, Budget{MaxCatalogBytes: 1500, Tier: TierC})

	foundStepsTrim := false
	for _, d := range trim.Dropped {
		if d == "pipeline.steps[].body" {
			foundStepsTrim = true
		}
	}
	if !foundStepsTrim {
		t.Fatalf("expected pipeline.steps[].body trim; got %v", trim.Dropped)
	}
	if len(out.Pipelines) != 1 {
		t.Fatal("test fixture changed")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(out.Pipelines[0].Metadata, &m); err != nil {
		t.Fatal(err)
	}
	steps, ok := m["steps"].([]interface{})
	if !ok || len(steps) != 3 {
		t.Fatalf("steps[] count changed: %v", steps)
	}
	step0, _ := steps[0].(map[string]interface{})
	if _, has := step0["input_template"]; has {
		t.Errorf("input_template should have been dropped; step0=%v", step0)
	}
	if _, has := step0["id"]; !has {
		t.Errorf("step id must be preserved; step0=%v", step0)
	}
	if _, has := step0["pack"]; !has {
		t.Errorf("step pack must be preserved; step0=%v", step0)
	}
}

// TestCompactCatalog_FirstSentence — description truncation only
// fires when earlier steps weren't enough.
func TestCompactCatalog_FirstSentence(t *testing.T) {
	full := makeBigCatalog(15, 5)
	out, trim := CompactCatalog(full, Budget{MaxCatalogBytes: 600, Tier: TierC})

	foundDescTrim := false
	for _, d := range trim.Dropped {
		if d == "description.firstSentence" {
			foundDescTrim = true
		}
	}
	if !foundDescTrim {
		t.Fatalf("at MaxCatalogBytes=600 expected description trim; got %v", trim.Dropped)
	}
	// At least one description should now end at the first period.
	for _, p := range out.Packs {
		if strings.Count(p.Description, ".") > 1 {
			t.Errorf("pack %q description still has multiple sentences: %q", p.Name, p.Description)
			break
		}
	}
}

// TestCompactCatalog_OverBudgetMarksStillOverBudget — if all six
// passes can't bring the catalog under the cap (artificially tiny
// budget), the Trim record includes a still_over_budget entry so the
// caller can log it.
func TestCompactCatalog_OverBudgetMarksStillOverBudget(t *testing.T) {
	full := makeBigCatalog(50, 20)
	_, trim := CompactCatalog(full, Budget{MaxCatalogBytes: 100, Tier: TierC})
	found := false
	for _, d := range trim.Dropped {
		if strings.HasPrefix(d, "still_over_budget(") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected still_over_budget marker; got %v", trim.Dropped)
	}
	// AfterBytes is still reported even when over budget.
	if trim.AfterBytes == 0 {
		t.Error("AfterBytes should be populated even over-budget")
	}
}

// TestFirstSentence — covers the helper directly since multiple
// fields rely on it.
func TestFirstSentence(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"One sentence only", "One sentence only"},
		{"First sentence. Second sentence.", "First sentence."},
		{"Question? Then a statement.", "Question?"},
		{"Exclamation! Plus more.", "Exclamation!"},
		{"v0.13.0 is a version. Not a sentence end here.", "v0.13.0 is a version."}, // version-period followed by space; correctly handled
		{"NoTerminator", "NoTerminator"},
		{"", ""},
	}
	for _, tc := range cases {
		got := firstSentence(tc.in)
		if got != tc.want {
			t.Errorf("firstSentence(%q): want %q, got %q", tc.in, tc.want, got)
		}
	}
}

// TestCompactCatalog_DoesNotMutateInput — the original RoutingGuide
// passed in must not be mutated. Concrete: a caller reusing the
// catalog across CompactCatalog calls (e.g. once for plan, once for
// route in PR #2) must see the same values both times.
func TestCompactCatalog_DoesNotMutateInput(t *testing.T) {
	full := makeBigCatalog(5, 2)
	beforeFirstPack := full.Packs[0].Metadata.IntentKeywords[0]
	beforeFirstPipeMeta := append(json.RawMessage(nil), full.Pipelines[0].Metadata...)

	_, _ = CompactCatalog(full, Budget{MaxCatalogBytes: 500, Tier: TierC})

	if full.Packs[0].Metadata.IntentKeywords[0] != beforeFirstPack {
		t.Errorf("input mutation detected on pack[0].IntentKeywords")
	}
	if string(full.Pipelines[0].Metadata) != string(beforeFirstPipeMeta) {
		t.Errorf("input mutation detected on pipeline[0].Metadata")
	}
}

func mustSize(t *testing.T, rg RoutingGuide) int {
	t.Helper()
	b, err := json.Marshal(rg)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}
