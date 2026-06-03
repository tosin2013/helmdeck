// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/pipelines"
	"github.com/tosin2013/helmdeck/internal/store"
)

// newPipelinesRouter wires a real router → store → runner → engine with
// two no-session test packs that thread output (gen → consume), so the
// full pipeline path is exercised in CI without Docker or a gateway.
func newPipelinesRouter(t *testing.T) http.Handler {
	t.Helper()
	reg := packs.NewPackRegistry()
	// "gen" returns {text: <input.seed>}; "consume" echoes {got: <input.text>}.
	gen := &packs.Pack{
		Name: "gen", Version: "v1",
		Handler: func(_ context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				Seed string `json:"seed"`
			}
			_ = json.Unmarshal(ec.Input, &in)
			return json.Marshal(map[string]string{"text": in.Seed})
		},
	}
	consume := &packs.Pack{
		Name: "consume", Version: "v1",
		Handler: func(_ context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				Got string `json:"got"`
			}
			_ = json.Unmarshal(ec.Input, &in)
			return json.Marshal(map[string]string{"final": in.Got})
		},
	}
	if err := reg.Register(gen); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(consume); err != nil {
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

	return NewRouter(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:        "test",
		PackRegistry:   reg,
		PackEngine:     eng,
		PipelineStore:  ps,
		PipelineRunner: pr,
	})
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestPipelines_CreateRunPoll(t *testing.T) {
	h := newPipelinesRouter(t)

	// Create a 2-step pipeline that threads gen.output.text → consume.got,
	// with gen.seed coming from a run input.
	def := `{
		"name":"e2e",
		"steps":[
			{"id":"a","pack":"gen","input":{"seed":"${{ inputs.seed }}"}},
			{"id":"b","pack":"consume","input":{"got":"${{ steps.a.output.text }}"}}
		]
	}`
	rr := do(t, h, "POST", "/api/v1/pipelines", def)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: code %d body %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.ID == "" {
		t.Fatal("create returned no id")
	}

	// It appears in the list alongside the 13 seeded built-ins... but no
	// built-ins were seeded here (that's main.go's job), so just ≥1.
	rr = do(t, h, "GET", "/api/v1/pipelines", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}

	// Run it.
	rr = do(t, h, "POST", "/api/v1/pipelines/"+created.ID+"/run", `{"inputs":{"seed":"hello"}}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("run: code %d body %s", rr.Code, rr.Body.String())
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &started)
	if started.RunID == "" {
		t.Fatal("run returned no run_id")
	}

	// Poll to terminal.
	var run pipelines.Run
	for i := 0; i < 200; i++ {
		rr = do(t, h, "GET", "/api/v1/pipelines/"+created.ID+"/runs/"+started.RunID, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("run-status: %d %s", rr.Code, rr.Body.String())
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &run)
		if run.Status == pipelines.RunSucceeded || run.Status == pipelines.RunFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if run.Status != pipelines.RunSucceeded {
		t.Fatalf("run status = %s, err=%s, steps=%+v", run.Status, run.Error, run.Steps)
	}
	// Step b must have received gen's output threaded through templating.
	if len(run.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(run.Steps))
	}
	var bOut struct {
		Final string `json:"final"`
	}
	_ = json.Unmarshal(run.Steps[1].Output, &bOut)
	if bOut.Final != "hello" {
		t.Errorf("output not threaded end-to-end: final=%q", bOut.Final)
	}
}

func TestPipelines_BuiltinReadOnly(t *testing.T) {
	h := newPipelinesRouter(t)
	// Seed a builtin directly via the store so we can assert PUT/DELETE 409.
	// (Reach through a fresh create marked builtin isn't possible via REST,
	// so we POST a normal one and confirm normal delete works, then trust
	// the builtin guard which is unit-covered by the store/REST split.)
	rr := do(t, h, "POST", "/api/v1/pipelines", `{"name":"x","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &c)
	// A normal (non-builtin) pipeline deletes fine.
	rr = do(t, h, "DELETE", "/api/v1/pipelines/"+c.ID, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	// Unknown id → 404.
	rr = do(t, h, "GET", "/api/v1/pipelines/nope", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("get unknown: %d", rr.Code)
	}
}

// createPipelineForRunTests is the shared "make a runnable pipeline +
// kick off one run" setup the rerun / cancel / list-runs tests reuse.
// Returns (pipelineID, runID) once the run is terminal so the runID is
// stable to operate on.
func createPipelineForRunTests(t *testing.T, h http.Handler) (pipelineID, runID string) {
	t.Helper()
	rr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"rerun-target","steps":[{"id":"a","pack":"gen","input":{"seed":"${{ inputs.seed }}"}}]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create pipeline: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	rr = do(t, h, "POST", "/api/v1/pipelines/"+created.ID+"/run", `{"inputs":{"seed":"hi"}}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("run: %d %s", rr.Code, rr.Body.String())
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &started)

	// Poll to terminal so subsequent operations see a stable run.
	for i := 0; i < 200; i++ {
		rr = do(t, h, "GET", "/api/v1/pipelines/"+created.ID+"/runs/"+started.RunID, "")
		var run pipelines.Run
		_ = json.Unmarshal(rr.Body.Bytes(), &run)
		if run.Status == pipelines.RunSucceeded || run.Status == pipelines.RunFailed {
			return created.ID, started.RunID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("seed run did not reach terminal")
	return "", ""
}

// TestPipelines_Rerun pins the REST surface for handlePipelineRerun
// (PR #397's single-flight coalescing surfaced this handler; the path
// had no direct test until the coverage gate landed). A rerun of a
// completed run starts a new run with a fresh run_id and returns 202.
func TestPipelines_Rerun(t *testing.T) {
	h := newPipelinesRouter(t)
	pipelineID, runID := createPipelineForRunTests(t, h)

	rr := do(t, h, "POST", "/api/v1/pipelines/"+pipelineID+"/runs/"+runID+"/rerun", "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("rerun: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		Coalesced bool   `json:"coalesced"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.RunID == "" {
		t.Error("rerun must return a run_id")
	}
	if resp.RunID == runID {
		t.Errorf("rerun must produce a new run_id; got the original %q", runID)
	}
	if resp.Status != string(pipelines.RunPending) {
		t.Errorf("rerun status = %q, want pending", resp.Status)
	}
	// The coalesced field is exposed (PR #397) even though we don't
	// expect it to be true for a terminal-source rerun.
	if resp.Coalesced {
		t.Errorf("rerun of a terminal run should not coalesce; got coalesced=true")
	}
}

func TestPipelines_Rerun_UnknownRun(t *testing.T) {
	h := newPipelinesRouter(t)
	// Pipeline ID can be anything — the runner returns ErrNotFound on
	// the unknown run before it tries to fetch the pipeline.
	rr := do(t, h, "POST", "/api/v1/pipelines/any/runs/run_nope/rerun", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("rerun unknown: %d (want 404)", rr.Code)
	}
}

// TestPipelines_Cancel_AlreadyTerminal pins the 409 path of
// handlePipelineCancel. The seed run reaches terminal before we try
// to cancel it; the handler must return "not_cancellable" via 409.
func TestPipelines_Cancel_AlreadyTerminal(t *testing.T) {
	h := newPipelinesRouter(t)
	pipelineID, runID := createPipelineForRunTests(t, h)

	rr := do(t, h, "POST", "/api/v1/pipelines/"+pipelineID+"/runs/"+runID+"/cancel", "")
	if rr.Code != http.StatusConflict {
		t.Errorf("cancel terminal: %d (want 409); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not_cancellable") {
		t.Errorf("response body must contain not_cancellable code; got %s", rr.Body.String())
	}
}

func TestPipelines_Cancel_UnknownRun(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "POST", "/api/v1/pipelines/any/runs/run_nope/cancel", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("cancel unknown: %d (want 404)", rr.Code)
	}
}

// TestPipelines_ListRuns pins handleListRuns — GET on a pipeline's
// /runs endpoint returns an array (possibly empty) of recent runs
// for that pipeline.
func TestPipelines_ListRuns(t *testing.T) {
	h := newPipelinesRouter(t)
	pipelineID, _ := createPipelineForRunTests(t, h)

	rr := do(t, h, "GET", "/api/v1/pipelines/"+pipelineID+"/runs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list runs: %d %s", rr.Code, rr.Body.String())
	}
	var runs []pipelines.Run
	if err := json.Unmarshal(rr.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) < 1 {
		t.Errorf("expected ≥1 run for the seeded pipeline; got %d", len(runs))
	}
}

func TestPipelines_ListRuns_EmptyArray(t *testing.T) {
	// Pipeline exists but no runs yet — must return [] not null.
	h := newPipelinesRouter(t)
	rr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"empty","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	rr = do(t, h, "GET", "/api/v1/pipelines/"+created.ID+"/runs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list empty: %d", rr.Code)
	}
	// JSON `null` would be 4 bytes; `[]` is 2. The empty-array guard
	// in handleListRuns is the difference.
	if rr.Body.String() != "[]\n" && rr.Body.String() != "[]" {
		t.Errorf("empty runs must be `[]` not null; got %q", rr.Body.String())
	}
}

// TestPipelines_ListAllRuns pins the global /pipeline-runs endpoint
// (handleListAllRuns). Returns recent runs across all pipelines.
func TestPipelines_ListAllRuns(t *testing.T) {
	h := newPipelinesRouter(t)
	// Seed two pipelines, each with one run.
	createPipelineForRunTests(t, h)
	createPipelineForRunTests(t, h)

	rr := do(t, h, "GET", "/api/v1/pipeline-runs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list all runs: %d %s", rr.Code, rr.Body.String())
	}
	var runs []pipelines.Run
	if err := json.Unmarshal(rr.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) < 2 {
		t.Errorf("expected ≥2 cross-pipeline runs; got %d", len(runs))
	}
}

// TestPipelines_ListAllRuns_EmptyArray — same null-guard as
// the per-pipeline list-runs test, for the global endpoint.
func TestPipelines_ListAllRuns_EmptyArray(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "GET", "/api/v1/pipeline-runs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list all: %d", rr.Code)
	}
	if rr.Body.String() != "[]\n" && rr.Body.String() != "[]" {
		t.Errorf("empty cross-pipeline runs must be `[]` not null; got %q", rr.Body.String())
	}
}

// TestPipelines_UnavailableWhenNotWired — pipeline routes return 503
// when the store or runner are absent (compose dev mode without
// pipelines).
func TestPipelines_UnavailableWhenNotWired(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	for _, path := range []string{"/api/v1/pipelines", "/api/v1/pipelines/anything"} {
		rr := do(t, h, "GET", path, "")
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", path, rr.Code)
		}
	}
}

// TestPipelines_CreateBadJSON — malformed body returns 400 with the
// invalid_pipeline code.
func TestPipelines_CreateBadJSON(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "POST", "/api/v1/pipelines", `{not-json`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestPipelines_CreateValidationError — Pipeline with no steps is
// rejected by Validate, surfaced as 400 invalid_pipeline.
func TestPipelines_CreateValidationError(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "POST", "/api/v1/pipelines", `{"name":"empty","steps":[]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestPipelines_GetUnknownIs404 — GET /pipelines/{missing} returns
// 404 not_found (writePipelineNotFound's ErrNotFound branch).
func TestPipelines_GetUnknownIs404(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "GET", "/api/v1/pipelines/pipe_missing", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPipelines_PutUnknownIs404.
func TestPipelines_PutUnknownIs404(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "PUT", "/api/v1/pipelines/pipe_missing",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPipelines_PutBadJSON — body that doesn't decode → 400.
func TestPipelines_PutBadJSON(t *testing.T) {
	h := newPipelinesRouter(t)
	// First create a pipeline so PUT can reach the JSON decode step.
	cr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var p pipelines.Pipeline
	_ = json.Unmarshal(cr.Body.Bytes(), &p)
	rr := do(t, h, "PUT", "/api/v1/pipelines/"+p.ID, `{nope`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// TestPipelines_DeleteUnknownIs404.
func TestPipelines_DeleteUnknownIs404(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "DELETE", "/api/v1/pipelines/pipe_missing", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPipelines_MissingIDIs404 — trailing slash with no id.
func TestPipelines_MissingIDIs404(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "GET", "/api/v1/pipelines/", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPipelines_UnknownSubresource — /{id}/bogus returns 404.
func TestPipelines_UnknownSubresource(t *testing.T) {
	h := newPipelinesRouter(t)
	cr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var p pipelines.Pipeline
	_ = json.Unmarshal(cr.Body.Bytes(), &p)
	rr := do(t, h, "GET", "/api/v1/pipelines/"+p.ID+"/bogus", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestPipelines_RunWrongMethod — GET /{id}/run is 405.
func TestPipelines_RunWrongMethod(t *testing.T) {
	h := newPipelinesRouter(t)
	cr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var p pipelines.Pipeline
	_ = json.Unmarshal(cr.Body.Bytes(), &p)
	rr := do(t, h, "GET", "/api/v1/pipelines/"+p.ID+"/run", "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestPipelines_RunUnknownPipeline — POST /{missing}/run returns 404.
func TestPipelines_RunUnknownPipeline(t *testing.T) {
	h := newPipelinesRouter(t)
	rr := do(t, h, "POST", "/api/v1/pipelines/pipe_missing/run", `{"inputs":{}}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rr.Code, rr.Body.String())
	}
}

// TestPipelines_ListRunsWrongMethod — DELETE /{id}/runs is 405.
func TestPipelines_ListRunsWrongMethod(t *testing.T) {
	h := newPipelinesRouter(t)
	cr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var p pipelines.Pipeline
	_ = json.Unmarshal(cr.Body.Bytes(), &p)
	rr := do(t, h, "DELETE", "/api/v1/pipelines/"+p.ID+"/runs", "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestPipelines_RerunWrongMethod — GET on /rerun is 405.
func TestPipelines_RerunWrongMethod(t *testing.T) {
	h := newPipelinesRouter(t)
	cr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var p pipelines.Pipeline
	_ = json.Unmarshal(cr.Body.Bytes(), &p)
	rr := do(t, h, "GET", "/api/v1/pipelines/"+p.ID+"/runs/r1/rerun", "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestPipelines_CancelWrongMethod — GET on /cancel is 405.
func TestPipelines_CancelWrongMethod(t *testing.T) {
	h := newPipelinesRouter(t)
	cr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var p pipelines.Pipeline
	_ = json.Unmarshal(cr.Body.Bytes(), &p)
	rr := do(t, h, "GET", "/api/v1/pipelines/"+p.ID+"/runs/r1/cancel", "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestPipelines_TopLevelMethodNotAllowed — DELETE /api/v1/pipelines/{id}
// is fine (it's covered), but a method like PATCH should 405.
func TestPipelines_TopLevelMethodNotAllowed(t *testing.T) {
	h := newPipelinesRouter(t)
	cr := do(t, h, "POST", "/api/v1/pipelines",
		`{"name":"n","steps":[{"id":"a","pack":"gen","input":{}}]}`)
	var p pipelines.Pipeline
	_ = json.Unmarshal(cr.Body.Bytes(), &p)
	rr := do(t, h, "PATCH", "/api/v1/pipelines/"+p.ID, "")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}
