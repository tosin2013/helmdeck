// Package repos manages the persistent repos volume (ADR 040): the
// shared, named Docker volume mounted into every session sidecar at
// /repos where repo.* clones and their per-language dependency caches
// live across sessions. The Janitor here is the volume's garbage
// collector — the on-disk counterpart to the artifact janitor.
package repos

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const (
	defaultInterval = time.Hour
	// defaultTTL evicts a clone untouched for two weeks. Clones are a
	// cache, not state — losing one only costs a re-clone next time.
	defaultTTL = 14 * 24 * time.Hour
	// defaultMaxBytes caps total volume usage (20 GiB) so a busy
	// deployment can't fill the host disk between TTL sweeps. 0 disables.
	defaultMaxBytes int64 = 20 << 30
)

// Janitor reaps stale clones from the persistent repos volume (ADR 040).
//
// Layout it assumes: Root/<subject>/<repo-hash>/ is a clone, with sibling
// marker files <repo-hash>.lock (the per-clone flock repo.fetch takes) and
// <repo-hash>.hdaccess (touched on every use). Each sweep:
//
//  1. Evicts every clone whose .hdaccess (or, missing that, the clone dir
//     mtime) is older than TTL.
//  2. If a total-size cap is set and still exceeded, evicts least-recently
//     -used clones until back under the cap.
//
// Before removing a clone the janitor takes the same flock repo.fetch
// uses, non-blocking: if a session currently holds it, the clone is in
// use and is skipped this pass. The volume is authoritative — no DB
// retention state — so the janitor survives control-plane restarts.
type Janitor struct {
	Root     string
	Interval time.Duration
	TTL      time.Duration
	MaxBytes int64 // 0 disables the size cap
	Logger   *slog.Logger
	Now      func() time.Time
}

// Config captures what the control plane wires; NewJanitor fills defaults.
type Config struct {
	Root     string
	Interval time.Duration
	TTL      time.Duration
	MaxBytes int64
	Logger   *slog.Logger
}

// NewJanitor builds a Janitor with defaults applied. Returns nil when
// Root is empty (no repos volume configured) so the caller can skip
// starting the loop.
func NewJanitor(cfg Config) *Janitor {
	if cfg.Root == "" {
		return nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.TTL <= 0 {
		cfg.TTL = defaultTTL
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.MaxBytes < 0 {
		cfg.MaxBytes = 0 // explicit "disable the cap"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Janitor{
		Root:     cfg.Root,
		Interval: cfg.Interval,
		TTL:      cfg.TTL,
		MaxBytes: cfg.MaxBytes,
		Logger:   cfg.Logger,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

// Run sweeps every Interval until ctx is cancelled. The first sweep runs
// immediately so operators see GC working without waiting a full cycle.
func (j *Janitor) Run(ctx context.Context) {
	if j == nil {
		return
	}
	t := time.NewTicker(j.Interval)
	defer t.Stop()
	j.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.Sweep(ctx)
		}
	}
}

// clone is one discovered clone directory plus the metadata the sweep
// needs to order and account for it.
type clone struct {
	dir      string // absolute path of the clone working tree
	accessed time.Time
	size     int64
}

// Sweep runs one GC pass and returns how many clones it evicted. Errors
// on individual clones are logged but never abort the pass.
func (j *Janitor) Sweep(ctx context.Context) int {
	if j == nil {
		return 0
	}
	now := j.Now()
	clones := j.discover()

	evicted := 0
	var kept []clone
	for _, c := range clones {
		if ctx.Err() != nil {
			return evicted
		}
		if now.Sub(c.accessed) > j.TTL {
			if j.evict(c.dir) {
				evicted++
				j.Logger.Info("repos janitor evicted stale clone",
					"dir", c.dir, "age", now.Sub(c.accessed).Round(time.Hour).String())
			}
			continue
		}
		kept = append(kept, c)
	}

	// Size cap: evict least-recently-used survivors until under MaxBytes.
	if j.MaxBytes > 0 {
		var total int64
		for _, c := range kept {
			total += c.size
		}
		if total > j.MaxBytes {
			sort.Slice(kept, func(a, b int) bool { return kept[a].accessed.Before(kept[b].accessed) })
			for _, c := range kept {
				if total <= j.MaxBytes {
					break
				}
				if ctx.Err() != nil {
					return evicted
				}
				if j.evict(c.dir) {
					evicted++
					total -= c.size
					j.Logger.Info("repos janitor evicted clone over size cap",
						"dir", c.dir, "freed_bytes", c.size)
				}
			}
		}
	}
	return evicted
}

// discover walks Root/<subject>/<hash> two levels deep and returns the
// clone directories with their access time and on-disk size. Non-clone
// stragglers (orphaned .lock/.hdaccess whose clone is gone) are cleaned
// up here too.
func (j *Janitor) discover() []clone {
	subjects, err := os.ReadDir(j.Root)
	if err != nil {
		if !os.IsNotExist(err) {
			j.Logger.Warn("repos janitor cannot read root", "root", j.Root, "err", err)
		}
		return nil
	}
	var out []clone
	for _, s := range subjects {
		if !s.IsDir() {
			continue
		}
		subjectDir := filepath.Join(j.Root, s.Name())
		entries, err := os.ReadDir(subjectDir)
		if err != nil {
			j.Logger.Warn("repos janitor cannot read subject dir", "dir", subjectDir, "err", err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue // marker files (.lock/.hdaccess) handled via their clone
			}
			dir := filepath.Join(subjectDir, e.Name())
			out = append(out, clone{
				dir:      dir,
				accessed: j.accessTime(dir),
				size:     dirSize(dir),
			})
		}
	}
	return out
}

// accessTime returns the clone's last-use time: the mtime of the sibling
// .hdaccess marker repo.fetch touches, falling back to the clone dir's
// own mtime when the marker is absent (e.g. a clone from before this
// landed, or one the touch failed on).
func (j *Janitor) accessTime(dir string) time.Time {
	if fi, err := os.Stat(dir + ".hdaccess"); err == nil {
		return fi.ModTime().UTC()
	}
	if fi, err := os.Stat(dir); err == nil {
		return fi.ModTime().UTC()
	}
	return time.Time{} // zero ⇒ treated as ancient ⇒ evicted
}

// evict removes a clone and its marker files, but only if it can take the
// per-clone flock non-blocking — a held lock means a session is actively
// using the clone, so it's skipped this pass. Returns true if removed.
func (j *Janitor) evict(dir string) bool {
	lockPath := dir + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		// Can't take the lock file; remove the clone anyway rather than
		// leak it forever — the TTL is long enough that a real in-use
		// clone would have refreshed .hdaccess.
		j.Logger.Warn("repos janitor lock open failed; removing without lock", "dir", dir, "err", err)
		return j.removeAll(dir)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Busy — a session holds it. Skip; try again next sweep.
		return false
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return j.removeAll(dir)
}

// removeAll deletes the clone tree and its sibling marker files.
func (j *Janitor) removeAll(dir string) bool {
	ok := true
	if err := os.RemoveAll(dir); err != nil {
		j.Logger.Warn("repos janitor remove failed", "dir", dir, "err", err)
		ok = false
	}
	// Best-effort cleanup of siblings; .lock is removed last by the OS
	// once our fd closes, so a leftover empty lock file is harmless.
	_ = os.Remove(dir + ".hdaccess")
	_ = os.Remove(dir + ".hdreused")
	_ = os.Remove(dir + ".lock")
	return ok
}

// dirSize sums regular-file sizes under dir. Best-effort: unreadable
// entries are skipped, so a transient error can't wedge the sweep.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
