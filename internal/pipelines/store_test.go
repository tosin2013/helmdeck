// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tosin2013/helmdeck/internal/store"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

func TestStore_PipelineRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := &Pipeline{ID: "p1", Name: "one", Description: "d", Steps: []Step{
		{ID: "a", Pack: "web.scrape", Input: json.RawMessage(`{"url":"${{inputs.url}}"}`)},
	}}
	if err := s.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "one" || len(got.Steps) != 1 || got.Steps[0].Pack != "web.scrape" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	got.Name = "renamed"
	if err := s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(ctx, "p1")
	if again.Name != "renamed" {
		t.Errorf("update not persisted: %s", again.Name)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Errorf("list len = %d", len(list))
	}
	if err := s.Delete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound after delete, got %v", err)
	}
}

func TestStore_SeedIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := Builtins()[0]
	ok := func(_, _ string) bool { return true }
	if err := s.Seed(ctx, p, ok); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(ctx, p, ok); err != nil { // second seed must not duplicate
		t.Fatal(err)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Errorf("seed should upsert, got %d rows", len(list))
	}
	if !list[0].Builtin {
		t.Error("seeded pipeline should be marked builtin")
	}
	// Seed must reject when a referenced pack is missing.
	if err := s.Seed(ctx, p, func(_, _ string) bool { return false }); err == nil {
		t.Error("seed should fail when packs are unavailable")
	}
}

func TestStore_RunHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r := &Run{ID: "run_x", PipelineID: "p1", Status: RunPending, StartedAt: s.now()}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Status = RunSucceeded
	r.Steps = []RunStep{{StepID: "a", Pack: "web.scrape", Status: RunSucceeded}}
	if err := s.SaveRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, "run_x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RunSucceeded || len(got.Steps) != 1 {
		t.Errorf("run round-trip mismatch: %+v", got)
	}
	runs, _ := s.ListRuns(ctx, "p1", 10)
	if len(runs) != 1 {
		t.Errorf("ListRuns len = %d", len(runs))
	}
}
