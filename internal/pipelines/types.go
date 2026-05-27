// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package pipelines makes a multi-step pack workflow a first-class,
// persisted resource (ADR 041). A pipeline is a stored, named, ordered
// list of pack "steps"; each step's input may reference a prior step's
// output via ${{ steps.<id>.output.<path> }} or pipeline inputs via
// ${{ inputs.<name> }}. The runner executes steps sequentially by
// reusing packs.Engine.Execute, threads outputs + session IDs forward,
// and records a run history. Definitions live in SQLite (unlike packs,
// which carry Go closures and live in an in-memory registry).
//
// v0.15.0 ships the data model, store, runner, REST + MCP surface, and a
// set of auto-seeded built-in starter pipelines. Cron/webhook triggers,
// the UI, and audit→pipeline promotion are deferred (Runner.Run is kept
// HTTP-decoupled so those reuse it unchanged).
package pipelines

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// RunStatus is the lifecycle state of a pipeline run (and of each step).
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// Step is one pack invocation in a pipeline. Input is the pack's input
// JSON, which may contain ${{ ... }} references resolved at run time.
type Step struct {
	ID      string          `json:"id"`                // unique within the pipeline; referenced by ${{ steps.<id>... }}
	Pack    string          `json:"pack"`              // pack name, e.g. "slides.render"
	Version string          `json:"version,omitempty"` // "" = latest
	Input   json.RawMessage `json:"input"`             // may contain ${{ }} refs
}

// Pipeline is a stored, ordered sequence of pack steps.
type Pipeline struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Builtin     bool            `json:"builtin"`
	Inputs      json.RawMessage `json:"inputs,omitempty"` // informational declared-input schema
	Steps       []Step          `json:"steps"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// RunStep is the per-step record within a run.
type RunStep struct {
	StepID string          `json:"step_id"`
	Pack   string          `json:"pack"`
	Status RunStatus       `json:"status"`
	Output json.RawMessage `json:"output,omitempty"`
	// Artifacts are the files this step produced (e.g. slides.render's
	// PDF). Surfaced in the run record so run-status (and the UI) shows
	// what each step actually emitted, and so tests can assert on it.
	Artifacts []packs.Artifact `json:"artifacts,omitempty"`
	Error     string           `json:"error,omitempty"`
	StartedAt time.Time        `json:"started_at"`
	EndedAt   time.Time        `json:"ended_at,omitempty"`
}

// Run is one execution of a pipeline with its per-step history.
type Run struct {
	ID         string          `json:"id"`
	PipelineID string          `json:"pipeline_id"`
	Status     RunStatus       `json:"status"`
	Inputs     json.RawMessage `json:"inputs,omitempty"`
	Steps      []RunStep       `json:"steps"`
	Error      string          `json:"error,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	EndedAt    time.Time       `json:"ended_at,omitempty"`
}

// Validate checks a pipeline definition for structural soundness:
// non-empty name, at least one step, unique step IDs, every step's pack
// resolves (via packExists), and every ${{ steps.X... }} reference points
// at an EARLIER step. packExists may be nil to skip pack-existence checks
// (e.g. when validating a definition before the registry is available).
func Validate(p *Pipeline, packExists func(name, version string) bool) error {
	if p == nil {
		return fmt.Errorf("pipeline is nil")
	}
	if p.Name == "" {
		return fmt.Errorf("pipeline name is required")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("pipeline must have at least one step")
	}
	seen := make(map[string]bool, len(p.Steps))
	for i, s := range p.Steps {
		if s.ID == "" {
			return fmt.Errorf("step %d: id is required", i)
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate step id %q", s.ID)
		}
		if s.Pack == "" {
			return fmt.Errorf("step %q: pack is required", s.ID)
		}
		if packExists != nil && !packExists(s.Pack, s.Version) {
			return fmt.Errorf("step %q: unknown pack %q", s.ID, s.Pack)
		}
		// Every step ref must target a step that ran BEFORE this one.
		refs, err := extractStepRefs(s.Input)
		if err != nil {
			return fmt.Errorf("step %q: %w", s.ID, err)
		}
		for _, ref := range refs {
			if !seen[ref] {
				return fmt.Errorf("step %q references step %q which is not an earlier step", s.ID, ref)
			}
		}
		seen[s.ID] = true
	}
	return nil
}
