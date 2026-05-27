-- 0007_pipelines.sql — Pipelines as a first-class resource (ADR 041, v0.15.0).
--
-- A pipeline is an ordered list of pack steps with ${{ }}-templated
-- inputs. Unlike packs (which carry Go closures and live in an in-memory
-- registry) a pipeline definition is pure data, so it persists here.
-- Built-in starter pipelines are upserted at startup with a stable
-- builtin.* id, so re-seeding is idempotent and operators can clone them.
--
-- pipeline_runs records each execution + its per-step history. steps_json
-- is rewritten on each step transition (whole-array update) — fine for the
-- short chains pipelines run. Additive only (CREATE TABLE); a v<prev>
-- binary ignores these tables.

CREATE TABLE IF NOT EXISTS pipelines (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    builtin     INTEGER NOT NULL DEFAULT 0,
    inputs_json TEXT,
    steps_json  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pipeline_runs (
    id          TEXT PRIMARY KEY,            -- "run_" + 16 hex
    pipeline_id TEXT NOT NULL,
    status      TEXT NOT NULL,               -- pending|running|succeeded|failed
    inputs_json TEXT,
    steps_json  TEXT NOT NULL DEFAULT '[]',  -- JSON array of RunStep, updated as the run progresses
    error       TEXT,
    started_at  TEXT NOT NULL,
    ended_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_pipeline_runs_pipeline_id
    ON pipeline_runs(pipeline_id, started_at DESC);
