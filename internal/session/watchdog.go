package session

import (
	"context"
	"log/slog"
	"time"
)

// Watchdog periodically scans the session table and terminates any session
// whose wall-clock lifetime has exceeded its Spec.Timeout. It is the
// in-process enforcement arm of ADR 004 (ephemeral stateless sessions).
//
// MaxTasks enforcement lives in the per-session command path (T106) since
// the watchdog cannot observe CDP traffic.
type Watchdog struct {
	rt       Runtime
	logger   *slog.Logger
	interval time.Duration
	now      func() time.Time
}

// NewWatchdog returns a watchdog that ticks every interval. Pass a small
// interval (1–10s) for tests; production wires 30s.
func NewWatchdog(rt Runtime, logger *slog.Logger, interval time.Duration) *Watchdog {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Watchdog{rt: rt, logger: logger, interval: interval, now: time.Now}
}

// Run blocks until ctx is canceled, scanning every interval.
func (w *Watchdog) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// tick is exposed for tests; production callers use Run.
func (w *Watchdog) tick(ctx context.Context) {
	sessions, err := w.rt.List(ctx)
	if err != nil {
		w.logger.Warn("watchdog list failed", "err", err)
		return
	}
	now := w.now()
	for _, s := range sessions {
		if s.Spec.Timeout <= 0 {
			continue
		}
		deadline := s.CreatedAt.Add(s.Spec.Timeout)
		if now.Before(deadline) {
			continue
		}
		w.logger.Info("watchdog terminating expired session", "session_id", s.ID, "age", now.Sub(s.CreatedAt).String())
		if err := w.rt.Terminate(ctx, s.ID); err != nil {
			w.logger.Warn("watchdog terminate failed", "session_id", s.ID, "err", err)
		}
	}
}
