package builtin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// fakePipelinesLister returns a fixed pipelines-list JSON so route's
// catalog projection can include pipelines without spinning up a real
// store.
type fakePipelinesLister struct {
	raw json.RawMessage
}

func (f fakePipelinesLister) List(_ context.Context) (json.RawMessage, error) {
	return f.raw, nil
}

// routeFixture sets up the registry, optional defaults seed, and
// dispatcher with a canned reply. Returns a ready-to-Execute engine
// plus the dispatcher (so a test can inspect captured requests).
func routeFixture(t *testing.T, reply string, seedAudit func(memory.MemoryStore, string)) (*packs.Engine, *scriptedDispatcher, *packs.Pack) {
	t.Helper()
	reg := packs.NewPackRegistry()
	// Seed a couple packs with metadata so the projection has something.
	if err := reg.Register(&packs.Pack{
		Name:        "blog.rewrite_for_audience",
		Version:     "v1",
		Description: "rewrite for audience",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"markdown", "source_content"},
			Produces:       []string{"blog_markdown"},
			IntentKeywords: []string{"rewrite for audience"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&packs.Pack{
		Name:        "doc.parse",
		Version:     "v1",
		Description: "parse pdf",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"pdf", "docx", "url"},
			Produces:       []string{"markdown"},
			IntentKeywords: []string{"parse pdf"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	disp := &scriptedDispatcher{replies: []string{reply}}
	pipes := fakePipelinesLister{raw: json.RawMessage(`[
		{"id":"builtin.doc-rewrite-blog","name":"Doc rewrite blog","description":"pdf to blog",
		 "metadata":{"accepts":["pdf","docx"],"produces":["blog_markdown"],"supersedes":["builtin.doc-ground-blog"]}}
	]`)}
	pack := Route(disp, reg, pipes)
	store := memory.NewInMemoryStore()
	if seedAudit != nil {
		seedAudit(store, "alice")
	}
	eng := packs.New(
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		packs.WithMemoryStore(store),
	)
	return eng, disp, pack
}

// TestRoute_HappyPath_ProposesPipeline — model picks the doc-rewrite-blog
// pipeline. Handler returns the recommendation verbatim (no demotion).
func TestRoute_HappyPath_ProposesPipeline(t *testing.T) {
	reply := `{
		"recommendation": {"kind":"pipeline","id":"builtin.doc-rewrite-blog",
		                    "suggested_inputs":{"persona":"technical","audience":"engineers"},
		                    "why":"pdf source + blog target — pipeline supersedes the manual chain"},
		"alternatives": [{"kind":"pack","id":"doc.parse","why":"step 1 of the chain if pipeline weren't available"}],
		"gap_warning": null,
		"reasoning": "Pipeline matches accepts=pdf produces=blog_markdown; supersedes pack chain."
	}`
	eng, _, pack := routeFixture(t, reply, nil)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"make a blog post from this PDF","model":"openrouter/auto"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Recommendation struct{ Kind, ID string } `json:"recommendation"`
		GapWarning     interface{}               `json:"gap_warning"`
		Model          string                    `json:"model"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Recommendation.Kind != "pipeline" || out.Recommendation.ID != "builtin.doc-rewrite-blog" {
		t.Errorf("want pipeline builtin.doc-rewrite-blog; got %s %s", out.Recommendation.Kind, out.Recommendation.ID)
	}
	if out.GapWarning != nil {
		t.Errorf("expected no gap_warning; got %+v", out.GapWarning)
	}
	if out.Model != "openrouter/auto" {
		t.Errorf("model should echo back; got %q", out.Model)
	}
}

// TestRoute_PromptCarriesDefaults — when audit history exists, the
// projection lands inside the model prompt so the LLM can pre-fill
// suggested_inputs.
func TestRoute_PromptCarriesDefaults(t *testing.T) {
	reply := `{"recommendation":{"kind":"pack","id":"blog.rewrite_for_audience"},"alternatives":[],"gap_warning":null,"reasoning":"ok"}`
	seed := func(s memory.MemoryStore, caller string) {
		body, _ := json.Marshal(packs.PackAudit{
			Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 100,
			LearnInputs: map[string]string{"persona": "technical", "audience": "platform engineers"},
		})
		_, _ = s.Put(context.Background(), caller, packs.AuditKeyPrefixPack+"blog.rewrite_for_audience/aaa", body,
			memory.WithCategory(packs.AuditCategoryPack))
	}
	eng, disp, pack := routeFixture(t, reply, seed)
	ctx := packs.WithCaller(context.Background(), "alice")
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"rewrite this for engineers","model":"openrouter/auto"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(disp.captured) != 1 {
		t.Fatalf("expected 1 dispatcher call; got %d", len(disp.captured))
	}
	// Find the user message body. The order is [system, user].
	if len(disp.captured[0].Messages) != 2 {
		t.Fatalf("expected 2 messages; got %d", len(disp.captured[0].Messages))
	}
	user := disp.captured[0].Messages[1].Content.Text()
	if !strings.Contains(user, "CALLER DEFAULTS") {
		t.Errorf("user message should include CALLER DEFAULTS block; got: %s", user)
	}
	if !strings.Contains(user, "platform engineers") {
		t.Errorf("learned audience value should land in the prompt; got: %s", user)
	}
}

// TestRoute_HallucinatedID_DemotedToGap — when the model returns an id
// that doesn't exist in the catalog, the handler demotes the
// recommendation and surfaces a gap_warning so the agent can't
// dispatch to nothing.
func TestRoute_HallucinatedID_DemotedToGap(t *testing.T) {
	reply := `{
		"recommendation": {"kind":"pack","id":"youtube.transcript","why":"matches"},
		"alternatives": [],
		"gap_warning": null,
		"reasoning": "guessing"
	}`
	eng, _, pack := routeFixture(t, reply, nil)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"get a youtube transcript","model":"openrouter/auto"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Recommendation struct{ Kind, ID string } `json:"recommendation"`
		GapWarning     *struct {
			MissingCapability string `json:"missing_capability"`
		} `json:"gap_warning"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Recommendation.ID != "" || out.Recommendation.Kind != "none" {
		t.Errorf("expected demoted recommendation; got %+v", out.Recommendation)
	}
	if out.GapWarning == nil {
		t.Fatal("expected gap_warning after hallucination demotion")
	}
	if !strings.Contains(out.Reasoning, "not in the catalog") {
		t.Errorf("reasoning should mention demotion; got %q", out.Reasoning)
	}
}

// TestRoute_GapWarning_PreservedWhenIDValid — when the model returns
// a valid id AND a gap_warning (partial match case), both survive.
func TestRoute_GapWarning_PreservedWhenIDValid(t *testing.T) {
	reply := `{
		"recommendation": {"kind":"pack","id":"doc.parse","why":"closest match"},
		"alternatives": [],
		"gap_warning": {
			"missing_capability": "audio transcription",
			"proposed_pack": {"name":"audio.transcribe","input_schema":{"url":"string"},
			                  "output_schema":{"text":"string"},"integration_pattern":"vault key + whisper",
			                  "why_useful":"would chain with blog.rewrite_for_audience"}
		},
		"reasoning": "doc.parse is closest but doesn't actually fit audio sources"
	}`
	eng, _, pack := routeFixture(t, reply, nil)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, _ := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"transcribe this podcast","model":"openrouter/auto"}`))
	var out struct {
		Recommendation struct{ ID string } `json:"recommendation"`
		GapWarning     *struct {
			MissingCapability string                 `json:"missing_capability"`
			ProposedPack      map[string]interface{} `json:"proposed_pack"`
		} `json:"gap_warning"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Recommendation.ID != "doc.parse" {
		t.Errorf("valid id should survive; got %q", out.Recommendation.ID)
	}
	if out.GapWarning == nil || out.GapWarning.MissingCapability != "audio transcription" {
		t.Errorf("gap_warning should be preserved; got %+v", out.GapWarning)
	}
}

// TestRoute_MissingDispatcher — registered without a dispatcher
// returns CodeInternal at call time.
func TestRoute_MissingDispatcher(t *testing.T) {
	reg := packs.NewPackRegistry()
	pack := Route(nil, reg, nil)
	eng := packs.New(packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"user_intent":"anything","model":"openrouter/auto"}`))
	if err == nil {
		t.Fatal("expected error when dispatcher is nil")
	}
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInternal {
		t.Errorf("want CodeInternal, got %v", err)
	}
}

// TestRoute_InvalidInput — missing user_intent fails fast with
// CodeInvalidInput (the agent should retry with a real intent string).
func TestRoute_InvalidInput(t *testing.T) {
	eng, _, pack := routeFixture(t, `{}`, nil)
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"model":"openrouter/auto"}`))
	if err == nil {
		t.Fatal("expected invalid-input error")
	}
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want CodeInvalidInput, got %v", err)
	}
}

// TestRoute_TierAModelGetsFullCatalog — ADR 050 PR #2 wired
// llmcontext.CompactCatalog into route.go. Tier A models must pass
// through with full metadata; the routeFixture's blog pack declared
// IntentKeywords that should survive in the prompt verbatim.
func TestRoute_TierAModelGetsFullCatalog(t *testing.T) {
	reply := `{"recommendation":{"kind":"pack","id":"blog.rewrite_for_audience"},"alternatives":[],"gap_warning":null,"reasoning":"ok"}`
	eng, disp, pack := routeFixture(t, reply, nil)
	ctx := packs.WithCaller(context.Background(), "alice")
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"rewrite this","model":"anthropic/claude-haiku-4-5"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	user := disp.captured[0].Messages[1].Content.Text()
	if !strings.Contains(user, "rewrite for audience") {
		t.Errorf("Tier A model should see full metadata including intent_keywords; user message lacks 'rewrite for audience'")
	}
}

// TestRoute_TierCModelPreservesSupersedes — ADR 050 PR #2 wired
// llmcontext.CompactCatalog into route.go. Tier C models trim
// aggressively, but pipeline metadata.supersedes must survive — the
// supersedes link anchors helmdeck.route's rule R2 the same way it
// anchors helmdeck.plan's rule P2.
func TestRoute_TierCModelPreservesSupersedes(t *testing.T) {
	reply := `{"recommendation":{"kind":"pipeline","id":"builtin.doc-rewrite-blog"},"alternatives":[],"gap_warning":null,"reasoning":"ok"}`
	eng, disp, pack := routeFixture(t, reply, nil)
	ctx := packs.WithCaller(context.Background(), "alice")
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"x","model":"openrouter/openrouter/free"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	user := disp.captured[0].Messages[1].Content.Text()
	// The fixture's pipeline JSON includes
	// "supersedes":["builtin.doc-ground-blog"] — that field MUST
	// survive Tier C compaction.
	if !strings.Contains(user, "supersedes") || !strings.Contains(user, "builtin.doc-ground-blog") {
		t.Errorf("Tier C compaction must preserve pipeline supersedes link; got: %s", user)
	}
}
