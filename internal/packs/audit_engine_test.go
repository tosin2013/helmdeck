// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package packs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// audit_engine_test.go covers the engine-level exported audit + memory
// surface (PR F of the v0.25.0 reliability arc).
//
// Already covered by audit_test.go: the implicit writePackAudit
// behind Engine.Execute. Missing in CI before this file:
//   - WritePlanAudit (called by the helmdeck.plan handler via
//     ec.engine.WritePlanAudit; production wire is internal/packs/builtin)
//   - WritePipelineAudit (called by internal/pipelines.Runner.RunSync
//     to attribute the run-level outcome to per-caller defaults)
//   - MemoryStore() accessor (called by internal/api/mcp_qmd_sse.go to
//     decide whether to mount the QMD MCP bridge or stub a 503)
//
// All three are load-bearing for the ADR 048 memory-as-context loop
// the cheap-model reliability bet rests on. If WritePlanAudit's key
// shape drifts, the planning-history projection silently breaks. If
// WritePipelineAudit attributes to the wrong caller, every pipeline's
// learned-defaults attribute to "unknown". If MemoryStore() returns
// the wrong instance, QMD bridges a non-existent corpus.

// TestEngine_WritePlanAudit_HappyPath — the plan handler writes its
// audit row through ec.engine.WritePlanAudit. Pin: row lands in the
// caller's namespace, under the plan_history/<intent_sha>/ prefix,
// with category=plan_history (ADR 049's reservation).
func TestEngine_WritePlanAudit_HappyPath(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))

	ctx := WithCaller(context.Background(), "alice")
	eng.WritePlanAudit(ctx, PlanAudit{
		IntentSHA:  "abc123",
		Complexity: "pack-chain",
		Outcome:    "ok",
		Steps: []PlanAuditStep{
			{Order: 1, Tool: "helmdeck.memory_store", ArgsSHA: "xyz"},
			{Order: 2, Tool: "blog.publish", ArgsSHA: "qrs"},
		},
		DurationMs: 250,
		Model:      "openrouter/auto",
	})

	entries, err := store.List(context.Background(), "alice", AuditKeyPrefixPlan)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 plan-audit row, got %d", len(entries))
	}
	if entries[0].Category != AuditCategoryPlan {
		t.Errorf("category = %q; want %q", entries[0].Category, AuditCategoryPlan)
	}
	// Key shape: plan_history/<intent_sha>/<nano>. Pin the prefix so
	// projection code keying on intent_sha doesn't break on a future
	// refactor that drops the SHA from the path.
	if !strings.HasPrefix(entries[0].Key, AuditKeyPrefixPlan+"abc123/") {
		t.Errorf("key = %q; want prefix plan_history/abc123/", entries[0].Key)
	}

	var got PlanAudit
	if err := json.Unmarshal(entries[0].Value, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.IntentSHA != "abc123" || got.Complexity != "pack-chain" || got.Outcome != "ok" {
		t.Errorf("row = %+v", got)
	}
	if len(got.Steps) != 2 || got.Steps[0].Tool != "helmdeck.memory_store" {
		t.Errorf("steps = %+v", got.Steps)
	}
	if got.DurationMs != 250 || got.Model != "openrouter/auto" {
		t.Errorf("metadata fields wrong: %+v", got)
	}
	// AtUnix must be auto-populated when caller leaves it 0.
	if got.AtUnix == 0 {
		t.Error("AtUnix should be auto-set when zero")
	}
}

// TestEngine_WritePlanAudit_PreservesNonZeroAtUnix — when the caller
// pre-sets AtUnix (e.g. replaying historical rows or testing), the
// engine must not stomp it. The branch existed but was untested.
func TestEngine_WritePlanAudit_PreservesNonZeroAtUnix(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))
	ctx := WithCaller(context.Background(), "alice")
	want := int64(1700000000)
	eng.WritePlanAudit(ctx, PlanAudit{IntentSHA: "x", Outcome: "ok", AtUnix: want})

	entries, _ := store.List(context.Background(), "alice", AuditKeyPrefixPlan)
	var got PlanAudit
	_ = json.Unmarshal(entries[0].Value, &got)
	if got.AtUnix != want {
		t.Errorf("AtUnix = %d; want %d (caller-set value should not be stomped)", got.AtUnix, want)
	}
}

// TestEngine_WritePlanAudit_NilStoreIsNoOp — handlers don't gate the
// call on the memory store being wired; the engine MUST silently no-op
// when e.memory is nil. Without this, every plan run in a no-memory
// deployment would panic via a nil-deref on Put.
func TestEngine_WritePlanAudit_NilStoreIsNoOp(t *testing.T) {
	eng := quietEngine() // no WithMemoryStore
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WritePlanAudit panicked with nil store: %v", r)
		}
	}()
	eng.WritePlanAudit(context.Background(), PlanAudit{IntentSHA: "x", Outcome: "ok"})
}

// TestEngine_WritePlanAudit_UnknownCallerNamespace — without
// WithCaller on the context, the row lands under "unknown" (the
// callerFromContext default) so a future authenticated run can find
// it via the same namespace OR an audit query for "unknown" can
// inspect un-attributed traffic. The exact value matters because
// projection code (BuildDefaults) keys on it.
func TestEngine_WritePlanAudit_UnknownCallerNamespace(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))
	// No WithCaller — the default ("unknown") should be used.
	eng.WritePlanAudit(context.Background(), PlanAudit{IntentSHA: "x", Outcome: "ok"})

	entries, _ := store.List(context.Background(), "unknown", AuditKeyPrefixPlan)
	if len(entries) != 1 {
		t.Errorf("want 1 row under 'unknown' namespace, got %d", len(entries))
	}
}

// TestEngine_WritePipelineAudit_HappyPath — Runner.RunSync calls this
// to record per-caller pipeline outcomes. Pin: row lands in the
// caller's namespace under pipeline_history/<pipeline>/<nano>,
// category=pipeline_history, learnable inputs extracted.
func TestEngine_WritePipelineAudit_HappyPath(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))

	ctx := WithCaller(context.Background(), "alice")
	inputs := json.RawMessage(`{"theme":"deep-dive","model":"openai/gpt-4o","markdown":"a huge body that should be dropped from learn_inputs"}`)
	eng.WritePipelineAudit(ctx, "builtin.blog-from-brief", "run_abc", inputs, "succeeded", 5*time.Second)

	entries, _ := store.List(context.Background(), "alice", AuditKeyPrefixPipeline)
	if len(entries) != 1 {
		t.Fatalf("want 1 pipeline-audit row, got %d", len(entries))
	}
	if entries[0].Category != AuditCategoryPipeline {
		t.Errorf("category = %q; want %q", entries[0].Category, AuditCategoryPipeline)
	}
	if !strings.HasPrefix(entries[0].Key, AuditKeyPrefixPipeline+"builtin.blog-from-brief/") {
		t.Errorf("key = %q; want prefix pipeline_history/builtin.blog-from-brief/", entries[0].Key)
	}

	var got PipelineAudit
	_ = json.Unmarshal(entries[0].Value, &got)
	if got.Pipeline != "builtin.blog-from-brief" || got.RunID != "run_abc" {
		t.Errorf("row = %+v", got)
	}
	if got.Outcome != "succeeded" {
		t.Errorf("outcome = %q; want succeeded", got.Outcome)
	}
	if got.DurationMs != 5000 {
		t.Errorf("DurationMs = %d; want 5000", got.DurationMs)
	}
	// Learnable inputs: theme + model, NOT markdown.
	if got.LearnInputs["theme"] != "deep-dive" || got.LearnInputs["model"] != "openai/gpt-4o" {
		t.Errorf("learn_inputs missing fields: %+v", got.LearnInputs)
	}
	if _, leaked := got.LearnInputs["markdown"]; leaked {
		t.Errorf("markdown body leaked into learn_inputs: %+v", got.LearnInputs)
	}
}

// TestEngine_WritePipelineAudit_EmptyPipelineIDIsNoOp — the engine
// declines to write an audit row when pipelineID is empty (no point
// projecting an unidentifiable run). Pin the guard so a future
// refactor doesn't drop it and pollute the projection with empty-ID
// rows the my-defaults UI can't group.
func TestEngine_WritePipelineAudit_EmptyPipelineIDIsNoOp(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))
	ctx := WithCaller(context.Background(), "alice")
	eng.WritePipelineAudit(ctx, "", "run_x", json.RawMessage(`{}`), "succeeded", time.Second)

	entries, _ := store.List(context.Background(), "alice", AuditKeyPrefixPipeline)
	if len(entries) != 0 {
		t.Errorf("want 0 rows (empty pipelineID), got %d", len(entries))
	}
}

// TestEngine_WritePipelineAudit_NilStoreIsNoOp — same nil-deref guard
// as WritePlanAudit. Pipeline runner can't assume memory is wired.
func TestEngine_WritePipelineAudit_NilStoreIsNoOp(t *testing.T) {
	eng := quietEngine()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("WritePipelineAudit panicked with nil store: %v", r)
		}
	}()
	eng.WritePipelineAudit(context.Background(), "p", "r", json.RawMessage(`{}`), "succeeded", 0)
}

// TestEngine_MemoryStoreAccessor_ReturnsConfiguredStore — the engine
// exposes its memory store so internal/api/mcp_qmd_sse.go can decide
// whether to mount the QMD MCP bridge (non-nil → real server, nil →
// 503 stub). A regression that returns nil from a wired engine would
// silently disable QMD without a clear error.
func TestEngine_MemoryStoreAccessor_ReturnsConfiguredStore(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))

	got := eng.MemoryStore()
	if got != store {
		t.Errorf("MemoryStore() = %v; want the configured store", got)
	}
}

// TestEngine_MemoryStoreAccessor_NilWhenUnwired — the nil branch the
// QMD bridge gates on. Without this, the bridge would crash on the
// first Recall instead of mounting the 503 stub.
func TestEngine_MemoryStoreAccessor_NilWhenUnwired(t *testing.T) {
	eng := quietEngine() // no WithMemoryStore
	if got := eng.MemoryStore(); got != nil {
		t.Errorf("MemoryStore() = %v; want nil for unwired engine", got)
	}
}
