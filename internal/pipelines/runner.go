// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"encoding/json"
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

// PackResolver resolves a pack by name+version. *packs.Registry.Get
// satisfies it (method value).
type PackResolver func(name, version string) (*packs.Pack, error)

// Runner executes pipelines and tracks live runs. Runs execute on a
// detached context so an HTTP request that kicked one off can return
// immediately; GetRun reads live in-memory status, falling back to the
// persisted row after the run finishes / is evicted.
type Runner struct {
	store   *Store
	resolve PackResolver
	exec    Executor
	logger  *slog.Logger
	now     func() time.Time

	// hook, when set, is called after a run reaches a terminal state
	// (succeeded/failed) — main.go wires it to the audit log.
	hook func(ctx context.Context, r *Run)

	timeout time.Duration // per-run cap on the detached context

	reg *runRegistry
}

// NewRunner constructs a Runner. resolve is typically packReg.Get; exec is
// the *packs.Engine.
func NewRunner(store *Store, resolve PackResolver, exec Executor, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		store:   store,
		resolve: resolve,
		exec:    exec,
		logger:  logger,
		now:     func() time.Time { return time.Now().UTC() },
		timeout: 30 * time.Minute,
		reg:     newRunRegistry(),
	}
}

// SetHook wires a terminal-run callback (e.g. audit). Optional.
func (r *Runner) SetHook(h func(ctx context.Context, r *Run)) { r.hook = h }

// RunSync executes a pipeline to completion on the caller's context and
// returns the finished Run. Used by tests and as the building block for
// the async StartRun. Never returns an error for a *step* failure — that
// is recorded on the Run (Status=failed); it errors only on setup faults
// (bad inputs JSON, store write failure).
func (r *Runner) RunSync(ctx context.Context, p *Pipeline, inputs json.RawMessage, run *Run) error {
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

		out, serr := r.runStep(ctx, step, inputMap, outputs, &prevSession)
		run.Steps[idx].EndedAt = r.now()
		if serr != nil {
			run.Steps[idx].Status = RunFailed
			run.Steps[idx].Error = serr.Error()
			run.Status = RunFailed
			run.Error = fmt.Sprintf("step %q: %v", step.ID, serr)
			run.EndedAt = r.now()
			r.finish(ctx, run)
			return nil
		}
		run.Steps[idx].Status = RunSucceeded
		run.Steps[idx].Output = out
		outputs[step.ID] = out
		_ = r.store.SaveRun(ctx, run)
		r.reg.put(run)
	}

	run.Status = RunSucceeded
	run.EndedAt = r.now()
	r.finish(ctx, run)
	return nil
}

// runStep resolves a step's input, threads the prior session, executes
// the pack, and returns the pack output. prevSession is updated to the
// session this step ran in (if any) for the next step.
func (r *Runner) runStep(ctx context.Context, step Step, inputs map[string]any, outputs map[string]json.RawMessage, prevSession *string) (json.RawMessage, error) {
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
	if res.SessionID != "" {
		*prevSession = res.SessionID
	}
	return res.Output, nil
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
