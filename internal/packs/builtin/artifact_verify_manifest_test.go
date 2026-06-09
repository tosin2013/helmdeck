// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func runVerifyManifest(t *testing.T, store packs.ArtifactStore, input string) (map[string]any, error) {
	t.Helper()
	pack := ArtifactVerifyManifest()
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		return nil, err
	}
	if verr := pack.OutputSchema.Validate(raw); verr != nil {
		t.Fatalf("output failed declared schema: %v", verr)
	}
	var out map[string]any
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("unmarshal output: %v", uerr)
	}
	return out, nil
}

func TestVerifyManifest_AllPresent_ObjectShape(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	k1 := seedArtifact(t, store, "blog.publish", "post-a.md", "text/markdown", []byte("# A\n\nbody"))
	k2 := seedArtifact(t, store, "blog.publish", "post-b.md", "text/markdown", []byte("# B\n\nbody body"))

	input := `{"expected":[{"artifact_key":` + strconvQuote(k1) + `},{"artifact_key":` + strconvQuote(k2) + `}]}`
	out, err := runVerifyManifest(t, store, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if ap, _ := out["all_present"].(bool); !ap {
		t.Errorf("all_present = false, want true")
	}
	if v, _ := out["verified"].([]any); len(v) != 2 {
		t.Errorf("verified count = %d, want 2", len(v))
	}
	if m, _ := out["missing"].([]any); len(m) != 0 {
		t.Errorf("missing count = %d, want 0", len(m))
	}
	if s, _ := out["summary"].(string); s != "2 of 2 claimed artifacts verified; 0 missing" {
		t.Errorf("summary = %q", s)
	}
}

func TestVerifyManifest_AllPresent_FlatStringShape(t *testing.T) {
	// Tier C friendliness: accept ["k1","k2"] in addition to
	// [{artifact_key:"k1"}, ...].
	store := packs.NewMemoryArtifactStore()
	k1 := seedArtifact(t, store, "blog.publish", "post-a.md", "text/markdown", []byte("a"))
	k2 := seedArtifact(t, store, "blog.publish", "post-b.md", "text/markdown", []byte("b"))

	input := `{"expected":[` + strconvQuote(k1) + `,` + strconvQuote(k2) + `]}`
	out, err := runVerifyManifest(t, store, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if ap, _ := out["all_present"].(bool); !ap {
		t.Errorf("all_present = false, want true")
	}
	if v, _ := out["verified"].([]any); len(v) != 2 {
		t.Errorf("verified count = %d, want 2", len(v))
	}
}

func TestVerifyManifest_PartialMissing_TheTodayTrace(t *testing.T) {
	// Reproduce the 2026-06-09 mcp-adr-analysis-server failure:
	// agent claimed 6 deposits, ground truth had 0. Empirical
	// proof that the audit pack surfaces the gap.
	store := packs.NewMemoryArtifactStore()
	// Operator deposited ONE artifact (the only real one — the
	// platform-engineers rewrite). The other 5 manifest entries
	// are fabricated.
	real1 := seedArtifact(t, store, "blog.publish", "mcp-adr-canonical.md", "text/markdown", []byte("# Canonical"))

	input := `{"expected":[
		{"artifact_key":` + strconvQuote(real1) + `},
		{"artifact_key":"blog.publish/abc-mcp-adr-linkedin.md"},
		{"artifact_key":"blog.publish/def-mcp-adr-devto.md"},
		{"artifact_key":"blog.publish/ghi-mcp-adr-dzone.md"},
		{"artifact_key":"blog.publish/jkl-mcp-adr-medium.md"},
		{"artifact_key":"blog.publish/mno-mcp-adr-hackernoon.md"}
	]}`
	out, err := runVerifyManifest(t, store, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if ap, _ := out["all_present"].(bool); ap {
		t.Errorf("all_present = true, want false (5 hallucinated entries)")
	}
	verified, _ := out["verified"].([]any)
	if len(verified) != 1 {
		t.Errorf("verified count = %d, want 1", len(verified))
	}
	missing, _ := out["missing"].([]any)
	if len(missing) != 5 {
		t.Errorf("missing count = %d, want 5", len(missing))
	}
	if s, _ := out["summary"].(string); s != "1 of 6 claimed artifacts verified; 5 missing" {
		t.Errorf("summary = %q", s)
	}
}

func TestVerifyManifest_AllMissing(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	input := `{"expected":[
		{"artifact_key":"never/exists-a.md"},
		{"artifact_key":"never/exists-b.md"}
	]}`
	out, err := runVerifyManifest(t, store, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if ap, _ := out["all_present"].(bool); ap {
		t.Errorf("all_present = true, want false")
	}
	if v, _ := out["verified"].([]any); len(v) != 0 {
		t.Errorf("verified count = %d, want 0", len(v))
	}
	if m, _ := out["missing"].([]any); len(m) != 2 {
		t.Errorf("missing count = %d, want 2", len(m))
	}
}

func TestVerifyManifest_DeduplicatesDuplicateKeys(t *testing.T) {
	// A Tier C model listing the same key twice shouldn't double-Get
	// the store. Verified should reflect the unique-key count.
	store := packs.NewMemoryArtifactStore()
	k1 := seedArtifact(t, store, "blog.publish", "post.md", "text/markdown", []byte("body"))
	input := `{"expected":[
		{"artifact_key":` + strconvQuote(k1) + `},
		{"artifact_key":` + strconvQuote(k1) + `},
		{"artifact_key":` + strconvQuote(k1) + `}
	]}`
	out, err := runVerifyManifest(t, store, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if v, _ := out["verified"].([]any); len(v) != 1 {
		t.Errorf("verified count = %d, want 1 (dedup)", len(v))
	}
	if s, _ := out["summary"].(string); s != "1 of 1 claimed artifacts verified; 0 missing" {
		t.Errorf("summary should report unique count: got %q", s)
	}
}

func TestVerifyManifest_EmptyEntriesDroppedSilently(t *testing.T) {
	// Whitespace-only and empty-string entries get dropped during
	// decode; they're not useful signal. Surviving entries are
	// what get verified.
	store := packs.NewMemoryArtifactStore()
	k1 := seedArtifact(t, store, "blog.publish", "real.md", "text/markdown", []byte("body"))
	input := `{"expected":[
		{"artifact_key":""},
		{"artifact_key":"   "},
		{"artifact_key":` + strconvQuote(k1) + `}
	]}`
	out, err := runVerifyManifest(t, store, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if v, _ := out["verified"].([]any); len(v) != 1 {
		t.Errorf("verified count = %d, want 1", len(v))
	}
	if s, _ := out["summary"].(string); s != "1 of 1 claimed artifacts verified; 0 missing" {
		t.Errorf("summary = %q", s)
	}
}

func TestVerifyManifest_VerifiedEntryShape(t *testing.T) {
	// Each verified entry should carry the metadata an LLM needs to
	// report meaningfully back to the operator: filename, namespace,
	// size, content_type.
	store := packs.NewMemoryArtifactStore()
	k1 := seedArtifact(t, store, "blog.publish", "post.md", "text/markdown", []byte("# Hello\n\nbody"))
	input := `{"expected":[{"artifact_key":` + strconvQuote(k1) + `}]}`
	out, err := runVerifyManifest(t, store, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	verified, _ := out["verified"].([]any)
	if len(verified) != 1 {
		t.Fatalf("verified count = %d", len(verified))
	}
	e := verified[0].(map[string]any)
	if e["filename"] != "post.md" {
		t.Errorf("filename = %v, want post.md", e["filename"])
	}
	if e["namespace"] != "blog.publish" {
		t.Errorf("namespace = %v, want blog.publish", e["namespace"])
	}
	if e["content_type"] != "text/markdown" {
		t.Errorf("content_type = %v", e["content_type"])
	}
	if sz, _ := e["size"].(float64); int(sz) != len("# Hello\n\nbody") {
		t.Errorf("size = %v, want %d", sz, len("# Hello\n\nbody"))
	}
}

func TestVerifyManifest_ErrorPaths(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		store    packs.ArtifactStore
		wantCode packs.ErrorCode
	}{
		{
			name:     "missing expected field",
			input:    `{}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "empty expected array",
			input:    `{"expected":[]}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "all-empty entries collapse to zero usable keys",
			input:    `{"expected":[{"artifact_key":""},{"artifact_key":"  "}]}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "expected is wrong type",
			input:    `{"expected":"not-an-array"}`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "malformed json",
			input:    `{bad`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "no artifact store wired",
			input:    `{"expected":[{"artifact_key":"x/y-z.md"}]}`,
			store:    nil,
			wantCode: packs.CodeArtifactFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := ArtifactVerifyManifest()
			ec := &packs.ExecutionContext{
				Pack:      pack,
				Input:     json.RawMessage(tc.input),
				Artifacts: tc.store,
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			_, err := pack.Handler(context.Background(), ec)
			if err == nil {
				t.Fatal("expected error")
			}
			var pe *packs.PackError
			if !errors.As(err, &pe) {
				t.Fatalf("expected PackError, got %T: %v", err, err)
			}
			if pe.Code != tc.wantCode {
				t.Errorf("code = %s, want %s", pe.Code, tc.wantCode)
			}
		})
	}
}

func TestVerifyManifest_RoundTripFromPutThenVerify(t *testing.T) {
	// Compose the end-to-end deposit-then-audit flow: put two
	// artifacts via artifact.put, then verify both via
	// artifact.verify_manifest. Proof that the pair works as a
	// matched producer/consumer.
	store := packs.NewMemoryArtifactStore()

	put := func(content string) string {
		t.Helper()
		pack := ArtifactPut()
		ec := &packs.ExecutionContext{
			Pack:      pack,
			Input:     json.RawMessage(`{"content":` + strconvQuote(content) + `,"kind":"blog","namespace":"blog.publish"}`),
			Artifacts: store,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		raw, err := pack.Handler(context.Background(), ec)
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		var out map[string]any
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			t.Fatalf("decode put output: %v", uerr)
		}
		return out["artifact_key"].(string)
	}

	k1 := put("# Variation 1\n\nbody body body")
	k2 := put("# Variation 2\n\nmore content")

	out, err := runVerifyManifest(t, store, `{"expected":[
		{"artifact_key":`+strconvQuote(k1)+`},
		{"artifact_key":`+strconvQuote(k2)+`}
	]}`)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ap, _ := out["all_present"].(bool); !ap {
		t.Errorf("all_present = false after round-trip")
	}
}
