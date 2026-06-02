package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// planFixture sets up a registry seeded with two packs + one pipeline
// (the pipeline supersedes one of the packs, so the supersedes-honor
// test has something concrete to assert against). Returns the engine,
// scripted dispatcher, and the helmdeck.plan pack.
func planFixture(t *testing.T, reply string) (*packs.Engine, *scriptedDispatcher, *packs.Pack, memory.MemoryStore) {
	t.Helper()
	reg := packs.NewPackRegistry()
	if err := reg.Register(&packs.Pack{
		Name:        "helmdeck.memory_store",
		Version:     "v1",
		Description: "persist a fact",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"key", "value"},
			Produces:       []string{"memory_entry"},
			IntentKeywords: []string{"remember", "persist", "save a fact"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&packs.Pack{
		Name:        "blog.rewrite_for_audience",
		Version:     "v1",
		Description: "rewrite for audience",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"markdown"},
			Produces:       []string{"blog_markdown"},
			IntentKeywords: []string{"rewrite for audience"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	disp := &scriptedDispatcher{replies: []string{reply}}
	pipes := fakePipelinesLister{raw: json.RawMessage(`[
		{"id":"builtin.brief-rewrite-blog","name":"Brief rewrite blog","description":"brief to blog",
		 "metadata":{"accepts":["brief","markdown"],"produces":["blog_markdown"],"supersedes":["blog.rewrite_for_audience"],
		             "intent_keywords":["draft a blog from this brief"]}}
	]`)}
	pack := Plan(disp, reg, pipes)
	store := memory.NewInMemoryStore()
	eng := packs.New(
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		packs.WithMemoryStore(store),
	)
	return eng, disp, pack, store
}

// TestPlan_PackChain_HappyPath — model returns a 3-step decomposition;
// handler echoes it, derives rewritten_prompt, classifies pack-chain.
func TestPlan_PackChain_HappyPath(t *testing.T) {
	reply := `{
		"steps": [
			{"order":1,"tool":"helmdeck.memory_store","args":{"key":"launches/minimax-m3","value":"..."},"rationale":"persist the source"},
			{"order":2,"tool":"helmdeck__pipeline-run","args":{"id":"builtin.brief-rewrite-blog","inputs":{"brief":"..."}},"rationale":"pipeline supersedes manual chain"},
			{"order":3,"tool":"blog.rewrite_for_audience","args":{"audience":"AI engineers"},"rationale":"final polish"}
		],
		"complexity":"pack-chain",
		"reasoning":"Three-action intent. Step 2 prefers brief-rewrite-blog over chaining its constituents."
	}`
	eng, _, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"remember this launch, draft a blog, polish for engineers","model":"openrouter/auto"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out planOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Steps) != 3 {
		t.Fatalf("want 3 steps; got %d", len(out.Steps))
	}
	if out.Steps[0].Tool != "helmdeck.memory_store" {
		t.Errorf("step 1 tool: %q", out.Steps[0].Tool)
	}
	if out.Steps[1].Tool != "helmdeck__pipeline-run" {
		t.Errorf("step 2 should be pipeline-run; got %q", out.Steps[1].Tool)
	}
	if out.Complexity != "pack-chain" {
		t.Errorf("complexity: %q", out.Complexity)
	}
	if !strings.Contains(out.RewrittenPrompt, "Step 1:") || !strings.Contains(out.RewrittenPrompt, "Step 3:") {
		t.Errorf("rewritten_prompt missing step lines: %q", out.RewrittenPrompt)
	}
	if out.Model != "openrouter/auto" {
		t.Errorf("model should echo: %q", out.Model)
	}
}

// TestPlan_PipelineDirect — one-pipeline plan classifies pipeline-direct.
func TestPlan_PipelineDirect(t *testing.T) {
	reply := `{
		"steps": [
			{"order":1,"tool":"helmdeck__pipeline-run","args":{"id":"builtin.brief-rewrite-blog","inputs":{"brief":"..."}},"rationale":"end-to-end pipeline fits the intent"}
		],
		"complexity":"pipeline-direct",
		"reasoning":"Pipeline covers accepts=brief produces=blog_markdown end-to-end."
	}`
	eng, _, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"draft a blog from this brief","model":"openrouter/auto"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out planOutput
	_ = json.Unmarshal(res.Output, &out)
	if out.Complexity != "pipeline-direct" {
		t.Errorf("want pipeline-direct; got %q", out.Complexity)
	}
	if len(out.Steps) != 1 || out.Steps[0].Tool != "helmdeck__pipeline-run" {
		t.Errorf("expected one pipeline-run step; got %+v", out.Steps)
	}
}

// TestPlan_HallucinatedTool_DemotedPerStep — unknown tool ids and the
// recursive helmdeck.plan id are both demoted to tool="unknown" with
// a populated rationale; known steps in the same plan survive.
func TestPlan_HallucinatedTool_DemotedPerStep(t *testing.T) {
	reply := `{
		"steps": [
			{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"known"},
			{"order":2,"tool":"youtube.transcript","args":{},"rationale":"model hallucinated"},
			{"order":3,"tool":"helmdeck.plan","args":{},"rationale":"recursive call attempt"},
			{"order":4,"tool":"helmdeck__pipeline-run","args":{"id":"builtin.does-not-exist"},"rationale":"bad pipeline id"}
		],
		"complexity":"pack-chain",
		"reasoning":"mixed"
	}`
	eng, _, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"do everything","model":"openrouter/auto"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out planOutput
	_ = json.Unmarshal(res.Output, &out)
	if len(out.Steps) != 4 {
		t.Fatalf("want 4 steps; got %d", len(out.Steps))
	}
	if out.Steps[0].Tool != "helmdeck.memory_store" {
		t.Errorf("known step 1 should survive; got %q", out.Steps[0].Tool)
	}
	if out.Steps[1].Tool != "unknown" || !strings.Contains(out.Steps[1].Rationale, "not in catalog") {
		t.Errorf("hallucinated step 2 should demote with reason; got tool=%q rationale=%q", out.Steps[1].Tool, out.Steps[1].Rationale)
	}
	if out.Steps[2].Tool != "unknown" || !strings.Contains(out.Steps[2].Rationale, "cannot call itself") {
		t.Errorf("recursive step 3 should demote; got tool=%q rationale=%q", out.Steps[2].Tool, out.Steps[2].Rationale)
	}
	if out.Steps[3].Tool != "unknown" || !strings.Contains(out.Steps[3].Rationale, "pipeline-run args.id") {
		t.Errorf("bad pipeline id step 4 should demote; got tool=%q rationale=%q", out.Steps[3].Tool, out.Steps[3].Rationale)
	}
}

// TestPlan_PipelineSupersedesInPrompt — the catalog projection sent to
// the model must include the pipeline's supersedes metadata so the
// model can apply rule P2. This is the upstream half of the
// supersedes story; the downstream half (model actually using it) is
// covered by TestPlan_PackChain_HappyPath above which has the model
// choose pipeline-run over the superseded pack.
func TestPlan_PipelineSupersedesInPrompt(t *testing.T) {
	reply := `{"steps":[{"order":1,"tool":"helmdeck__pipeline-run","args":{"id":"builtin.brief-rewrite-blog"},"rationale":"x"}],"complexity":"pipeline-direct","reasoning":"x"}`
	eng, disp, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"draft blog","model":"openrouter/auto"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(disp.captured) != 1 {
		t.Fatalf("expected 1 dispatcher call; got %d", len(disp.captured))
	}
	user := disp.captured[0].Messages[1].Content.Text()
	if !strings.Contains(user, "supersedes") || !strings.Contains(user, "blog.rewrite_for_audience") {
		t.Errorf("user message must surface the supersedes link; got: %s", user)
	}
	if !strings.Contains(user, "CATALOG") || !strings.Contains(user, "CALLER DEFAULTS") {
		t.Errorf("prompt structure should include both blocks; got: %s", user)
	}
}

// TestPlan_AuditRowLands — a successful plan writes one plan_history
// row under the caller's bare namespace, with category=plan_history
// and a body the projection can decode back into a PlanAudit.
func TestPlan_AuditRowLands(t *testing.T) {
	reply := `{
		"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{"k":"v"},"rationale":"x"}],
		"complexity":"single-action","reasoning":"x"
	}`
	eng, _, pack, store := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"remember this","model":"openrouter/auto"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	entries, err := store.List(context.Background(), "alice", packs.AuditKeyPrefixPlan)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 plan_history row; got %d", len(entries))
	}
	if entries[0].Category != packs.AuditCategoryPlan {
		t.Errorf("category: %q", entries[0].Category)
	}
	var audit packs.PlanAudit
	if err := json.Unmarshal(entries[0].Value, &audit); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if audit.Complexity != "single-action" {
		t.Errorf("audit.complexity: %q", audit.Complexity)
	}
	if audit.IntentSHA == "" || len(audit.IntentSHA) != 16 {
		t.Errorf("intent_sha should be 16 hex chars; got %q", audit.IntentSHA)
	}
	if len(audit.Steps) != 1 || audit.Steps[0].Tool != "helmdeck.memory_store" || audit.Steps[0].ArgsSHA == "" {
		t.Errorf("audit step summary wrong: %+v", audit.Steps)
	}
	if audit.Model != "openrouter/auto" {
		t.Errorf("audit.model: %q", audit.Model)
	}
}

// TestPlan_ComplexityDerivedFromShape — when the model omits or sends
// an invalid complexity value, the handler derives it from the step
// shape (the LLM's classification is advisory).
func TestPlan_ComplexityDerivedFromShape(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{
			name: "empty-complexity-with-single-pack-step",
			reply: `{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"}],
			          "complexity":"","reasoning":"x"}`,
			want: "single-action",
		},
		{
			name: "invalid-complexity-with-pipeline-step",
			reply: `{"steps":[{"order":1,"tool":"helmdeck__pipeline-run","args":{"id":"builtin.brief-rewrite-blog"},"rationale":"x"}],
			          "complexity":"nonsense","reasoning":"x"}`,
			want: "pipeline-direct",
		},
		{
			name: "missing-complexity-with-multi-step",
			reply: `{"steps":[
			           {"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"},
			           {"order":2,"tool":"blog.rewrite_for_audience","args":{},"rationale":"y"}
			         ],"reasoning":"x"}`,
			want: "pack-chain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, _, pack, _ := planFixture(t, tc.reply)
			ctx := packs.WithCaller(context.Background(), "alice")
			res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"x","model":"openrouter/auto"}`))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var out planOutput
			_ = json.Unmarshal(res.Output, &out)
			if out.Complexity != tc.want {
				t.Errorf("want complexity %q; got %q", tc.want, out.Complexity)
			}
		})
	}
}

// TestPlan_MissingDispatcher — registered without a dispatcher returns
// CodeInternal at call time, matching helmdeck.route's contract.
func TestPlan_MissingDispatcher(t *testing.T) {
	reg := packs.NewPackRegistry()
	pack := Plan(nil, reg, nil)
	eng := packs.New(packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"user_intent":"x","model":"openrouter/auto"}`))
	if err == nil {
		t.Fatal("expected error when dispatcher is nil")
	}
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInternal {
		t.Errorf("want CodeInternal, got %v", err)
	}
}

// TestPlan_InvalidInput — missing user_intent fails fast.
func TestPlan_InvalidInput(t *testing.T) {
	eng, _, pack, _ := planFixture(t, `{}`)
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

// TestPlan_CompactsCatalogForTierCModels — when the model id maps to
// Tier C, the catalog projection in the user message must NOT carry
// the full metadata. Asserts the ADR 050 integration: free models
// see a slimmed catalog so the empty-completion failure that
// motivated the ADR doesn't recur.
func TestPlan_CompactsCatalogForTierCModels(t *testing.T) {
	reply := `{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`
	eng, disp, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	// openrouter/openrouter/free is Tier C in the budgets table with
	// MaxCatalogBytes=10000. The fixture's tiny catalog (2 packs + 1
	// pipeline) is under that cap so compaction's a pass-through —
	// but a Tier C call must NEVER drop catalog entries (only metadata
	// fields). Use a tighter budget by going through a known-Tier-C
	// model and asserting the entries survive.
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"remember this","model":"openrouter/openrouter/free"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(disp.captured) != 1 {
		t.Fatalf("expected 1 dispatcher call; got %d", len(disp.captured))
	}
	user := disp.captured[0].Messages[1].Content.Text()
	// Pack names must always survive compaction — they're dispatch
	// identifiers, not metadata.
	if !strings.Contains(user, "helmdeck.memory_store") {
		t.Errorf("pack name must survive compaction; user message lacks it")
	}
	// Pipeline supersedes link must always survive compaction — it
	// anchors rule P2 in the system prompt.
	if !strings.Contains(user, "supersedes") || !strings.Contains(user, "blog.rewrite_for_audience") {
		t.Errorf("pipeline supersedes must survive Tier C compaction; got: %s", user)
	}
}

// TestPlan_TierAModelGetsFullCatalog — frontier models (Tier A) bypass
// compaction entirely. The full catalog including intent_keywords,
// typical_use, and limitations should land in the prompt verbatim.
func TestPlan_TierAModelGetsFullCatalog(t *testing.T) {
	reply := `{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`
	eng, disp, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	// anthropic/claude-haiku-* maps to Tier A → MaxCatalogBytes=0 →
	// pass-through. The fixture seeded packs with intent_keywords so
	// we can assert they survived.
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"x","model":"anthropic/claude-haiku-4-5"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	user := disp.captured[0].Messages[1].Content.Text()
	// The fixture's blog.rewrite_for_audience pack declared
	// IntentKeywords: ["rewrite for audience"]. Tier A pass-through
	// means it must show up in the prompt.
	if !strings.Contains(user, "rewrite for audience") {
		t.Errorf("Tier A model should see full metadata including intent_keywords; user message lacks 'rewrite for audience'")
	}
}

// TestPlan_StrictJSONFlipsOnTierA — ADR 051 PR #3. Tier A models with
// WantsStrictJSON=true should opt the dispatch into provider-side
// structured-output mode by setting ChatRequest.ResponseFormat to
// "json_object". This locks the wiring from Budget → ChatRequest.
func TestPlan_StrictJSONFlipsOnTierA(t *testing.T) {
	reply := `{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`
	eng, disp, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	// anthropic/claude-haiku-* is Tier A + WantsStrictJSON=true per
	// budgets.go. The fact that strict JSON is forwarded to a provider
	// that ignores it (Anthropic uses tool-call structure) is fine —
	// the gateway-level field is provider-agnostic and providers that
	// don't support it pass through unchanged.
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"x","model":"anthropic/claude-haiku-4-5"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := disp.captured[0].ResponseFormat; got != "json_object" {
		t.Errorf("Tier A + WantsStrictJSON should set ResponseFormat=json_object; got %q", got)
	}
}

// TestPlan_StrictJSONSuppressedOnTierC — Tier C entries deliberately
// stay on the prompt-engineered JSON path even when an admin flags
// WantsStrictJSON. The research synthesis cited in ADR 051 names
// constrained-decoding deadlock as the dominant failure mode of
// quantized open-weight inference; strict mode makes that worse.
func TestPlan_StrictJSONSuppressedOnTierC(t *testing.T) {
	reply := `{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`
	eng, disp, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	// Unknown model id falls through to the Tier C default fallback.
	// Even if a future admin sets WantsStrictJSON=true on the fallback
	// entry, the Tier C tier guard suppresses strict mode at the
	// dispatch site, so ResponseFormat must stay empty here.
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"x","model":"someone/unknown-model-id"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := disp.captured[0].ResponseFormat; got != "" {
		t.Errorf("Tier C should suppress strict-JSON mode regardless of flag; got ResponseFormat=%q", got)
	}
}

// TestPlan_CompactionFieldOmittedOnTierA — Tier A models pass the
// catalog through unchanged; the plan output must NOT carry a
// compaction field in that case. Asserts the omitempty contract on
// the wire shape PR #2 ships.
func TestPlan_CompactionFieldOmittedOnTierA(t *testing.T) {
	reply := `{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`
	eng, _, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"x","model":"anthropic/claude-haiku-4-5"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Decode into a permissive map so we can assert the field's
	// absence (a typed planOutput would carry a nil pointer that
	// looks the same as an omitted field on the wire).
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := out["compaction"]; has {
		t.Errorf("Tier A output should omit `compaction` field; got %+v", out["compaction"])
	}
}

// TestPlan_CompactionFieldPresentOnTierCWithLargeCatalog — when
// compaction actually fires (Tier C model + catalog over budget),
// the output carries the Trim record so agents can inspect it. The
// planFixture's catalog is too small to trigger trim, so this test
// uses a deliberately-tiny budget via the openrouter/openrouter/free
// model id and a third pack with enough metadata to push the catalog
// past 10000 bytes.
func TestPlan_CompactionFieldPresentOnTierCWithLargeCatalog(t *testing.T) {
	reply := `{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`
	// Reuse the standard fixture, then register more packs with
	// verbose metadata so the catalog projection exceeds 10000 bytes
	// before compaction. The fixture's helper hides the registry, so
	// we rebuild here from scratch.
	reg := packs.NewPackRegistry()
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("test.bulk%d", i)
		if err := reg.Register(&packs.Pack{
			Name:        name,
			Version:     "v1",
			Description: "A bulky test pack that exists to push the catalog past Tier C's MaxCatalogBytes budget so the compaction loop fires every priority step. This description is deliberately long to consume bytes.",
			Metadata: packs.PackMetadata{
				Accepts:        []string{"input_kind_a", "input_kind_b", "input_kind_c"},
				Produces:       []string{"output_kind"},
				IntentKeywords: []string{"keyword one", "keyword two", "keyword three"},
				TypicalUse:     "A long typical-use string explaining the canonical caller pattern in detail so this entry consumes bytes during compaction tests.",
				Limitations:    []string{"limit one with explanation", "limit two with explanation"},
			},
			Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	disp := &scriptedDispatcher{replies: []string{reply}}
	pipes := fakePipelinesLister{raw: json.RawMessage(`[]`)}
	pack := Plan(disp, reg, pipes)
	store := memory.NewInMemoryStore()
	eng := packs.New(
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		packs.WithMemoryStore(store),
	)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"x","model":"openrouter/openrouter/free"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out planOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Compaction == nil {
		t.Fatalf("compaction should be present on Tier C with large catalog; got nil")
	}
	if out.Compaction.BeforeBytes <= out.Compaction.AfterBytes {
		t.Errorf("compaction should reduce size; before=%d after=%d", out.Compaction.BeforeBytes, out.Compaction.AfterBytes)
	}
	if len(out.Compaction.Dropped) == 0 {
		t.Errorf("compaction.dropped should name the steps that fired; got empty")
	}
}

// TestPlan_TwoPassLLMFilterFires — ADR 050 PR #4: when the lexical
// pass leaves an ambiguous result on a Tier C model that allows the
// filter pass, the handler dispatches a SECOND LLM call (the filter)
// before the planning call. The scripted dispatcher returns the
// filter response first, then the plan response — order matters.
func TestPlan_TwoPassLLMFilterFires(t *testing.T) {
	// Filter response: pick a small set of ids the planner should see.
	filterReply := `{"ids":["test.bulk0","test.bulk1","test.bulk2"]}`
	// Planning response: a valid 1-step plan referring to one of the kept ids.
	planReply := `{"steps":[{"order":1,"tool":"test.bulk0","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`

	// Build a registry with enough packs to force lexical truncation
	// AND lexical ambiguity (close scores).
	reg := packs.NewPackRegistry()
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("test.bulk%d", i)
		if err := reg.Register(&packs.Pack{
			Name:        name,
			Version:     "v1",
			Description: "Bulk test pack to push catalog over Tier C budget for filter-pass coverage tests; padded with words to consume bytes consistently across the metadata fields the compaction loop strips.",
			Metadata: packs.PackMetadata{
				Accepts:        []string{"input_a", "input_b"},
				Produces:       []string{"output_a"},
				IntentKeywords: []string{"do something useful", "another keyword phrase"},
				TypicalUse:     "Generic typical-use string to push compaction along through every priority step until lexical truncation triggers escalation.",
				Limitations:    []string{"slow on weak models", "not stable in heavy parallelism", "requires network access"},
			},
			Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	disp := &scriptedDispatcher{replies: []string{filterReply, planReply}}
	pipes := fakePipelinesLister{raw: json.RawMessage(`[]`)}
	pack := Plan(disp, reg, pipes)
	store := memory.NewInMemoryStore()
	eng := packs.New(
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		packs.WithMemoryStore(store),
	)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"do something useful","model":"openrouter/openrouter/free"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The dispatcher should have been called TWICE: filter pass + planning pass.
	if len(disp.captured) != 2 {
		t.Fatalf("expected 2 dispatcher calls (filter + plan); got %d", len(disp.captured))
	}
	// First call uses the filter system prompt, second uses the planning prompt.
	if !strings.Contains(disp.captured[0].Messages[0].Content.Text(), "tool-filter assistant") {
		t.Errorf("first call should be the filter pass; got system=%s",
			disp.captured[0].Messages[0].Content.Text()[:60])
	}
	if !strings.Contains(disp.captured[1].Messages[0].Content.Text(), "planning agent") {
		t.Errorf("second call should be the planning pass; got system=%s",
			disp.captured[1].Messages[0].Content.Text()[:60])
	}
	// Plan output should have the compaction record with an llm_filter entry.
	var out planOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Compaction == nil {
		t.Fatal("compaction record missing")
	}
	sawFilter := false
	for _, d := range out.Compaction.Dropped {
		if strings.HasPrefix(d, "llm_filter(") {
			sawFilter = true
			break
		}
	}
	if !sawFilter {
		t.Errorf("compaction.dropped should include llm_filter(...) entry; got %v", out.Compaction.Dropped)
	}
}

// TestPlan_FilterFailsFallsBackToLexical — when the filter dispatch
// errors or returns garbage, the handler must continue with the
// lexical-only selection instead of failing the plan call. The
// filter is an OPTIONAL enhancement, never a hard dependency.
func TestPlan_FilterFailsFallsBackToLexical(t *testing.T) {
	// Filter response is malformed (model returned prose); planning
	// response is a valid plan. The handler should fall back to the
	// lexical selection and still produce a plan.
	filterReply := `Sorry, I cannot determine the relevant tools from this list.`
	planReply := `{"steps":[{"order":1,"tool":"test.bulk0","args":{},"rationale":"x"}],"complexity":"single-action","reasoning":"x"}`

	reg := packs.NewPackRegistry()
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("test.bulk%d", i)
		if err := reg.Register(&packs.Pack{
			Name:        name,
			Version:     "v1",
			Description: "Bulk test pack with enough metadata fields to push the catalog past Tier C's 10000-byte budget so lexical truncation fires and the filter pass gets dispatched.",
			Metadata: packs.PackMetadata{
				Accepts:        []string{"input_a", "input_b"},
				Produces:       []string{"output_a"},
				IntentKeywords: []string{"do something useful", "another phrase"},
				TypicalUse:     "Long typical-use string padding bytes so compaction has multiple metadata fields to strip before escalating.",
				Limitations:    []string{"slow", "limited concurrency"},
			},
			Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	disp := &scriptedDispatcher{replies: []string{filterReply, planReply}}
	pipes := fakePipelinesLister{raw: json.RawMessage(`[]`)}
	pack := Plan(disp, reg, pipes)
	store := memory.NewInMemoryStore()
	eng := packs.New(
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		packs.WithMemoryStore(store),
	)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"user_intent":"do something useful","model":"openrouter/openrouter/free"}`))
	if err != nil {
		t.Fatalf("Execute should not fail when filter fails; got err=%v", err)
	}
	var out planOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Steps) == 0 {
		t.Errorf("plan should still produce steps after filter fallback")
	}
	// Compaction record exists (lexical fired) but should NOT
	// include the llm_filter(...) entry — the filter pass failed.
	if out.Compaction != nil {
		for _, d := range out.Compaction.Dropped {
			if strings.HasPrefix(d, "llm_filter(") {
				t.Errorf("filter failure should NOT add llm_filter entry; got %v", out.Compaction.Dropped)
			}
		}
	}
}
