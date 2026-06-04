// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// my_resources_test.go (PR G of the v0.25.0 reliability arc) covers
// the helmdeck://my-defaults, helmdeck://my-memory, and
// helmdeck://routing-guide MCP resource builders. These are the
// projections the chat agent reads at the top of every session to
// understand who it's talking to and what's been learned. A
// regression here breaks the agent's per-caller memory loop silently.

// TestBuildMyDefaults_NoEngine_Note — an engine-less server (rare —
// dev-mode without packs) still returns a well-shaped response with
// the explanatory note instead of a nil deref.
func TestBuildMyDefaults_NoEngine_Note(t *testing.T) {
	srv := NewPackServer(packs.NewPackRegistry(), nil)
	out, rerr := srv.buildMyDefaults(context.Background(), "alice")
	if rerr != nil {
		t.Fatalf("rpcError: %+v", rerr)
	}
	if out.Scope != "caller=alice" {
		t.Errorf("Scope = %q", out.Scope)
	}
	if !strings.Contains(out.Note, "memory store not configured") {
		t.Errorf("note should explain missing store: %q", out.Note)
	}
	// Non-nil empty slices so JSON renders as `[]`, not `null`.
	if out.Packs == nil || out.Pipelines == nil {
		t.Error("Packs/Pipelines should be non-nil empty slices")
	}
}

// TestBuildMyDefaults_NoStore_Note — engine wired but no memory
// store: same explanatory note as no-engine. The QMD bridge's
// 503 stub uses this signal to mount a clear "not configured"
// response rather than a misleading "no data" one.
func TestBuildMyDefaults_NoStore_Note(t *testing.T) {
	srv := NewPackServer(packs.NewPackRegistry(), packs.New())
	out, _ := srv.buildMyDefaults(context.Background(), "alice")
	if !strings.Contains(out.Note, "memory store not configured") {
		t.Errorf("note should explain missing store: %q", out.Note)
	}
}

// TestBuildMyDefaults_EmptyHistory_Note — engine + store wired but
// no audit rows for this caller yet (fresh subject). The Note
// changes shape — "no audit history yet" — so the UI can distinguish
// "memory off" from "memory on but new caller".
func TestBuildMyDefaults_EmptyHistory_Note(t *testing.T) {
	store := memory.NewInMemoryStore()
	srv := NewPackServer(packs.NewPackRegistry(),
		packs.New(packs.WithMemoryStore(store)))
	out, _ := srv.buildMyDefaults(context.Background(), "fresh-user")
	if !strings.Contains(out.Note, "no audit history yet") {
		t.Errorf("empty-history note should differ from no-store: %q", out.Note)
	}
}

// TestBuildMyDefaults_PopulatedProjection — seed pack + pipeline
// audit rows, verify the wire-shape rewrite from packs.Defaults to
// MyDefaults preserves ID, Calls/Runs, LastUsed, and CommonInputs.
// Without this test, a future refactor that renames a JSON tag on
// the wire would silently corrupt the agent's defaults reading.
func TestBuildMyDefaults_PopulatedProjection(t *testing.T) {
	store := memory.NewInMemoryStore()
	caller := "alice"

	for i := 0; i < 3; i++ {
		audit := packs.PackAudit{
			Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: int64(100 + i),
			LearnInputs: map[string]string{"persona": "technical"},
		}
		body, _ := json.Marshal(audit)
		_, _ = store.Put(context.Background(), caller,
			packs.AuditKeyPrefixPack+"blog.rewrite_for_audience/"+pad(i),
			body, memory.WithCategory(packs.AuditCategoryPack))
	}
	pipeAudit := packs.PipelineAudit{Pipeline: "p1", Outcome: "succeeded", AtUnix: 500}
	body, _ := json.Marshal(pipeAudit)
	_, _ = store.Put(context.Background(), caller,
		packs.AuditKeyPrefixPipeline+"p1/0001", body,
		memory.WithCategory(packs.AuditCategoryPipeline))

	srv := NewPackServer(packs.NewPackRegistry(),
		packs.New(packs.WithMemoryStore(store)))
	out, _ := srv.buildMyDefaults(context.Background(), caller)
	if len(out.Packs) != 1 || out.Packs[0].ID != "blog.rewrite_for_audience" {
		t.Errorf("Packs projection wrong: %+v", out.Packs)
	}
	if out.Packs[0].Calls != 3 {
		t.Errorf("Calls = %d; want 3", out.Packs[0].Calls)
	}
	if out.Packs[0].CommonInputs["persona"] != "technical" {
		t.Errorf("CommonInputs[persona] = %q", out.Packs[0].CommonInputs["persona"])
	}
	if len(out.Pipelines) != 1 || out.Pipelines[0].ID != "p1" {
		t.Errorf("Pipelines projection wrong: %+v", out.Pipelines)
	}
	if out.Note != "" {
		t.Errorf("note should be empty when projection is populated: %q", out.Note)
	}
}

func pad(i int) string {
	if i < 10 {
		return "000" + string(rune('0'+i))
	}
	return "00" + string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// TestBuildMyMemory_NoStore_Note — same nil-store gating as
// buildMyDefaults, distinct note message.
func TestBuildMyMemory_NoStore_Note(t *testing.T) {
	srv := NewPackServer(packs.NewPackRegistry(), packs.New())
	out, _ := srv.buildMyMemory(context.Background(), "alice")
	if !strings.Contains(out.Note, "memory store not configured") {
		t.Errorf("note should explain missing store: %q", out.Note)
	}
	if out.Categories == nil {
		t.Error("Categories should be non-nil empty slice")
	}
}

// TestBuildMyMemory_NoFacts_Note — memory wired but no user-written
// facts yet (audit categories are filtered out). Note message is
// different from "no store" so the UI can show a clear "store some
// facts via memory_store" prompt.
func TestBuildMyMemory_NoFacts_Note(t *testing.T) {
	store := memory.NewInMemoryStore()
	// Seed an audit row — this MUST be filtered out, not surfaced
	// as a "category" the agent thinks it can recall from.
	audit := packs.PackAudit{Pack: "x", Outcome: "ok"}
	body, _ := json.Marshal(audit)
	_, _ = store.Put(context.Background(), "alice",
		packs.AuditKeyPrefixPack+"x/0001", body,
		memory.WithCategory(packs.AuditCategoryPack))

	srv := NewPackServer(packs.NewPackRegistry(),
		packs.New(packs.WithMemoryStore(store)))
	out, _ := srv.buildMyMemory(context.Background(), "alice")
	if !strings.Contains(out.Note, "no user facts stored yet") {
		t.Errorf("note should explain no facts: %q", out.Note)
	}
	if len(out.Categories) != 0 {
		t.Errorf("audit categories should be filtered out, got %+v", out.Categories)
	}
}

// TestBuildMyMemory_GroupsByCategory — agent-written facts in two
// different categories surface as two MyMemoryCategory entries, each
// with a count and recent-keys peek. Recent-keys are sorted by
// UpdatedAt desc; the cap (myMemoryRecentCap = 5) is enforced; the
// final categories slice is alphabetically sorted so successive
// reads don't churn the wire.
func TestBuildMyMemory_GroupsByCategory(t *testing.T) {
	store := memory.NewInMemoryStore()
	caller := "alice"
	ctx := context.Background()

	// 6 preferences (will be capped to 5 recent), 2 project_conventions.
	for i := 0; i < 6; i++ {
		_, _ = store.Put(ctx, caller, "preferences/key"+pad(i),
			[]byte("v"), memory.WithCategory("preferences"))
	}
	_, _ = store.Put(ctx, caller, "project_conventions/style",
		[]byte("v"), memory.WithCategory("project_conventions"))
	_, _ = store.Put(ctx, caller, "project_conventions/tooling",
		[]byte("v"), memory.WithCategory("project_conventions"))

	srv := NewPackServer(packs.NewPackRegistry(),
		packs.New(packs.WithMemoryStore(store)))
	out, _ := srv.buildMyMemory(ctx, caller)

	if len(out.Categories) != 2 {
		t.Fatalf("Categories = %d; want 2", len(out.Categories))
	}
	// Alphabetical: preferences < project_conventions.
	if out.Categories[0].Name != "preferences" {
		t.Errorf("first category = %q; want preferences (alphabetical)",
			out.Categories[0].Name)
	}
	if out.Categories[0].Count != 6 {
		t.Errorf("preferences count = %d; want 6", out.Categories[0].Count)
	}
	if len(out.Categories[0].RecentKeys) != myMemoryRecentCap {
		t.Errorf("RecentKeys length = %d; want %d (cap)",
			len(out.Categories[0].RecentKeys), myMemoryRecentCap)
	}
	if out.Categories[1].Name != "project_conventions" {
		t.Errorf("second category = %q", out.Categories[1].Name)
	}
	if out.Categories[1].Count != 2 {
		t.Errorf("project_conventions count = %d; want 2", out.Categories[1].Count)
	}
}

// TestBuildRoutingGuide_NoPackOrPipeline — server with empty registry
// + no pipeline service: routing guide returns non-nil empty slices
// and the standard policy block. Wire shape stays clean for an
// agent reading the guide before any packs are registered.
func TestBuildRoutingGuide_NoPackOrPipeline(t *testing.T) {
	srv := NewPackServer(packs.NewPackRegistry(), packs.New())
	out, rerr := srv.buildRoutingGuide(context.Background())
	if rerr != nil {
		t.Fatalf("rpcError: %+v", rerr)
	}
	if out.Policy == "" {
		t.Error("Policy should always be populated")
	}
	if out.Packs == nil || out.Pipelines == nil {
		t.Error("Packs/Pipelines should be non-nil empty slices")
	}
}

// TestBuildRoutingGuide_WithPipelines — pipeline service wired: the
// guide aggregates the pipeline list. Tests the full happy path
// including the JSON re-decode into the routing-guide subset shape.
func TestBuildRoutingGuide_WithPipelines(t *testing.T) {
	ps := &stubPipelineService{
		listResp: json.RawMessage(`[
			{"id":"builtin.brief-rewrite-blog","name":"Brief rewrite","description":"brief to blog","metadata":{"accepts":["brief"]}},
			{"id":"builtin.research-deck","name":"Research deck","description":"query to deck"}
		]`),
	}
	srv := NewPackServer(packs.NewPackRegistry(), packs.New(), WithPipelines(ps))
	out, rerr := srv.buildRoutingGuide(context.Background())
	if rerr != nil {
		t.Fatalf("rpcError: %+v", rerr)
	}
	if len(out.Pipelines) != 2 {
		t.Errorf("Pipelines = %d; want 2", len(out.Pipelines))
	}
	if out.Pipelines[0].ID != "builtin.brief-rewrite-blog" {
		t.Errorf("Pipelines[0].ID = %q", out.Pipelines[0].ID)
	}
	if out.Pipelines[0].Name != "Brief rewrite" {
		t.Errorf("Pipelines[0].Name = %q", out.Pipelines[0].Name)
	}
}

// TestBuildRoutingGuide_PipelineListError — Service.List failures
// surface as -32603 internal-error so the MCP client doesn't see a
// corrupt response.
func TestBuildRoutingGuide_PipelineListError(t *testing.T) {
	ps := &stubPipelineService{listErr: errIntentional("backend down")}
	srv := NewPackServer(packs.NewPackRegistry(), packs.New(), WithPipelines(ps))
	_, rerr := srv.buildRoutingGuide(context.Background())
	if rerr == nil {
		t.Fatal("rpcError should be non-nil on pipeline service error")
	}
	if rerr.Code != -32603 {
		t.Errorf("Code = %d; want -32603", rerr.Code)
	}
	if !strings.Contains(rerr.Message, "backend down") {
		t.Errorf("message should surface service error: %q", rerr.Message)
	}
}

// errIntentional is a small typed error used to drive the routing-
// guide pipeline-list error path. Distinguishing it from errors.New
// keeps the test self-documenting.
type errIntentional string

func (e errIntentional) Error() string { return string(e) }

// TestFormatPipelineAuditChunk_AllFields — the QMD MCP corpus bridge
// renders pipeline-audit rows into markdown chunks for the LLM to
// search. Pin: every field the audit declares appears in the output,
// in a stable header → key/value layout the LLM can scan.
func TestFormatPipelineAuditChunk_AllFields(t *testing.T) {
	chunk := formatPipelineAuditChunk(packs.PipelineAudit{
		Pipeline:    "builtin.brief-rewrite-blog",
		RunID:       "run_abc",
		Outcome:     "succeeded",
		DurationMs:  5000,
		LearnInputs: map[string]string{"theme": "deep-dive", "model": "openai/gpt-4o"},
	})
	for _, want := range []string{
		"Pipeline run: builtin.brief-rewrite-blog",
		"Outcome: succeeded",
		"Run ID: run_abc",
		"Duration: 5000ms",
		"- model: openai/gpt-4o",
		"- theme: deep-dive",
	} {
		if !strings.Contains(chunk, want) {
			t.Errorf("chunk missing %q\nfull:\n%s", want, chunk)
		}
	}
}

// TestFormatPipelineAuditChunk_OptionalFieldsOmitted — empty Run ID,
// zero DurationMs, and empty LearnInputs must NOT produce empty
// lines / dangling labels in the markdown. Pin so a future refactor
// doesn't accidentally emit "Run ID: \nDuration: 0ms" for fresh runs.
func TestFormatPipelineAuditChunk_OptionalFieldsOmitted(t *testing.T) {
	chunk := formatPipelineAuditChunk(packs.PipelineAudit{
		Pipeline: "p1",
		Outcome:  "succeeded",
	})
	if strings.Contains(chunk, "Run ID:") {
		t.Errorf("chunk should not include empty Run ID: %s", chunk)
	}
	if strings.Contains(chunk, "Duration:") {
		t.Errorf("chunk should not include zero Duration: %s", chunk)
	}
	if strings.Contains(chunk, "Inputs used:") {
		t.Errorf("chunk should not include empty Inputs section: %s", chunk)
	}
}
