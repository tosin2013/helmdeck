-- T203: encrypted provider API key store.
--
-- Each row holds one provider credential. The plaintext API key is NEVER
-- stored — only the AES-256-GCM ciphertext, the per-row nonce, a
-- SHA-256 fingerprint (first 16 hex chars) for uniqueness checks, and
-- the last four characters of the plaintext for UI display. The master
-- encryption key lives outside the database, in the env var
-- HELMDECK_KEYSTORE_KEY (32 raw bytes hex-encoded).

CREATE TABLE IF NOT EXISTS provider_keys (
    id           TEXT    PRIMARY KEY,
    provider     TEXT    NOT NULL,
    label        TEXT    NOT NULL,
    ciphertext   BLOB    NOT NULL,
    nonce        BLOB    NOT NULL,
    fingerprint  TEXT    NOT NULL,
    last4        TEXT    NOT NULL,
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL,
    last_used_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_provider_keys_provider ON provider_keys(provider);
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_keys_provider_label ON provider_keys(provider, label);
