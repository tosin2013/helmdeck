-- 0004_credential_vault.sql
--
-- Credential vault tables (T501, ADR 007).
--
-- Three tables:
--
--   credentials           — encrypted secret + host/path pattern + non-secret metadata
--   credential_acl        — per-credential allow list of (actor_subject, actor_client) tuples
--   credential_usage_log  — append-only history of every Resolve call
--
-- The encryption key is HELMDECK_VAULT_KEY (32 raw bytes hex-encoded),
-- intentionally distinct from HELMDECK_KEYSTORE_KEY (the AI provider
-- key store, T203). Both keys MUST be provided separately so a leak of
-- one does not automatically expose the other. Key handling matches
-- the keystore pattern: parse at startup, never persist, autogenerate
-- with a loud warning when missing in dev mode.
--
-- Pattern matching: host_pattern is a glob ("*.github.com",
-- "api.example.com"); path_pattern is an optional URL path prefix
-- ("/repos/", left empty for whole-host match). Resolve(host, path)
-- returns the credential whose patterns match — the vault store does
-- the matching in Go because SQL globs aren't portable.

CREATE TABLE IF NOT EXISTS credentials (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    type            TEXT NOT NULL,        -- 'login' | 'cookie' | 'api_key' | 'oauth' | 'ssh'
    host_pattern    TEXT NOT NULL,        -- glob: "*.github.com"
    path_pattern    TEXT NOT NULL DEFAULT '',  -- prefix: "/repos/" or ""
    ciphertext      BLOB NOT NULL,
    nonce           BLOB NOT NULL,
    fingerprint     TEXT NOT NULL,        -- sha256[:16] of plaintext, safe to log
    metadata_json   TEXT NOT NULL DEFAULT '{}',  -- non-secret hints (e.g. username for login)
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    last_used_at    TEXT
);

CREATE INDEX IF NOT EXISTS idx_credentials_type        ON credentials(type);
CREATE INDEX IF NOT EXISTS idx_credentials_host        ON credentials(host_pattern);

-- ACL: each row grants an (actor_subject, actor_client) tuple read
-- access to one credential. actor_subject = '*' means "any subject";
-- actor_client = '' means "any client". A credential with no ACL
-- rows is unreadable by anything — operators must explicitly grant
-- before a pack can resolve it.
CREATE TABLE IF NOT EXISTS credential_acl (
    credential_id   TEXT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    actor_subject   TEXT NOT NULL,
    actor_client    TEXT NOT NULL DEFAULT '',
    granted_at      TEXT NOT NULL,
    PRIMARY KEY (credential_id, actor_subject, actor_client)
);

-- Usage log: one row per Resolve call. Append-only and intentionally
-- denormalized — credential_id is not a foreign key so the log
-- survives credential deletion (operators expect a forensic trail to
-- outlive the credential it tracks).
CREATE TABLE IF NOT EXISTS credential_usage_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id   TEXT NOT NULL,
    actor_subject   TEXT,
    actor_client    TEXT,
    host_matched    TEXT,
    path_matched    TEXT,
    result          TEXT NOT NULL,         -- 'allowed' | 'denied' | 'no_match' | 'expired'
    ts              TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_credential_usage_credential ON credential_usage_log(credential_id);
CREATE INDEX IF NOT EXISTS idx_credential_usage_ts         ON credential_usage_log(ts);
