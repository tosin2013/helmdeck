package llmcontext

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildFilterUserMessage_ShapeAndCompactness — the filter prompt
// should list every catalog entry with at most a one-sentence
// description and end with the intent. Compactness is the whole
// point of the filter pass; assert ~3 KB max for a 70-entry catalog.
func TestBuildFilterUserMessage_ShapeAndCompactness(t *testing.T) {
	rg := makeBigCatalog(50, 20)
	got := BuildFilterUserMessage(rg, "draft a blog from this brief")

	if !strings.HasPrefix(got, "AVAILABLE TOOLS:\n") {
		t.Errorf("expected leading marker; got %q", got[:30])
	}
	if !strings.Contains(got, "INTENT:") {
		t.Errorf("expected INTENT marker in body")
	}
	if !strings.HasSuffix(got, "Return the JSON object now.") {
		t.Errorf("expected trailing instruction; tail=%q", got[max(0, len(got)-40):])
	}
	// Filter prompt should be substantially smaller than the full-
	// metadata catalog (~35KB) — that's the whole point of the
	// two-pass cascade. 12KB ceiling allows for ~70 entries with
	// one-sentence descriptions while still flagging regressions.
	if len(got) > 12000 {
		t.Errorf("filter prompt should stay under 12KB; got %d bytes", len(got))
	}
	// Every catalog id should appear in the prompt.
	for _, p := range rg.Packs {
		if !strings.Contains(got, p.Name) {
			t.Errorf("pack name %q missing from filter prompt", p.Name)
			break
		}
	}
	for _, p := range rg.Pipelines {
		if !strings.Contains(got, p.ID) {
			t.Errorf("pipeline id %q missing from filter prompt", p.ID)
			break
		}
	}
}

// TestParseFilterResponse_HappyPath — straight JSON object returns
// the id list verbatim.
func TestParseFilterResponse_HappyPath(t *testing.T) {
	resp := `{"ids":["pack.one","pack.two","pipeline.three"]}`
	got := ParseFilterResponse(resp)
	want := []string{"pack.one", "pack.two", "pipeline.three"}
	if len(got) != len(want) {
		t.Fatalf("got %d ids, want %d", len(got), len(want))
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, g, want[i])
		}
	}
}

// TestParseFilterResponse_CodeFenced — weak models often wrap their
// JSON in ```json fences despite the system prompt's instruction not
// to. Parser must tolerate.
func TestParseFilterResponse_CodeFenced(t *testing.T) {
	resp := "```json\n{\"ids\":[\"a\",\"b\"]}\n```"
	got := ParseFilterResponse(resp)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("expected [a b]; got %v", got)
	}
}

// TestParseFilterResponse_LeadingProse — model narrated then emitted
// the JSON. We should extract the object anyway.
func TestParseFilterResponse_LeadingProse(t *testing.T) {
	resp := `Sure thing, here's the filter result: {"ids":["x.y","z.w"]} that's all.`
	got := ParseFilterResponse(resp)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

// TestParseFilterResponse_Deduplicates — repeated ids collapse to
// first occurrence; order preserved.
func TestParseFilterResponse_Deduplicates(t *testing.T) {
	resp := `{"ids":["a","b","a","c","b"]}`
	got := ParseFilterResponse(resp)
	want := []string{"a", "b", "c"}
	if len(got) != 3 {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, g, want[i])
		}
	}
}

// TestParseFilterResponse_EmptyOrInvalid — degenerate cases produce
// nil so callers fall back to the unfiltered catalog.
func TestParseFilterResponse_EmptyOrInvalid(t *testing.T) {
	cases := []string{"", "not json", "{}", `{"ids":[]}`, `{"wrong_key":["a"]}`}
	for _, tc := range cases {
		got := ParseFilterResponse(tc)
		if len(got) != 0 {
			t.Errorf("input %q should parse to empty; got %v", tc, got)
		}
	}
}

// TestRestrictCatalog_KeepsListedDropsRest — exact membership check.
func TestRestrictCatalog_KeepsListedDropsRest(t *testing.T) {
	rg := RoutingGuide{
		Packs: []Pack{
			{Name: "keep.this.pack"},
			{Name: "drop.this.pack"},
			{Name: "also.keep"},
		},
		Pipelines: []Pipeline{
			{ID: "builtin.keep-this"},
			{ID: "builtin.drop-this"},
		},
	}
	out := RestrictCatalog(rg, []string{"keep.this.pack", "also.keep", "builtin.keep-this", "unknown.id"})
	if len(out.Packs) != 2 {
		t.Errorf("want 2 surviving packs; got %d", len(out.Packs))
	}
	if len(out.Pipelines) != 1 {
		t.Errorf("want 1 surviving pipeline; got %d", len(out.Pipelines))
	}
	for _, p := range out.Packs {
		if p.Name == "drop.this.pack" {
			t.Errorf("drop.this.pack should not survive")
		}
	}
}

// TestRestrictCatalog_EmptyKeep — empty keep list yields empty
// guide (not nil — the slices stay allocated for safe JSON marshal).
func TestRestrictCatalog_EmptyKeep(t *testing.T) {
	rg := RoutingGuide{Packs: []Pack{{Name: "p"}}, Pipelines: []Pipeline{{ID: "pipe"}}}
	out := RestrictCatalog(rg, nil)
	if len(out.Packs) != 0 || len(out.Pipelines) != 0 {
		t.Errorf("empty keep should produce empty guide; got %+v", out)
	}
	if out.Packs == nil {
		t.Errorf("Packs slice should be allocated even when empty")
	}
}

// TestMergeKeepOrder_PreservesPrimaryOrder — primary list dictates
// order; secondary entries get appended only if novel.
func TestMergeKeepOrder_PreservesPrimaryOrder(t *testing.T) {
	primary := []string{"a", "b", "c"}
	secondary := []string{"c", "d", "a"}
	got := MergeKeepOrder(primary, secondary)
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, g, want[i])
		}
	}
}

// TestIDsFromRoutingGuide_StableOrdering — sorted output so
// concurrent identical inputs produce identical filter prompts.
// Reproducibility is essential for debugging empty-completion
// failures.
func TestIDsFromRoutingGuide_StableOrdering(t *testing.T) {
	rg := RoutingGuide{
		Packs:     []Pack{{Name: "z.pack"}, {Name: "a.pack"}},
		Pipelines: []Pipeline{{ID: "m.pipe"}, {ID: "b.pipe"}},
	}
	got := IDsFromRoutingGuide(rg)
	want := []string{"a.pack", "b.pipe", "m.pipe", "z.pack"}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, g, want[i])
		}
	}
}

// TestShouldEscalateToFilter — ambiguous (close scores) above the
// minEntries threshold should escalate; confident or empty cases
// shouldn't.
func TestShouldEscalateToFilter(t *testing.T) {
	confident := []Scored{{Score: 10}, {Score: 4}, {Score: 3}, {Score: 2}}
	if ShouldEscalateToFilter(confident, 3) {
		t.Errorf("confident ranking should NOT escalate; gap=0.6 >= 0.4")
	}
	ambiguous := []Scored{{Score: 10}, {Score: 9}, {Score: 8}, {Score: 7}}
	if !ShouldEscalateToFilter(ambiguous, 3) {
		t.Errorf("ambiguous ranking SHOULD escalate; gap=0.1 < 0.4")
	}
	tooFew := []Scored{{Score: 10}, {Score: 1}}
	if ShouldEscalateToFilter(tooFew, 3) {
		t.Errorf("ranking below minEntries should NOT escalate")
	}
}

// TestParseFilterResponse_RealWorldExample — what a free model
// actually returns from the system prompt. Regression-guard so a
// prompt edit doesn't break parsing.
func TestParseFilterResponse_RealWorldExample(t *testing.T) {
	// Simulate a free-model response with all the formatting weak
	// models commonly produce.
	resp := "Here are the relevant tools for the intent:\n```json\n" +
		`{"ids": ["helmdeck.memory_store", "blog.rewrite_for_audience", "image.generate"]}` + "\n```\n"
	got := ParseFilterResponse(resp)
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	expected := []string{"helmdeck.memory_store", "blog.rewrite_for_audience", "image.generate"}
	for i, g := range got {
		if g != expected[i] {
			t.Errorf("idx %d: got %q, want %q", i, g, expected[i])
		}
	}
}

// TestRestrictCatalog_PreservesMetadata — restricted entries keep
// their full metadata (we restrict, we don't trim further). Guard
// against accidental field stripping in the restriction path.
func TestRestrictCatalog_PreservesMetadata(t *testing.T) {
	meta := map[string]interface{}{"supersedes": []string{"old.pack"}}
	mb, _ := json.Marshal(meta)
	rg := RoutingGuide{
		Pipelines: []Pipeline{
			{ID: "builtin.keep", Name: "Keep", Description: "keep this", Metadata: mb},
		},
	}
	out := RestrictCatalog(rg, []string{"builtin.keep"})
	if len(out.Pipelines) != 1 {
		t.Fatal("pipeline didn't survive")
	}
	if string(out.Pipelines[0].Metadata) != string(mb) {
		t.Errorf("metadata was modified; got %q, want %q", out.Pipelines[0].Metadata, mb)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
