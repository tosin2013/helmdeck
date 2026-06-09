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

func runArtifactList(t *testing.T, store packs.ArtifactStore, input string) (map[string]any, error) {
	t.Helper()
	pack := ArtifactList()
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

func TestArtifactList_EmptyStore(t *testing.T) {
	out, err := runArtifactList(t, packs.NewMemoryArtifactStore(), `{}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if count, _ := out["count"].(float64); count != 0 {
		t.Errorf("count = %v, want 0", count)
	}
	arr, _ := out["artifacts"].([]any)
	if len(arr) != 0 {
		t.Errorf("artifacts len = %d, want 0", len(arr))
	}
}

func TestArtifactList_ListAll(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	seedArtifact(t, store, "blog.publish", "a.md", "text/markdown", []byte("a"))
	seedArtifact(t, store, "av.validate", "v.json", "application/json", []byte("{}"))
	seedArtifact(t, store, "image.generate", "img.png", "image/png", []byte("x"))

	out, err := runArtifactList(t, store, `{}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	count, _ := out["count"].(float64)
	if int(count) != 3 {
		t.Errorf("count = %v, want 3", count)
	}
}

func TestArtifactList_NamespaceFilter(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	seedArtifact(t, store, "blog.publish", "a.md", "text/markdown", []byte("a"))
	seedArtifact(t, store, "blog.publish", "b.md", "text/markdown", []byte("b"))
	seedArtifact(t, store, "av.validate", "v.json", "application/json", []byte("{}"))

	out, err := runArtifactList(t, store, `{"namespace":"blog.publish"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	count, _ := out["count"].(float64)
	if int(count) != 2 {
		t.Errorf("count = %v, want 2 (blog.publish only)", count)
	}
	for _, raw := range out["artifacts"].([]any) {
		entry := raw.(map[string]any)
		if entry["namespace"] != "blog.publish" {
			t.Errorf("entry namespace = %v, want blog.publish", entry["namespace"])
		}
	}
}

func TestArtifactList_FilenameSubstringFilter(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	seedArtifact(t, store, "test", "draft.md", "text/markdown", []byte("a"))
	seedArtifact(t, store, "test", "final.md", "text/markdown", []byte("b"))
	seedArtifact(t, store, "test", "notes.txt", "text/plain", []byte("c"))

	out, err := runArtifactList(t, store, `{"filename":"final"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	count, _ := out["count"].(float64)
	if int(count) != 1 {
		t.Errorf("count = %v, want 1 (substring 'final')", count)
	}

	// Case-insensitive.
	out, err = runArtifactList(t, store, `{"filename":"FINAL"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	count, _ = out["count"].(float64)
	if int(count) != 1 {
		t.Errorf("case-insensitive: count = %v, want 1", count)
	}

	// .md substring matches two.
	out, err = runArtifactList(t, store, `{"filename":".md"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	count, _ = out["count"].(float64)
	if int(count) != 2 {
		t.Errorf("'.md' filter: count = %v, want 2", count)
	}
}

func TestArtifactList_NamespaceAndFilenameCombined(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	seedArtifact(t, store, "blog.publish", "draft.md", "text/markdown", []byte("a"))
	seedArtifact(t, store, "blog.publish", "final.md", "text/markdown", []byte("b"))
	seedArtifact(t, store, "av.validate", "final.json", "application/json", []byte("{}"))

	out, err := runArtifactList(t, store, `{"namespace":"blog.publish","filename":"final"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	count, _ := out["count"].(float64)
	if int(count) != 1 {
		t.Errorf("combined: count = %v, want 1", count)
	}
}

func TestArtifactList_LimitAndTruncation(t *testing.T) {
	store := packs.NewMemoryArtifactStore()
	for i := 0; i < 5; i++ {
		seedArtifact(t, store, "test", "file", "text/plain", []byte("x"))
	}

	out, err := runArtifactList(t, store, `{"limit":3}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	count, _ := out["count"].(float64)
	if int(count) != 3 {
		t.Errorf("limit=3: count = %v, want 3", count)
	}
	if truncated, _ := out["truncated"].(bool); !truncated {
		t.Error("expected truncated=true")
	}

	out, err = runArtifactList(t, store, `{"limit":100}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if truncated, _ := out["truncated"].(bool); truncated {
		t.Error("expected truncated=false when limit > result count")
	}
}

func TestArtifactList_ErrorPaths(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		store    packs.ArtifactStore
		wantCode packs.ErrorCode
	}{
		{
			name:     "no store wired",
			input:    `{}`,
			store:    nil,
			wantCode: packs.CodeArtifactFailed,
		},
		{
			name:     "malformed json",
			input:    `{bad`,
			store:    packs.NewMemoryArtifactStore(),
			wantCode: packs.CodeInvalidInput,
		},
		{
			name:     "list backend error",
			input:    `{}`,
			store:    failingListStore{},
			wantCode: packs.CodeArtifactFailed,
		},
		{
			name:     "namespace list backend error",
			input:    `{"namespace":"x"}`,
			store:    failingListStore{},
			wantCode: packs.CodeArtifactFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := ArtifactList()
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

// failingListStore returns errors from ListAll and ListForPack so we
// can exercise the CodeArtifactFailed branches in artifact.list.
type failingListStore struct{}

func (failingListStore) Put(_ context.Context, _, _ string, _ []byte, _ string) (packs.Artifact, error) {
	return packs.Artifact{}, errors.New("nope")
}
func (failingListStore) ListForPack(_ context.Context, _ string) ([]packs.Artifact, error) {
	return nil, errors.New("namespace list failed")
}
func (failingListStore) Get(_ context.Context, _ string) ([]byte, packs.Artifact, error) {
	return nil, packs.Artifact{}, errors.New("not found")
}
func (failingListStore) ListAll(_ context.Context) ([]packs.Artifact, error) {
	return nil, errors.New("list-all failed")
}
func (failingListStore) Delete(_ context.Context, _ string) error { return nil }
