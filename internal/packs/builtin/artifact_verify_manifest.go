// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// artifact_verify_manifest.go — anti-hallucination audit pack
// (issue #461 Phase 1).
//
// Motivating trace, 2026-06-09: tech-blog-publisher on `openai/
// gpt-oss-120b:free` made ONE real `blog.rewrite_for_audience`
// call, then produced a confidently-formatted "Artifact Deposit
// Manifest" table listing six entries with byte sizes (7.4 KB,
// 2.1 KB, ...) — and a disclaimer "(mandatory per SKILL.md)".
// Empirical ground truth from `GET /api/v1/artifacts`: zero
// artifacts in the blog.publish namespace. Every line of the
// manifest was fabricated.
//
// The architectural fixes shipped this morning (PR #450 typed
// deposit, PR #453 default model arg, layered SOUL/IDENTITY/
// USER/AGENTS in workspace-blog) close the prose-instruction
// failure mode. They don't close the lying-about-tool-calls
// failure mode — a Tier C model that produces a manifest table
// without ever calling artifact.put. Same architectural shape
// as ADR 052's av-validate solved at the producer side: turn
// an implicit trust ("the agent said it deposited") into a
// typed pack call that reads ground truth and surfaces the gap.
//
// Usage shape: skill prose tells the agent "after producing the
// deposit manifest, you MUST call artifact.verify-manifest with
// each artifact_key from the table." The pack reads the store,
// returns {verified[], missing[], all_present, summary}, and the
// LLM's next text-output turn sees the structured result in its
// context window — making the gap visible to the operator
// instead of hidden behind plausibility-shaped output.
//
// Phase 2 follow-ups (tracked in #461): repo.verify-clone,
// blog.verify-published, pack.verify-completed, slides.verify-
// rendered, content.verify-grounded, pipeline.verify-completion.
// Same shape, different ground-truth source.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// ArtifactVerifyManifest constructs the pack. No external deps,
// no session. Pure passthrough to ArtifactStore.Get per claimed
// key, accumulating verified vs missing.
func ArtifactVerifyManifest() *packs.Pack {
	return &packs.Pack{
		Name:    "artifact.verify_manifest",
		Version: "v1",
		Description: "Verify that a list of artifact keys actually exist in the store. Use this " +
			"after a multi-step workflow that produced (or claimed to produce) several artifacts — " +
			"a Tier C model can hallucinate a deposit manifest table with fabricated keys; calling " +
			"this pack with each claimed key forces an explicit verify-against-ground-truth step. " +
			"Returns {verified, missing, all_present, summary}. The LLM's next response turn sees " +
			"the structured result in context and can surface the gap to the operator instead of " +
			"hiding behind plausibility-shaped output.",
		NeedsSession: false,
		Async:        false,
		InputSchema: packs.BasicSchema{
			Required: []string{"expected"},
			Properties: map[string]string{
				"expected": "array",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"verified", "missing", "all_present", "summary"},
			Properties: map[string]string{
				"verified":    "array",
				"missing":     "array",
				"all_present": "boolean",
				"summary":     "string",
			},
		},
		Metadata: packs.PackMetadata{
			Accepts:  []string{"artifact_key", "manifest"},
			Produces: []string{"verification_report"},
			IntentKeywords: []string{
				"verify artifacts exist", "audit deposit manifest",
				"check artifact keys", "confirm deposit happened",
			},
			TypicalUse: "Final audit step after a multi-artifact workflow. Pair with the " +
				"deposit step in any skill that produces multiple artifacts (tech-blog-publisher, " +
				"future research-summarizer, etc.) so a Tier C model's hallucinated manifest " +
				"surfaces as missing[] entries instead of silently misleading the operator.",
			Limitations: []string{
				"verifies existence only — does not compare content against an expected hash",
				"missing[] reasons are best-effort store errors; semantic-level reasons (e.g. wrong namespace) require the caller to interpret",
			},
		},
		Handler: artifactVerifyManifestHandler(),
	}
}

// artifactVerifyExpectedEntry is the object form of an expected
// entry. The pack ALSO accepts flat strings ("key1", "key2") for
// Tier C friendliness — see decodeExpected below.
type artifactVerifyExpectedEntry struct {
	ArtifactKey string `json:"artifact_key"`
}

// artifactVerifyOutputVerified mirrors what the store returns
// for a found artifact, decomposed so the LLM can read filename/
// namespace without re-parsing the key.
type artifactVerifyOutputVerified struct {
	ArtifactKey string `json:"artifact_key"`
	Filename    string `json:"filename"`
	Namespace   string `json:"namespace"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

type artifactVerifyOutputMissing struct {
	ArtifactKey string `json:"artifact_key"`
	Reason      string `json:"reason"`
}

func artifactVerifyManifestHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		// Two-pass decode: first try the object shape; on shape
		// mismatch (Tier C passed flat strings) fall back to the
		// string shape. Keep both code paths simple — neither
		// validates beyond "is the key a non-empty string."
		keys, err := decodeExpected(ec.Input)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if len(keys) == 0 {
			return nil, &packs.PackError{
				Code:    packs.CodeInvalidInput,
				Message: "artifact.verify_manifest: expected must contain at least one artifact_key",
			}
		}
		if ec.Artifacts == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeArtifactFailed,
				Message: "artifact.verify_manifest: no artifact store wired into this execution context",
			}
		}

		// Dedupe while preserving first-seen order. A Tier C model
		// listing the same key twice in its manifest shouldn't
		// hammer the store; one Get per unique key is enough.
		seen := make(map[string]bool, len(keys))
		var uniq []string
		for _, k := range keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			uniq = append(uniq, k)
		}

		verified := make([]artifactVerifyOutputVerified, 0, len(uniq))
		missing := make([]artifactVerifyOutputMissing, 0)
		for _, key := range uniq {
			_, art, gerr := ec.Artifacts.Get(ctx, key)
			if gerr != nil {
				missing = append(missing, artifactVerifyOutputMissing{
					ArtifactKey: key,
					Reason:      gerr.Error(),
				})
				continue
			}
			filename, namespace := splitArtifactKey(art.Key)
			verified = append(verified, artifactVerifyOutputVerified{
				ArtifactKey: art.Key,
				Filename:    filename,
				Namespace:   namespace,
				Size:        art.Size,
				ContentType: art.ContentType,
			})
		}

		allPresent := len(missing) == 0
		summary := fmt.Sprintf("%d of %d claimed artifacts verified; %d missing",
			len(verified), len(uniq), len(missing))

		out := map[string]any{
			"verified":    verified,
			"missing":     missing,
			"all_present": allPresent,
			"summary":     summary,
		}
		return json.Marshal(out)
	}
}

// decodeExpected accepts both shapes:
//
//	{"expected": [{"artifact_key": "ns/abc-name.md"}, ...]}
//	{"expected": ["ns/abc-name.md", ...]}
//
// Both forms work because the BasicSchema only validates that
// `expected` is an array; element type isn't enforced. Tier C
// models routinely pick one shape or the other; accepting both
// is the same "make the pack succeed when the caller's intent is
// clear" posture as defaultPackModel() (see model_defaults.go).
//
// Empty strings inside either shape are dropped silently — they
// would Get an empty key from the store and surface as missing,
// but the more useful signal is "the caller's manifest entry was
// malformed" so we drop and let len(uniq) == 0 escalate.
func decodeExpected(rawInput json.RawMessage) ([]string, error) {
	// Outer wrapper is always {"expected": <...>}; parse the
	// wrapper as raw JSON so we can pivot on the element type.
	var wrapper struct {
		Expected json.RawMessage `json:"expected"`
	}
	if err := json.Unmarshal(rawInput, &wrapper); err != nil {
		return nil, fmt.Errorf("artifact.verify_manifest: %w", err)
	}
	if len(wrapper.Expected) == 0 {
		return nil, fmt.Errorf("artifact.verify_manifest: expected field is required")
	}

	// Try object shape first; it's the spec-canonical form.
	var asObjects []artifactVerifyExpectedEntry
	if err := json.Unmarshal(wrapper.Expected, &asObjects); err == nil {
		// Object decode succeeded — but if EVERY entry is the zero
		// value, it's almost certainly because the caller sent
		// strings instead. Fall through to the string path.
		anyNonEmpty := false
		out := make([]string, 0, len(asObjects))
		for _, e := range asObjects {
			k := strings.TrimSpace(e.ArtifactKey)
			if k != "" {
				out = append(out, k)
				anyNonEmpty = true
			}
		}
		if anyNonEmpty {
			return out, nil
		}
	}

	// Fall back to flat-string shape.
	var asStrings []string
	if err := json.Unmarshal(wrapper.Expected, &asStrings); err == nil {
		out := make([]string, 0, len(asStrings))
		for _, k := range asStrings {
			k = strings.TrimSpace(k)
			if k != "" {
				out = append(out, k)
			}
		}
		return out, nil
	}

	return nil, fmt.Errorf("artifact.verify_manifest: expected must be an array of " +
		"objects {artifact_key: \"...\"} or an array of strings")
}
