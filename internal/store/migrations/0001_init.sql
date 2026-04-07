-- Initial helmdeck schema (T108).
--
-- audit_log carries every API call against the control plane. Persistent
-- session state (T108 has sessions in-process only — durable session table
-- arrives in T701 alongside the K8s SessionRuntime backend).
--
-- Indexes are tuned for the Audit Logs panel filters specified in §8.8 of
-- the PRD: time range, session id, event type.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            TEXT    NOT NULL,
    severity      TEXT    NOT NULL,
    event_type    TEXT    NOT NULL,
    actor_subject TEXT,
    actor_client  TEXT,
    session_id    TEXT,
    method        TEXT,
    path          TEXT,
    status_code   INTEGER,
    payload_json  TEXT
);

CREATE INDEX IF NOT EXISTS idx_audit_log_ts      ON audit_log(ts);
CREATE INDEX IF NOT EXISTS idx_audit_log_session ON audit_log(session_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_event   ON audit_log(event_type);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor   ON audit_log(actor_subject);
