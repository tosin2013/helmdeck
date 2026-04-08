package api

import (
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/tosin2013/helmdeck/internal/inject"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// newTestDB opens an in-memory SQLite with all migrations applied.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestVault constructs a vault.Store backed by db with a fixed
// (predictable) master key. Tests should not depend on the key
// material — only on round-trip behavior.
func newTestVault(t *testing.T, db *sql.DB) *vault.Store {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := vault.New(db, key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// newTestInjector wraps a vault store in an Injector with a discard
// logger. Tests use this to wire deps.Injector for navigate-handler
// integration tests.
func newTestInjector(v *vault.Store) *inject.Injector {
	return inject.New(v, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// vaultCreateInput is a one-liner constructor for the common
// CreateInput shape (no metadata, no path pattern). Reduces noise in
// tests that just want a credential to exist.
func vaultCreateInput(name, typ, host string, plaintext []byte) vault.CreateInput {
	return vault.CreateInput{
		Name:        name,
		Type:        vault.CredentialType(typ),
		HostPattern: host,
		Plaintext:   plaintext,
	}
}

// vaultGrant builds a Grant struct for tests.
func vaultGrant(subject, client string) vault.Grant {
	return vault.Grant{ActorSubject: subject, ActorClient: client}
}
