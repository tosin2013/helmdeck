// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// captureRecorder is a CallRecorder that retains every record in
// memory so tests can assert on the new diagnostic columns.
type captureRecorder struct {
	mu      sync.Mutex
	records []CallRecord
}

func (c *captureRecorder) Record(_ context.Context, r CallRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return nil
}

func (c *captureRecorder) last() CallRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records) == 0 {
		return CallRecord{}
	}
	return c.records[len(c.records)-1]
}

// #183: Dispatch threads JobID from context, FinishReason from
// resp.Choices[0], and RawContentLen from message content text — all
// onto the CallRecord. Validates the diagnostic surface that turns
// "model returned no visible text" into a one-query lookup.
func TestRegistryDispatch_RecordsDiagnostics_OnSuccess(t *testing.T) {
	reg := NewRegistry()
	sp := &stubProvider{name: "anthropic", models: []string{"claude-sonnet-4-6"}}
	reg.Register(sp)
	rec := &captureRecorder{}
	reg.Recorder = rec

	ctx := WithJobID(context.Background(), "job-abc-123")
	_, err := reg.Dispatch(ctx, ChatRequest{
		Model:    "anthropic/claude-sonnet-4-6",
		Messages: []Message{{Role: "user", Content: TextContent("hello")}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	got := rec.last()
	if got.JobID != "job-abc-123" {
		t.Errorf("JobID = %q, want job-abc-123", got.JobID)
	}
	if got.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want 'stop' (from stubProvider)", got.FinishReason)
	}
	// stubProvider returns "hi from anthropic using claude-sonnet-4-6".
	expectedContent := "hi from anthropic using claude-sonnet-4-6"
	if got.RawContentLen != len(expectedContent) {
		t.Errorf("RawContentLen = %d, want %d", got.RawContentLen, len(expectedContent))
	}
}

// JobID is recorded even on the error path so operators can locate
// the failed call by job id. FinishReason / RawContentLen stay zero
// because there's no response to extract from.
func TestRegistryDispatch_RecordsJobID_OnError(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "anthropic", err: errors.New("upstream down")})
	rec := &captureRecorder{}
	reg.Recorder = rec

	ctx := WithJobID(context.Background(), "job-fail-1")
	_, err := reg.Dispatch(ctx, ChatRequest{
		Model:    "anthropic/x",
		Messages: []Message{{Role: "user", Content: TextContent("hi")}},
	})
	if err == nil {
		t.Fatal("expected dispatch error from stub")
	}
	got := rec.last()
	if got.JobID != "job-fail-1" {
		t.Errorf("JobID = %q, want job-fail-1", got.JobID)
	}
	if got.Status != "error" {
		t.Errorf("Status = %q, want error", got.Status)
	}
	if got.FinishReason != "" {
		t.Errorf("FinishReason = %q, want empty on error path", got.FinishReason)
	}
	if got.RawContentLen != 0 {
		t.Errorf("RawContentLen = %d, want 0 on error path", got.RawContentLen)
	}
}

// No WithJobID on the context means JobID stays empty — the sync /
// non-async path. Confirms the existing contract isn't broken.
func TestRegistryDispatch_NoJobIDInContext_StillRecords(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "anthropic"})
	rec := &captureRecorder{}
	reg.Recorder = rec

	_, err := reg.Dispatch(context.Background(), ChatRequest{
		Model:    "anthropic/x",
		Messages: []Message{{Role: "user", Content: TextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	got := rec.last()
	if got.JobID != "" {
		t.Errorf("JobID = %q, want empty for ctx without WithJobID", got.JobID)
	}
	if got.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want still populated on success", got.FinishReason)
	}
}
