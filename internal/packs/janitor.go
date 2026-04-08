package packs

import (
	"context"
	"log/slog"
	"time"
)

// Janitor is the artifact TTL reaper described by ADR 031 and T211b.
//
// Every Interval it walks the configured ArtifactStore via ListAll
// and deletes any object older than the per-pack TTL (or DefaultTTL
// when the pack hasn't set its own). The bucket is treated as the
// authoritative source of "what artifacts exist" — the audit log
// records the lifecycle events for observability, but the bucket
// scan is what drives deletion. This keeps the design backend-portable
// (no SQL schema for retention state) and survives control-plane
// crashes (no in-memory progress to lose).
//
// Per-pack overrides come from the Pack.ArtifactTTL field. The janitor
// resolves a pack name to its TTL via the PackTTL callback, which the
// control plane wires to the active pack registry. If the pack name
// is unknown (e.g. a pack was deleted but its artifacts remain), the
// janitor falls back to DefaultTTL.
//
// The janitor runs in its own goroutine started by Run; cancelling the
// context stops the loop on the next iteration.
type Janitor struct {
	Store      ArtifactStore
	Interval   time.Duration
	DefaultTTL time.Duration
	PackTTL    func(pack string) (time.Duration, bool)
	Logger     *slog.Logger
	Now        func() time.Time
}

// JanitorConfig captures the dependencies the control plane wires.
// Run binds them onto a Janitor and kicks off the loop.
type JanitorConfig struct {
	Store      ArtifactStore
	Interval   time.Duration
	DefaultTTL time.Duration
	PackTTL    func(pack string) (time.Duration, bool)
	Logger     *slog.Logger
}

// NewJanitor builds a Janitor with sane defaults applied to any
// zero-value config field. Returns nil if Store is nil — callers use
// the nil return to skip starting the loop entirely.
func NewJanitor(cfg JanitorConfig) *Janitor {
	if cfg.Store == nil {
		return nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = 7 * 24 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.PackTTL == nil {
		cfg.PackTTL = func(string) (time.Duration, bool) { return 0, false }
	}
	return &Janitor{
		Store:      cfg.Store,
		Interval:   cfg.Interval,
		DefaultTTL: cfg.DefaultTTL,
		PackTTL:    cfg.PackTTL,
		Logger:     cfg.Logger,
		Now:        func() time.Time { return time.Now().UTC() },
	}
}

// Run blocks until ctx is cancelled, sweeping the store every
// Interval. The first sweep happens immediately so operators don't
// have to wait an hour after startup to see the janitor working.
func (j *Janitor) Run(ctx context.Context) {
	if j == nil {
		return
	}
	t := time.NewTicker(j.Interval)
	defer t.Stop()
	j.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.sweep(ctx)
		}
	}
}

// sweep runs one pass: list everything, decide what's expired, delete
// in a loop. Errors are logged but don't abort the sweep — one bad
// object shouldn't keep the rest from being collected.
func (j *Janitor) sweep(ctx context.Context) {
	now := j.Now()
	all, err := j.Store.ListAll(ctx)
	if err != nil {
		j.Logger.Error("artifact janitor: list failed", "err", err)
		return
	}
	deleted := 0
	for _, art := range all {
		if ctx.Err() != nil {
			return
		}
		ttl := j.DefaultTTL
		if art.Pack != "" {
			if override, ok := j.PackTTL(art.Pack); ok && override > 0 {
				ttl = override
			}
		}
		age := now.Sub(art.CreatedAt)
		if age <= ttl {
			continue
		}
		if err := j.Store.Delete(ctx, art.Key); err != nil {
			j.Logger.Warn("artifact janitor: delete failed",
				"key", art.Key, "pack", art.Pack, "age", age, "err", err)
			continue
		}
		deleted++
		j.Logger.Info("artifact janitor: deleted",
			"key", art.Key, "pack", art.Pack, "age", age, "ttl", ttl)
	}
	if deleted > 0 {
		j.Logger.Info("artifact janitor: sweep complete",
			"scanned", len(all), "deleted", deleted)
	}
}
