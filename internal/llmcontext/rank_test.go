package llmcontext

import (
	"encoding/json"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// TestTokenize_BasicSplitAndLower — tokenize lowercases and splits on
// non-word runes, drops stop words and single-character tokens.
func TestTokenize_BasicSplitAndLower(t *testing.T) {
	got := tokenize("Remember THIS launch — then draft a blog about it.")
	// "this", "a", "it" are stop words. The em-dash is a non-word
	// boundary. "remember", "launch", "then", "draft", "blog",
	// "about" should survive.
	want := []string{"remember", "launch", "then", "draft", "blog", "about"}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("token %q missing from %v", w, got)
		}
	}
	if _, ok := got["this"]; ok {
		t.Errorf("stop word 'this' should have been dropped")
	}
	if _, ok := got["a"]; ok {
		t.Errorf("stop word 'a' should have been dropped")
	}
	if _, ok := got["it"]; ok {
		t.Errorf("stop word 'it' should have been dropped")
	}
}

// TestLexicalRank_IntentKeywordsBeatNameMatches — an intent_keyword
// phrase match should outscore a single name token match; concrete
// guard against weight regression.
func TestLexicalRank_IntentKeywordsBeatNameMatches(t *testing.T) {
	rg := RoutingGuide{
		Packs: []Pack{
			{Name: "blog.publish", Description: "publish blog",
				Metadata: packs.PackMetadata{Accepts: []string{"markdown"}}},
			{Name: "content.rewrite_for_audience", Description: "rewrite text",
				Metadata: packs.PackMetadata{
					IntentKeywords: []string{"rewrite for audience"},
				}},
		},
	}
	ranked := LexicalRank(rg, "I want to rewrite for audience")
	if ranked[0].ID != "content.rewrite_for_audience" {
		t.Errorf("intent_keyword phrase match should win; top=%q", ranked[0].ID)
	}
}

// TestLexicalRank_SupersedesBoost — pipeline.supersedes overlap with
// intent tokens beats matching the pack the pipeline supersedes.
// Implements P2 / R2 at the ranking layer.
func TestLexicalRank_SupersedesBoost(t *testing.T) {
	pipeMeta := map[string]interface{}{
		"accepts":    []string{"brief"},
		"produces":   []string{"blog_markdown"},
		"supersedes": []string{"blog.rewrite_for_audience"},
	}
	pipeJSON, _ := json.Marshal(pipeMeta)
	rg := RoutingGuide{
		Packs: []Pack{
			{Name: "blog.rewrite_for_audience", Description: "rewrite for audience",
				Metadata: packs.PackMetadata{Accepts: []string{"markdown"}}},
		},
		Pipelines: []Pipeline{
			{ID: "builtin.brief-rewrite-blog", Name: "Brief rewrite blog",
				Description: "brief to blog", Metadata: pipeJSON},
		},
	}
	ranked := LexicalRank(rg, "I want to rewrite my blog for audience")
	if ranked[0].ID != "builtin.brief-rewrite-blog" {
		t.Errorf("pipeline supersedes should win over superseded pack; top=%q", ranked[0].ID)
	}
}

// TestLexicalRank_DeterministicTieOrder — entries with identical
// scores must order by ID asc for reproducibility. Same inputs →
// same ranked list, every time.
func TestLexicalRank_DeterministicTieOrder(t *testing.T) {
	rg := RoutingGuide{
		Packs: []Pack{
			{Name: "z.something_unrelated", Description: "unrelated"},
			{Name: "a.something_unrelated", Description: "unrelated"},
			{Name: "m.something_unrelated", Description: "unrelated"},
		},
	}
	ranked := LexicalRank(rg, "intent with no matches at all")
	if ranked[0].ID != "a.something_unrelated" {
		t.Errorf("ties should sort ID asc; got %q first", ranked[0].ID)
	}
	if ranked[2].ID != "z.something_unrelated" {
		t.Errorf("ties should sort ID asc; got %q last", ranked[2].ID)
	}
}

// TestLexicalRank_EmptyIntent — empty intent string yields zero
// scores. Caller can detect this and fall back to compaction-only.
func TestLexicalRank_EmptyIntent(t *testing.T) {
	rg := RoutingGuide{
		Packs: []Pack{{Name: "x", Metadata: packs.PackMetadata{IntentKeywords: []string{"do something"}}}},
	}
	ranked := LexicalRank(rg, "")
	if ranked[0].Score != 0 {
		t.Errorf("empty intent should yield score 0; got %f", ranked[0].Score)
	}
}

// TestTopK_TruncatesAndPreservesOrder — TopK keeps the head of the
// slice without re-sorting; truncation is the caller's job to
// confirm input is already ranked.
func TestTopK_TruncatesAndPreservesOrder(t *testing.T) {
	ranked := []Scored{
		{ID: "a", Score: 5},
		{ID: "b", Score: 4},
		{ID: "c", Score: 3},
		{ID: "d", Score: 2},
	}
	got := TopK(ranked, 2)
	if len(got) != 2 {
		t.Fatalf("TopK(2) should return 2 entries; got %d", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("TopK should preserve head; got %+v", got)
	}
}

// TestTopK_KZeroOrLargerThanList — k=0 or k>=len is a no-op.
func TestTopK_KZeroOrLargerThanList(t *testing.T) {
	ranked := []Scored{{ID: "a", Score: 5}, {ID: "b", Score: 4}}
	if len(TopK(ranked, 0)) != 2 {
		t.Errorf("TopK(0) should return full list")
	}
	if len(TopK(ranked, 10)) != 2 {
		t.Errorf("TopK(10) on 2-entry list should return full list")
	}
}

// TestHighConfidence_AboveThreshold — when the top score is well
// ahead of the second, confidence is high.
func TestHighConfidence_AboveThreshold(t *testing.T) {
	ranked := []Scored{{Score: 10}, {Score: 4}, {Score: 3}}
	if !HighConfidence(ranked, 0.3) {
		t.Errorf("(10-4)/10=0.6 >= 0.3; should be HighConfidence")
	}
}

// TestHighConfidence_BelowThreshold — close top scores are not
// confident; the future LLM-filter pass should fire in this case.
func TestHighConfidence_BelowThreshold(t *testing.T) {
	ranked := []Scored{{Score: 10}, {Score: 9}, {Score: 8}}
	if HighConfidence(ranked, 0.3) {
		t.Errorf("(10-9)/10=0.1 < 0.3; should NOT be HighConfidence")
	}
}

// TestHighConfidence_EmptyOrSingleton — degenerate cases.
func TestHighConfidence_EmptyOrSingleton(t *testing.T) {
	if !HighConfidence([]Scored{{Score: 5}}, 0.5) {
		t.Errorf("single entry should be HighConfidence")
	}
	if HighConfidence([]Scored{{Score: 0}, {Score: 0}}, 0.5) {
		t.Errorf("all-zero scores should NOT be HighConfidence")
	}
}
