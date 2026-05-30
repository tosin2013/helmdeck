// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// Executor runs a single pack. *packs.Engine satisfies it; tests inject a
// fake so the runner can be exercised without a real engine/session.
type Executor interface {
	Execute(ctx context.Context, pack *packs.Pack, input json.RawMessage) (*packs.Result, error)
}

// PipelineAuditWriter is satisfied by executors that want a callback
// on terminal pipeline states (ADR 047 PR #2). *packs.Engine
// satisfies it; tests that don't care can implement only Executor.
type PipelineAuditWriter interface {
	WritePipelineAudit(ctx context.Context, pipelineID, runID string, inputs json.RawMessage, outcome string, duration time.Duration)
}

// SessionCanceller force-terminates every session container tagged with a run
// id. *docker.Runtime satisfies it (TerminateByRunID). Optional — when nil,
// CancelRun still cancels the run context but can't tear down a stuck
// container (degrades to a soft cancel).
type SessionCanceller interface {
	TerminateByRunID(ctx context.Context, runID string) (int, error)
}

// PackResolver resolves a pack by name+version. *packs.Registry.Get
// satisfies it (method value).
type PackResolver func(name, version string) (*packs.Pack, error)

// Runner executes pipelines and tracks live runs. Runs execute on a
// detached context so an HTTP request that kicked one off can return
// immediately; GetRun reads live in-memory status, falling back to the
// persisted row after the run finishes / is evicted.
type Runner struct {
	store     *Store
	resolve   PackResolver
	exec      Executor
	canceller SessionCanceller // optional — nil ⇒ soft cancel (ctx-only)
	logger    *slog.Logger
	now       func() time.Time

	// hook, when set, is called after a run reaches a terminal state
	// (succeeded/failed/cancelled) — main.go wires it to the audit log.
	hook func(ctx context.Context, r *Run)

	timeout time.Duration // per-run cap on the detached context

	reg *runRegistry
}

// NewRunner constructs a Runner. resolve is typically packReg.Get; exec is
// the *packs.Engine; canceller (optional, may be nil) is the session
// terminator used by CancelRun to tear down a stuck run — *docker.Runtime
// satisfies it. When nil, CancelRun still cancels the run context but can't
// kill an in-flight container (degraded soft cancel).
func NewRunner(store *Store, resolve PackResolver, exec Executor, canceller SessionCanceller, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		store:     store,
		resolve:   resolve,
		exec:      exec,
		canceller: canceller,
		logger:    logger,
		now:       func() time.Time { return time.Now().UTC() },
		timeout:   30 * time.Minute,
		reg:       newRunRegistry(),
	}
}

// SetHook wires a terminal-run callback (e.g. audit). Optional.
func (r *Runner) SetHook(h func(ctx context.Context, r *Run)) { r.hook = h }

// orphanReason is the failure_reason stamped on runs the orphan reaper
// reconciles at boot. Surfaced verbatim on both the run-level error and
// every in-flight step's FailureReason so the UI shows WHY a run is
// suddenly failed without a step that actually crashed.
const orphanReason = "control plane restarted while this run was in flight"

// ReconcileOrphans reaps runs the store still records as pending/running
// at startup — their owning goroutine died with a previous control-plane
// process, so nothing in-process will ever flip them. It is called once
// from main on boot before the HTTP listener accepts requests, so there
// is no live goroutine to race with. Each reaped run is marked failed
// (with failure_class "transient" — a retry is reasonable), and every
// in-flight step inside is marked the same way so the UI's step badges
// aren't stuck on "running" inside an overall-failed run. Returns the
// number reaped (terminal rows are untouched). Safe to call at any time
// in principle, but only sensible at boot.
func (r *Runner) ReconcileOrphans(ctx context.Context) (int, error) {
	runs, err := r.store.ListInFlightRuns(ctx)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, run := range runs {
		ended := r.now()
		for i := range run.Steps {
			st := run.Steps[i].Status
			if st != RunPending && st != RunRunning {
				continue
			}
			run.Steps[i].Status = RunFailed
			if run.Steps[i].Error == "" {
				run.Steps[i].Error = orphanReason
			}
			if run.Steps[i].FailureClass == "" {
				run.Steps[i].FailureClass = "transient"
			}
			if run.Steps[i].FailureReason == "" {
				run.Steps[i].FailureReason = orphanReason
			}
			if run.Steps[i].EndedAt.IsZero() {
				run.Steps[i].EndedAt = ended
			}
		}
		run.Status = RunFailed
		if run.Error == "" {
			run.Error = orphanReason
		}
		if run.FailureClass == "" {
			run.FailureClass = "transient"
		}
		if run.FailureReason == "" {
			run.FailureReason = orphanReason
		}
		if run.EndedAt.IsZero() {
			run.EndedAt = ended
		}
		if err := r.store.SaveRun(ctx, run); err != nil {
			return reaped, fmt.Errorf("reap orphan %s: %w", run.ID, err)
		}
		reaped++
	}
	return reaped, nil
}

// RunSync executes a pipeline to completion on the caller's context and
// returns the finished Run. Used by tests and as the building block for
// the async StartRun. Never returns an error for a *step* failure — that
// is recorded on the Run (Status=failed); it errors only on setup faults
// (bad inputs JSON, store write failure).
func (r *Runner) RunSync(ctx context.Context, p *Pipeline, inputs json.RawMessage, run *Run) error {
	start := r.now()
	// ADR 047 PR #2: record one pipeline-level audit row on every
	// terminal outcome. Type-asserted off the executor so tests with
	// a minimal fake aren't forced to implement it. Outcome strings
	// mirror Run.Status so downstream projection logic doesn't have
	// to translate.
	defer func() {
		aw, ok := r.exec.(PipelineAuditWriter)
		if !ok || p == nil || run == nil {
			return
		}
		aw.WritePipelineAudit(ctx, p.ID, run.ID, inputs, string(run.Status), r.now().Sub(start))
	}()
	// If cancel landed before the goroutine reached this point (pending
	// run cancelled), short-circuit cleanly — single-writer: this
	// goroutine owns run.Status, CancelRun only flags + triggers.
	if r.reg.cancelRequested(run.ID) {
		run.Status = RunCancelled
		run.Error = "cancelled before start"
		run.EndedAt = r.now()
		r.finish(context.Background(), run)
		return nil
	}

	var inputMap map[string]any
	if len(inputs) > 0 {
		if err := json.Unmarshal(inputs, &inputMap); err != nil {
			return fmt.Errorf("inputs is not a JSON object: %w", err)
		}
	}

	run.Status = RunRunning
	if err := r.store.SaveRun(ctx, run); err != nil {
		return err
	}
	r.reg.put(run)

	outputs := make(map[string]json.RawMessage, len(p.Steps))
	var prevSession string

	for _, step := range p.Steps {
		rs := RunStep{StepID: step.ID, Pack: step.Pack, Status: RunRunning, StartedAt: r.now()}
		run.Steps = append(run.Steps, rs)
		idx := len(run.Steps) - 1
		_ = r.store.SaveRun(ctx, run)
		r.reg.put(run)

		// Capture pack progress (ec.Report) onto this step's record live —
		// the callback runs synchronously inside Execute on this goroutine,
		// so no race on run.Steps[idx]; reg.put deep-copies. Use a fresh
		// ctx for the save so a cancelled run ctx doesn't drop the write.
		stepCtx := packs.WithProgress(ctx, func(pct float64, msg string) {
			run.Steps[idx].Progress = append(run.Steps[idx].Progress, StepProgress{At: r.now(), Pct: pct, Message: msg})
			r.reg.put(run)
			_ = r.store.SaveRun(context.Background(), run)
		})

		res, serr := r.runStep(stepCtx, step, inputMap, outputs, &prevSession)
		run.Steps[idx].EndedAt = r.now()
		if serr != nil {
			// A cancel request (or an explicit ctx-cancel — distinct from
			// the 30-min timeout's DeadlineExceeded) resolves the step to
			// RunCancelled, not RunFailed. The flag is the authoritative
			// signal; ctx.Err()==Canceled is the secondary, set by
			// CancelRun's captured cancel().
			if r.reg.cancelRequested(run.ID) || errors.Is(ctx.Err(), context.Canceled) {
				run.Steps[idx].Status = RunCancelled
				run.Status = RunCancelled
				run.Error = "cancelled by request"
				run.EndedAt = r.now()
				r.finish(context.Background(), run)
				return nil
			}
			code, class, reason := classify(serr, step.Pack)
			run.Steps[idx].Status = RunFailed
			run.Steps[idx].Error = serr.Error()
			run.Steps[idx].ErrorCode = code
			run.Steps[idx].FailureClass = class
			run.Steps[idx].FailureReason = reason
			run.Status = RunFailed
			run.Error = fmt.Sprintf("step %q: %v", step.ID, serr)
			run.FailureClass = class
			run.FailureReason = reason
			run.EndedAt = r.now()
			r.finish(ctx, run)
			return nil
		}
		run.Steps[idx].Status = RunSucceeded
		run.Steps[idx].Output = res.Output
		run.Steps[idx].Artifacts = res.Artifacts
		outputs[step.ID] = res.Output
		_ = r.store.SaveRun(ctx, run)
		r.reg.put(run)
	}

	run.Status = RunSucceeded
	run.EndedAt = r.now()
	r.finish(ctx, run)
	return nil
}

// runStep resolves a step's input, threads the prior session, executes
// the pack, and returns the pack result (output + artifacts). prevSession
// is updated to the session this step ran in (if any) for the next step.
func (r *Runner) runStep(ctx context.Context, step Step, inputs map[string]any, outputs map[string]json.RawMessage, prevSession *string) (*packs.Result, error) {
	pack, err := r.resolve(step.Pack, step.Version)
	if err != nil {
		return nil, fmt.Errorf("pack %q not available: %w", step.Pack, err)
	}
	resolved, err := Resolve(step.Input, inputs, outputs)
	if err != nil {
		return nil, err
	}
	// Thread the previous step's session forward (repo.fetch → fs.* etc.)
	// without letting a definition override it. Inject only when we have
	// one and the resolved input didn't already set _session_id.
	if *prevSession != "" {
		resolved, err = injectSessionID(resolved, *prevSession)
		if err != nil {
			return nil, err
		}
	}
	res, err := r.exec.Execute(ctx, pack, resolved)
	if err != nil {
		return nil, err
	}
	// Carry the session forward ONLY when this pack PRESERVES it. A
	// non-preserved session is torn down the moment the step ends, so
	// threading its id into a later step yields "session not found" — which
	// is exactly what broke podcast.generate (PreserveSession:false) →
	// hyperframes.render (which needs its own hyperframes-sidecar session
	// anyway). Preserved creators like repo.fetch still thread to their
	// follow-on packs (repo.map, fs.*, git.*, repo.push) via _session_id.
	if res.SessionID != "" && pack.PreserveSession {
		*prevSession = res.SessionID
	}
	return res, nil
}

// injectSessionID sets _session_id on the resolved input object unless it
// is already present.
func injectSessionID(input json.RawMessage, sessionID string) (json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &m); err != nil {
			return input, nil // non-object input: leave as-is
		}
	}
	if _, ok := m["_session_id"]; ok {
		return input, nil
	}
	enc, _ := json.Marshal(sessionID)
	m["_session_id"] = enc
	return json.Marshal(m)
}

// finish persists the terminal run, updates the live registry, and fires
// the optional hook.
func (r *Runner) finish(ctx context.Context, run *Run) {
	if err := r.store.SaveRun(ctx, run); err != nil {
		r.logger.Warn("pipeline run save failed", "run_id", run.ID, "err", err)
	}
	r.reg.put(run)
	r.logger.Info("pipeline run finished",
		"run_id", run.ID, "pipeline_id", run.PipelineID, "status", run.Status)
	if r.hook != nil {
		r.hook(ctx, run)
	}
}
