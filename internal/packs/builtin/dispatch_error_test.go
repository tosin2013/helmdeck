// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
)

func TestDispatchError_Classifies(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode packs.ErrorCode
		wantHint bool // message points at helmdeck://models
	}{
		{
			name:     "unknown provider is caller-fixable",
			err:      fmt.Errorf("%w: minimax", gateway.ErrUnknownProvider),
			wantCode: packs.CodeInvalidInput,
			wantHint: true,
		},
		{
			name:     "invalid model syntax is caller-fixable",
			err:      gateway.ErrInvalidModel,
			wantCode: packs.CodeInvalidInput,
			wantHint: true,
		},
		{
			name:     "generic dispatch failure stays handler_failed",
			err:      errors.New("connection reset"),
			wantCode: packs.CodeHandlerFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := dispatchError("claim extractor dispatch", tc.err)
			if pe.Code != tc.wantCode {
				t.Errorf("code = %s, want %s", pe.Code, tc.wantCode)
			}
			if tc.wantHint && !strings.Contains(pe.Message, "helmdeck://models") {
				t.Errorf("expected helmdeck://models hint, got %q", pe.Message)
			}
			// Both branches fold the error into Message and leave Cause
			// nil, so PackError.Error() never prints the underlying error
			// twice (the "unknown provider: X: unknown provider: X" bug).
			if pe.Cause != nil {
				t.Errorf("Cause should be nil to avoid doubled message; got %v", pe.Cause)
			}
			if n := strings.Count(pe.Error(), tc.err.Error()); n != 1 {
				t.Errorf("underlying error appears %d× (want 1): %q", n, pe.Error())
			}
			if !strings.Contains(pe.Message, tc.err.Error()) {
				t.Errorf("Message should carry the upstream detail (MCP surfaces Message); got %q", pe.Message)
			}
		})
	}
}

// TestContentGround_BadModelIsInvalidInput proves the end-to-end path the
// user hit: a gateway "unknown provider" during claim extraction surfaces
// as CodeInvalidInput (recoverable), not handler_failed.
func TestContentGround_BadModelIsInvalidInput(t *testing.T) {
	disp := &scriptedDispatcherWT{
		replyErr: []error{fmt.Errorf("%w: minimax", gateway.ErrUnknownProvider)},
	}
	// content.ground is Firecrawl-gated, so supply a stub to get past the
	// enabled-check; it is never called because claim extraction (the
	// gateway dispatch) fails first.
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Firecrawl must not be reached — dispatch should fail first")
		http.Error(w, "nope", 500)
	})
	_, err := runContentGround(t, disp, &execScript{}, fc,
		`{"text":"WebAssembly is fast.","model":"minimax/abab6.5"}`)
	if err == nil {
		t.Fatal("expected an error")
	}
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *packs.PackError, got %T", err)
	}
	if pe.Code != packs.CodeInvalidInput {
		t.Errorf("code = %s, want invalid_input (so the agent retries with a valid model)", pe.Code)
	}
	if !strings.Contains(pe.Message, "helmdeck://models") {
		t.Errorf("expected helmdeck://models hint, got %q", pe.Message)
	}
}
