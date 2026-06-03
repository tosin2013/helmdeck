// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package gateway

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Property-based tests for SplitModel (PR D of the v0.24.0 reliability
// arc).
//
// SplitModel is the seam every gateway call passes through — the
// `provider/model` string the model picker emits is parsed here, and
// the result drives dispatch. Two invariants:
//
//   - Round-trip identity: SplitModel(provider + "/" + model) should
//     return (provider, model) for any non-empty pair where provider
//     contains no "/" and model is non-empty. This pins the docstring
//     promise that the split is on the FIRST "/" only so
//     "ollama/library/llama3" survives intact (provider="ollama",
//     model="library/llama3"). A naive strings.Split would corrupt it.
//
//   - Malformed identifiers reject: empty string, leading "/", trailing
//     "/", and inputs with no "/" must all return ErrInvalidModel.
//     The LLM's recovery key here is the error value — if a regression
//     swaps it for a generic error, callers can't distinguish "bad
//     model id" from "transient failure" and the recovery logic breaks.

// genProvider is a non-empty identifier containing no slash (since
// the split is on the first "/", the provider segment cannot contain
// one by construction).
var genProvider = rapid.StringMatching(`[a-z][a-z0-9_-]{0,15}`)

// genModelTail is a non-empty model identifier that may contain
// slashes (so "ollama/library/llama3" can be exercised end-to-end).
// Must not be empty or end in "/" — both are explicit reject cases.
var genModelTail = rapid.StringMatching(`[a-z][a-z0-9._/-]*[a-z0-9._-]`)

// TestProperty_SplitJoinRoundTrip: SplitModel(p + "/" + m) = (p, m)
// for any well-formed pair. The invariant the gateway dispatch
// relies on — if it breaks, every routed call sees a wrong provider
// or model and the typed-error story silently degrades.
func TestProperty_SplitJoinRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		provider := genProvider.Draw(t, "provider")
		model := genModelTail.Draw(t, "model")

		joined := provider + "/" + model
		gotProvider, gotModel, err := SplitModel(joined)
		if err != nil {
			t.Fatalf("SplitModel(%q) errored: %v", joined, err)
		}
		if gotProvider != provider {
			t.Errorf("provider = %q; want %q (input %q)", gotProvider, provider, joined)
		}
		if gotModel != model {
			t.Errorf("model = %q; want %q (input %q)", gotModel, model, joined)
		}
	})
}

// TestProperty_FirstSlashIsTheSplitPoint — for any well-formed pair
// where the model tail contains additional slashes, the split must
// still happen at the FIRST slash, not the last. This is the
// docstring's load-bearing claim that lets "ollama/library/llama3"
// route to the ollama provider with model="library/llama3".
func TestProperty_FirstSlashIsTheSplitPoint(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		provider := genProvider.Draw(t, "provider")
		// Force a multi-slash model tail.
		segments := rapid.IntRange(2, 4).Draw(t, "segments")
		parts := make([]string, segments)
		for i := 0; i < segments; i++ {
			parts[i] = rapid.StringMatching(`[a-z][a-z0-9._-]{0,5}`).Draw(t, "seg")
		}
		model := strings.Join(parts, "/")
		joined := provider + "/" + model

		gotProvider, gotModel, err := SplitModel(joined)
		if err != nil {
			t.Fatalf("SplitModel(%q) errored: %v", joined, err)
		}
		if gotProvider != provider {
			t.Errorf("first-slash split wrong: provider=%q want %q (input %q)",
				gotProvider, provider, joined)
		}
		if gotModel != model {
			t.Errorf("first-slash split wrong: model=%q want %q (input %q)",
				gotModel, model, joined)
		}
		// Also: the returned model must still contain the inner slashes.
		if strings.Count(gotModel, "/") != segments-1 {
			t.Errorf("model slash count drift: got %d in %q; want %d",
				strings.Count(gotModel, "/"), gotModel, segments-1)
		}
	})
}

// TestProperty_MalformedAlwaysReturnsErrInvalidModel — the specific
// shapes the docstring promises to reject must all surface
// ErrInvalidModel via errors.Is. The LLM's recovery branches on this
// specific sentinel.
func TestProperty_MalformedAlwaysReturnsErrInvalidModel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		bad := rapid.SampledFrom([]string{
			"",        // empty
			"   ",     // whitespace only
			"openai",  // no slash at all
			"/gpt-4o", // leading slash → provider empty
			"openai/", // trailing slash → model empty
			"/",       // both empty
		}).Draw(t, "bad")
		_, _, err := SplitModel(bad)
		if err == nil {
			t.Fatalf("SplitModel(%q) accepted; should reject", bad)
		}
		if !errors.Is(err, ErrInvalidModel) {
			t.Errorf("SplitModel(%q) returned %v; want errors.Is ErrInvalidModel", bad, err)
		}
	})
}

// TestProperty_LeadingTrailingWhitespaceTrimmed — the docstring
// promises TrimSpace, so " openai/gpt-4o " parses identically to
// "openai/gpt-4o". A regression that drops the trim would silently
// reject leading-space inputs the LLM might emit when assembling
// the model id from concatenation.
func TestProperty_LeadingTrailingWhitespaceTrimmed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		provider := genProvider.Draw(t, "provider")
		model := genModelTail.Draw(t, "model")
		joined := provider + "/" + model
		padded := "  " + joined + "\t\n "

		gotProvider, gotModel, err := SplitModel(padded)
		if err != nil {
			t.Fatalf("padded input rejected: %v", err)
		}
		if gotProvider != provider || gotModel != model {
			t.Errorf("padded input parsed wrong: provider=%q model=%q; want %q %q",
				gotProvider, gotModel, provider, model)
		}
	})
}
