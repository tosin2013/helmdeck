// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/store"
)

func TestSQLiteRecorder_RoundTrip(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	r := NewSQLiteRecorder(db)
	ctx := context.Background()

	if err := r.Record(ctx, CallRecord{
		Provider:         "openai",
		Model:            "gpt-4o",
		Status:           "success",
		LatencyMS:        123,
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
	}); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if err := r.Record(ctx, CallRecord{
		Provider:  "openai",
		Model:     "gpt-4o",
		Status:    "error",
		LatencyMS: 50,
		ErrorCode: "http_5xx",
	}); err != nil {
		t.Fatalf("record error: %v", err)
	}

	var (
		total   int
		success int
		latency float64
	)
	row := db.QueryRow(`
        SELECT COUNT(*),
               SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),
               AVG(latency_ms)
        FROM provider_calls`)
	if err := row.Scan(&total, &success, &latency); err != nil {
		t.Fatal(err)
	}
	if total != 2 || success != 1 {
		t.Errorf("total=%d success=%d, want 2/1", total, success)
	}
	if latency == 0 {
		t.Errorf("avg latency unexpectedly zero")
	}
}

func TestSQLiteRecorder_DefaultsTimestamp(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	r := NewSQLiteRecorder(db)
	if err := r.Record(context.Background(), CallRecord{
		Provider: "anthropic", Model: "claude-opus", Status: "success", LatencyMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	var ts string
	if err := db.QueryRow(`SELECT ts FROM provider_calls LIMIT 1`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	got, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("ts not RFC3339Nano: %q (%v)", ts, err)
	}
	if time.Since(got) > 5*time.Second {
		t.Errorf("auto-stamped ts is %v old, want recent", time.Since(got))
	}
}

func TestClassifyRecordError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"5xx", &providerError{Provider: "openai", StatusCode: 503}, "http_5xx"},
		{"4xx", &providerError{Provider: "openai", StatusCode: 429}, "http_4xx"},
		{"unknown_provider", ErrUnknownProvider, "unknown_provider"},
		{"net error timeout", &fakeNetErr{timeout: true}, "timeout"},
		{"net error generic", &fakeNetErr{}, "network"},
		{"decode", errors.New("could not decode response"), "decode"},
		{"unknown", errors.New("something else"), "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyRecordError(c.err)
			if got != c.want {
				t.Errorf("classifyRecordError(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// fakeNetErr satisfies net.Error so we can drive the timeout vs
// non-timeout branches of classifyRecordError without hitting an
// actual network.
type fakeNetErr struct {
	timeout bool
}

func (e *fakeNetErr) Error() string   { return "fake net error" }
func (e *fakeNetErr) Timeout() bool   { return e.timeout }
func (e *fakeNetErr) Temporary() bool { return false }

// Compile-time assertion that fakeNetErr satisfies net.Error.
var _ net.Error = (*fakeNetErr)(nil)

// #183: the three diagnostic columns (job_id, finish_reason,
// raw_content_len) round-trip through the recorder. Asserts both
// directions of the new nullIfEmpty plumbing — non-empty values
// land as their literal text; empty job_id / finish_reason persist
// as NULL so downstream queries can `WHERE job_id IS NOT NULL`
// without false matches on a sentinel empty string.
func TestSQLiteRecorder_DiagnosticColumns_RoundTrip(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	r := NewSQLiteRecorder(db)
	ctx := context.Background()

	if err := r.Record(ctx, CallRecord{
		Provider:      "openrouter",
		Model:         "openai/gpt-oss-120b",
		Status:        "success",
		LatencyMS:     500,
		JobID:         "0e5d27beb509e9bdf36420f1b0749aa9",
		FinishReason:  "length",
		RawContentLen: 742,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Record(ctx, CallRecord{
		Provider: "openrouter", Model: "openai/gpt-oss-120b",
		Status: "success", LatencyMS: 12,
		// no JobID, no FinishReason, no RawContentLen — emulates a
		// sync call dispatched without WithJobID.
	}); err != nil {
		t.Fatal(err)
	}

	type row struct {
		jobID, finishReason any
		rawContentLen       int
	}
	rows, err := db.Query(`SELECT job_id, finish_reason, raw_content_len FROM provider_calls ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.jobID, &r.finishReason, &r.rawContentLen); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	// Row 0: populated.
	if s, ok := got[0].jobID.(string); !ok || s != "0e5d27beb509e9bdf36420f1b0749aa9" {
		t.Errorf("row 0 job_id = %v, want the populated id", got[0].jobID)
	}
	if s, ok := got[0].finishReason.(string); !ok || s != "length" {
		t.Errorf("row 0 finish_reason = %v, want 'length'", got[0].finishReason)
	}
	if got[0].rawContentLen != 742 {
		t.Errorf("row 0 raw_content_len = %d, want 742", got[0].rawContentLen)
	}
	// Row 1: empty job_id / finish_reason should be NULL (not "").
	if got[1].jobID != nil {
		t.Errorf("row 1 job_id = %v, want NULL", got[1].jobID)
	}
	if got[1].finishReason != nil {
		t.Errorf("row 1 finish_reason = %v, want NULL", got[1].finishReason)
	}
	if got[1].rawContentLen != 0 {
		t.Errorf("row 1 raw_content_len = %d, want 0 (NOT NULL DEFAULT 0)", got[1].rawContentLen)
	}
}

// #183: job_id index is queryable — the motivating diagnostic query
// (`WHERE job_id = ?`) should use the new index. We don't assert
// EXPLAIN QUERY PLAN here (SQLite version-dependent output); just
// confirm the query returns the right rows.
func TestSQLiteRecorder_QueryByJobID(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	r := NewSQLiteRecorder(db)
	ctx := context.Background()
	// Three calls under the same job + one unrelated call.
	for i := 0; i < 3; i++ {
		if err := r.Record(ctx, CallRecord{
			Provider: "anthropic", Model: "claude-sonnet", Status: "success",
			LatencyMS: int64(100 + i), JobID: "target-job",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Record(ctx, CallRecord{
		Provider: "openai", Model: "gpt-4o", Status: "success",
		LatencyMS: 999, JobID: "other-job",
	}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_calls WHERE job_id = ?`, "target-job").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("expected 3 rows for target-job, got %d", n)
	}
}
