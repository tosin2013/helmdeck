// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// unfilledPlaceholderRE matches a value that is *entirely* an
// unsubstituted prompt-template variable like {{TITLE}} or {{REPO_URL}}
// — the UPPER_SNAKE convention the docs use for "fill this in". We
// anchor to the whole (trimmed) value on purpose: a short input whose
// only content is {{TITLE}} is almost certainly a template the caller
// pasted without substituting (the reported failure: title="{{TITLE}}"
// silently published a post literally titled "{{TITLE}}"). Matching the
// whole value — not any {{…}} substring — keeps real content (a markdown
// body that happens to mention {{API_KEY}}) from false-positiving.
var unfilledPlaceholderRE = regexp.MustCompile(`^\s*\{\{\s*[A-Z][A-Z0-9_]*\s*\}\}\s*$`)

// validateInputsFilled rejects pipeline inputs whose value is still an
// unfilled {{UPPER_SNAKE}} template placeholder, before the run starts.
// The error is caller-fixable and prescribes the recovery so an agent
// that forgot to substitute a variable is told what to do — ask the user
// for a value, or propose one and confirm it — rather than running with
// the literal placeholder and producing "{{TITLE}}" output. Shape errors
// (inputs not an object) are left to the runner's own unmarshal.
func validateInputsFilled(inputs json.RawMessage) error {
	if len(inputs) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(inputs, &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error across re-runs
	for _, k := range keys {
		s, ok := m[k].(string)
		if !ok {
			continue
		}
		if ph := strings.TrimSpace(s); unfilledPlaceholderRE.MatchString(s) {
			return fmt.Errorf(
				"input %q is still the template placeholder %s — fill it in before running "+
					"(ask the user for a value, or propose one and confirm it), then re-run",
				k, ph)
		}
	}
	return nil
}

// runRegistry holds live run snapshots in memory (mirrors the MCP async
// jobRegistry). Terminal runs are evicted after runTTL; durable history
// always lives in pipeline_runs, so GetRun falls back to the store.
//
// cancels holds the per-run ctx-cancel func StartRun's goroutine captured —
// CancelRun fires it to unblock ctx-aware work (and to flip ctx.Err() to
// context.Canceled, distinguishing explicit-cancel from the 30-min timeout's
// DeadlineExceeded). cancelReq records the "cancel requested" intent before
// the kill — RunSync reads it to resolve a step error to RunCancelled
// instead of RunFailed (the race guard). Both clear on sweep / on finish.
type runRegistry struct {
	mu        sync.Mutex
	runs      map[string]*Run
	cancels   map[string]context.CancelFunc
	cancelReq map[string]bool
	ttl       time.Duration
}

const (
	runTTL        = time.Hour
	runSweepEvery = 10 * time.Minute
)

func newRunRegistry() *runRegistry {
	r := &runRegistry{
		runs:      map[string]*Run{},
		cancels:   map[string]context.CancelFunc{},
		cancelReq: map[string]bool{},
		ttl:       runTTL,
	}
	return r
}

// setCancel stores the cancel func StartRun's goroutine captured. Called
// once per run (StartRun goroutine). No-op if a cancel is already stored.
func (rr *runRegistry) setCancel(id string, cancel context.CancelFunc) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if _, ok := rr.cancels[id]; ok {
		return
	}
	rr.cancels[id] = cancel
}

// takeCancel removes and returns the cancel func, so CancelRun can fire it
// exactly once. Returns nil if no cancel was registered (pending run whose
// goroutine hasn't reached setCancel yet — flag-only cancel still works).
func (rr *runRegistry) takeCancel(id string) context.CancelFunc {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	c, ok := rr.cancels[id]
	if !ok {
		return nil
	}
	delete(rr.cancels, id)
	return c
}

// markCancelRequested records the intent so RunSync's race guard resolves
// the resulting step error as RunCancelled, not RunFailed.
func (rr *runRegistry) markCancelRequested(id string) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.cancelReq[id] = true
}

// cancelRequested reports whether CancelRun was called for this run id.
func (rr *runRegistry) cancelRequested(id string) bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.cancelReq[id]
}

func (rr *runRegistry) put(run *Run) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	cp := *run
	cp.Steps = append([]RunStep(nil), run.Steps...)
	rr.runs[run.ID] = &cp
}

func (rr *runRegistry) get(id string) (*Run, bool) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	run, ok := rr.runs[id]
	if !ok {
		return nil, false
	}
	cp := *run
	cp.Steps = append([]RunStep(nil), run.Steps...)
	return &cp, true
}

// sweep evicts terminal runs older than the TTL and clears their cancel
// bookkeeping. RunCancelled is terminal — counted here too.
func (rr *runRegistry) sweep(now time.Time) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for id, run := range rr.runs {
		if run.Status.IsTerminal() && !run.EndedAt.IsZero() && now.Sub(run.EndedAt) > rr.ttl {
			delete(rr.runs, id)
			delete(rr.cancels, id)
			delete(rr.cancelReq, id)
		}
	}
}

// StartRun loads a pipeline by id, creates a pending run, and executes it
// on a detached, timed context in a background goroutine. Returns the run
// id immediately. The run's progress is observable via GetRun.
//
// caller is the authenticated subject (JWT "sub"); it is re-attached to
// the detached run context via packs.WithCaller so per-step packs (e.g.
// repo.fetch's persistent clone namespace) see the real caller instead of
// "unknown". Pass "" when unauthenticated.
func (r *Runner) StartRun(ctx context.Context, pipelineID string, inputs json.RawMessage, caller string) (string, error) {
	p, err := r.store.Get(ctx, pipelineID)
	if err != nil {
		return "", err
	}
	// Re-validate against the live registry — a referenced pack may have
	// been unregistered since the pipeline was created.
	if err := Validate(p, func(name, ver string) bool { _, e := r.resolve(name, ver); return e == nil }); err != nil {
		return "", err
	}
	// Reject inputs the caller pasted from a prompt template but never
	// filled (e.g. title="{{TITLE}}") — fail fast with a fixable message
	// instead of running with the literal placeholder.
	if err := validateInputsFilled(inputs); err != nil {
		return "", err
	}

	run := &Run{
		ID:         newRunID(),
		PipelineID: pipelineID,
		Status:     RunPending,
		Inputs:     inputs,
		StartedAt:  r.now(),
	}
	if err := r.store.CreateRun(ctx, run); err != nil {
		return "", err
	}
	r.reg.put(run)

	go func() {
		bg, cancel := context.WithTimeout(context.Background(), r.timeout)
		defer cancel()
		bg = packs.WithCaller(bg, caller)
		// Stamp the run id on the ctx so the engine labels created
		// sessions with helmdeck.run_id=<id> — TerminateByRunID uses
		// that label to find the containers to kill on cancel.
		bg = packs.WithRunID(bg, run.ID)
		// Hand the cancel func to the registry so CancelRun can fire
		// it. setCancel is once-only; later puts are ignored.
		r.reg.setCancel(run.ID, cancel)
		if err := r.RunSync(bg, p, inputs, run); err != nil {
			// Don't overwrite a cancel with a generic setup-failure
			// (e.g. bad inputs JSON that the cancel already pre-empted).
			if r.reg.cancelRequested(run.ID) {
				return
			}
			run.Status = RunFailed
			run.Error = err.Error()
			run.EndedAt = r.now()
			r.finish(context.Background(), run)
		}
	}()
	return run.ID, nil
}

// CancelRun stops a running (or pending) run: it flags the intent (so the
// goroutine's race-guard resolves to RunCancelled), cancels the run ctx
// (unblocks ctx-aware work), and force-terminates every session container
// tagged with this run id (the only way to stop a wedged docker-exec read,
// which ignores ctx). Returns ErrNotFound for an unknown id and a
// not_cancellable error if the run is already terminal. Cancelling a run
// that owns a preserved session (e.g. repo.fetch's per-caller clone) kills
// that session too — preserved sessions live for the run's duration, so
// hard cancel is the intended semantics. Externally-pinned sessions (a
// _session_id from another caller) are unlabeled and unaffected.
//
// Single-writer: this method only triggers. The run goroutine writes the
// terminal RunCancelled status (via RunSync's race guard) — no two-writer
// race with the goroutine over run.Status.
func (r *Runner) CancelRun(ctx context.Context, runID string) error {
	run, err := r.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.IsTerminal() {
		return fmt.Errorf("run is already %s and cannot be cancelled", run.Status)
	}
	r.reg.markCancelRequested(runID)
	if c := r.reg.takeCancel(runID); c != nil {
		c()
	}
	if r.canceller != nil {
		tctx, tcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer tcancel()
		if killed, terr := r.canceller.TerminateByRunID(tctx, runID); terr != nil {
			r.logger.Warn("cancel: terminate sessions failed",
				"run_id", runID, "killed", killed, "err", terr)
		} else if killed > 0 {
			r.logger.Info("cancel: terminated sessions",
				"run_id", runID, "killed", killed)
		}
	}
	return nil
}

// Rerun starts a fresh run of the pipeline + inputs from an existing run
// — the CI/CD "re-run this job" affordance. It is NOT a resume: every
// step executes again from the top (resume-from-failed-step is ADR 044
// slice 2). Returns the new run id.
func (r *Runner) Rerun(ctx context.Context, runID string, caller string) (string, error) {
	prev, err := r.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	return r.StartRun(ctx, prev.PipelineID, prev.Inputs, caller)
}

// GetRun returns the live snapshot if present, else the persisted row.
func (r *Runner) GetRun(ctx context.Context, runID string) (*Run, error) {
	if run, ok := r.reg.get(runID); ok {
		return run, nil
	}
	return r.store.GetRun(ctx, runID)
}

// RunSweeper runs the TTL eviction loop until ctx is cancelled (started
// by main.go as a goroutine, like the artifact janitor).
func (r *Runner) RunSweeper(ctx context.Context) {
	t := time.NewTicker(runSweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reg.sweep(r.now())
		}
	}
}

func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "run_" + hex.EncodeToString(b[:])
}
