package builtin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// runForgetWithCallerAudit seeds the engine's bare-caller namespace
// with a few audit-shaped entries (the same shape packs.writePackAudit
// produces) and returns the engine + memory store so a test can call
// MemoryForget through Engine.Execute and verify the deletions.
func runForgetWithCallerAudit(t *testing.T, caller string) (*packs.Engine, memory.MemoryStore) {
	t.Helper()
	store := memory.NewInMemoryStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := packs.New(packs.WithMemoryStore(store), packs.WithLogger(logger))
	// Seed two pack-history + one pipeline-history entries.
	mustPut(t, store, caller, packs.AuditKeyPrefixPack+"blog.publish/1",
		`{"pack":"blog.publish","outcome":"ok","at_unix":1}`, packs.AuditCategoryPack)
	mustPut(t, store, caller, packs.AuditKeyPrefixPack+"blog.publish/2",
		`{"pack":"blog.publish","outcome":"ok","at_unix":2}`, packs.AuditCategoryPack)
	mustPut(t, store, caller, packs.AuditKeyPrefixPipeline+"builtin.grounded-blog/r1",
		`{"pipeline":"builtin.grounded-blog","outcome":"succeeded","at_unix":3}`, packs.AuditCategoryPipeline)
	// One cache-shaped entry under a different prefix proves forget
	// doesn't touch non-audit rows.
	mustPut(t, store, caller, "content.ground/abc123",
		`{"cached":true}`, "cache")
	return eng, store
}

func mustPut(t *testing.T, s memory.MemoryStore, ns, key, val, cat string) {
	t.Helper()
	if _, err := s.Put(context.Background(), ns, key, []byte(val), memory.WithCategory(cat)); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryForget_All_DropsAuditButKeepsCache(t *testing.T) {
	eng, store := runForgetWithCallerAudit(t, "alice")
	ctx := packs.WithCaller(context.Background(), "alice")
	pack := MemoryForget()

	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"scope":"all"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Scope   string `json:"scope"`
		Deleted int    `json:"deleted"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.Scope != "all" || out.Deleted != 3 {
		t.Errorf("want scope=all deleted=3, got scope=%q deleted=%d", out.Scope, out.Deleted)
	}
	// Cache row survives.
	if _, err := store.Get(context.Background(), "alice", "content.ground/abc123"); err != nil {
		t.Errorf("non-audit cache row should survive forget; got %v", err)
	}
	// Pack audit prefix is empty.
	left, _ := store.List(context.Background(), "alice", packs.AuditKeyPrefixPack)
	if len(left) != 0 {
		t.Errorf("pack audits should be empty post-forget; got %d", len(left))
	}
}

func TestMemoryForget_Scoped_ByPackID(t *testing.T) {
	eng, store := runForgetWithCallerAudit(t, "alice")
	ctx := packs.WithCaller(context.Background(), "alice")
	pack := MemoryForget()

	res, err := eng.Execute(ctx, pack, json.RawMessage(`{"scope":"pack:blog.publish"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Deleted != 2 {
		t.Errorf("want 2 blog.publish audits deleted; got %d", out.Deleted)
	}
	// Pipeline audit survives.
	left, _ := store.List(context.Background(), "alice", packs.AuditKeyPrefixPipeline)
	if len(left) != 1 {
		t.Errorf("pipeline audits should survive scoped pack-forget; got %d", len(left))
	}
}

func TestMemoryForget_BadScopeReturnsInvalidInput(t *testing.T) {
	eng, _ := runForgetWithCallerAudit(t, "alice")
	ctx := packs.WithCaller(context.Background(), "alice")
	pack := MemoryForget()
	_, err := eng.Execute(ctx, pack, json.RawMessage(`{"scope":"banana"}`))
	if err == nil {
		t.Fatal("expected invalid-input error")
	}
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want CodeInvalidInput, got %v", err)
	}
}

func TestMemoryForget_NoMemoryStoreReturnsNoop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := packs.New(packs.WithLogger(logger))
	ctx := packs.WithCaller(context.Background(), "alice")
	pack := MemoryForget()
	res, err := eng.Execute(ctx, pack, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !json.Valid(res.Output) {
		t.Fatalf("invalid output: %s", res.Output)
	}
}
