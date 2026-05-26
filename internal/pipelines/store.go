// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a pipeline or run id is unknown.
var ErrNotFound = errors.New("pipelines: not found")

// Store is the SQLite-backed persistence for pipeline definitions and
// run history. Raw database/sql with ? placeholders and RFC3339Nano
// timestamps, matching internal/audit and internal/store conventions.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore wraps an open *sql.DB (migrations already applied by store.Open).
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
}

const ts = time.RFC3339Nano

// Create inserts a new pipeline. The caller sets ID (server-generated for
// user pipelines, builtin.* for starters).
func (s *Store) Create(ctx context.Context, p *Pipeline) error {
	now := s.now()
	p.CreatedAt, p.UpdatedAt = now, now
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO pipelines (id, name, description, builtin, inputs_json, steps_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, boolToInt(p.Builtin), nullJSON(p.Inputs), string(steps),
		now.Format(ts), now.Format(ts))
	return err
}

// Get returns one pipeline by id.
func (s *Store) Get(ctx context.Context, id string) (*Pipeline, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, builtin, inputs_json, steps_json, created_at, updated_at
		FROM pipelines WHERE id = ?`, id)
	return scanPipeline(row)
}

// List returns all pipelines, builtins first then by name.
func (s *Store) List(ctx context.Context) ([]*Pipeline, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, builtin, inputs_json, steps_json, created_at, updated_at
		FROM pipelines ORDER BY builtin DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pipeline
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update replaces a pipeline's mutable fields. Returns ErrNotFound if the
// id doesn't exist.
func (s *Store) Update(ctx context.Context, p *Pipeline) error {
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return err
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
		UPDATE pipelines SET name=?, description=?, inputs_json=?, steps_json=?, updated_at=?
		WHERE id=?`,
		p.Name, p.Description, nullJSON(p.Inputs), string(steps), now.Format(ts), p.ID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// Delete removes a pipeline. Returns ErrNotFound if absent.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pipelines WHERE id=?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// Seed upserts a built-in pipeline idempotently, but only if every
// referenced pack exists (packExists). Returns a sentinel-free error when
// a pack is missing so the caller can skip-and-log. Builtins are marked
// builtin=1 so the REST layer can keep them read-only.
func (s *Store) Seed(ctx context.Context, p *Pipeline, packExists func(name, version string) bool) error {
	if err := Validate(p, packExists); err != nil {
		return err
	}
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return err
	}
	now := s.now().Format(ts)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO pipelines (id, name, description, builtin, inputs_json, steps_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  name=excluded.name, description=excluded.description,
		  inputs_json=excluded.inputs_json, steps_json=excluded.steps_json,
		  updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Description, nullJSON(p.Inputs), string(steps), now, now)
	return err
}

// --- runs ---

// CreateRun inserts a pending run row and returns it.
func (s *Store) CreateRun(ctx context.Context, r *Run) error {
	stepsJSON, err := json.Marshal(r.Steps)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO pipeline_runs (id, pipeline_id, status, inputs_json, steps_json, error, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.PipelineID, string(r.Status), nullJSON(r.Inputs), string(stepsJSON),
		nullStr(r.Error), r.StartedAt.Format(ts), nullTime(r.EndedAt))
	return err
}

// SaveRun persists the current run state (status, steps, error, ended_at).
func (s *Store) SaveRun(ctx context.Context, r *Run) error {
	stepsJSON, err := json.Marshal(r.Steps)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE pipeline_runs SET status=?, steps_json=?, error=?, ended_at=? WHERE id=?`,
		string(r.Status), string(stepsJSON), nullStr(r.Error), nullTime(r.EndedAt), r.ID)
	return err
}

// GetRun returns a single run by id.
func (s *Store) GetRun(ctx context.Context, runID string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, pipeline_id, status, inputs_json, steps_json, error, started_at, ended_at
		FROM pipeline_runs WHERE id=?`, runID)
	return scanRun(row)
}

// ListRuns returns recent runs for a pipeline, newest first.
func (s *Store) ListRuns(ctx context.Context, pipelineID string, limit int) ([]*Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, pipeline_id, status, inputs_json, steps_json, error, started_at, ended_at
		FROM pipeline_runs WHERE pipeline_id=? ORDER BY started_at DESC LIMIT ?`, pipelineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- scan helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func scanPipeline(row scanner) (*Pipeline, error) {
	var p Pipeline
	var builtin int
	var inputs sql.NullString
	var steps, created, updated string
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &builtin, &inputs, &steps, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Builtin = builtin != 0
	if inputs.Valid {
		p.Inputs = json.RawMessage(inputs.String)
	}
	if err := json.Unmarshal([]byte(steps), &p.Steps); err != nil {
		return nil, fmt.Errorf("decode steps for %s: %w", p.ID, err)
	}
	p.CreatedAt, _ = time.Parse(ts, created)
	p.UpdatedAt, _ = time.Parse(ts, updated)
	return &p, nil
}

func scanRun(row scanner) (*Run, error) {
	var r Run
	var inputs, errStr, ended sql.NullString
	var steps, started, status string
	if err := row.Scan(&r.ID, &r.PipelineID, &status, &inputs, &steps, &errStr, &started, &ended); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Status = RunStatus(status)
	if inputs.Valid {
		r.Inputs = json.RawMessage(inputs.String)
	}
	if errStr.Valid {
		r.Error = errStr.String
	}
	_ = json.Unmarshal([]byte(steps), &r.Steps)
	r.StartedAt, _ = time.Parse(ts, started)
	if ended.Valid {
		r.EndedAt, _ = time.Parse(ts, ended.String)
	}
	return &r, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullJSON(j json.RawMessage) any {
	if len(j) == 0 {
		return nil
	}
	return string(j)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(ts)
}

func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
