package repos

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// flockNB takes a non-blocking exclusive lock, mirroring what the janitor
// and repo.fetch do, so the "skip locked clone" test can simulate a
// session holding a clone.
func flockNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// mkClone creates Root/<subject>/<hash> with a file of the given size and
// an .hdaccess marker set to accessedAgo in the past.
func mkClone(t *testing.T, root, subject, hash string, size int, accessedAgo time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, subject, hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := dir + ".hdaccess"
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-accessedAgo)
	if err := os.Chtimes(marker, at, at); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestJanitor_EvictsStaleByTTL(t *testing.T) {
	root := t.TempDir()
	fresh := mkClone(t, root, "alice", "fresh", 10, time.Hour)
	stale := mkClone(t, root, "alice", "stale", 10, 48*time.Hour)

	j := NewJanitor(Config{Root: root, TTL: 24 * time.Hour, MaxBytes: -1})
	if n := j.Sweep(context.Background()); n != 1 {
		t.Fatalf("evicted %d, want 1", n)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale clone should be gone")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh clone should survive")
	}
	// Marker siblings of the evicted clone should be cleaned up too.
	if _, err := os.Stat(stale + ".hdaccess"); !os.IsNotExist(err) {
		t.Error("stale .hdaccess marker should be removed")
	}
}

func TestJanitor_SizeCapEvictsLRU(t *testing.T) {
	root := t.TempDir()
	// All within TTL; total 3000 bytes, cap 2500 ⇒ evict the LRU one.
	oldest := mkClone(t, root, "bob", "old", 1000, 3*time.Hour)
	mid := mkClone(t, root, "bob", "mid", 1000, 2*time.Hour)
	newest := mkClone(t, root, "bob", "new", 1000, 1*time.Hour)

	j := NewJanitor(Config{Root: root, TTL: 24 * time.Hour, MaxBytes: 2500})
	j.Sweep(context.Background())

	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Error("LRU clone should be evicted under size cap")
	}
	if _, err := os.Stat(mid); err != nil {
		t.Error("mid clone should survive")
	}
	if _, err := os.Stat(newest); err != nil {
		t.Error("newest clone should survive")
	}
}

func TestJanitor_NilForEmptyRoot(t *testing.T) {
	if NewJanitor(Config{Root: ""}) != nil {
		t.Error("empty root should yield a nil janitor")
	}
}

func TestJanitor_SkipsLockedClone(t *testing.T) {
	root := t.TempDir()
	stale := mkClone(t, root, "carol", "busy", 10, 72*time.Hour)

	// Hold the per-clone flock to simulate an in-use session.
	lf, err := os.OpenFile(stale+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := flockNB(lf); err != nil {
		t.Fatalf("could not take test lock: %v", err)
	}

	j := NewJanitor(Config{Root: root, TTL: 24 * time.Hour, MaxBytes: -1})
	if n := j.Sweep(context.Background()); n != 0 {
		t.Fatalf("evicted %d, want 0 (clone is locked)", n)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Error("locked clone must not be evicted")
	}
}

func TestJanitor_MissingMarkerFallsBackToDirMtime(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dave", "nomk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	j := NewJanitor(Config{Root: root, TTL: 24 * time.Hour, MaxBytes: -1})
	if n := j.Sweep(context.Background()); n != 1 {
		t.Fatalf("evicted %d, want 1 (stale by dir mtime)", n)
	}
}
