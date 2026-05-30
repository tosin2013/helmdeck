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

// TestStore_ListAllRuns covers the cross-pipeline query behind the Management
// UI's "which pipelines are running" poll: it must return runs spanning every
// pipeline, which the per-pipeline ListRuns can't.
func TestStore_ListAllRuns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, rc := range []struct {
		id, pid string
		st      RunStatus
	}{
		{"run_a", "p1", RunRunning},
		{"run_b", "p2", RunSucceeded},
	} {
		if err := s.CreateRun(ctx, &Run{ID: rc.id, PipelineID: rc.pid, Status: rc.st, StartedAt: s.now()}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.ListAllRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("ListAllRuns len = %d, want 2 (spans pipelines)", len(runs))
	}
	pids := map[string]bool{}
	for _, r := range runs {
		pids[r.PipelineID] = true
	}
	if !pids["p1"] || !pids["p2"] {
		t.Errorf("ListAllRuns should span pipelines, got %v", pids)
	}
}

// TestStore_PruneStaleBuiltins — builtin rows whose ID isn't in the
// current set are reaped on boot, but user-created pipelines (built-
// in=false) survive even if their ID happens to start with "builtin.".
func TestStore_PruneStaleBuiltins(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ok := func(_, _ string) bool { return true }

	// Seed two builtins. Reuse two real Builtins() entries so the
	// Seed call's pack-validation passes via the always-ok shim.
	keep := Builtins()[0]   // simulates a builtin still in source
	remove := Builtins()[1] // simulates one removed in source
	for _, p := range []*Pipeline{keep, remove} {
		if err := s.Seed(ctx, p, ok); err != nil {
			t.Fatalf("seed %s: %v", p.ID, err)
		}
	}
	// Also create a user pipeline with a builtin-looking ID — must
	// NOT be touched (the guard is the builtin column, not the
	// id prefix).
	user := &Pipeline{ID: "builtin.user-clone", Name: "User clone", Builtin: false, Steps: keep.Steps}
	if err := s.Create(ctx, user); err != nil {
		t.Fatal(err)
	}

	reaped, err := s.PruneStaleBuiltins(ctx, []string{keep.ID})
	if err != nil {
		t.Fatalf("PruneStaleBuiltins: %v", err)
	}
	if reaped != 1 {
		t.Errorf("reaped = %d, want 1 (just %s)", reaped, remove.ID)
	}
	list, _ := s.List(ctx)
	gotIDs := map[string]bool{}
	for _, p := range list {
		gotIDs[p.ID] = true
	}
	if !gotIDs[keep.ID] {
		t.Errorf("keep id %s missing after prune", keep.ID)
	}
	if gotIDs[remove.ID] {
		t.Errorf("stale builtin %s still present after prune", remove.ID)
	}
	if !gotIDs["builtin.user-clone"] {
		t.Error("user-created pipeline with builtin-looking id was wrongly reaped")
	}

	// Second pass: nothing left to reap.
	reaped, err = s.PruneStaleBuiltins(ctx, []string{keep.ID})
	if err != nil {
		t.Fatalf("PruneStaleBuiltins pass 2: %v", err)
	}
	if reaped != 0 {
		t.Errorf("second pass reaped %d, want 0 (idempotent)", reaped)
	}
}
