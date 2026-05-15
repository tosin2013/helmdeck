// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package marketplace

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Service holds the parsed catalog plus the metadata the REST handlers
// surface (last-refresh timestamp, source URL, error from the last
// fetch attempt). One Service per control-plane process; thread-safe.
//
// The service is deliberately tiny: it doesn't poll, doesn't pre-warm,
// doesn't background-refresh. The catalog is fetched once at boot
// (via Refresh) and re-fetched only when an operator calls
// POST /api/v1/marketplace/refresh. Marketplaces change at git-push
// speed, not real-time speed; aggressive polling would just hammer
// raw.githubusercontent.com for no operator-visible benefit.
type Service struct {
	source string
	logger *slog.Logger

	mu          sync.RWMutex
	index       *Index
	resolvedURL string
	fetchedAt   time.Time
	lastErr     error
}

// NewService constructs a Service. source is the marketplace base URL
// (typically `HELMDECK_MARKETPLACE_URL` or DefaultMarketplaceURL).
// The caller must invoke Refresh at least once before Catalog returns
// useful data — typically via the control-plane startup path. This
// keeps NewService a pure constructor with no I/O.
func NewService(source string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{source: source, logger: logger}
}

// Catalog returns the cached catalog snapshot. Returns ErrNotReady
// when Refresh has never succeeded; callers (the REST handler) should
// surface 503 in that case so operators know to retry.
//
// The returned Index is a defensive copy of the cached pointer — the
// fields inside are themselves shared, but the *Index can be safely
// inspected without holding the service mutex.
func (s *Service) Catalog() (*Index, CatalogMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.index == nil {
		return nil, s.metaLocked(), ErrNotReady
	}
	return s.index, s.metaLocked(), nil
}

// Refresh pulls index.yaml from the configured source and replaces
// the cached catalog. On error the previously-cached catalog (if any)
// is preserved so a transient outage doesn't blank the marketplace
// for operators — Catalog() keeps returning the last-good index until
// the next successful refresh.
func (s *Service) Refresh(ctx context.Context) error {
	idx, resolvedURL, err := LoadIndex(ctx, s.source)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetchedAt = time.Now().UTC()
	s.lastErr = err
	if err != nil {
		s.logger.Warn("marketplace refresh failed",
			"source", s.source, "err", err)
		return err
	}
	s.index = idx
	s.resolvedURL = resolvedURL
	s.logger.Info("marketplace refresh ok",
		"source", s.source, "resolved_url", resolvedURL,
		"packs", len(idx.Packs))
	return nil
}

// Source returns the configured marketplace URL (whatever
// HELMDECK_MARKETPLACE_URL or the default was set to).
func (s *Service) Source() string {
	return s.source
}

// CatalogMeta is the operator-facing metadata about the cached
// catalog. Surfaced alongside the Index in the REST response.
type CatalogMeta struct {
	Source      string    `json:"source"`
	ResolvedURL string    `json:"resolved_url,omitempty"`
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

func (s *Service) metaLocked() CatalogMeta {
	m := CatalogMeta{
		Source:      s.source,
		ResolvedURL: s.resolvedURL,
		FetchedAt:   s.fetchedAt,
	}
	if s.lastErr != nil {
		m.LastError = s.lastErr.Error()
	}
	return m
}

// ErrNotReady is returned by Catalog when Refresh has not yet
// succeeded. The control plane should map this to HTTP 503 so callers
// distinguish "marketplace genuinely down" from "marketplace empty."
var ErrNotReady = errors.New("marketplace catalog not ready (no successful refresh yet)")
