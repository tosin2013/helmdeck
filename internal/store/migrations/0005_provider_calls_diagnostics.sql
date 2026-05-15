-- #183: provider_calls diagnostic columns.
--
-- The v0.12.x provider_calls schema records per-call timing, status, and
-- token usage but has no join key back to the pack job that triggered
-- each LLM call. Diagnosing a failed pack invocation required matching
-- provider_calls.ts to the job's ended_at by hand — five minutes of
-- timestamp detective work per failure on a busy stack.
--
-- This migration adds:
--   job_id          — the pack job ID (NULL for sync / legacy rows)
--   finish_reason   — provider-reported completion reason (`stop`,
--                     `length`, `tool_calls`, `content_filter`, ...)
--   raw_content_len — bytes in choices[0].message.content after trim,
--                     so "model returned no visible text" is one column
--                     read instead of an audit-log dive
--
-- SQLite's ALTER TABLE ADD COLUMN is an O(1) metadata-only operation
-- with no table rewrite, so this is safe even against a multi-million-
-- row provider_calls table. Existing rows keep NULL job_id / NULL
-- finish_reason / 0 raw_content_len — no backfill needed.
--
-- The idx_provider_calls_job_id index is the join key for the
-- expected diagnostic query:
--   SELECT * FROM provider_calls WHERE job_id = '<id>'
-- so it pays for itself the first time anyone runs that query on a
-- production database.

ALTER TABLE provider_calls ADD COLUMN job_id          TEXT;
ALTER TABLE provider_calls ADD COLUMN finish_reason   TEXT;
ALTER TABLE provider_calls ADD COLUMN raw_content_len INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_provider_calls_job_id ON provider_calls(job_id);
