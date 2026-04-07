package store_test

import (
	"path/filepath"
	"testing"

	"github.com/tosin2013/helmdeck/internal/store"
)

func TestOpenInMemoryRunsMigrations(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("schema_migrations query: %v", err)
	}
	if n == 0 {
		t.Fatal("expected at least one applied migration")
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatalf("audit_log query: %v", err)
	}
	if n != 0 {
		t.Fatalf("audit_log should be empty after migration, got %d", n)
	}
}

func TestOpenFileIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "helmdeck.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db.Close()

	db, err = store.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n == 0 {
		t.Fatal("expected migrations to persist across opens")
	}
}
