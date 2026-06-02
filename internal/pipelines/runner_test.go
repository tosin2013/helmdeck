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
	return NewRunner(testStore(t), resolverFor(), ex, nil, nil)
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
	runID, _, err := r.StartRun(ctx, "p", json.RawMessage(`{"url":"u"}`), "")
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
	newID, _, err := r.Rerun(context.Background(), "run-1", "")
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
	runID, _, err := r.StartRun(ctx, "p", nil, "alice")
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

type progressMilestone struct {
	pct float64
	msg string
}

// progressEmittingExec fires a handful of ec.Report milestones via the
// progress sink the runner attached to the ctx (packs.WithProgress).
type progressEmittingExec struct{ milestones []progressMilestone }

func (e *progressEmittingExec) Execute(ctx context.Context, _ *packs.Pack, _ json.RawMessage) (*packs.Result, error) {
	// Mimic how a real pack reports progress — the engine wires this
	// callback from the runner's WithProgress.
	report := packs.ProgressFromContext(ctx)
	for _, m := range e.milestones {
		report(m.pct, m.msg)
	}
	return &packs.Result{Output: json.RawMessage(`{}`)}, nil
}

// TestRunner_RecordsStepProgress — the runner attaches a progress sink to the
// step's ctx, so each ec.Report(pct,msg) the pack emits is appended to the
// step's Progress slice and persisted (live-visible to the UI poll).
func TestRunner_RecordsStepProgress(t *testing.T) {
	ex := &progressEmittingExec{milestones: []progressMilestone{
		{pct: 10, msg: "starting"},
		{pct: 50, msg: "rendering"},
		{pct: 100, msg: "uploaded"},
	}}
	r := newTestRunner(t, ex)
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{{ID: "s1", Pack: "any.pack", Input: json.RawMessage(`{}`)}}}
	run := &Run{ID: "run-prog", PipelineID: "p", StartedAt: r.now()}
	_ = r.store.CreateRun(context.Background(), run)
	if err := r.RunSync(context.Background(), p, nil, run); err != nil {
		t.Fatal(err)
	}
	if got := len(run.Steps[0].Progress); got != 3 {
		t.Fatalf("Progress entries = %d, want 3", got)
	}
	if msg := run.Steps[0].Progress[1].Message; msg != "rendering" {
		t.Errorf("Progress[1].Message = %q, want rendering", msg)
	}
	persisted, _ := r.store.GetRun(context.Background(), "run-prog")
	if len(persisted.Steps[0].Progress) != 3 {
		t.Errorf("persisted Progress not durable: %+v", persisted.Steps[0])
	}
}

// stubCanceller records TerminateByRunID calls — the SessionCanceller seam.
type stubCanceller struct {
	mu     sync.Mutex
	calls  []string
	killed int
	err    error
}

func (s *stubCanceller) TerminateByRunID(_ context.Context, runID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, runID)
	return s.killed, s.err
}

func (s *stubCanceller) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func TestRunner_CancelRun_UnknownRun(t *testing.T) {
	r := NewRunner(testStore(t), resolverFor(), &recordingExec{}, &stubCanceller{}, nil)
	if err := r.CancelRun(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CancelRun on unknown = %v, want ErrNotFound", err)
	}
}

func TestRunner_CancelRun_AlreadyTerminal(t *testing.T) {
	r := NewRunner(testStore(t), resolverFor(), &recordingExec{}, &stubCanceller{}, nil)
	done := &Run{ID: "run-done", PipelineID: "p", Status: RunSucceeded, StartedAt: r.now(), EndedAt: r.now()}
	if err := r.store.CreateRun(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	// Save the terminal status so GetRun returns succeeded.
	if err := r.store.SaveRun(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	err := r.CancelRun(context.Background(), "run-done")
	if err == nil || !strings.Contains(err.Error(), "cannot be cancelled") {
		t.Fatalf("CancelRun on terminal = %v, want 'cannot be cancelled' error", err)
	}
}

// blockingExec blocks until the test releases it, so the goroutine can sit on
// a step while CancelRun fires. ctx-cancel unblocks it (mimics how the engine
// returns when a session container is killed).
type blockingExec struct {
	started chan struct{}
}

func (b *blockingExec) Execute(ctx context.Context, _ *packs.Pack, _ json.RawMessage) (*packs.Result, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRunner_CancelRun_RaceGuard — a mid-flight cancel resolves the step to
// RunCancelled, NOT RunFailed (the cancelReq flag set before the kill is the
// authoritative signal; without it, the step's ctx.Canceled error would
// otherwise be classified as a transient failure).
func TestRunner_CancelRun_RaceGuard(t *testing.T) {
	ex := &blockingExec{started: make(chan struct{}, 1)}
	canc := &stubCanceller{}
	r := NewRunner(testStore(t), resolverFor(), ex, canc, nil)
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{{ID: "s1", Pack: "any.pack", Input: json.RawMessage(`{}`)}}}
	if err := r.store.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	runID, _, err := r.StartRun(context.Background(), "p", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	<-ex.started // step is now blocked in Execute
	if err := r.CancelRun(context.Background(), runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if calls := canc.Calls(); len(calls) != 1 || calls[0] != runID {
		t.Errorf("TerminateByRunID calls = %v, want [%s]", calls, runID)
	}
	// Wait for the goroutine to flip the run.
	deadline := time.Now().Add(2 * time.Second)
	var got *Run
	for time.Now().Before(deadline) {
		got, _ = r.GetRun(context.Background(), runID)
		if got != nil && got.Status.IsTerminal() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got == nil || got.Status != RunCancelled {
		t.Fatalf("final run status = %v, want %s", got, RunCancelled)
	}
	if len(got.Steps) == 0 || got.Steps[0].Status != RunCancelled {
		t.Errorf("step status = %v, want %s", got.Steps, RunCancelled)
	}
	// And the persisted row reflects it too.
	persisted, _ := r.store.GetRun(context.Background(), runID)
	if persisted.Status != RunCancelled {
		t.Errorf("persisted status = %s, want %s", persisted.Status, RunCancelled)
	}
}

// TestRunRegistry_SweepEvictsCancelled — sweep removes a terminated
// RunCancelled run and clears its cancel bookkeeping (no leak).
func TestRunRegistry_SweepEvictsCancelled(t *testing.T) {
	rr := newRunRegistry()
	rr.ttl = 1 * time.Nanosecond
	cancelled := &Run{ID: "rc", Status: RunCancelled, EndedAt: time.Now().Add(-time.Hour)}
	rr.put(cancelled)
	rr.markCancelRequested("rc")
	rr.setCancel("rc", func() {})
	rr.sweep(time.Now())
	if _, ok := rr.get("rc"); ok {
		t.Errorf("RunCancelled should be evicted by sweep")
	}
	if rr.cancelRequested("rc") {
		t.Errorf("sweep should clear cancelReq for evicted runs")
	}
	if c := rr.takeCancel("rc"); c != nil {
		t.Errorf("sweep should clear cancels for evicted runs")
	}
}

// TestRunner_ReconcileOrphans — runs the store still records as
// pending/running at boot are reaped to failed with the orphan reason,
// every in-flight step inside is reaped too (so the UI step badges are
// not stuck), and already-terminal runs are untouched.
func TestRunner_ReconcileOrphans(t *testing.T) {
	store := testStore(t)
	r := NewRunner(store, resolverFor(), &recordingExec{}, nil, nil)
	ctx := context.Background()

	// Seed: succeeded (untouched), running (reaped), pending (reaped).
	done := &Run{ID: "r-done", PipelineID: "p", Status: RunSucceeded, StartedAt: r.now(), EndedAt: r.now()}
	running := &Run{ID: "r-run", PipelineID: "p", Status: RunRunning, StartedAt: r.now(),
		Steps: []RunStep{
			{StepID: "a", Pack: "x", Status: RunSucceeded, StartedAt: r.now(), EndedAt: r.now()},
			{StepID: "b", Pack: "y", Status: RunRunning, StartedAt: r.now()},
		}}
	pending := &Run{ID: "r-pend", PipelineID: "p", Status: RunPending, StartedAt: r.now()}
	for _, run := range []*Run{done, running, pending} {
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	n, err := r.ReconcileOrphans(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphans: %v", err)
	}
	if n != 2 {
		t.Errorf("reaped = %d, want 2", n)
	}

	// The succeeded run must be untouched.
	gotDone, _ := store.GetRun(ctx, "r-done")
	if gotDone.Status != RunSucceeded {
		t.Errorf("succeeded run was mutated: %s", gotDone.Status)
	}

	// Running → failed with reason on run + on its in-flight step only.
	gotRun, _ := store.GetRun(ctx, "r-run")
	if gotRun.Status != RunFailed {
		t.Errorf("running run not reaped: %s", gotRun.Status)
	}
	if gotRun.Error == "" || gotRun.EndedAt.IsZero() {
		t.Errorf("reaped run missing error/ended_at: %+v", gotRun)
	}
	if gotRun.Steps[0].Status != RunSucceeded {
		t.Errorf("already-succeeded step was clobbered: %+v", gotRun.Steps[0])
	}
	if gotRun.Steps[1].Status != RunFailed {
		t.Errorf("in-flight step not reaped: %+v", gotRun.Steps[1])
	}
	if gotRun.Steps[1].FailureClass != "transient" || gotRun.Steps[1].FailureReason != orphanReason {
		t.Errorf("step attribution wrong: %+v", gotRun.Steps[1])
	}

	// Pending → failed too.
	gotPend, _ := store.GetRun(ctx, "r-pend")
	if gotPend.Status != RunFailed {
		t.Errorf("pending run not reaped: %s", gotPend.Status)
	}

	// Re-running the reaper finds nothing.
	n2, _ := r.ReconcileOrphans(ctx)
	if n2 != 0 {
		t.Errorf("second pass reaped %d, want 0 (idempotent)", n2)
	}
}

// --- single-flight coalescing (migration 0008) ---

// TestComputeRunFingerprint_StableAndDistinct confirms the fingerprint is
// (a) deterministic, (b) insensitive to JSON whitespace and key ordering,
// and (c) genuinely distinguishes different callers/pipelines/inputs.
// Without these properties, coalescing either misses real duplicates or
// over-coalesces unrelated calls.
func TestComputeRunFingerprint_StableAndDistinct(t *testing.T) {
	cases := []struct {
		name     string
		a        struct{ caller, pid, in string }
		b        struct{ caller, pid, in string }
		wantSame bool
	}{
		{
			name:     "identical (caller, pid, inputs) → same fingerprint",
			a:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"u","title":"t"}`},
			b:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"u","title":"t"}`},
			wantSame: true,
		},
		{
			name:     "reordered object keys → same fingerprint",
			a:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"u","title":"t"}`},
			b:        struct{ caller, pid, in string }{"alice", "p1", `{"title":"t","url":"u"}`},
			wantSame: true,
		},
		{
			name:     "whitespace differences → same fingerprint",
			a:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"u"}`},
			b:        struct{ caller, pid, in string }{"alice", "p1", `{ "url" :  "u" }`},
			wantSame: true,
		},
		{
			name:     "nested object reordered → same fingerprint",
			a:        struct{ caller, pid, in string }{"alice", "p1", `{"a":{"x":1,"y":2}}`},
			b:        struct{ caller, pid, in string }{"alice", "p1", `{"a":{"y":2,"x":1}}`},
			wantSame: true,
		},
		{
			name:     "different caller → different fingerprint",
			a:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"u"}`},
			b:        struct{ caller, pid, in string }{"bob", "p1", `{"url":"u"}`},
			wantSame: false,
		},
		{
			name:     "different pipeline id → different fingerprint",
			a:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"u"}`},
			b:        struct{ caller, pid, in string }{"alice", "p2", `{"url":"u"}`},
			wantSame: false,
		},
		{
			name:     "different input value → different fingerprint",
			a:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"u"}`},
			b:        struct{ caller, pid, in string }{"alice", "p1", `{"url":"v"}`},
			wantSame: false,
		},
		{
			name:     "empty inputs normalize to null and coalesce with each other",
			a:        struct{ caller, pid, in string }{"alice", "p1", ``},
			b:        struct{ caller, pid, in string }{"alice", "p1", ``},
			wantSame: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fpA := computeRunFingerprint(tc.a.caller, tc.a.pid, json.RawMessage(tc.a.in))
			fpB := computeRunFingerprint(tc.b.caller, tc.b.pid, json.RawMessage(tc.b.in))
			got := fpA == fpB
			if got != tc.wantSame {
				t.Errorf("fpA=%s fpB=%s same=%v, want same=%v", fpA, fpB, got, tc.wantSame)
			}
		})
	}
}

// blockingChannelExec blocks each Execute on a release channel — lets us
// hold a run in the running state while a second StartRun fires for the
// coalesce test. Mirrors how a real long-running pack (slides.narrate's
// ffmpeg loop) would still be encoding when the retry hits.
type blockingChannelExec struct {
	release chan struct{}
	started chan struct{}
}

func (b *blockingChannelExec) Execute(ctx context.Context, _ *packs.Pack, _ json.RawMessage) (*packs.Result, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &packs.Result{Output: json.RawMessage(`{}`)}, nil
}

// TestRunner_StartRun_CoalescesIdenticalInFlight — the duplicate-pipeline-run
// failure mode (LLM retries on tool-call timeout while the first run is still
// going). Two identical StartRun calls must return the SAME run id with the
// second one flagged coalesced=true; only ONE underlying execution starts.
func TestRunner_StartRun_CoalescesIdenticalInFlight(t *testing.T) {
	ex := &blockingChannelExec{release: make(chan struct{}), started: make(chan struct{}, 1)}
	r := newTestRunner(t, ex)
	ctx := context.Background()
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "s1", Pack: "long.pack", Input: json.RawMessage(`{}`)},
	}}
	if err := r.store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	inputs := json.RawMessage(`{"url":"u","title":"t"}`)

	id1, coalesced1, err := r.StartRun(ctx, "p", inputs, "alice")
	if err != nil {
		t.Fatalf("first StartRun: %v", err)
	}
	if coalesced1 {
		t.Errorf("first call coalesced=true, want false (no prior in-flight run)")
	}
	<-ex.started // ensure the first run reached Execute and is blocked

	// Same caller, same pipeline, same inputs (different whitespace) →
	// must coalesce onto id1.
	id2, coalesced2, err := r.StartRun(ctx, "p", json.RawMessage(`{ "title" : "t", "url" : "u" }`), "alice")
	if err != nil {
		t.Fatalf("second StartRun: %v", err)
	}
	if id2 != id1 {
		t.Errorf("coalesced run id = %q, want first run %q", id2, id1)
	}
	if !coalesced2 {
		t.Errorf("second call coalesced=false, want true")
	}

	// Different caller with identical inputs → must NOT coalesce.
	id3, coalesced3, err := r.StartRun(ctx, "p", inputs, "bob")
	if err != nil {
		t.Fatalf("third StartRun: %v", err)
	}
	if id3 == id1 {
		t.Errorf("different caller got same run id (over-coalesce): %q", id3)
	}
	if coalesced3 {
		t.Errorf("different-caller call coalesced=true, want false")
	}

	// Different inputs from the same caller → must NOT coalesce.
	id4, coalesced4, err := r.StartRun(ctx, "p", json.RawMessage(`{"url":"DIFFERENT"}`), "alice")
	if err != nil {
		t.Fatalf("fourth StartRun: %v", err)
	}
	if id4 == id1 {
		t.Errorf("different inputs got same run id (over-coalesce): %q", id4)
	}
	if coalesced4 {
		t.Errorf("different-inputs call coalesced=true, want false")
	}

	close(ex.release) // let the runs finish
}

// TestRunner_StartRun_DoesNotCoalesceOntoTerminalRun — once a run is
// terminal, a fresh identical StartRun must produce a NEW run id (not
// dredge up the completed run). Otherwise an operator who Reruns a
// finished pipeline would silently get the prior result back forever.
func TestRunner_StartRun_DoesNotCoalesceOntoTerminalRun(t *testing.T) {
	ex := &recordingExec{outputs: map[string]string{"a.pack": `{}`}}
	r := newTestRunner(t, ex)
	ctx := context.Background()
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "s1", Pack: "a.pack", Input: json.RawMessage(`{}`)},
	}}
	if err := r.store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	inputs := json.RawMessage(`{"url":"u"}`)
	id1, _, err := r.StartRun(ctx, "p", inputs, "alice")
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the first run to reach a terminal state.
	for i := 0; i < 200; i++ {
		got, _ := r.GetRun(ctx, id1)
		if got != nil && got.Status.IsTerminal() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Second call with the SAME caller/pipeline/inputs after the first ran to
	// completion → must produce a new run id, NOT coalesce.
	id2, coalesced2, err := r.StartRun(ctx, "p", inputs, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Errorf("StartRun coalesced onto terminal run %q — must spawn a fresh run", id1)
	}
	if coalesced2 {
		t.Errorf("second call after terminal coalesced=true, want false")
	}
}

// TestRunner_StartRun_ConcurrentIdenticalCalls — the race-window guard.
// N goroutines fire identical StartRun calls simultaneously; exactly ONE
// run row must exist in the store afterward, and all callers must observe
// the same run id. The startMu + partial unique index together guarantee
// this even when goroutines interleave past the lookup before any insert.
func TestRunner_StartRun_ConcurrentIdenticalCalls(t *testing.T) {
	ex := &blockingChannelExec{release: make(chan struct{}), started: make(chan struct{}, 16)}
	r := newTestRunner(t, ex)
	ctx := context.Background()
	p := &Pipeline{ID: "p", Name: "n", Steps: []Step{
		{ID: "s1", Pack: "long.pack", Input: json.RawMessage(`{}`)},
	}}
	if err := r.store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	inputs := json.RawMessage(`{"url":"u"}`)

	const N = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	gotIDs := make([]string, 0, N)
	coalescedCount := 0
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			id, c, err := r.StartRun(ctx, "p", inputs, "alice")
			if err != nil {
				t.Errorf("StartRun: %v", err)
				return
			}
			mu.Lock()
			gotIDs = append(gotIDs, id)
			if c {
				coalescedCount++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// All goroutines must have seen the same run id.
	first := gotIDs[0]
	for _, id := range gotIDs {
		if id != first {
			t.Fatalf("got distinct run ids under concurrency: %v", gotIDs)
		}
	}
	if coalescedCount != N-1 {
		t.Errorf("coalesced count = %d, want %d (N-1: one initiator + N-1 coalesced)", coalescedCount, N-1)
	}
	// And the store must contain exactly one in-flight run for this fingerprint.
	inFlight, err := r.store.ListInFlightRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, run := range inFlight {
		if run.PipelineID == "p" && run.Caller == "alice" {
			matched++
		}
	}
	if matched != 1 {
		t.Errorf("in-flight run count = %d, want 1", matched)
	}

	close(ex.release)
}
