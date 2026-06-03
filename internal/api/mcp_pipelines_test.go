// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/pipelines"
	"github.com/tosin2013/helmdeck/internal/store"
)

// mcp_pipelines_test.go covers the pipelineServiceAdapter wrappers
// that bridge internal/mcp's PipelineService interface to the
// pipelines store + runner. These are intentionally thin — each
// method is a 2-5 line delegation — so the tests focus on ensuring
// the delegation happens, errors propagate, and the JSON shape is
// correct. Together they close the seven 0%-coverage functions in
// mcp_pipelines.go.

func newPipelineAdapter(t *testing.T) (pipelineServiceAdapter, *pipelines.Store, *pipelines.Runner, *packs.Registry) {
	t.Helper()
	reg := packs.NewPackRegistry()
	gen := &packs.Pack{
		Name: "gen", Version: "v1",
		Handler: func(_ context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{"text":"out"}`), nil
		},
	}
	if err := reg.Register(gen); err != nil {
		t.Fatal(err)
	}

	eng := packs.New(packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ps := pipelines.NewStore(db)
	pr := pipelines.NewRunner(ps, reg.Get, eng, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	deps := Deps{
		PackRegistry:   reg,
		PipelineStore:  ps,
		PipelineRunner: pr,
	}
	adapter, ok := newPipelineServiceAdapter(deps)
	if !ok {
		t.Fatal("newPipelineServiceAdapter returned !ok despite full Deps")
	}
	return adapter, ps, pr, reg
}

// TestPipelineAdapter_NotWiredWhenStoreNil — guard against the engine
// being constructed without pipeline state. The adapter must return
// ok=false rather than constructing a nil-deref-prone partial.
func TestPipelineAdapter_NotWiredWhenStoreNil(t *testing.T) {
	_, ok := newPipelineServiceAdapter(Deps{}) // empty Deps
	if ok {
		t.Error("adapter must report not-wired when PipelineStore + PipelineRunner are nil")
	}
}

// TestPipelineAdapter_List exercises pipelineServiceAdapter.List —
// returns JSON-marshalled []*Pipeline, with the nil → [] normalization
// the MCP surface relies on so resource handlers don't render `null`.
func TestPipelineAdapter_List(t *testing.T) {
	a, _, _, _ := newPipelineAdapter(t)
	// Empty store → [] not null.
	raw, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("empty list must be `[]`; got %s", raw)
	}
	// After Create, the list has one element.
	def := json.RawMessage(`{"name":"L","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	if _, err := a.Create(context.Background(), def); err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err = a.List(context.Background())
	if err != nil {
		t.Fatalf("List after create: %v", err)
	}
	var list []*pipelines.Pipeline
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "L" {
		t.Errorf("list = %+v; want one pipeline named L", list)
	}
}

// TestPipelineAdapter_Get covers both the happy path and ErrNotFound
// passthrough — the MCP layer maps ErrNotFound to a tool-result error
// so a clean delegation matters.
func TestPipelineAdapter_Get(t *testing.T) {
	a, _, _, _ := newPipelineAdapter(t)
	def := json.RawMessage(`{"name":"G","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	createdRaw, err := a.Create(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	var created pipelines.Pipeline
	_ = json.Unmarshal(createdRaw, &created)

	got, err := a.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var p pipelines.Pipeline
	_ = json.Unmarshal(got, &p)
	if p.Name != "G" {
		t.Errorf("got pipeline name = %q; want G", p.Name)
	}

	if _, err := a.Get(context.Background(), "pipe_nope"); err == nil {
		t.Error("Get on unknown id must return error")
	}
}

// TestPipelineAdapter_Create covers the validate/insert path including
// the auto-generated ID and the Builtin=false enforcement.
func TestPipelineAdapter_Create(t *testing.T) {
	a, _, _, _ := newPipelineAdapter(t)
	def := json.RawMessage(`{"name":"C","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	raw, err := a.Create(context.Background(), def)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	var p pipelines.Pipeline
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.ID == "" {
		t.Error("Create must auto-generate ID")
	}
	if p.Builtin {
		t.Error("Create must force Builtin=false on MCP-created pipelines")
	}

	// Bad JSON → error.
	if _, err := a.Create(context.Background(), json.RawMessage(`{not-json}`)); err == nil {
		t.Error("Create with malformed JSON must error")
	}

	// Missing required field (steps) → Validate error.
	if _, err := a.Create(context.Background(), json.RawMessage(`{"name":"bad"}`)); err == nil {
		t.Error("Create with no steps must error via Validate")
	}
}

// TestPipelineAdapter_StartRun_And_RunStatus covers the runner-side
// delegations. Includes a small poll loop for the run to reach
// terminal, mirroring the REST-handler tests.
func TestPipelineAdapter_StartRun_And_RunStatus(t *testing.T) {
	a, _, _, _ := newPipelineAdapter(t)
	def := json.RawMessage(`{"name":"R","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	createdRaw, _ := a.Create(context.Background(), def)
	var created pipelines.Pipeline
	_ = json.Unmarshal(createdRaw, &created)

	runID, coalesced, err := a.StartRun(context.Background(), created.ID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if runID == "" {
		t.Error("StartRun must return a run_id")
	}
	if coalesced {
		t.Error("First StartRun call must not coalesce")
	}

	// Poll RunStatus until terminal.
	for i := 0; i < 200; i++ {
		raw, err := a.RunStatus(context.Background(), runID)
		if err != nil {
			t.Fatalf("RunStatus: %v", err)
		}
		var run pipelines.Run
		_ = json.Unmarshal(raw, &run)
		if run.Status.IsTerminal() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("StartRun did not reach terminal")
}

// TestPipelineAdapter_Rerun_And_Cancel — Rerun on a terminal run
// returns a fresh run_id. Cancel on a terminal run returns an error.
func TestPipelineAdapter_Rerun_And_Cancel(t *testing.T) {
	a, _, _, _ := newPipelineAdapter(t)
	def := json.RawMessage(`{"name":"Z","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	createdRaw, _ := a.Create(context.Background(), def)
	var created pipelines.Pipeline
	_ = json.Unmarshal(createdRaw, &created)

	runID, _, _ := a.StartRun(context.Background(), created.ID, json.RawMessage(`{}`))
	// Poll to terminal.
	for i := 0; i < 200; i++ {
		raw, _ := a.RunStatus(context.Background(), runID)
		var run pipelines.Run
		_ = json.Unmarshal(raw, &run)
		if run.Status.IsTerminal() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Rerun → new run id.
	rerunID, _, err := a.Rerun(context.Background(), runID)
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if rerunID == "" || rerunID == runID {
		t.Errorf("Rerun must return a NEW run id; got %q (original %q)", rerunID, runID)
	}

	// Cancel on a terminal source run errors.
	if err := a.Cancel(context.Background(), runID); err == nil {
		t.Error("Cancel on terminal run must error (already-terminal guard)")
	}
}

// TestMCPCaller covers the no-auth path of the context extractor —
// returns "" when no Claims were attached. The auth-attached path is
// exercised indirectly through the MCP middleware in mcp_sse_test.go;
// stamping a Claims directly here would require exporting the
// internal/auth contextKey (intentionally unexported per ADR 028).
func TestMCPCaller(t *testing.T) {
	if got := mcpCaller(context.Background()); got != "" {
		t.Errorf("no-auth caller = %q; want \"\"", got)
	}
}
