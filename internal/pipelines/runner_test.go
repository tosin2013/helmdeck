// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// recordingExec is a fake Executor: it records each (pack, input) and
// returns scripted outputs/session IDs per pack name.
type recordingExec struct {
	calls   []execCall
	outputs map[string]string // pack name -> output JSON
	session map[string]string // pack name -> SessionID to return
	failOn  string            // pack name that should error
}

type execCall struct {
	pack  string
	input json.RawMessage
}

func (e *recordingExec) Execute(_ context.Context, p *packs.Pack, in json.RawMessage) (*packs.Result, error) {
	e.calls = append(e.calls, execCall{pack: p.Name, input: in})
	if e.failOn == p.Name {
		return nil, errors.New("boom")
	}
	out := e.outputs[p.Name]
	if out == "" {
		out = "{}"
	}
	return &packs.Result{Output: json.RawMessage(out), SessionID: e.session[p.Name]}, nil
}

func resolverFor(_ ...string) PackResolver {
	return func(name, _ string) (*packs.Pack, error) {
		p := &packs.Pack{Name: name}
		// Mirror the real registry: repo.fetch preserves its session so
		// follow-on packs (repo.map, fs.*, git.*, repo.push) reuse it. The
		// runner only threads a session forward from a preserving pack.
		if name == "repo.fetch" {
			p.PreserveSession = true
		}
		return p, nil
	}
}

func newTestRunner(t *testing.T, ex Executor) *Runner {
	t.Helper()
	return NewRunner(testStore(t), resolverFor(), ex, nil)
}

func TestRunner_ThreadsOutputForward(t *testing.T) {
	ex := &recordingExec{outputs: map[string]string{
		"research.deep": `{"synthesis":"# Deck"}`,
		"slides.render": `{"artifact_key":"k1"}`,
	}}
	r := newTestRunner(t, ex)
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "research", Pack: "research.deep", Input: json.RawMessage(`{"query":"${{inputs.q}}"}`)},
		{ID: "render", Pack: "slides.render", Input: json.RawMessage(`{"markdown":"${{steps.research.output.synthesis}}"}`)},
	}}
	run := &Run{ID: "run1", PipelineID: "p", StartedAt: r.now()}
	_ = r.store.CreateRun(context.Background(), run)
	if err := r.RunSync(context.Background(), p, json.RawMessage(`{"q":"k8s"}`), run); err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("status = %s (err=%s)", run.Status, run.Error)
	}
	// Step 1 received the resolved input query.
	var in0 map[string]any
	_ = json.Unmarshal(ex.calls[0].input, &in0)
	if in0["query"] != "k8s" {
		t.Errorf("step1 query = %v", in0["query"])
	}
	// Step 2 received step1's output threaded in.
	var in1 map[string]any
	_ = json.Unmarshal(ex.calls[1].input, &in1)
	if in1["markdown"] != "# Deck" {
		t.Errorf("step2 markdown = %v (output not threaded)", in1["markdown"])
	}
}

func TestRunner_ThreadsSessionID(t *testing.T) {
	ex := &recordingExec{
		outputs: map[string]string{"repo.fetch": `{"readme":{"content":"R"}}`, "slides.narrate": `{}`},
		session: map[string]string{"repo.fetch": "sess-abc"},
	}
	r := newTestRunner(t, ex)
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "fetch", Pack: "repo.fetch", Input: json.RawMessage(`{"url":"${{inputs.repo_url}}"}`)},
		{ID: "narrate", Pack: "slides.narrate", Input: json.RawMessage(`{"markdown":"${{steps.fetch.output.readme.content}}"}`)},
	}}
	run := &Run{ID: "run2", PipelineID: "p", StartedAt: r.now()}
	_ = r.store.CreateRun(context.Background(), run)
	if err := r.RunSync(context.Background(), p, json.RawMessage(`{"repo_url":"u"}`), run); err != nil {
		t.Fatal(err)
	}
	var in1 map[string]any
	_ = json.Unmarshal(ex.calls[1].input, &in1)
	if in1["_session_id"] != "sess-abc" {
		t.Errorf("step2 should inherit _session_id from step1's Result.SessionID, got %v", in1["_session_id"])
	}
	if in1["markdown"] != "R" {
		t.Errorf("nested readme.content not threaded: %v", in1["markdown"])
	}
}

// TestRunner_DoesNotThreadNonPreservedSession is the prompt-narrated-video
// regression: podcast.generate produces a session but does NOT preserve it, so
// its id must not be threaded into a later step — doing so made
// hyperframes.render fail "session_unavailable: session not found" (the session
// was already torn down, and render needs its own hyperframes session anyway).
func TestRunner_DoesNotThreadNonPreservedSession(t *testing.T) {
	ex := &recordingExec{
		outputs: map[string]string{"podcast.generate": `{"audio_url":"a"}`, "hyperframes.render": `{"artifact_key":"k"}`},
		session: map[string]string{"podcast.generate": "dead-sess"}, // produced but not preserved
	}
	r := newTestRunner(t, ex)
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "podcast", Pack: "podcast.generate", Input: json.RawMessage(`{}`)},
		{ID: "render", Pack: "hyperframes.render", Input: json.RawMessage(`{}`)},
	}}
	run := &Run{ID: "run-nopreserve", PipelineID: "p", StartedAt: r.now()}
	_ = r.store.CreateRun(context.Background(), run)
	if err := r.RunSync(context.Background(), p, nil, run); err != nil {
		t.Fatal(err)
	}
	var in1 map[string]any
	_ = json.Unmarshal(ex.calls[1].input, &in1)
	if v, ok := in1["_session_id"]; ok {
		t.Errorf("render must NOT inherit podcast's non-preserved session, got _session_id=%v", v)
	}
}

func TestRunner_FailFast(t *testing.T) {
	ex := &recordingExec{failOn: "research.deep"}
	r := newTestRunner(t, ex)
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "research", Pack: "research.deep", Input: json.RawMessage(`{}`)},
		{ID: "render", Pack: "slides.render", Input: json.RawMessage(`{}`)},
	}}
	run := &Run{ID: "run3", PipelineID: "p", StartedAt: r.now()}
	_ = r.store.CreateRun(context.Background(), run)
	_ = r.RunSync(context.Background(), p, nil, run)
	if run.Status != RunFailed {
		t.Errorf("status = %s, want failed", run.Status)
	}
	if len(ex.calls) != 1 {
		t.Errorf("later step should NOT run after a failure; calls = %d", len(ex.calls))
	}
	if len(run.Steps) != 1 || run.Steps[0].Status != RunFailed {
		t.Errorf("failed step not recorded: %+v", run.Steps)
	}
	// Persisted run reflects the failure.
	got, _ := r.store.GetRun(context.Background(), "run3")
	if got.Status != RunFailed {
		t.Errorf("persisted status = %s", got.Status)
	}
}

func TestRunner_StartRunAsync(t *testing.T) {
	ex := &recordingExec{outputs: map[string]string{"web.scrape": `{"markdown":"M"}`, "slides.render": `{"artifact_key":"k"}`}}
	r := newTestRunner(t, ex)
	ctx := context.Background()
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "scrape", Pack: "web.scrape", Input: json.RawMessage(`{"url":"${{inputs.url}}"}`)},
		{ID: "render", Pack: "slides.render", Input: json.RawMessage(`{"markdown":"${{steps.scrape.output.markdown}}"}`)},
	}}
	if err := r.store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	runID, err := r.StartRun(ctx, "p", json.RawMessage(`{"url":"u"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	// Poll the run to terminal (the goroutine runs against a detached ctx).
	var got *Run
	for i := 0; i < 200; i++ {
		got, _ = r.GetRun(ctx, runID)
		if got != nil && (got.Status == RunSucceeded || got.Status == RunFailed) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got == nil || got.Status != RunSucceeded {
		t.Fatalf("async run did not succeed: %+v", got)
	}
}

// failingExec returns a typed *packs.PackError for a named pack, so we
// can assert the runner classifies and records the failure attribution.
type failingExec struct {
	failOn string
	err    *packs.PackError
}

func (e *failingExec) Execute(_ context.Context, p *packs.Pack, _ json.RawMessage) (*packs.Result, error) {
	if p.Name == e.failOn {
		return nil, e.err
	}
	return &packs.Result{Output: json.RawMessage(`{}`)}, nil
}

func TestRunner_RecordsFailureAttribution(t *testing.T) {
	ex := &failingExec{failOn: "slides.render", err: &packs.PackError{Code: packs.CodeHandlerFailed, Message: "marp blew up"}}
	r := newTestRunner(t, ex)
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "render", Pack: "slides.render", Input: json.RawMessage(`{"markdown":"# x"}`)},
	}}
	run := &Run{ID: "run-attr", PipelineID: "p", StartedAt: r.now()}
	if err := r.store.CreateRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := r.RunSync(context.Background(), p, nil, run); err != nil {
		t.Fatal(err)
	}
	if run.Status != RunFailed {
		t.Fatalf("status = %s", run.Status)
	}
	step := run.Steps[0]
	if step.ErrorCode != packs.CodeHandlerFailed {
		t.Errorf("step ErrorCode = %q, want handler_failed", step.ErrorCode)
	}
	if step.FailureClass != FailurePackBug {
		t.Errorf("step FailureClass = %q, want pack_bug", step.FailureClass)
	}
	if run.FailureClass != FailurePackBug {
		t.Errorf("run FailureClass = %q, want pack_bug (run-level mirror)", run.FailureClass)
	}
	if !strings.Contains(run.FailureReason, "issues/new") {
		t.Errorf("pack_bug run reason should link an issue; got %q", run.FailureReason)
	}
}

func TestRunner_Rerun(t *testing.T) {
	ex := &recordingExec{outputs: map[string]string{"a.pack": `{}`}}
	r := newTestRunner(t, ex)
	p := &Pipeline{ID: "p", Name: "n", Builtin: true, Steps: []Step{
		{ID: "s1", Pack: "a.pack", Input: json.RawMessage(`{"k":"${{ inputs.v }}"}`)},
	}}
	if err := r.store.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	// First run.
	run := &Run{ID: "run-1", PipelineID: "p", Inputs: json.RawMessage(`{"v":"hello"}`), StartedAt: r.now()}
	_ = r.store.CreateRun(context.Background(), run)
	if err := r.RunSync(context.Background(), p, run.Inputs, run); err != nil {
		t.Fatal(err)
	}
	// Re-run it → new run id, same pipeline + inputs replayed.
	newID, err := r.Rerun(context.Background(), "run-1", "")
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if newID == "" || newID == "run-1" {
		t.Fatalf("Rerun should return a fresh run id, got %q", newID)
	}
	got, err := r.GetRun(context.Background(), newID)
	if err != nil {
		t.Fatalf("GetRun(new): %v", err)
	}
	if string(got.Inputs) != `{"v":"hello"}` {
		t.Errorf("rerun inputs = %s, want the original inputs", got.Inputs)
	}
	if got.PipelineID != "p" {
		t.Errorf("rerun pipeline = %s, want p", got.PipelineID)
	}
}

type callerCapturingExec struct {
	mu     sync.Mutex
	caller string
}

func (e *callerCapturingExec) Execute(ctx context.Context, _ *packs.Pack, _ json.RawMessage) (*packs.Result, error) {
	e.mu.Lock()
	e.caller = packs.CallerFromContext(ctx)
	e.mu.Unlock()
	return &packs.Result{Output: json.RawMessage(`{}`)}, nil
}

func (e *callerCapturingExec) Caller() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.caller
}

func TestRunner_StartRunThreadsCaller(t *testing.T) {
	ex := &callerCapturingExec{}
	r := newTestRunner(t, ex)
	ctx := context.Background()
	p := &Pipeline{ID: "p", Name: "n", Builtin: true, Steps: []Step{
		{ID: "s1", Pack: "a.pack", Input: json.RawMessage(`{}`)},
	}}
	if err := r.store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	runID, err := r.StartRun(ctx, "p", nil, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		got, _ := r.GetRun(ctx, runID)
		if got != nil && (got.Status == RunSucceeded || got.Status == RunFailed) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if c := ex.Caller(); c != "alice" {
		t.Errorf("step Execute ctx caller = %q, want alice (StartRun must thread the caller onto the detached run context)", c)
	}
}
