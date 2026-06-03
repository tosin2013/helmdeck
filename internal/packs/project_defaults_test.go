// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package packs

import (
	"testing"
)

// project_defaults_test.go pins the exported ProjectDefaults variant
// (PR F of the v0.25.0 reliability arc). BuildDefaults — the
// store-backed version — is already covered by defaults_test.go.
// ProjectDefaults runs the same projection over pre-decoded slices,
// which the helmdeck.route meta-pack uses (it reads audit rows via
// the per-caller namespace-scoped MemoryInterface and decodes them
// itself before projecting). A regression in the projection logic
// here would break the route handler's recommendation surface
// silently — the routing brain would forget what the caller actually
// uses and start recommending generic packs.

// TestProjectDefaults_EmptyInputsReturnsEmpty — projecting empty
// audit slices returns a Defaults value with non-nil empty slices
// (NOT zero-value nil slices) so JSON marshalling renders `[]`
// rather than `null`. Same shape contract BuildDefaults follows.
func TestProjectDefaults_EmptyInputsReturnsEmpty(t *testing.T) {
	def := ProjectDefaults(nil, nil)
	if def.Packs == nil {
		t.Error("Packs should be non-nil empty slice (JSON marshals as [])")
	}
	if def.Pipelines == nil {
		t.Error("Pipelines should be non-nil empty slice")
	}
	if len(def.Packs) != 0 || len(def.Pipelines) != 0 {
		t.Errorf("expected empty projection; got %+v", def)
	}
}

// TestProjectDefaults_RanksByCallCount — the most-used pack surfaces
// first. The route handler uses this ordering to recommend the
// caller's top pack when no better signal is available.
func TestProjectDefaults_RanksByCallCount(t *testing.T) {
	audits := []PackAudit{
		// 3 calls to blog.rewrite_for_audience.
		{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 100,
			LearnInputs: map[string]string{"persona": "technical"}},
		{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 200,
			LearnInputs: map[string]string{"persona": "technical"}},
		{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 300,
			LearnInputs: map[string]string{"persona": "marketing"}},
		// 1 call to slides.outline.
		{Pack: "slides.outline", Outcome: "ok", AtUnix: 250,
			LearnInputs: map[string]string{"persona": "executive"}},
	}
	def := ProjectDefaults(audits, nil)
	if len(def.Packs) != 2 {
		t.Fatalf("Packs = %d; want 2", len(def.Packs))
	}
	if def.Packs[0].ID != "blog.rewrite_for_audience" {
		t.Errorf("most-used pack = %q; want blog.rewrite_for_audience", def.Packs[0].ID)
	}
	if def.Packs[0].Calls != 3 {
		t.Errorf("Calls = %d; want 3", def.Packs[0].Calls)
	}
	// Most-common persona for the top pack: technical (2 of 3).
	if def.Packs[0].CommonInputs["persona"] != "technical" {
		t.Errorf("CommonInputs[persona] = %q; want technical",
			def.Packs[0].CommonInputs["persona"])
	}
}

// TestProjectDefaults_FilterFailedRuns — only successful runs
// contribute to learned defaults. A caller-fixable failure with
// persona="executive" must NOT pin executive as a default — the
// caller's intent was wrong, the failure should not be reinforced.
func TestProjectDefaults_FilterFailedRuns(t *testing.T) {
	audits := []PackAudit{
		{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 100,
			LearnInputs: map[string]string{"persona": "technical"}},
		{Pack: "blog.rewrite_for_audience", Outcome: "invalid_input", AtUnix: 200,
			LearnInputs: map[string]string{"persona": "executive"}},
		{Pack: "blog.rewrite_for_audience", Outcome: "handler_failed", AtUnix: 300,
			LearnInputs: map[string]string{"persona": "marketing"}},
	}
	def := ProjectDefaults(audits, nil)
	if len(def.Packs) != 1 {
		t.Fatalf("Packs = %d; want 1", len(def.Packs))
	}
	if def.Packs[0].Calls != 1 {
		t.Errorf("Calls = %d; want 1 (failures excluded)", def.Packs[0].Calls)
	}
	if def.Packs[0].CommonInputs["persona"] != "technical" {
		t.Errorf("CommonInputs[persona] = %q; want technical only", def.Packs[0].CommonInputs["persona"])
	}
}

// TestProjectDefaults_PipelineAcceptsBothOutcomeValues — pipeline-
// level audits use "succeeded" (Run.Status), pack-level audits use
// "ok". The projection accepts BOTH so a future refactor that
// unifies the outcome vocabulary doesn't silently drop projections.
func TestProjectDefaults_PipelineAcceptsBothOutcomeValues(t *testing.T) {
	pipes := []PipelineAudit{
		{Pipeline: "p1", Outcome: "succeeded", AtUnix: 100},
		{Pipeline: "p1", Outcome: "ok", AtUnix: 200},
		{Pipeline: "p1", Outcome: "failed", AtUnix: 300}, // excluded
	}
	def := ProjectDefaults(nil, pipes)
	if len(def.Pipelines) != 1 {
		t.Fatalf("Pipelines = %d; want 1", len(def.Pipelines))
	}
	if def.Pipelines[0].Runs != 2 {
		t.Errorf("Runs = %d; want 2 (succeeded + ok counted, failed excluded)",
			def.Pipelines[0].Runs)
	}
}

// TestProjectDefaults_TopNCapApplied — when more than DefaultsTopN
// distinct packs have audit history, only the top N survive. Pin
// the cap so a heavy caller doesn't blow up the routing prompt with
// every pack they've ever called.
func TestProjectDefaults_TopNCapApplied(t *testing.T) {
	audits := make([]PackAudit, 0, DefaultsTopN+5)
	// DefaultsTopN+3 distinct packs, each used once.
	for i := 0; i < DefaultsTopN+3; i++ {
		audits = append(audits, PackAudit{
			Pack: packName(i), Outcome: "ok", AtUnix: int64(100 + i),
		})
	}
	def := ProjectDefaults(audits, nil)
	if len(def.Packs) != DefaultsTopN {
		t.Errorf("Packs = %d; want exactly DefaultsTopN=%d", len(def.Packs), DefaultsTopN)
	}
}

// packName generates a deterministic, distinct pack id for the
// top-N cap test.
func packName(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	return "test.pack_" + string(letters[i%len(letters)]) + "_" + string(rune('0'+i/10))
}
