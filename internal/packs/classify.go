package packs

import (
	"context"
	"errors"
	"strings"

	"github.com/tosin2013/helmdeck/internal/session"
)

// validCodes is the closed-set referenced by ADR 008. T206's promise
// is that no error returned from Engine.Execute carries a code outside
// this set, regardless of what a handler did internally.
var validCodes = map[ErrorCode]struct{}{
	CodeInvalidInput:       {},
	CodeInvalidOutput:      {},
	CodeSessionUnavailable: {},
	CodeHandlerFailed:      {},
	CodeArtifactFailed:     {},
	CodeTimeout:            {},
	CodeInternal:           {},
}

// IsValidCode reports whether c is one of the closed-set error codes.
// Used by Classify and by tests to assert no stray code escapes.
func IsValidCode(c ErrorCode) bool {
	_, ok := validCodes[c]
	return ok
}

// Classify maps any error returned from a pack handler to one of the
// closed-set codes in errors.go. The order matters — context errors
// must be checked before PackError because a handler that returns
// `&PackError{Code: "weird"}` after its context expired should still
// surface as a timeout, not as a coerced "internal" code.
//
// Rules, in order:
//
//  1. context.DeadlineExceeded / context.Canceled  → timeout
//  2. session.ErrSessionNotFound                    → session_unavailable
//  3. *PackError with a valid Code                  → that Code
//  4. *PackError with an unknown Code               → coerced to internal
//  5. error message hints (substring scan)          → matched bucket
//  6. anything else                                  → handler_failed
//
// This is the "middleware" T206 calls for: every uncategorized error
// gets bucketed by a single function instead of each call site
// inventing its own classification.
func Classify(err error) ErrorCode {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return CodeTimeout
	}
	if errors.Is(err, session.ErrSessionNotFound) {
		return CodeSessionUnavailable
	}
	var perr *PackError
	if errors.As(err, &perr) {
		if IsValidCode(perr.Code) {
			return perr.Code
		}
		return CodeInternal
	}
	// Substring hints — last resort. We only match on lowercase
	// fragments unique enough that a real error message containing
	// them is almost certainly about that bucket. Order matters: an
	// "artifact upload timeout" should classify as timeout because
	// the time check ran first.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "artifact"):
		return CodeArtifactFailed
	case strings.Contains(msg, "session"):
		return CodeSessionUnavailable
	case strings.Contains(msg, "schema") || strings.Contains(msg, "validation"):
		return CodeInvalidInput
	}
	return CodeHandlerFailed
}

// wrap converts an arbitrary handler error into a *PackError carrying
// the classified code. If err is already a *PackError with a valid
// code we return it unchanged so the cause chain stays intact.
func wrap(err error) *PackError {
	if err == nil {
		return nil
	}
	var perr *PackError
	if errors.As(err, &perr) && IsValidCode(perr.Code) {
		return perr
	}
	return &PackError{
		Code:    Classify(err),
		Message: err.Error(),
		Cause:   err,
	}
}
