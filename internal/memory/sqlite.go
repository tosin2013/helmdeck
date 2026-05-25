package memory

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SQLiteStore is the durable MemoryStore backend. It encrypts every
// value at rest with AES-256-GCM using the exact construction the
// credential vault uses (internal/vault/vault.go:New) — a leak of the
// SQLite file alone yields only ciphertext. The fingerprint
// (sha256(plaintext)[:16]) is stored in the clear for cache coherence
// and is safe to log.
//
// The table (memory_entries, migration 0006) carries a
// UNIQUE(namespace,key) constraint; Put upserts via INSERT ... ON
// CONFLICT. Expiry is lazy: reads filter on expires_at, and
// DeleteExpired performs the bulk sweep the janitor calls.
type SQLiteStore struct {
	db  *sql.DB
	gcm cipher.AEAD
	now func() time.Time
}

// NewSQLiteStore constructs a SQLiteStore. key must be exactly 32
// bytes (AES-256), the same shape as the vault/keystore master keys.
func NewSQLiteStore(db *sql.DB, key []byte) (*SQLiteStore, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("memory: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("memory: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("memory: gcm: %w", err)
	}
	return &SQLiteStore{
		db:  db,
		gcm: gcm,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *SQLiteStore) Put(ctx context.Context, ns, key string, value []byte, opts ...PutOption) (Entry, error) {
	cfg := applyPutOptions(opts)
	now := s.now()

	ct, nonce, err := s.encrypt(value)
	if err != nil {
		return Entry{}, err
	}
	tagsJSON, err := json.Marshal(append([]string{}, cfg.tags...))
	if err != nil {
		return Entry{}, fmt.Errorf("memory: tags marshal: %w", err)
	}
	id, err := s.newID()
	if err != nil {
		return Entry{}, err
	}
	fp := Fingerprint(value)
	var expiresAt sql.NullString
	e := Entry{
		Namespace:   ns,
		Key:         key,
		Value:       append([]byte(nil), value...),
		Category:    cfg.category,
		Tags:        append([]string(nil), cfg.tags...),
		CreatedAt:   now,
		UpdatedAt:   now,
		Fingerprint: fp,
	}
	if cfg.ttl > 0 {
		e.ExpiresAt = now.Add(cfg.ttl)
		expiresAt = sql.NullString{String: e.ExpiresAt.Format(time.RFC3339Nano), Valid: true}
	}

	// Upsert on UNIQUE(namespace,key). On conflict we refresh the
	// ciphertext, metadata, updated_at and expiry but preserve the
	// original created_at (via the existing row, COALESCE keeps it).
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_entries
			(id, namespace, key, value_ciphertext, value_nonce, fingerprint, category, tags_json, created_at, updated_at, expires_at, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')
		ON CONFLICT(namespace, key) DO UPDATE SET
			value_ciphertext = excluded.value_ciphertext,
			value_nonce      = excluded.value_nonce,
			fingerprint      = excluded.fingerprint,
			category         = excluded.category,
			tags_json        = excluded.tags_json,
			updated_at       = excluded.updated_at,
			expires_at       = excluded.expires_at`,
		id, ns, key, ct, nonce, fp, cfg.category, string(tagsJSON),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), expiresAt,
	)
	if err != nil {
		return Entry{}, fmt.Errorf("memory: put: %w", err)
	}
	// Re-read created_at so the returned entry reflects the true
	// creation time on an upsert (the row may pre-date this call).
	var createdStr string
	if err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM memory_entries WHERE namespace = ? AND key = ?`, ns, key).Scan(&createdStr); err == nil {
		if t, perr := time.Parse(time.RFC3339Nano, createdStr); perr == nil {
			e.CreatedAt = t
		}
	}
	return e, nil
}

const memCols = `namespace, key, value_ciphertext, value_nonce, fingerprint, category, tags_json, created_at, updated_at, expires_at`

func (s *SQLiteStore) Get(ctx context.Context, ns, key string) (Entry, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+memCols+` FROM memory_entries WHERE namespace = ? AND key = ?`, ns, key)
	e, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if s.isExpired(e) {
		// Lazy-expire: drop the stale row and report not found.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM memory_entries WHERE namespace = ? AND key = ?`, ns, key)
		return Entry{}, ErrNotFound
	}
	return e, nil
}

func (s *SQLiteStore) List(ctx context.Context, ns, prefix string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+memCols+` FROM memory_entries WHERE namespace = ? AND key LIKE ? ORDER BY updated_at DESC`,
		ns, likePrefix(prefix))
	if err != nil {
		return nil, fmt.Errorf("memory: list: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		if s.isExpired(e) {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Delete(ctx context.Context, ns, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memory_entries WHERE namespace = ? AND key = ?`, ns, key)
	if err != nil {
		return fmt.Errorf("memory: delete: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListAll(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+memCols+` FROM memory_entries ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("memory: list all: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteExpired(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM memory_entries WHERE expires_at IS NOT NULL AND expires_at <= ?`,
		s.now().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("memory: delete expired: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- internal helpers -----------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func (s *SQLiteStore) scan(r scanner) (Entry, error) {
	var (
		e        Entry
		ct       []byte
		nonce    []byte
		tagsJSON string
		created  string
		updated  string
		expires  sql.NullString
	)
	if err := r.Scan(&e.Namespace, &e.Key, &ct, &nonce, &e.Fingerprint, &e.Category, &tagsJSON, &created, &updated, &expires); err != nil {
		return Entry{}, err
	}
	pt, err := s.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("memory: decrypt: %w", err)
	}
	e.Value = pt
	if tagsJSON != "" && tagsJSON != "[]" {
		_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
	}
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if expires.Valid {
		e.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires.String)
	}
	return e, nil
}

func (s *SQLiteStore) isExpired(e Entry) bool {
	return !e.ExpiresAt.IsZero() && !s.now().Before(e.ExpiresAt)
}

func (s *SQLiteStore) encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce, err = randomBytes(s.gcm.NonceSize())
	if err != nil {
		return nil, nil, fmt.Errorf("memory: nonce: %w", err)
	}
	ciphertext = s.gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

func (s *SQLiteStore) newID() (string, error) {
	b, err := randomBytes(16)
	if err != nil {
		return "", err
	}
	return "mem_" + hex.EncodeToString(b), nil
}

// likePrefix escapes LIKE wildcards in prefix and appends '%'. An
// empty prefix becomes "%" (match all). We escape with backslash and
// pair it with an ESCAPE clause would be ideal, but SQLite's default
// LIKE has no escape char; instead we replace the wildcards so a key
// prefix containing % or _ is matched literally enough for our keys
// (which are slash/hex/hash shaped and never contain wildcards).
func likePrefix(prefix string) string {
	if prefix == "" {
		return "%"
	}
	return prefix + "%"
}
