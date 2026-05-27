package packs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
)

func quietEngine(opts ...Option) *Engine {
	base := []Option{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	return New(append(base, opts...)...)
}

// countingPack returns a pack whose handler increments *calls each
// time it runs and echoes a fixed output. Used to prove the cache seam
// skips the handler on a hit.
func countingPack(calls *int, mc *MemoryConfig) *Pack {
	return &Pack{
		Name:    "count.pack",
		Version: "v1",
		Memory:  mc,
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			*calls++
			return json.RawMessage(`{"n":1}`), nil
		},
	}
}

// TestMemoryDefaultOff proves that with NO MemoryStore wired,
// ec.Memory is nil and a normal pack runs unchanged.
func TestMemoryDefaultOff(t *testing.T) {
	var sawMemory bool
	pack := &Pack{
		Name: "probe", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			if ec.Memory != nil {
				sawMemory = true
			}
			return json.RawMessage(`{}`), nil
		},
	}
	res, err := quietEngine().Execute(context.Background(), pack, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sawMemory {
		t.Fatal("ec.Memory should be nil when no store is wired")
	}
	if string(res.Output) != `{}` {
		t.Fatalf("unexpected output: %s", res.Output)
	}
}

// TestMemoryWiredButPackNotOptedIn proves the cache seam is inert for a
// pack that doesn't set Memory, even when a store IS wired — the
// handler runs every time.
func TestMemoryWiredButPackNotOptedIn(t *testing.T) {
	calls := 0
	pack := countingPack(&calls, nil) // nil MemoryConfig
	eng := quietEngine(WithMemoryStore(memory.NewInMemoryStore()))
	for i := 0; i < 3; i++ {
		if _, err := eng.Execute(context.Background(), pack, json.RawMessage(`{"x":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("handler should run every time without opt-in; ran %d times", calls)
	}
}

// TestMemoryCacheHitSkipsHandler proves a 2nd identical call within TTL
// is served from memory and does NOT re-invoke the handler.
func TestMemoryCacheHitSkipsHandler(t *testing.T) {
	calls := 0
	pack := countingPack(&calls, &MemoryConfig{Cache: true, TTL: time.Hour, Category: "cache"})
	eng := quietEngine(WithMemoryStore(memory.NewInMemoryStore()))
	ctx := WithCaller(context.Background(), "tester")

	r1, err := eng.Execute(ctx, pack, json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := eng.Execute(ctx, pack, json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected handler to run once (2nd served from cache), ran %d", calls)
	}
	if string(r1.Output) != string(r2.Output) {
		t.Fatalf("cached output differs: %s vs %s", r1.Output, r2.Output)
	}
}

// TestMemoryCacheMissOnDifferentInput proves the cache keys on the
// exact input bytes — a different input is a miss.
func TestMemoryCacheMissOnDifferentInput(t *testing.T) {
	calls := 0
	pack := countingPack(&calls, &MemoryConfig{Cache: true, TTL: time.Hour})
	eng := quietEngine(WithMemoryStore(memory.NewInMemoryStore()))
	ctx := WithCaller(context.Background(), "tester")

	_, _ = eng.Execute(ctx, pack, json.RawMessage(`{"x":1}`))
	_, _ = eng.Execute(ctx, pack, json.RawMessage(`{"x":2}`))
	if calls != 2 {
		t.Fatalf("expected 2 handler runs for 2 distinct inputs, got %d", calls)
	}
}

// TestMemoryCacheNamespaceIsolation proves a cached entry written under
// one caller's namespace is not served to another caller.
func TestMemoryCacheNamespaceIsolation(t *testing.T) {
	calls := 0
	pack := countingPack(&calls, &MemoryConfig{Cache: true, TTL: time.Hour})
	eng := quietEngine(WithMemoryStore(memory.NewInMemoryStore()))

	_, _ = eng.Execute(WithCaller(context.Background(), "alice"), pack, json.RawMessage(`{"x":1}`))
	_, _ = eng.Execute(WithCaller(context.Background(), "bob"), pack, json.RawMessage(`{"x":1}`))
	if calls != 2 {
		t.Fatalf("different callers must not share cache; expected 2 runs, got %d", calls)
	}
}

// TestMemoryContextAggregation proves Context() returns recent entries
// grouped by category for the caller's namespace.
func TestMemoryContextAggregation(t *testing.T) {
	store := memory.NewInMemoryStore()
	var captured *SessionContext
	pack := &Pack{
		Name: "ctx.probe", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			_ = ec.Memory.Store("solve/a", []byte("n1"), memory.WithCategory("solve"))
			_ = ec.Memory.Store("cache/b", []byte("n2"), memory.WithCategory("cache"))
			sc, err := ec.Memory.Context()
			if err != nil {
				return nil, err
			}
			captured = sc
			return json.RawMessage(`{}`), nil
		},
	}
	eng := quietEngine(WithMemoryStore(store))
	if _, err := eng.Execute(WithCaller(context.Background(), "carol"), pack, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("Context() not captured")
	}
	if captured.Namespace != "carol" {
		t.Fatalf("namespace = %q, want carol", captured.Namespace)
	}
	if len(captured.Entries["solve"]) != 1 || len(captured.Entries["cache"]) != 1 {
		t.Fatalf("expected one entry per category, got %+v", captured.Entries)
	}
}

// TestCallerFromContextDefault proves the namespace defaults to
// "unknown" when no caller is attached.
func TestCallerFromContextDefault(t *testing.T) {
	if got := callerFromContext(context.Background()); got != "unknown" {
		t.Fatalf("default caller = %q, want unknown", got)
	}
	if got := callerFromContext(WithCaller(context.Background(), "")); got != "unknown" {
		t.Fatalf("empty subject should map to unknown, got %q", got)
	}
	if got := callerFromContext(WithCaller(context.Background(), "x")); got != "x" {
		t.Fatalf("caller = %q, want x", got)
	}
}
