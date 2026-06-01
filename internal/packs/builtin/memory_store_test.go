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

// runMemoryStorePack drives the pack through Engine.Execute so the
// engine-built memoryAdapter handles namespace scoping the same way
// production does. NeedsSession=false → ec.Memory's ns is the bare
// caller, which mirrors the audit-write seam.
func runMemoryStorePack(t *testing.T, caller string, input string) (*packs.Result, error, memory.MemoryStore) {
	t.Helper()
	store := memory.NewInMemoryStore()
	eng := packs.New(packs.WithMemoryStore(store), packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	ctx := packs.WithCaller(context.Background(), caller)
	res, err := eng.Execute(ctx, MemoryStore(), json.RawMessage(input))
	return res, err, store
}

// TestMemoryStorePack_HappyPath proves the pack writes under the bare
// caller namespace with the default category + applied TTL.
func TestMemoryStorePack_HappyPath(t *testing.T) {
	res, err, store := runMemoryStorePack(t, "alice",
		`{"key":"preferences/lang","value":"Go for backend"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Key       string `json:"key"`
		Category  string `json:"category"`
		ExpiresAt string `json:"expires_at"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Key != "preferences/lang" {
		t.Errorf("want key preferences/lang, got %q", out.Key)
	}
	if out.Category != packs.DefaultFactCategory {
		t.Errorf("want default category, got %q", out.Category)
	}
	// Fact landed under alice's bare namespace.
	entries, _ := store.List(context.Background(), "alice", "preferences/")
	if len(entries) != 1 {
		t.Fatalf("want 1 stored row, got %d", len(entries))
	}
	if string(entries[0].Value) != "Go for backend" {
		t.Errorf("value lost in flight: %q", entries[0].Value)
	}
}

// TestMemoryStorePack_ReservedCategoryRejected proves the agent can't
// pollute audit categories.
func TestMemoryStorePack_ReservedCategoryRejected(t *testing.T) {
	for _, cat := range []string{packs.AuditCategoryPack, packs.AuditCategoryPipeline} {
		_, err, _ := runMemoryStorePack(t, "alice",
			`{"key":"x","value":"y","category":"`+cat+`"}`)
		if err == nil {
			t.Errorf("category %q should be rejected", cat)
			continue
		}
		pe, ok := err.(*packs.PackError)
		if !ok || pe.Code != packs.CodeInvalidInput {
			t.Errorf("category %q want CodeInvalidInput, got %v", cat, err)
		}
	}
}

// TestMemoryStorePack_PerCallerIsolated proves a fact written under
// alice's namespace is invisible under bob's — same caller-isolation
// guarantee the audit-write seam carries.
func TestMemoryStorePack_PerCallerIsolated(t *testing.T) {
	res, err, store := runMemoryStorePack(t, "alice",
		`{"key":"prefs/x","value":"alice-only"}`)
	if err != nil {
		t.Fatalf("Execute alice: %v", err)
	}
	_ = res
	// Bob's view of "prefs/" is empty.
	bobEntries, _ := store.List(context.Background(), "bob", "prefs/")
	if len(bobEntries) != 0 {
		t.Errorf("alice's facts leaked to bob's namespace: %+v", bobEntries)
	}
}

// TestMemoryStorePack_NilStoreNoOp proves the pack soft-succeeds with
// a note when no memory store is wired.
func TestMemoryStorePack_NilStoreNoOp(t *testing.T) {
	eng := packs.New(packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	ctx := packs.WithCaller(context.Background(), "alice")
	res, err := eng.Execute(ctx, MemoryStore(), json.RawMessage(`{"key":"x","value":"y"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Note string `json:"note"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Note == "" {
		t.Errorf("nil-store path should surface a note; got: %s", res.Output)
	}
}

// TestMemoryStorePack_NoAudit confirms the meta-tooling exemption —
// writing a fact must NOT write an audit row for helmdeck.memory_store.
func TestMemoryStorePack_NoAudit(t *testing.T) {
	_, err, store := runMemoryStorePack(t, "alice",
		`{"key":"x","value":"y"}`)
	if err != nil {
		t.Fatal(err)
	}
	auditRows, _ := store.List(context.Background(), "alice", packs.AuditKeyPrefixPack)
	for _, e := range auditRows {
		if string(e.Value) != "" && containsPack(string(e.Value), "helmdeck.memory_store") {
			t.Errorf("storing a fact must not generate a memory_store audit row; got %s", e.Value)
		}
	}
}

func containsPack(blob, needle string) bool {
	return blob != "" && (len(blob) >= len(needle)) && (indexOf(blob, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
