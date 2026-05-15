// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// CallRecord captures a single provider dispatch — one row in the
// provider_calls table. Recorded by Registry.Dispatch on both the
// success and error paths so the success-rate aggregation in
// /api/v1/providers/stats has the full denominator.
//
// JobID/FinishReason/RawContentLen (#183) supply the diagnostic
// surface that turns a failed LLM-backed pack call into a one-query
// post-mortem. They're all optional from the recorder's perspective:
// JobID is empty for sync (non-async-job) calls, FinishReason is
// empty when the provider didn't report one, and RawContentLen
// stays zero on the error path (no response to measure).
type CallRecord struct {
	Timestamp        time.Time
	Provider         string
	Model            string
	Status           string // "success" or "error"
	LatencyMS        int64
	ErrorCode        string // populated when Status == "error"
	FallbackUsed     bool
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	JobID            string // pack job ID (empty for sync calls)
	FinishReason     string // provider-reported finish_reason
	RawContentLen    int    // bytes in choices[0].message.content
}

// CallRecorder persists CallRecord rows. The interface keeps the
// gateway dispatch path decoupled from the SQLite schema so tests
// can use the in-memory NoopRecorder and the production wiring
// uses SQLiteRecorder. Implementations must be safe for concurrent
// use — Registry.Dispatch is called from many goroutines.
type CallRecorder interface {
	Record(ctx context.Context, c CallRecord) error
}

// NoopRecorder discards every record. Used as the zero-value
// fallback when the control plane wires no real recorder (tests,
// dev mode without a database).
type NoopRecorder struct{}

// Record implements CallRecorder.
func (NoopRecorder) Record(context.Context, CallRecord) error { return nil }

// SQLiteRecorder writes CallRecords into the provider_calls table
// managed by internal/store. The caller owns the *sql.DB.
type SQLiteRecorder struct {
	db *sql.DB
}

// NewSQLiteRecorder wraps an open *sql.DB.
func NewSQLiteRecorder(db *sql.DB) *SQLiteRecorder {
	return &SQLiteRecorder{db: db}
}

// Record implements CallRecorder. Errors are returned to the caller
// (Registry.Dispatch) but the dispatch path itself swallows them
// after logging — a metrics-table write failure must never break
// the actual API request.
func (r *SQLiteRecorder) Record(ctx context.Context, c CallRecord) error {
	if c.Timestamp.IsZero() {
		c.Timestamp = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
        INSERT INTO provider_calls
            (ts, provider, model, status, latency_ms, error_code,
             fallback_used, prompt_tokens, completion_tokens, total_tokens,
             job_id, finish_reason, raw_content_len)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Timestamp.UTC().Format(time.RFC3339Nano),
		c.Provider,
		c.Model,
		c.Status,
		c.LatencyMS,
		nullIfEmpty(c.ErrorCode),
		boolToInt(c.FallbackUsed),
		c.PromptTokens,
		c.CompletionTokens,
		c.TotalTokens,
		nullIfEmpty(c.JobID),
		nullIfEmpty(c.FinishReason),
		c.RawContentLen,
	)
	if err != nil {
		return fmt.Errorf("recorder: insert: %w", err)
	}
	return nil
}

// classifyRecordError maps a Go error returned by Provider.ChatCompletion
// into the small closed set of error_code values stored in
// provider_calls. Operators querying success rates need to be able
// to GROUP BY error_code without facing a thousand unique strings,
// so the function intentionally collapses everything into one of
// six buckets.
func classifyRecordError(err error) string {
	if err == nil {
		return ""
	}
	// providerError carries the upstream HTTP status if the failure
	// happened after a successful round trip. transport.go wraps
	// every 4xx/5xx in this type.
	var pe *providerError
	if errors.As(err, &pe) {
		switch {
		case pe.StatusCode >= 500:
			return "http_5xx"
		case pe.StatusCode >= 400:
			return "http_4xx"
		}
	}
	// Network-layer failures — DNS, dial, TLS — surface as
	// net.Error or wrapped url.Error. Timeouts get their own bucket
	// because they're an actionable signal (raise the per-provider
	// timeout, not "the upstream is broken").
	var nerr net.Error
	if errors.As(err, &nerr) {
		if nerr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	if strings.Contains(err.Error(), "decode") || strings.Contains(err.Error(), "unmarshal") {
		return "decode"
	}
	if errors.Is(err, ErrUnknownProvider) {
		return "unknown_provider"
	}
	return "unknown"
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
