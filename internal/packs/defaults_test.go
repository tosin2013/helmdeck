package packs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// TestBuildDefaults_RoundTrip seeds a memory store with audit rows,
// runs BuildDefaults, and verifies the projection: top-N pack ranking,
// most-common-input picking, success-only filter, per-caller scope.
func TestBuildDefaults_RoundTrip(t *testing.T) {
	store := memory.NewInMemoryStore()
	ctx := context.Background()
	caller := "alice"

	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0001",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 100,
			LearnInputs: map[string]string{"persona": "technical", "audience": "engineers"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0002",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 200,
			LearnInputs: map[string]string{"persona": "technical", "audience": "engineers"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0003",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 300,
			LearnInputs: map[string]string{"persona": "marketing"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0004",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "invalid_input", AtUnix: 400,
			LearnInputs: map[string]string{"persona": "executive"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"slides.outline/0001",
		PackAudit{Pack: "slides.outline", Outcome: "ok", AtUnix: 500,
			LearnInputs: map[string]string{"persona": "marketing"}})
	putPipelineAudit(t, store, caller, AuditKeyPrefixPipeline+"builtin.brief-rewrite-blog/r1",
		PipelineAudit{Pipeline: "builtin.brief-rewrite-blog", Outcome: "succeeded", AtUnix: 1000,
			LearnInputs: map[string]string{"persona": "technical"}})

	def, err := BuildDefaults(ctx, store, caller)
	if err != nil {
		t.Fatalf("BuildDefaults: %v", err)
	}
	if len(def.Packs) != 2 {
		t.Fatalf("want 2 packs, got %d", len(def.Packs))
	}
	if def.Packs[0].ID != "blog.rewrite_for_audience" {
		t.Errorf("want blog.rewrite_for_audience ranked first; got %q", def.Packs[0].ID)
	}
	if def.Packs[0].Calls != 3 {
		t.Errorf("want 3 successful calls (failure excluded), got %d", def.Packs[0].Calls)
	}
	if def.Packs[0].CommonInputs["persona"] != "technical" {
		t.Errorf("want persona=technical (2 of 3), got %q", def.Packs[0].CommonInputs["persona"])
	}
	if def.Packs[0].CommonInputs["audience"] != "engineers" {
		t.Errorf("want audience=engineers, got %q", def.Packs[0].CommonInputs["audience"])
	}
	if len(def.Pipelines) != 1 {
		t.Fatalf("want 1 pipeline, got %d", len(def.Pipelines))
	}
	if def.Pipelines[0].ID != "builtin.brief-rewrite-blog" {
		t.Errorf("want brief-rewrite-blog pipeline, got %q", def.Pipelines[0].ID)
	}
}

func TestBuildDefaults_NilStore(t *testing.T) {
	def, err := BuildDefaults(context.Background(), nil, "alice")
	if err != nil {
		t.Fatalf("BuildDefaults(nil): %v", err)
	}
	if len(def.Packs) != 0 || len(def.Pipelines) != 0 {
		t.Errorf("nil store should yield empty projection; got %+v", def)
	}
}

func TestBuildDefaults_EmptyHistory(t *testing.T) {
	store := memory.NewInMemoryStore()
	def, err := BuildDefaults(context.Background(), store, "fresh-user")
	if err != nil {
		t.Fatalf("BuildDefaults: %v", err)
	}
	if len(def.Packs) != 0 || len(def.Pipelines) != 0 {
		t.Errorf("empty history should yield empty projection; got %+v", def)
	}
}

func putAudit(t *testing.T, s memory.MemoryStore, ns, key string, a PackAudit) {
	t.Helper()
	body, _ := json.Marshal(a)
	if _, err := s.Put(context.Background(), ns, key, body, memory.WithCategory(AuditCategoryPack)); err != nil {
		t.Fatal(err)
	}
}

func putPipelineAudit(t *testing.T, s memory.MemoryStore, ns, key string, a PipelineAudit) {
	t.Helper()
	body, _ := json.Marshal(a)
	if _, err := s.Put(context.Background(), ns, key, body, memory.WithCategory(AuditCategoryPipeline)); err != nil {
		t.Fatal(err)
	}
}
