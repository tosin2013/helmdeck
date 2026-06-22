package memory

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/store"
)

// stores returns both backends so the same behavioral tests cover the
// in-memory and SQLite implementations.
func stores(t *testing.T) map[string]MemoryStore {
	t.Helper()
	db := openTestDB(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	sq, err := NewSQLiteStore(db, key)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	return map[string]MemoryStore{
		"inmemory": NewInMemoryStore(),
		"sqlite":   sq,
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			val := []byte(`{"hello":"world"}`)
			put, err := s.Put(ctx, "ns1", "k1", val, WithCategory("cache"), WithTags("a", "b"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			if put.Fingerprint != Fingerprint(val) {
				t.Fatalf("fingerprint mismatch: %q vs %q", put.Fingerprint, Fingerprint(val))
			}
			got, err := s.Get(ctx, "ns1", "k1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got.Value, val) {
				t.Fatalf("value mismatch: %q", got.Value)
			}
			if got.Category != "cache" {
				t.Fatalf("category mismatch: %q", got.Category)
			}
			if len(got.Tags) != 2 || got.Tags[0] != "a" || got.Tags[1] != "b" {
				t.Fatalf("tags mismatch: %v", got.Tags)
			}
		})
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, err := s.Get(ctx, "ns1", "nope")
			if err != ErrNotFound {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
		})
	}
}

func TestUpsertOnConflict(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Put(ctx, "ns", "k", []byte("v1")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Put(ctx, "ns", "k", []byte("v2")); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(ctx, "ns", "k")
			if err != nil {
				t.Fatal(err)
			}
			if string(got.Value) != "v2" {
				t.Fatalf("expected upsert to v2, got %q", got.Value)
			}
			// Only one row should exist for (ns,k).
			all, err := s.List(ctx, "ns", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 1 {
				t.Fatalf("expected 1 entry after upsert, got %d", len(all))
			}
		})
	}
}

func TestNamespaceIsolation(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Put(ctx, "b", "secret", []byte("from-b")); err != nil {
				t.Fatal(err)
			}
			// ns "a" must not see ns "b"'s key.
			if _, err := s.Get(ctx, "a", "secret"); err != ErrNotFound {
				t.Fatalf("namespace isolation violated: ns a read ns b, err=%v", err)
			}
			list, err := s.List(ctx, "a", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 0 {
				t.Fatalf("expected empty list for ns a, got %d", len(list))
			}
		})
	}
}

func TestTTLExpiry(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			// Use a tiny TTL and a controllable clock.
			now := time.Now().UTC()
			switch v := s.(type) {
			case *InMemoryStore:
				v.now = func() time.Time { return now }
			case *SQLiteStore:
				v.now = func() time.Time { return now }
			}
			if _, err := s.Put(ctx, "ns", "k", []byte("v"), WithTTL(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(ctx, "ns", "k"); err != nil {
				t.Fatalf("entry should be live before TTL: %v", err)
			}
			// Advance the clock past the TTL.
			now = now.Add(2 * time.Minute)
			if _, err := s.Get(ctx, "ns", "k"); err != ErrNotFound {
				t.Fatalf("entry should have expired, err=%v", err)
			}
		})
	}
}

func TestDeleteExpired(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			switch v := s.(type) {
			case *InMemoryStore:
				v.now = func() time.Time { return now }
			case *SQLiteStore:
				v.now = func() time.Time { return now }
			}
			if _, err := s.Put(ctx, "ns", "expiring", []byte("x"), WithTTL(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Put(ctx, "ns", "permanent", []byte("y")); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Minute)
			n, err := s.DeleteExpired(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("expected 1 expired deleted, got %d", n)
			}
			if _, err := s.Get(ctx, "ns", "permanent"); err != nil {
				t.Fatalf("permanent entry should survive: %v", err)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Put(ctx, "ns", "k", []byte("v")); err != nil {
				t.Fatal(err)
			}
			if err := s.Delete(ctx, "ns", "k"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(ctx, "ns", "k"); err != ErrNotFound {
				t.Fatalf("expected ErrNotFound after delete, got %v", err)
			}
			// Deleting a missing key is a no-op, not an error.
			if err := s.Delete(ctx, "ns", "missing"); err != nil {
				t.Fatalf("delete of missing key should be no-op: %v", err)
			}
		})
	}
}

// TestDeletePrefix — bulk-clear by prefix without decrypting. Covers
// both backends; the SQLite path is load-bearing for the routing-memory
// "Clear all history" UI button when the master key has rotated and
// existing ciphertexts are no longer decryptable (a list-then-delete
// approach would block at the list step).
func TestDeletePrefix(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, _ = s.Put(ctx, "ns", "pack/github.list_issues/aaa", []byte("1"))
			_, _ = s.Put(ctx, "ns", "pack/github.list_issues/bbb", []byte("2"))
			_, _ = s.Put(ctx, "ns", "pack/swe.solve/notes", []byte("3"))
			_, _ = s.Put(ctx, "ns", "pipeline/issue-to-pr/run-1", []byte("4"))
			_, _ = s.Put(ctx, "ns-other", "pack/should/survive", []byte("5"))

			// Prefix "pack/github.list_issues/" matches 2 entries.
			n, err := s.DeletePrefix(ctx, "ns", "pack/github.list_issues/")
			if err != nil {
				t.Fatalf("DeletePrefix: %v", err)
			}
			if n != 2 {
				t.Errorf("expected 2 deletes, got %d", n)
			}

			// The matching pair is gone; siblings survive.
			if _, err := s.Get(ctx, "ns", "pack/github.list_issues/aaa"); err != ErrNotFound {
				t.Errorf("expected ErrNotFound for deleted key, got %v", err)
			}
			if _, err := s.Get(ctx, "ns", "pack/swe.solve/notes"); err != nil {
				t.Errorf("sibling pack should survive: %v", err)
			}
			if _, err := s.Get(ctx, "ns-other", "pack/should/survive"); err != nil {
				t.Errorf("other namespace should survive: %v", err)
			}

			// Empty prefix clears the rest of the namespace.
			n, err = s.DeletePrefix(ctx, "ns", "")
			if err != nil {
				t.Fatalf("DeletePrefix empty: %v", err)
			}
			if n != 2 {
				t.Errorf("expected 2 remaining deletes (swe.solve + pipeline), got %d", n)
			}

			// Cross-namespace isolation: ns-other still intact.
			if _, err := s.Get(ctx, "ns-other", "pack/should/survive"); err != nil {
				t.Errorf("ns-other should survive empty-prefix clear of ns: %v", err)
			}

			// Idempotency: re-running on an empty namespace returns 0.
			n, err = s.DeletePrefix(ctx, "ns", "")
			if err != nil {
				t.Fatalf("DeletePrefix on empty namespace: %v", err)
			}
			if n != 0 {
				t.Errorf("expected 0 on empty namespace, got %d", n)
			}
		})
	}
}

// TestDeletePrefix_WildcardsTreatedLiterally — SQLite's LIKE uses %
// and _ as wildcards. A caller's prefix containing those characters
// MUST NOT match other keys. Critical because the routing-memory
// audit-key vocabulary is operator-extensible and we can't constrain
// what's in the prefix.
func TestDeletePrefix_WildcardsTreatedLiterally(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, _ = s.Put(ctx, "ns", "pack/100%off", []byte("literal"))
			_, _ = s.Put(ctx, "ns", "pack/100xoff", []byte("should-survive"))
			_, _ = s.Put(ctx, "ns", "pack/100_off", []byte("literal underscore"))
			_, _ = s.Put(ctx, "ns", "pack/100Xoff", []byte("should-survive"))

			n, err := s.DeletePrefix(ctx, "ns", "pack/100%")
			if err != nil {
				t.Fatalf("DeletePrefix: %v", err)
			}
			if n != 1 {
				t.Errorf("'%%' should be literal — expected 1 delete, got %d", n)
			}
			if _, err := s.Get(ctx, "ns", "pack/100xoff"); err != nil {
				t.Errorf("wildcard match leaked — pack/100xoff should survive: %v", err)
			}

			n, err = s.DeletePrefix(ctx, "ns", "pack/100_")
			if err != nil {
				t.Fatalf("DeletePrefix _: %v", err)
			}
			if n != 1 {
				t.Errorf("'_' should be literal — expected 1 delete, got %d", n)
			}
			if _, err := s.Get(ctx, "ns", "pack/100Xoff"); err != nil {
				t.Errorf("wildcard match leaked — pack/100Xoff should survive: %v", err)
			}
		})
	}
}

// TestDeletePrefix_SkipsDecryption — load-bearing for the
// "memory: decrypt: cipher: message authentication failed" UI recovery
// path. SQLite-backed only: simulates the operator-visible scenario
// where the master key has rotated between writes and reads. Confirms
// DeletePrefix succeeds even when List would fail.
func TestDeletePrefix_SkipsDecryption_SQLite(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Write with one key (the "old" key).
	oldKey := make([]byte, 32)
	for i := range oldKey {
		oldKey[i] = byte(i)
	}
	oldStore, err := NewSQLiteStore(db, oldKey)
	if err != nil {
		t.Fatalf("oldStore: %v", err)
	}
	_, _ = oldStore.Put(ctx, "ns", "pack/old-key/a", []byte("v1"))
	_, _ = oldStore.Put(ctx, "ns", "pack/old-key/b", []byte("v2"))

	// Re-open with a DIFFERENT key (simulates a restart with a new
	// ephemeral master). Same DB, different cipher.
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = byte(i + 1) // different from oldKey
	}
	newStore, err := NewSQLiteStore(db, newKey)
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}

	// List under the new key MUST fail (this is the operator-visible
	// symptom — "memory: decrypt: cipher: message authentication failed").
	if _, err := newStore.List(ctx, "ns", "pack/old-key/"); err == nil {
		t.Errorf("expected list to fail under rotated key, but it succeeded")
	}

	// DeletePrefix MUST succeed and clear the orphaned rows even
	// though they can't be decrypted.
	n, err := newStore.DeletePrefix(ctx, "ns", "pack/old-key/")
	if err != nil {
		t.Fatalf("DeletePrefix under rotated key: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 orphans cleared, got %d", n)
	}

	// After clear, List works again (no rows → nothing to decrypt).
	got, err := newStore.List(ctx, "ns", "pack/old-key/")
	if err != nil {
		t.Errorf("list after clear should succeed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list after clear, got %d entries", len(got))
	}
}

func TestListPrefix(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_, _ = s.Put(ctx, "ns", "github.list_issues/aaa", []byte("1"))
			_, _ = s.Put(ctx, "ns", "github.list_issues/bbb", []byte("2"))
			_, _ = s.Put(ctx, "ns", "swe.solve/notes", []byte("3"))
			got, err := s.List(ctx, "ns", "github.list_issues/")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 {
				t.Fatalf("expected 2 prefix matches, got %d", len(got))
			}
		})
	}
}

// TestEncryptionAtRest proves the SQLite backend never writes plaintext
// to the value column and round-trips through decryption.
func TestEncryptionAtRest(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := NewSQLiteStore(db, key)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("super-secret-marker-value-12345")
	if _, err := s.Put(ctx, "ns", "k", plaintext); err != nil {
		t.Fatal(err)
	}
	// Read the raw stored ciphertext directly from the table.
	var stored []byte
	if err := db.QueryRowContext(ctx,
		`SELECT value_ciphertext FROM memory_entries WHERE namespace = ? AND key = ?`, "ns", "k").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, plaintext) {
		t.Fatalf("plaintext leaked into ciphertext column: %q", stored)
	}
	if bytes.Equal(stored, plaintext) {
		t.Fatalf("stored value equals plaintext (not encrypted)")
	}
	// And it round-trips.
	got, err := s.Get(ctx, "ns", "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Value, plaintext) {
		t.Fatalf("decrypted value mismatch: %q", got.Value)
	}
}

func TestNewSQLiteStoreRejectsBadKey(t *testing.T) {
	db := openTestDB(t)
	if _, err := NewSQLiteStore(db, []byte("too-short")); err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}

func TestMigrationApplies(t *testing.T) {
	// A fresh :memory: DB opened via store.Open must have the
	// memory_entries table (migration 0006 auto-applied).
	db := openTestDB(t)
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memory_entries'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("memory_entries table not created by migration; found %d", n)
	}
}
