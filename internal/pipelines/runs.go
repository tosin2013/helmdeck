// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// runRegistry holds live run snapshots in memory (mirrors the MCP async
// jobRegistry). Terminal runs are evicted after runTTL; durable history
// always lives in pipeline_runs, so GetRun falls back to the store.
type runRegistry struct {
	mu   sync.Mutex
	runs map[string]*Run
	ttl  time.Duration
}

const (
	runTTL        = time.Hour
	runSweepEvery = 10 * time.Minute
)

func newRunRegistry() *runRegistry {
	r := &runRegistry{runs: map[string]*Run{}, ttl: runTTL}
	return r
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

// sweep evicts terminal runs older than the TTL.
func (rr *runRegistry) sweep(now time.Time) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for id, run := range rr.runs {
		terminal := run.Status == RunSucceeded || run.Status == RunFailed
		if terminal && !run.EndedAt.IsZero() && now.Sub(run.EndedAt) > rr.ttl {
			delete(rr.runs, id)
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
		if err := r.RunSync(bg, p, inputs, run); err != nil {
			run.Status = RunFailed
			run.Error = err.Error()
			run.EndedAt = r.now()
			r.finish(bg, run)
		}
	}()
	return run.ID, nil
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
