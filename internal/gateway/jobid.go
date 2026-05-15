// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

// jobid.go (#183) — JobID context plumbing for provider_calls audit
// rows.
//
// Background: the gateway's Dispatch records every provider call into
// the provider_calls table for the success-rate panel (T607). Adding
// the originating pack job ID to each row lets operators diagnose a
// failed pack invocation with a single WHERE clause instead of
// timestamp-matching the audit table by hand. The job ID is set on
// the context by the async-job runner (internal/mcp/jobs.go) before
// dispatching the pack handler — Dispatch reads it back out when
// building the CallRecord.
//
// The context-value approach matches the existing helmdeck pattern
// (progressCtxKey in packs, holderKey in auth) and avoids touching
// every ChatRequest call site to thread a new field through.

import "context"

// jobIDCtxKey is the unexported context key under which the active
// pack job ID is stored. Unexported so callers can't accidentally
// collide with other context values keyed by raw strings.
type jobIDCtxKey struct{}

// WithJobID returns a derived context carrying id. When id is empty,
// the returned context is unchanged (no key set) so JobIDFromContext
// can't distinguish "explicitly empty" from "not set" — both look
// like an absent job ID, which matches the provider_calls behavior
// (job_id stays NULL).
func WithJobID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, jobIDCtxKey{}, id)
}

// JobIDFromContext returns the job ID set by WithJobID, or empty if
// none was set. Used by Registry.Dispatch when constructing the
// CallRecord row.
func JobIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(jobIDCtxKey{}).(string); ok {
		return id
	}
	return ""
}
