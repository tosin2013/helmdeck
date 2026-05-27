-- 0006_memory_entries.sql
--
-- Universal Memory delivery layer (ADR 039, epic #254).
--
-- One table — memory_entries — backing the pluggable MemoryStore
-- (internal/memory). Each row is a namespace-scoped key/value entry
-- with optional TTL, a category + tags for Context() aggregation, and
-- an AES-256-GCM-encrypted value.
--
-- Encryption: value_ciphertext + value_nonce are produced with the
-- same construction the credential vault uses (AES-256-GCM, random
-- nonce per write), keyed by HELMDECK_MEMORY_KEY (32 raw bytes
-- hex-encoded) — intentionally distinct from the keystore and vault
-- keys so a leak of one domain's key does not expose memory. The
-- fingerprint (sha256(plaintext)[:16]) is stored in the clear for
-- cache coherence and is safe to log.
--
-- Expiry is lazy: reads filter on expires_at and the janitor sweep
-- (DeleteExpired) bulk-removes stale rows. expires_at IS NULL means
-- the entry never expires.

CREATE TABLE IF NOT EXISTS memory_entries (
    id                TEXT PRIMARY KEY,
    namespace         TEXT NOT NULL,
    key               TEXT NOT NULL,
    value_ciphertext  BLOB NOT NULL,
    value_nonce       BLOB NOT NULL,
    fingerprint       TEXT NOT NULL,                 -- sha256[:16] of plaintext, safe to log
    category          TEXT NOT NULL DEFAULT '',      -- 'cache' | 'solve' | 'repo' | ...
    tags_json         TEXT NOT NULL DEFAULT '[]',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    expires_at        TEXT,                          -- NULL = never expires
    metadata_json     TEXT NOT NULL DEFAULT '{}',
    UNIQUE (namespace, key)
);

CREATE INDEX IF NOT EXISTS idx_memory_namespace  ON memory_entries(namespace);
CREATE INDEX IF NOT EXISTS idx_memory_expires_at ON memory_entries(expires_at);
