// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// Output-schema contract tests.
//
// PR #390 (slides.narrate) and PR #408 (podcast.generate) exposed the bug
// class: a pack's unit tests invoke pack.Handler directly, which BYPASSES
// packs.Engine.Execute — the only place OutputSchema.Validate runs. So a
// handler can emit output that violates its declared schema and every unit
// test passes, while real pipeline runs (which go through Execute) fail
// with `invalid_output: field "tts_chars": expected number, got object`.
//
// These tests close the gap by explicitly validating each pack's handler
// output against the declared OutputSchema. They run on the LLM-backed
// surface — the packs whose output shape is the model's responsibility,
// not the runtime's — because that's where the drift between "what the
// schema says" and "what the model produces given the prompt we wrote"
// matters most for the cheap-model reliability bet (ADR 008, ADR 050).
//
// Pattern per test:
//   1. Build a scripted dispatcher with a reply that should pass the schema.
//   2. Invoke pack.Handler (directly or via eng.Execute, whichever matches
//      the pack's existing test scaffolding).
//   3. Assert pack.OutputSchema.Validate(output) returns nil.
//
// Packs deliberately skipped here:
//   - browser_interact, screenshot_url, github.*: real-session/real-network
//     dependencies; covered by PR C of the v0.24.0 arc with a different
//     scaffolding pattern (cdpfake + httptest).
//   - vision packs: dispatcher reply must be both valid JSON action AND
//     pass the action-shape validator; covered by their own action-shape
//     contract tests.

func TestSlidesNarrate_RealOutputMatchesSchema(t *testing.T) {
	schema := SlidesNarrate(nil, nil, nil).OutputSchema
	disp := &scriptedDispatcherWT{replies: []string{
		`{"title":"T","description":"d","tags":["x"],"category":"Education","language":"en"}`,
	}}
	raw, err := runNarrate(t, disp, nil, &narrateExecScript{},
		`{"markdown":"---\nmarp: true\n---\n\n# A\n\n<!-- some notes -->","metadata_model":"openai/gpt-4o-mini","allow_silent_output":true}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if verr := schema.Validate(raw); verr != nil {
		t.Errorf("slides.narrate real-run output violates its declared OutputSchema (Execute would reject it): %v\noutput: %s", verr, raw)
	}
}

func TestPodcastGenerate_RealOutputMatchesSchema(t *testing.T) {
	schema := PodcastGenerate(nil, nil, nil).OutputSchema
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90finalmp3goeshere")}
	raw, err := runPodcastGenerate(t, v, ex,
		`{"speakers":{"Alex":"v1","Jordan":"v2"},"script":[{"speaker":"Alex","text":"Hi."},{"speaker":"Jordan","text":"Hello."}],"theme":"deep-dive","allow_silent_output":true}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if verr := schema.Validate(raw); verr != nil {
		t.Errorf("podcast.generate real-run output violates its declared OutputSchema (Execute would reject it): %v\noutput: %s", verr, raw)
	}
}

// TestPlan_RealOutputMatchesSchema covers helmdeck.plan — the routing
// brain. It uses eng.Execute (which already runs schema.Validate inside
// the engine), but the explicit re-validation below makes the contract
// visible. A schema/handler drift here would be caught by Execute's
// own validation; this test pins WHICH check failed so a maintainer
// editing the schema doesn't have to guess.
func TestPlan_RealOutputMatchesSchema(t *testing.T) {
	reply := `{
		"steps":[
			{"order":1,"tool":"helmdeck.memory_store","args":{"key":"x","value":"y"},"rationale":"persist"}
		],
		"complexity":"single-pack",
		"reasoning":"one-step intent"
	}`
	eng, _, pack, _ := planFixture(t, reply)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack,
		json.RawMessage(`{"user_intent":"remember this","model":"openrouter/auto"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if verr := pack.OutputSchema.Validate(res.Output); verr != nil {
		t.Errorf("helmdeck.plan real-run output violates its declared OutputSchema: %v\noutput: %s", verr, res.Output)
	}
}

// TestRoute_RealOutputMatchesSchema covers helmdeck.route — the
// single-pack/pipeline picker. Output shape is fully owned by the
// model + the routePostProcess step that decorates the recommendation.
func TestRoute_RealOutputMatchesSchema(t *testing.T) {
	reply := `{
		"recommendation":{"kind":"pack","id":"doc.parse","why":"intent matches accepts=pdf"},
		"alternatives":[],
		"gap_warning":null,
		"reasoning":"single-pack match"
	}`
	eng, _, pack := routeFixture(t, reply, nil)
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, pack,
		json.RawMessage(`{"user_intent":"parse this pdf","model":"openrouter/auto"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if verr := pack.OutputSchema.Validate(res.Output); verr != nil {
		t.Errorf("helmdeck.route real-run output violates its declared OutputSchema: %v\noutput: %s", verr, res.Output)
	}
}

// TestContentGround_RealOutputMatchesSchema covers content.ground —
// fact-checks markdown against sources and rewrites with citations.
// The no-claims path is the cleanest contract target: text mode (no
// session/exec needed), dispatcher returns an empty claim list, the
// handler emits the "nothing to ground; pass-through" envelope that
// downstream pipelines depend on. Same envelope shape as the claims-
// found path, so schema conformance here pins the contract for both.
//
// HELMDECK_FIRECRAWL_ENABLED is required even on the no-claims path
// because content.ground checks the flag up front (it shares
// Firecrawl gating with research.deep — both off the same overlay).
func TestContentGround_RealOutputMatchesSchema(t *testing.T) {
	t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "true")
	disp := &scriptedDispatcherWT{replies: []string{`{"claims":[]}`}}
	raw, err := runContentGround(t, disp, &execScript{}, nil,
		`{"text":"Hello world.","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	pack := ContentGround(nil)
	if verr := pack.OutputSchema.Validate(raw); verr != nil {
		t.Errorf("content.ground real-run output violates its declared OutputSchema: %v\noutput: %s", verr, raw)
	}
}

// TestResearchDeep_RealOutputMatchesSchema covers research.deep —
// multi-source synthesis from Firecrawl results. The handler errors
// out on zero results (caller_fixable, refine the query), so the
// minimum contract target is one usable source + a synthesis reply
// from the dispatcher. That exercises the full envelope shape
// (query, sources[], synthesis, model) the downstream packs depend on.
func TestResearchDeep_RealOutputMatchesSchema(t *testing.T) {
	pack := ResearchDeep(nil)
	schema := pack.OutputSchema
	srv := stubFirecrawlSearch(t, 200, `{
		"success": true,
		"data": [
			{"url":"https://a.example","title":"A","description":"first",
			 "markdown":"source A body","metadata":{"title":"A","statusCode":200}}
		]
	}`)
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replies: []string{
		"# Synthesis\n\nFrom [A](https://a.example): first finding.",
	}}
	raw, err := runResearchDeep(t, disp,
		`{"query":"valid topic","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if verr := schema.Validate(raw); verr != nil {
		t.Errorf("research.deep real-run output violates its declared OutputSchema: %v\noutput: %s", verr, raw)
	}
}

// TestSweSolve_RealOutputMatchesSchema covers swe.solve — the
// agentic code-edit pack. Patch mode is the simplest contract target:
// it ends at "diff + trajectory artifact" without commit/push/PR
// steps, so the executor stub only needs to script the first five
// session calls. The output schema covers all four modes (patch,
// branch, pull_request, fail), so any drift in the patch envelope
// catches schema/handler skew.
func TestSweSolve_RealOutputMatchesSchema(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(sweCloneEnvelope)},                  // 0: clone
		{Stdout: []byte(`{"map":"pkg/x.go:\n  func Foo"}`)}, // 1: repo.map
		{}, // 2: mini run
		{Stdout: []byte("diff --git a/x b/x\n+added")},        // 3: git diff
		{Stdout: []byte(`{"messages":[{"content":"done"}]}`)}, // 4: cat trajectory
	}}
	eng := newSweEngine(t, ex)
	pack := SweSolve(nil, nil)
	res, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"repo_url":"https://github.com/octocat/Hello-World.git","task":"add a test","mode":"patch"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if verr := pack.OutputSchema.Validate(res.Output); verr != nil {
		t.Errorf("swe.solve real-run (patch mode) output violates its declared OutputSchema: %v\noutput: %s", verr, res.Output)
	}
}
