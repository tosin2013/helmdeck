// Package store opens the helmdeck SQLite database and runs embedded
// migrations. SQLite is the dev/Compose backend (ADR 009); the same
// migration set is replayable against PostgreSQL behind a SQL-compatible
// driver in Phase 7 (T703).
//
// We use modernc.org/sqlite (pure Go) so the control plane stays
// CGO_ENABLED=0 and ships in distroless/static.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (and creates if missing) the SQLite database at path and runs
// every embedded migration that hasn't been applied yet. The special path
// ":memory:" yields an ephemeral in-memory database, used by tests.
func Open(path string) (*sql.DB, error) {
	dsn := path
	if path == ":memory:" {
		// shared cache so multiple connections from the same process see
		// the same in-memory database (sqlite default behavior is per-conn)
		dsn = "file::memory:?cache=shared"
	} else {
		dsn = "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite is happiest with a single writer; bump for postgres
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies every embedded migration whose version is not yet in
// schema_migrations.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("store: bootstrap schema_migrations: %w", err)
	}
	applied, err := loadAppliedVersions(db)
	if err != nil {
		return err
	}

	files, err := listMigrationFiles()
	if err != nil {
		return err
	}

	for _, name := range files {
		version := strings.TrimSuffix(name, ".sql")
		if _, ok := applied[version]; ok {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("store: read %s: %w", name, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin %s: %w", version, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply %s: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit %s: %w", version, err)
		}
	}
	return nil
}

func loadAppliedVersions(db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: load applied: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

func listMigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}
