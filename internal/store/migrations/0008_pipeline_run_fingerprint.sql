-- 0008_pipeline_run_fingerprint.sql — single-flight coalescing for
-- duplicate concurrent pipeline-run requests.
--
-- Motivation: some MCP clients (LLM-driven agents) time out on a
-- long-running pipeline-run call before the underlying pipeline
-- finishes and then RETRY the same call with the same inputs. The
-- original run is still in-flight; the retry starts a SECOND
-- identical run. With pipelines like slides.narrate, two concurrent
-- runs against the same memory/quota/encoder budget reliably OOM
-- both — the exact failure we just fixed for the single-run case
-- via PR #390's thread cap + adaptive retry.
--
-- Fix: every run row records a deterministic fingerprint =
-- sha256(caller || pipeline_id || canonical_json(inputs)). On a new
-- StartRun, the runner looks up any pending/running row with the
-- same fingerprint and, if one exists, returns its id instead of
-- inserting a duplicate. Both columns are additive with safe defaults
-- so a downgrade-to-prev-binary keeps reading old rows fine.
--
-- The partial unique index enforces the "at most one in-flight per
-- fingerprint" invariant at the DB level — protects the
-- millisecond-window race where two StartRun goroutines both miss
-- the lookup before either has inserted. Empty-fingerprint rows (the
-- backfill default on pre-existing rows) are excluded from the
-- uniqueness check so legacy data doesn't trip the constraint.

ALTER TABLE pipeline_runs ADD COLUMN caller TEXT NOT NULL DEFAULT '';
ALTER TABLE pipeline_runs ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS uniq_pipeline_runs_inflight_fingerprint
    ON pipeline_runs(fingerprint)
    WHERE fingerprint <> '' AND status IN ('pending','running');
