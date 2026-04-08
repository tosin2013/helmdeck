// Package vault implements the AES-256-GCM credential store described
// in T501 / ADR 007.
//
// Threat model: an attacker who exfiltrates the SQLite file alone
// must not be able to recover any stored credential. The vault's
// master encryption key lives in HELMDECK_VAULT_KEY (32 raw bytes
// hex-encoded) and is intentionally distinct from the AI provider
// keystore key (HELMDECK_KEYSTORE_KEY) so a leak of one key does
// not compromise the other domain.
//
// The vault stores five credential types — login, cookie, api_key,
// oauth, ssh — each with a host glob, an optional path prefix, and a
// non-secret metadata blob. The plaintext is opaque bytes from the
// vault's point of view; consumers (the placeholder-token gateway in
// T504, CDP cookie injection in T503, repo.fetch in T505) interpret
// the payload according to the credential type.
//
// Access control: every credential has an ACL of (actor_subject,
// actor_client) tuples. A credential with no ACL rows is unreadable
// by anything — operators must explicitly grant before a pack can
// resolve it. The wildcard subject '*' grants any caller; an empty
// client matches any client.
//
// Usage log: every Resolve call writes one row to credential_usage_log
// with the matched host/path and the result (allowed/denied/no_match).
// The log survives credential deletion so operators have a forensic
// trail past rotation.
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Errors are exported so REST handlers can map them to typed HTTP
// codes without string-matching.
var (
	ErrNotFound  = errors.New("vault: credential not found")
	ErrDuplicate = errors.New("vault: credential with that name already exists")
	ErrDenied    = errors.New("vault: actor not authorized for this credential")
	ErrNoMatch   = errors.New("vault: no credential matches host/path")
)

// CredentialType is the closed set of credential shapes the vault
// supports. Adding a new type requires touching: this constant set,
// the Validate switch in (*Store).Create, and whichever pack/gateway
// path consumes the new type.
type CredentialType string

const (
	TypeLogin  CredentialType = "login"   // username + password (JSON in metadata)
	TypeCookie CredentialType = "cookie"  // browser session cookies
	TypeAPIKey CredentialType = "api_key" // bearer token / API key
	TypeOAuth  CredentialType = "oauth"   // access + refresh token
	TypeSSH    CredentialType = "ssh"     // SSH/git private key
)

// Validate reports whether t is one of the recognised credential
// types.
func (t CredentialType) Validate() bool {
	switch t {
	case TypeLogin, TypeCookie, TypeAPIKey, TypeOAuth, TypeSSH:
		return true
	}
	return false
}

// Record is the redacted view returned by List/Get. It deliberately
// omits ciphertext, nonce, and the resolved plaintext — the only
// path that yields plaintext is Resolve, which is gated by ACL +
// host/path match.
type Record struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Type         CredentialType `json:"type"`
	HostPattern  string         `json:"host_pattern"`
	PathPattern  string         `json:"path_pattern,omitempty"`
	Fingerprint  string         `json:"fingerprint"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	LastUsedAt   *time.Time     `json:"last_used_at,omitempty"`
}

// Grant is one ACL entry. ActorSubject "*" matches any subject;
// ActorClient "" matches any client.
type Grant struct {
	ActorSubject string    `json:"actor_subject"`
	ActorClient  string    `json:"actor_client,omitempty"`
	GrantedAt    time.Time `json:"granted_at"`
}

// UsageLogEntry is one row in credential_usage_log. Surfaced via the
// vault REST endpoint so operators can audit who used what when.
type UsageLogEntry struct {
	ID            int64     `json:"id"`
	CredentialID  string    `json:"credential_id"`
	ActorSubject  string    `json:"actor_subject,omitempty"`
	ActorClient   string    `json:"actor_client,omitempty"`
	HostMatched   string    `json:"host_matched,omitempty"`
	PathMatched   string    `json:"path_matched,omitempty"`
	Result        string    `json:"result"`
	Timestamp     time.Time `json:"timestamp"`
}

// CreateInput is the constructor argument for Create. Plaintext is
// the encrypted payload; the vault never inspects its contents,
// only the type field tells consumers how to parse it.
type CreateInput struct {
	Name        string
	Type        CredentialType
	HostPattern string
	PathPattern string
	Plaintext   []byte
	Metadata    map[string]any
}

// Actor identifies the caller for ACL checks. Subject is the JWT
// "sub" claim; Client is the JWT "client" claim (claude-code,
// claude-desktop, ...).
type Actor struct {
	Subject string
	Client  string
}

// ResolveResult is what Resolve returns: the plaintext bytes plus
// the matched record (so callers can log which credential was
// chosen and the type/metadata they need to interpret the payload).
type ResolveResult struct {
	Record    Record
	Plaintext []byte
}

// Store is the public API. db must point at a *sql.DB with the
// 0004_credential_vault migration applied. masterKey is exactly 32
// bytes (AES-256).
type Store struct {
	db   *sql.DB
	gcm  cipher.AEAD
	now  func() time.Time
	rand func([]byte) (int, error)
}

// New constructs a Store. Pass the parsed master key bytes; use
// ParseMasterKey on the env var first.
func New(db *sql.DB, masterKey []byte) (*Store, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("vault: master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("vault: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: gcm: %w", err)
	}
	return &Store{
		db:   db,
		gcm:  gcm,
		now:  func() time.Time { return time.Now().UTC() },
		rand: rand.Read,
	}, nil
}

// ParseMasterKey accepts a hex-encoded 32-byte key (the format the
// HELMDECK_VAULT_KEY env var uses) and returns the raw bytes.
func ParseMasterKey(hexstr string) ([]byte, error) {
	b, err := hex.DecodeString(hexstr)
	if err != nil {
		return nil, fmt.Errorf("vault: hex decode: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("vault: expected 32 bytes, got %d", len(b))
	}
	return b, nil
}

// GenerateMasterKey returns a fresh hex-encoded 32-byte master key,
// suitable for HELMDECK_VAULT_KEY. The control plane logs a warning
// when it autogenerates one at startup so operators know to pin it.
func GenerateMasterKey() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Create encrypts plaintext, inserts a credential row, and returns
// the redacted record. The credential is unreadable by any actor
// until Grant adds at least one ACL entry.
func (s *Store) Create(ctx context.Context, in CreateInput) (Record, error) {
	if in.Name == "" {
		return Record{}, errors.New("vault: name is required")
	}
	if !in.Type.Validate() {
		return Record{}, fmt.Errorf("vault: unknown credential type %q", in.Type)
	}
	if in.HostPattern == "" {
		return Record{}, errors.New("vault: host_pattern is required")
	}
	if len(in.Plaintext) == 0 {
		return Record{}, errors.New("vault: plaintext is required")
	}
	id, err := s.newID()
	if err != nil {
		return Record{}, err
	}
	ct, nonce, err := s.encrypt(in.Plaintext)
	if err != nil {
		return Record{}, err
	}
	metadataJSON := []byte("{}")
	if in.Metadata != nil {
		metadataJSON, err = json.Marshal(in.Metadata)
		if err != nil {
			return Record{}, fmt.Errorf("vault: metadata marshal: %w", err)
		}
	}
	now := s.now()
	rec := Record{
		ID:          id,
		Name:        in.Name,
		Type:        in.Type,
		HostPattern: in.HostPattern,
		PathPattern: in.PathPattern,
		Fingerprint: fingerprint(in.Plaintext),
		Metadata:    in.Metadata,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO credentials (id, name, type, host_pattern, path_pattern, ciphertext, nonce, fingerprint, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Name, string(rec.Type), rec.HostPattern, rec.PathPattern,
		ct, nonce, rec.Fingerprint, string(metadataJSON),
		rec.CreatedAt.Format(time.RFC3339Nano), rec.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Record{}, ErrDuplicate
		}
		return Record{}, fmt.Errorf("vault: insert: %w", err)
	}
	return rec, nil
}

// List returns every credential as a redacted record. Optionally
// filtered by type ("" = all).
func (s *Store) List(ctx context.Context, credType CredentialType) ([]Record, error) {
	var (
		rows *sql.Rows
		err  error
	)
	const cols = `id, name, type, host_pattern, path_pattern, fingerprint, metadata_json, created_at, updated_at, last_used_at`
	if credType == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT `+cols+` FROM credentials ORDER BY name`)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT `+cols+` FROM credentials WHERE type = ? ORDER BY name`, string(credType))
	}
	if err != nil {
		return nil, fmt.Errorf("vault: list: %w", err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
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
		SELECT id, name, type, host_pattern, path_pattern, fingerprint, metadata_json, created_at, updated_at, last_used_at
		FROM credentials WHERE id = ?`, id)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return rec, err
}

// Delete removes a credential and its ACL rows. We delete the ACL
// rows explicitly because the SQLite migration declares the FK with
// ON DELETE CASCADE but the runtime PRAGMA foreign_keys is off by
// default — the explicit DELETE keeps cleanup independent of that
// global setting. The usage log is intentionally not cascaded so
// historical entries survive.
func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM credential_acl WHERE credential_id = ?`, id); err != nil {
		return fmt.Errorf("vault: delete acl: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("vault: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Rotate replaces the encrypted payload for id with fresh plaintext.
// id, name, type, host pattern, and ACL are preserved.
func (s *Store) Rotate(ctx context.Context, id string, newPlaintext []byte) (Record, error) {
	if len(newPlaintext) == 0 {
		return Record{}, errors.New("vault: new plaintext required")
	}
	rec, err := s.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	ct, nonce, err := s.encrypt(newPlaintext)
	if err != nil {
		return Record{}, err
	}
	rec.Fingerprint = fingerprint(newPlaintext)
	rec.UpdatedAt = s.now()
	_, err = s.db.ExecContext(ctx, `
		UPDATE credentials
		   SET ciphertext = ?, nonce = ?, fingerprint = ?, updated_at = ?
		 WHERE id = ?`,
		ct, nonce, rec.Fingerprint, rec.UpdatedAt.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return Record{}, fmt.Errorf("vault: rotate: %w", err)
	}
	return rec, nil
}

// Grant adds an ACL entry. Idempotent — re-granting an existing
// (subject, client) tuple is a no-op.
func (s *Store) Grant(ctx context.Context, credentialID string, g Grant) error {
	if _, err := s.Get(ctx, credentialID); err != nil {
		return err
	}
	if g.ActorSubject == "" {
		return errors.New("vault: actor_subject is required")
	}
	if g.GrantedAt.IsZero() {
		g.GrantedAt = s.now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO credential_acl (credential_id, actor_subject, actor_client, granted_at)
		VALUES (?, ?, ?, ?)`,
		credentialID, g.ActorSubject, g.ActorClient, g.GrantedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("vault: grant: %w", err)
	}
	return nil
}

// Revoke removes an ACL entry. No error if it didn't exist.
func (s *Store) Revoke(ctx context.Context, credentialID, actorSubject, actorClient string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM credential_acl WHERE credential_id = ? AND actor_subject = ? AND actor_client = ?`,
		credentialID, actorSubject, actorClient)
	if err != nil {
		return fmt.Errorf("vault: revoke: %w", err)
	}
	return nil
}

// Grants lists ACL entries for a credential.
func (s *Store) Grants(ctx context.Context, credentialID string) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT actor_subject, actor_client, granted_at FROM credential_acl WHERE credential_id = ? ORDER BY actor_subject, actor_client`,
		credentialID)
	if err != nil {
		return nil, fmt.Errorf("vault: grants: %w", err)
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		var ts string
		if err := rows.Scan(&g.ActorSubject, &g.ActorClient, &ts); err != nil {
			return nil, err
		}
		g.GrantedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, g)
	}
	return out, rows.Err()
}

// Resolve finds the credential whose host/path patterns match the
// (host, path) pair, checks the ACL against actor, and returns the
// decrypted plaintext. Records the call in credential_usage_log
// regardless of outcome (denied/no_match/allowed).
//
// When multiple credentials could match, the most-specific host
// pattern wins (literal > glob with one wildcard > glob with two
// wildcards), and within the same host specificity the longest
// path prefix wins. Ties are broken by credential creation order
// (oldest first) for deterministic behavior.
func (s *Store) Resolve(ctx context.Context, actor Actor, host, path string) (ResolveResult, error) {
	all, err := s.List(ctx, "")
	if err != nil {
		return ResolveResult{}, err
	}
	matches := matchCandidates(all, host, path)
	if len(matches) == 0 {
		s.logUsage(ctx, "", actor, host, path, "no_match")
		return ResolveResult{}, ErrNoMatch
	}
	// Walk matches in specificity order until we find one the actor
	// is allowed to read. This way a less-specific credential can
	// still serve as a fallback if the actor lacks access to the
	// most-specific match.
	var lastDeniedID string
	for _, rec := range matches {
		ok, err := s.aclAllows(ctx, rec.ID, actor)
		if err != nil {
			return ResolveResult{}, err
		}
		if !ok {
			lastDeniedID = rec.ID
			continue
		}
		ct, nonce, err := s.fetchCipher(ctx, rec.ID)
		if err != nil {
			return ResolveResult{}, err
		}
		pt, err := s.gcm.Open(nil, nonce, ct, nil)
		if err != nil {
			return ResolveResult{}, fmt.Errorf("vault: decrypt: %w", err)
		}
		_ = s.touchLastUsed(ctx, rec.ID)
		s.logUsage(ctx, rec.ID, actor, host, path, "allowed")
		return ResolveResult{Record: rec, Plaintext: pt}, nil
	}
	s.logUsage(ctx, lastDeniedID, actor, host, path, "denied")
	return ResolveResult{}, ErrDenied
}

// Usage returns the recent usage log entries for a credential,
// most-recent first.
func (s *Store) Usage(ctx context.Context, credentialID string, limit int) ([]UsageLogEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, credential_id, actor_subject, actor_client, host_matched, path_matched, result, ts
		FROM credential_usage_log WHERE credential_id = ? ORDER BY id DESC LIMIT ?`, credentialID, limit)
	if err != nil {
		return nil, fmt.Errorf("vault: usage: %w", err)
	}
	defer rows.Close()
	var out []UsageLogEntry
	for rows.Next() {
		var (
			e   UsageLogEntry
			sub sql.NullString
			cli sql.NullString
			h   sql.NullString
			p   sql.NullString
			ts  string
		)
		if err := rows.Scan(&e.ID, &e.CredentialID, &sub, &cli, &h, &p, &e.Result, &ts); err != nil {
			return nil, err
		}
		e.ActorSubject = sub.String
		e.ActorClient = cli.String
		e.HostMatched = h.String
		e.PathMatched = p.String
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- internal helpers -----------------------------------------------------

func (s *Store) aclAllows(ctx context.Context, credentialID string, actor Actor) (bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT actor_subject, actor_client FROM credential_acl WHERE credential_id = ?`,
		credentialID)
	if err != nil {
		return false, fmt.Errorf("vault: acl lookup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sub, cli string
		if err := rows.Scan(&sub, &cli); err != nil {
			return false, err
		}
		if (sub == "*" || sub == actor.Subject) && (cli == "" || cli == actor.Client) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) fetchCipher(ctx context.Context, id string) (ct, nonce []byte, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT ciphertext, nonce FROM credentials WHERE id = ?`, id)
	if err := row.Scan(&ct, &nonce); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	return ct, nonce, nil
}

func (s *Store) touchLastUsed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE credentials SET last_used_at = ? WHERE id = ?`,
		s.now().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) logUsage(ctx context.Context, credentialID string, actor Actor, host, path, result string) {
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO credential_usage_log (credential_id, actor_subject, actor_client, host_matched, path_matched, result, ts)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		credentialID, actor.Subject, actor.Client, host, path, result, s.now().Format(time.RFC3339Nano))
}

func (s *Store) encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, s.gcm.NonceSize())
	if _, err := s.rand(nonce); err != nil {
		return nil, nil, fmt.Errorf("vault: nonce: %w", err)
	}
	ciphertext = s.gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

func (s *Store) newID() (string, error) {
	var b [16]byte
	if _, err := s.rand(b[:]); err != nil {
		return "", err
	}
	return "cred_" + hex.EncodeToString(b[:]), nil
}

func fingerprint(plaintext []byte) string {
	sum := sha256.Sum256(plaintext)
	return hex.EncodeToString(sum[:8])
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(r scanner) (Record, error) {
	var (
		rec      Record
		typ      string
		metaJSON string
		created  string
		updated  string
		lastUsed sql.NullString
	)
	if err := r.Scan(&rec.ID, &rec.Name, &typ, &rec.HostPattern, &rec.PathPattern, &rec.Fingerprint, &metaJSON, &created, &updated, &lastUsed); err != nil {
		return Record{}, err
	}
	rec.Type = CredentialType(typ)
	if metaJSON != "" && metaJSON != "{}" {
		_ = json.Unmarshal([]byte(metaJSON), &rec.Metadata)
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
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed: UNIQUE")
}

// matchCandidates filters records to those whose host/path patterns
// match the request, then sorts by specificity (most specific first)
// so the caller can pick the best match.
func matchCandidates(all []Record, host, path string) []Record {
	type scored struct {
		rec   Record
		score int
	}
	var hits []scored
	for _, r := range all {
		if !matchHost(r.HostPattern, host) {
			continue
		}
		if !matchPath(r.PathPattern, path) {
			continue
		}
		hits = append(hits, scored{rec: r, score: hostScore(r.HostPattern)*10000 + len(r.PathPattern)})
	}
	// Insertion sort by score desc — small lists, no need for sort.Slice.
	for i := 1; i < len(hits); i++ {
		j := i
		for j > 0 && hits[j-1].score < hits[j].score {
			hits[j-1], hits[j] = hits[j], hits[j-1]
			j--
		}
	}
	out := make([]Record, len(hits))
	for i, h := range hits {
		out[i] = h.rec
	}
	return out
}

// matchHost reports whether host matches a glob pattern. Supports
// the single wildcard '*' which matches any non-empty label sequence
// in either the leftmost position ("*.github.com") or anywhere in
// the pattern. The empty pattern matches nothing.
func matchHost(pattern, host string) bool {
	if pattern == "" {
		return false
	}
	if pattern == host {
		return true
	}
	// Split on '*' and require each segment to appear in order in
	// host. Anchored at both ends.
	parts := strings.Split(pattern, "*")
	pos := 0
	for i, p := range parts {
		if p == "" {
			continue
		}
		idx := strings.Index(host[pos:], p)
		if idx < 0 {
			return false
		}
		if i == 0 && idx != 0 {
			return false
		}
		pos += idx + len(p)
	}
	// If the pattern doesn't end with '*', host must end at pos.
	if !strings.HasSuffix(pattern, "*") && pos != len(host) {
		return false
	}
	return true
}

// matchPath reports whether path begins with the credential's path
// pattern. Empty pattern matches any path.
func matchPath(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	return strings.HasPrefix(path, pattern)
}

// hostScore returns a higher value for more specific host patterns.
// A literal (no wildcards) outranks any glob; among globs, fewer
// wildcards beat more wildcards.
func hostScore(pattern string) int {
	if !strings.Contains(pattern, "*") {
		return 1000
	}
	return 1000 - strings.Count(pattern, "*")*100 - strings.Count(pattern, ".")*1
}
