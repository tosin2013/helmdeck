package packs

import "fmt"

// ErrorCode is one of the closed-set codes every pack failure maps
// to. ADR 008 mandates that handlers cannot invent new codes — T206
// will ship middleware that bucketizes any uncategorized error from
// a handler into the nearest defined code so the wire contract stays
// stable across pack versions.
type ErrorCode string

const (
	// CodeInvalidInput — request payload failed the pack's input schema.
	CodeInvalidInput ErrorCode = "invalid_input"
	// CodeInvalidOutput — handler returned a payload that doesn't
	// match the declared output schema. Treated as a server-side bug,
	// not a client error.
	CodeInvalidOutput ErrorCode = "invalid_output"
	// CodeSessionUnavailable — engine could not acquire a session
	// (runtime missing, quota exceeded, container failed to start).
	CodeSessionUnavailable ErrorCode = "session_unavailable"
	// CodeHandlerFailed — handler returned a non-typed error or
	// panicked. T206 maps stray errors here.
	CodeHandlerFailed ErrorCode = "handler_failed"
	// CodeArtifactFailed — artifact upload or listing failed mid-run.
	CodeArtifactFailed ErrorCode = "artifact_failed"
	// CodeTimeout — handler exceeded its deadline or the caller cancelled.
	CodeTimeout ErrorCode = "timeout"
	// CodeInternal — engine bug or invariant violation. Should never
	// be observable in production.
	CodeInternal ErrorCode = "internal"
)

// PackError is the typed error every Engine.Execute return path uses.
// REST/MCP/A2A frontends switch on Code to map to wire-level error
// envelopes; Cause is preserved for the audit log but never returned
// to remote agents.
type PackError struct {
	Code    ErrorCode
	Message string
	Cause   error
}

func (e *PackError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes Cause to errors.Is / errors.As so callers can still
// match on the underlying failure (e.g. context.DeadlineExceeded).
func (e *PackError) Unwrap() error { return e.Cause }
