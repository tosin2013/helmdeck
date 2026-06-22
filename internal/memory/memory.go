// Package memory implements the Universal Memory delivery layer
// described in ADR 039 (epic #254). It gives packs and agents a
// namespace-scoped, TTL-aware key/value store so capabilities like
// swe.solve can recall prior context and read-heavy packs (github.*)
// can cache responses.
//
// The package is intentionally backend-pluggable: MemoryStore is the
// contract, InMemoryStore is the dev/test default, and SQLiteStore
// (sqlite.go) is the production default with AES-256-GCM encryption at
// rest. Redis-backed episodic memory and a pgvector semantic tier are
// deferred per ADR 039 — the interface keeps that door open.
//
// Design notes:
//   - The store is namespace-explicit at the backend layer; the engine
//     wraps it into a namespace-scoped handle (packs.MemoryInterface)
//     so handlers never pass a namespace by hand.
//   - memory MUST NOT import internal/packs (the engine imports memory,
//     not the other way around) to avoid an import cycle.
//   - Values are opaque bytes; categories and tags are non-secret
//     metadata used by Context() aggregation and the cache seam.
package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a (namespace, key) pair does not exist
// or has expired. Backends map their native "no rows" error (e.g.
// sql.ErrNoRows) to this sentinel, mirroring internal/keystore and
// internal/vault, so callers can errors.Is without string-matching.
var ErrNotFound = errors.New("memory: entry not found")

// Entry is one stored value plus its metadata. Value holds the
// plaintext bytes (decrypted on read by encrypting backends).
// Fingerprint is sha256(plaintext)[:16 hex] — safe to log and used for
// cache coherence. ExpiresAt is the zero time when the entry never
// expires.
type Entry struct {
	Namespace   string    `json:"namespace"`
	Key         string    `json:"key"`
	Value       []byte    `json:"value"`
	Category    string    `json:"category,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}

// MemoryStore is the backend contract. It is namespace-explicit: the
// engine resolves a caller's namespace once and threads it on every
// call. List filters by key prefix within a namespace. ListAll and
// DeleteExpired are the janitor surface (cross-namespace), mirroring
// ArtifactStore's ListAll + Delete.
// NamespaceCount is one row in MemoryStore.ListNamespaces — a caller
// subject plus the count of memory rows that caller has written.
type NamespaceCount struct {
	Namespace string `json:"namespace"`
	Count     int    `json:"count"`
}

type MemoryStore interface {
	Put(ctx context.Context, ns, key string, value []byte, opts ...PutOption) (Entry, error)
	Get(ctx context.Context, ns, key string) (Entry, error)
	List(ctx context.Context, ns, prefix string) ([]Entry, error)
	Delete(ctx context.Context, ns, key string) error
	// ListNamespaces returns every namespace currently present in the
	// store plus the row count per namespace. Used by the Routing
	// Memory caller-selector UI so admins can see which caller has
	// activity to inspect. NamespaceCount entries are sorted by row
	// count descending — the busiest caller floats to the top.
	ListNamespaces(ctx context.Context) ([]NamespaceCount, error)
	// DeletePrefix removes EVERY entry in (ns) whose key starts with
	// prefix. Returns the deleted row count. The key difference from
	// List + per-key Delete is that DeletePrefix never decrypts —
	// the SQL DELETE works on raw rows. This lets `forget` clear
	// orphaned entries left by ephemeral-key rotation (memory-key
	// changed between writes and reads → decrypt fails on every
	// list-then-delete pass → forget gets stuck). An empty prefix
	// matches every key in the namespace.
	DeletePrefix(ctx context.Context, ns, prefix string) (int, error)
	ListAll(ctx context.Context) ([]Entry, error)
	DeleteExpired(ctx context.Context) (int, error)
}

// PutOption configures a Put. Functional options keep Put extensible
// without churning the signature as new metadata lands.
type PutOption func(*putConfig)

type putConfig struct {
	ttl      time.Duration
	category string
	tags     []string
}

// WithTTL sets a relative expiry. A zero or negative duration means
// "never expires".
func WithTTL(d time.Duration) PutOption {
	return func(c *putConfig) { c.ttl = d }
}

// WithCategory tags the entry with a category used by Context()
// grouping (e.g. "cache", "solve", "repo").
func WithCategory(s string) PutOption {
	return func(c *putConfig) { c.category = s }
}

// WithTags attaches free-form tags to the entry.
func WithTags(tags ...string) PutOption {
	return func(c *putConfig) { c.tags = append(c.tags, tags...) }
}

func applyPutOptions(opts []PutOption) putConfig {
	var c putConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// Fingerprint returns the cache-coherence fingerprint for a plaintext
// payload: the first 16 hex chars of its sha256. Exported so the
// engine cache seam and tests can compute it independently.
func Fingerprint(plaintext []byte) string {
	sum := sha256.Sum256(plaintext)
	return hex.EncodeToString(sum[:])[:16]
}

// InMemoryStore is the dev/test backend — a map keyed by
// namespace+"\x00"+key. It honors TTL lazily (expired entries are
// filtered on Get/List and removed by DeleteExpired) and makes
// defensive copies of values so callers can reuse their buffers. It is
// process-local and lost on restart; SQLiteStore is the durable
// default. Mirrors MemoryArtifactStore in internal/packs/artifacts.go.
type InMemoryStore struct {
	mu      sync.Mutex
	entries map[string]Entry
	now     func() time.Time
}

// NewInMemoryStore returns an empty in-memory store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		entries: make(map[string]Entry),
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func mapKey(ns, key string) string { return ns + "\x00" + key }

func (s *InMemoryStore) Put(ctx context.Context, ns, key string, value []byte, opts ...PutOption) (Entry, error) {
	cfg := applyPutOptions(opts)
	now := s.now()
	cp := make([]byte, len(value))
	copy(cp, value)

	s.mu.Lock()
	defer s.mu.Unlock()
	mk := mapKey(ns, key)
	created := now
	if existing, ok := s.entries[mk]; ok {
		created = existing.CreatedAt
	}
	e := Entry{
		Namespace:   ns,
		Key:         key,
		Value:       cp,
		Category:    cfg.category,
		Tags:        append([]string(nil), cfg.tags...),
		CreatedAt:   created,
		UpdatedAt:   now,
		Fingerprint: Fingerprint(value),
	}
	if cfg.ttl > 0 {
		e.ExpiresAt = now.Add(cfg.ttl)
	}
	s.entries[mk] = e
	return copyEntry(e), nil
}

func (s *InMemoryStore) Get(ctx context.Context, ns, key string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[mapKey(ns, key)]
	if !ok {
		return Entry{}, ErrNotFound
	}
	if s.isExpired(e) {
		delete(s.entries, mapKey(ns, key))
		return Entry{}, ErrNotFound
	}
	return copyEntry(e), nil
}

func (s *InMemoryStore) List(ctx context.Context, ns, prefix string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Entry
	for mk, e := range s.entries {
		if e.Namespace != ns {
			continue
		}
		if s.isExpired(e) {
			delete(s.entries, mk)
			continue
		}
		if prefix != "" && !strings.HasPrefix(e.Key, prefix) {
			continue
		}
		out = append(out, copyEntry(e))
	}
	sortEntriesRecentFirst(out)
	return out, nil
}

func (s *InMemoryStore) Delete(ctx context.Context, ns, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, mapKey(ns, key))
	return nil
}

func (s *InMemoryStore) ListNamespaces(ctx context.Context) ([]NamespaceCount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	for mk := range s.entries {
		// mapKey is ns + "\x00" + key — split on the first NUL.
		idx := -1
		for i, r := range mk {
			if r == '\x00' {
				idx = i
				break
			}
		}
		if idx > 0 {
			counts[mk[:idx]]++
		}
	}
	out := make([]NamespaceCount, 0, len(counts))
	for ns, c := range counts {
		out = append(out, NamespaceCount{Namespace: ns, Count: c})
	}
	// Sort by Count descending, then Namespace ascending for stable
	// ordering when counts tie.
	sortNamespaceCounts(out)
	return out, nil
}

func sortNamespaceCounts(s []NamespaceCount) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0; j-- {
			if s[j].Count > s[j-1].Count ||
				(s[j].Count == s[j-1].Count && s[j].Namespace < s[j-1].Namespace) {
				s[j], s[j-1] = s[j-1], s[j]
				continue
			}
			break
		}
	}
}

func (s *InMemoryStore) DeletePrefix(ctx context.Context, ns, prefix string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nsPrefix := mapKey(ns, prefix)
	deleted := 0
	for mk := range s.entries {
		// mapKey is ns + "\x00" + key, so checking HasPrefix(mk, nsPrefix)
		// correctly limits matching to keys IN the given namespace
		// whose key portion starts with prefix.
		if hasPrefix(mk, nsPrefix) {
			delete(s.entries, mk)
			deleted++
		}
	}
	return deleted, nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func (s *InMemoryStore) ListAll(ctx context.Context) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, copyEntry(e))
	}
	sortEntriesRecentFirst(out)
	return out, nil
}

func (s *InMemoryStore) DeleteExpired(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for mk, e := range s.entries {
		if s.isExpired(e) {
			delete(s.entries, mk)
			n++
		}
	}
	return n, nil
}

func (s *InMemoryStore) isExpired(e Entry) bool {
	return !e.ExpiresAt.IsZero() && !s.now().Before(e.ExpiresAt)
}

func copyEntry(e Entry) Entry {
	cp := e
	if e.Value != nil {
		cp.Value = make([]byte, len(e.Value))
		copy(cp.Value, e.Value)
	}
	if e.Tags != nil {
		cp.Tags = append([]string(nil), e.Tags...)
	}
	return cp
}

// sortEntriesRecentFirst orders entries newest-UpdatedAt first so
// callers and Context() see the freshest entries at the head.
func sortEntriesRecentFirst(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		return es[i].UpdatedAt.After(es[j].UpdatedAt)
	})
}

// randomBytes returns n cryptographically-random bytes. Shared helper
// for backend id/nonce generation.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
