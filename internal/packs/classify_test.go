package packs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/tosin2013/helmdeck/internal/session"
)

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"deadline", context.DeadlineExceeded, CodeTimeout},
		{"canceled", context.Canceled, CodeTimeout},
		{"wrapped deadline", fmt.Errorf("upstream: %w", context.DeadlineExceeded), CodeTimeout},
		{"session not found", session.ErrSessionNotFound, CodeSessionUnavailable},
		{"valid pack error", &PackError{Code: CodeArtifactFailed, Message: "s3 down"}, CodeArtifactFailed},
		{"invalid pack error code", &PackError{Code: "weird", Message: "x"}, CodeInternal},
		{"artifact hint", errors.New("artifact upload failed"), CodeArtifactFailed},
		{"session hint", errors.New("session was reaped"), CodeSessionUnavailable},
		{"schema hint", errors.New("schema mismatch on field x"), CodeInvalidInput},
		{"validation hint", errors.New("input validation failed"), CodeInvalidInput},
		{"plain", errors.New("kaboom"), CodeHandlerFailed},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Errorf("Classify = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsValidCode(t *testing.T) {
	for c := range validCodes {
		if !IsValidCode(c) {
			t.Errorf("%q reported invalid", c)
		}
	}
	if IsValidCode("not_a_real_code") {
		t.Error("unknown code reported valid")
	}
	if IsValidCode("") {
		t.Error("empty reported valid")
	}
}

func TestWrapPreservesValidPackError(t *testing.T) {
	original := &PackError{Code: CodeInvalidOutput, Message: "bad shape"}
	wrapped := wrap(original)
	if wrapped != original {
		t.Error("wrap copied a valid PackError instead of returning it")
	}
}

func TestWrapCoercesUnknownPackErrorCode(t *testing.T) {
	bad := &PackError{Code: "made_up", Message: "x"}
	wrapped := wrap(bad)
	if wrapped.Code != CodeInternal {
		t.Errorf("code = %q, want internal", wrapped.Code)
	}
}

func TestEngineEnforcesClosedSetOnAllPaths(t *testing.T) {
	// Every distinct handler failure mode must come back as a
	// PackError whose Code passes IsValidCode. This is the
	// promise T206 makes to wire-format consumers.
	cases := map[string]HandlerFunc{
		"plain error": func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, errors.New("kaboom")
		},
		"hand-rolled bad code": func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, &PackError{Code: "totally_made_up", Message: "nope"}
		},
		"panic": func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			panic("boom")
		},
		"artifact-flavored error": func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, errors.New("artifact upload to s3 failed")
		},
	}
	eng := New(WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			pack := &Pack{Name: "x", Version: "v1", Handler: h}
			_, err := eng.Execute(context.Background(), pack, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			var perr *PackError
			if !errors.As(err, &perr) {
				t.Fatalf("err is not *PackError: %T %v", err, err)
			}
			if !IsValidCode(perr.Code) {
				t.Errorf("leaked invalid code %q from engine", perr.Code)
			}
		})
	}
}
