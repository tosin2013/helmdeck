// Package keystore implements the AES-256-GCM encrypted provider API
// key store described in T203 / ADR 005 / ADR 007.
//
// Threat model: an attacker who exfiltrates the SQLite file alone must
// not be able to recover any provider API key. The master encryption
// key lives in the HELMDECK_KEYSTORE_KEY environment variable (32 raw
// bytes, hex-encoded — same format as HELMDECK_JWT_SECRET) and is
// never persisted by helmdeck. The keystore returns ciphertext + nonce
// + a non-reversible fingerprint via list/get operations; the only
// path that yields plaintext is Decrypt, which is gated to internal
// callers (the AI gateway dispatcher and the provider-test endpoint).
package keystore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a key id does not exist.
var ErrNotFound = errors.New("provider key not found")

// ErrDuplicate is returned when (provider, label) already exists.
var ErrDuplicate = errors.New("provider key with that label already exists")

// Record is the redacted view returned by List/Get. It deliberately
// omits ciphertext and nonce — UI code has no business with either.
type Record struct {
	ID          string     `json:"id"`
	Provider    string     `json:"provider"`
	Label       string     `json:"label"`
	Fingerprint string     `json:"fingerprint"`
	Last4       string     `json:"last4"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// Store is the public API. db must point at a *sql.DB opened by
// internal/store.Open so the migrations have run.
type Store struct {
	db   *sql.DB
	gcm  cipher.AEAD
	now  func() time.Time
	rand func([]byte) (int, error) // injectable for tests
}

// New constructs a Store. masterKey must be exactly 32 bytes (AES-256).
// Call ParseMasterKey on the env var first if you need hex parsing.
func New(db *sql.DB, masterKey []byte) (*Store, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("keystore: master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("keystore: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keystore: gcm: %w", err)
	}
	return &Store{
		db:   db,
		gcm:  gcm,
		now:  func() time.Time { return time.Now().UTC() },
		rand: rand.Read,
	}, nil
}

// ParseMasterKey accepts a hex-encoded 32-byte key (the format the env
// var uses) and returns the raw bytes. Mirrors auth.ParseSecret.
func ParseMasterKey(hexstr string) ([]byte, error) {
	b, err := hex.DecodeString(hexstr)
	if err != nil {
		return nil, fmt.Errorf("keystore: hex decode: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("keystore: expected 32 bytes, got %d", len(b))
	}
	return b, nil
}

// GenerateMasterKey returns a fresh hex-encoded 32-byte master key,
// suitable for HELMDECK_KEYSTORE_KEY. The control plane logs a warning
// when it autogenerates one at startup so operators know they need to
// pin it for persistence across restarts.
func GenerateMasterKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Create encrypts plaintext and inserts a new row. Returns the
// redacted Record (so callers never accidentally pass the plaintext
// onward).
func (s *Store) Create(ctx context.Context, provider, label, plaintext string) (Record, error) {
	if provider == "" || label == "" || plaintext == "" {
		return Record{}, errors.New("keystore: provider, label, and key are required")
	}
	id, err := s.newID()
	if err != nil {
		return Record{}, err
	}
	ct, nonce, err := s.encrypt([]byte(plaintext))
	if err != nil {
		return Record{}, err
	}
	now := s.now()
	rec := Record{
		ID:          id,
		Provider:    provider,
		Label:       label,
		Fingerprint: fingerprint(plaintext),
		Last4:       last4(plaintext),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO provider_keys (id, provider, label, ciphertext, nonce, fingerprint, last4, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Provider, rec.Label, ct, nonce, rec.Fingerprint, rec.Last4,
		rec.CreatedAt.Format(time.RFC3339Nano), rec.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		// modernc.org/sqlite returns "constraint failed" on UNIQUE.
		if isUniqueViolation(err) {
			return Record{}, ErrDuplicate
		}
		return Record{}, fmt.Errorf("keystore: insert: %w", err)
	}
	return rec, nil
}

// List returns all redacted records, optionally filtered by provider.
func (s *Store) List(ctx context.Context, provider string) ([]Record, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if provider == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, provider, label, fingerprint, last4, created_at, updated_at, last_used_at
			FROM provider_keys ORDER BY provider, label`)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, provider, label, fingerprint, last4, created_at, updated_at, last_used_at
			FROM provider_keys WHERE provider = ? ORDER BY label`, provider)
	}
	if err != nil {
		return nil, fmt.Errorf("keystore: list: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Get returns the redacted record for id.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, provider, label, fingerprint, last4, created_at, updated_at, last_used_at
		FROM provider_keys WHERE id = ?`, id)
	rec, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return rec, err
}

// Decrypt returns the plaintext API key for id and stamps last_used_at.
// This is the ONLY path that yields plaintext; callers (gateway
// dispatcher, provider-test endpoint) must not log or echo the result.
func (s *Store) Decrypt(ctx context.Context, id string) (string, error) {
	var ct, nonce []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext, nonce FROM provider_keys WHERE id = ?`, id).Scan(&ct, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keystore: select: %w", err)
	}
	pt, err := s.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("keystore: decrypt: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE provider_keys SET last_used_at = ? WHERE id = ?`,
		s.now().Format(time.RFC3339Nano), id); err != nil {
		// Best-effort timestamp; do NOT fail the decrypt on a bookkeeping
		// error because that would break every chat completion.
		_ = err
	}
	return string(pt), nil
}

// Rotate replaces the encrypted key value for id with a fresh plaintext.
// The id, provider, and label are preserved so any external references
// remain valid — this is the "swap the secret without changing its
// identity" operation operators expect from a rotation API.
func (s *Store) Rotate(ctx context.Context, id, newPlaintext string) (Record, error) {
	if newPlaintext == "" {
		return Record{}, errors.New("keystore: new key required")
	}
	rec, err := s.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	ct, nonce, err := s.encrypt([]byte(newPlaintext))
	if err != nil {
		return Record{}, err
	}
	rec.Fingerprint = fingerprint(newPlaintext)
	rec.Last4 = last4(newPlaintext)
	rec.UpdatedAt = s.now()
	_, err = s.db.ExecContext(ctx, `
		UPDATE provider_keys
		   SET ciphertext = ?, nonce = ?, fingerprint = ?, last4 = ?, updated_at = ?
		 WHERE id = ?`,
		ct, nonce, rec.Fingerprint, rec.Last4, rec.UpdatedAt.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return Record{}, fmt.Errorf("keystore: rotate: %w", err)
	}
	return rec, nil
}

// Delete removes a key by id. Returns ErrNotFound if missing.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM provider_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("keystore: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- internal helpers -----------------------------------------------------

func (s *Store) encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, s.gcm.NonceSize())
	if _, err := s.rand(nonce); err != nil {
		return nil, nil, fmt.Errorf("keystore: nonce: %w", err)
	}
	ciphertext = s.gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

func (s *Store) newID() (string, error) {
	var b [16]byte
	if _, err := s.rand(b[:]); err != nil {
		return "", err
	}
	return "pk_" + hex.EncodeToString(b[:]), nil
}

// fingerprint is a non-reversible identifier safe to log and surface in
// the UI. SHA-256 truncated to 16 hex chars (64 bits) — collision risk
// is negligible at the scale of provider keys per tenant.
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func last4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRow(r scanner) (Record, error) {
	var rec Record
	var created, updated string
	var lastUsed sql.NullString
	if err := r.Scan(&rec.ID, &rec.Provider, &rec.Label, &rec.Fingerprint, &rec.Last4, &created, &updated, &lastUsed); err != nil {
		return Record{}, err
	}
	rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if lastUsed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		rec.LastUsedAt = &t
	}
	return rec, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc.org/sqlite formats unique-constraint failures with this prefix.
	return contains(msg, "UNIQUE constraint failed") || contains(msg, "constraint failed: UNIQUE")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
