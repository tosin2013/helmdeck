// Package memory — MemoryMiddleware interface for validation hooks.
// Part of ADR 039's pluggable memory-validation seam (issue #268).
package memory

import (
	"context"
)

// MemoryMiddleware validates memory entries before they're stored.
// Implementations can reject, redact, or allow entries based on security
// policy. The default (nil middleware) is a no-op — no behavior change.
//
// This is the seam described in issue #268 for plugging in validators
// like OWASP Agent Memory Guard.
type MemoryMiddleware interface {
	// Validate is called before each Put operation. It inspects the
	// (key, value) pair plus the source context and returns a verdict.
	//
	// When verdict.Allowed is false, the Put operation fails with the
	// verdict's Reason. When verdict.Action is "redact", the value is
	// masked before storage. When verdict.Action is "allow", the value
	// is stored as-is.
	Validate(ctx context.Context, key string, value []byte, source ValidationSource) ValidationVerdict
}

// ValidationSource provides context about where a memory entry came from.
// Validators can apply different policies based on the source — e.g.,
// stricter checks for external_tool vs agent_authored.
type ValidationSource struct {
	// Category is the memory category ("cache", "solve", "repo", etc.)
	Category string
	// Namespace is the caller's memory namespace (e.g. "user:session-123")
	Namespace string
	// SourceClass is the origin type: "agent_authored", "external_tool",
	// "user_input", or "git.commit_message"
	SourceClass string
}

// ValidationVerdict is the result of a middleware validation.
type ValidationVerdict struct {
	// Allowed is true when the entry should be stored (possibly redacted).
	Allowed bool
	// Action is "allow", "block", "redact", or "quarantine"
	Action string
	// Reason explains why the entry was blocked or redacted (empty on allow)
	Reason string
	// ThreatType names the detected threat class (e.g. "prompt_injection",
	// "data_leakage") or "" when no threat was found
	ThreatType string
	// Confidence is the threat-detection confidence (0.0-1.0)
	Confidence float64
}
