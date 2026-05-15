// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package marketplace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- ResolveIndexURL ---------------------------------------------------

func TestResolveIndexURL(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		// github.com/<owner>/<repo> → raw.githubusercontent.com/.../main/index.yaml
		{
			"https://github.com/tosin2013/helmdeck-marketplace",
			"https://raw.githubusercontent.com/tosin2013/helmdeck-marketplace/main/index.yaml",
			false,
		},
		// Trailing slash tolerated.
		{
			"https://github.com/tosin2013/helmdeck-marketplace/",
			"https://raw.githubusercontent.com/tosin2013/helmdeck-marketplace/main/index.yaml",
			false,
		},
		// Already a raw URL — pass through.
		{
			"https://raw.githubusercontent.com/foo/bar/main/index.yaml",
			"https://raw.githubusercontent.com/foo/bar/main/index.yaml",
			false,
		},
		// file:// pass-through (caller responsibility to point at the file).
		{
			"file:///tmp/marketplace/index.yaml",
			"file:///tmp/marketplace/index.yaml",
			false,
		},
		// Custom mirror without trailing /index.yaml → append.
		{
			"https://mirror.example.com/marketplace",
			"https://mirror.example.com/marketplace/index.yaml",
			false,
		},
		// Custom mirror that already names index.yaml — pass through.
		{
			"https://mirror.example.com/marketplace/index.yaml",
			"https://mirror.example.com/marketplace/index.yaml",
			false,
		},
		// Empty input → error.
		{"", "", true},
		// github.com without owner+repo → error.
		{"https://github.com/", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ResolveIndexURL(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", c.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("resolveIndexURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- LoadIndex via file:// --------------------------------------------

func TestLoadIndex_FileFixture(t *testing.T) {
	dir := t.TempDir()
	body := `catalog_version: v1
packs:
  - name: cmd.upper
    version: v1
    path: packs/cmd.upper
    description: Uppercase a string
    author: tosin2013
    category: developer-tools
    tags: [example, string]
`
	indexPath := filepath.Join(dir, "index.yaml")
	if err := os.WriteFile(indexPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, resolved, err := LoadIndex(context.Background(), "file://"+indexPath)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if idx.CatalogVersion != "v1" {
		t.Errorf("catalog_version = %q", idx.CatalogVersion)
	}
	if len(idx.Packs) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(idx.Packs))
	}
	if idx.Packs[0].Name != "cmd.upper" {
		t.Errorf("pack name = %q", idx.Packs[0].Name)
	}
	if resolved != "file://"+indexPath {
		t.Errorf("resolved = %q", resolved)
	}
}

// --- LoadIndex via HTTP -----------------------------------------------

func TestLoadIndex_HTTPFixture(t *testing.T) {
	body := `catalog_version: v1
packs:
  - name: cmd.upper
    version: v1
    path: packs/cmd.upper
    description: A test pack
    author: tester
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/index.yaml") {
			http.Error(w, "want index.yaml path, got "+r.URL.Path, 404)
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	idx, _, err := LoadIndex(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if len(idx.Packs) != 1 || idx.Packs[0].Name != "cmd.upper" {
		t.Errorf("unexpected packs: %+v", idx.Packs)
	}
}

func TestLoadIndex_HTTPMalformedYAML_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is: not: valid: yaml: [unclosed"))
	}))
	defer srv.Close()
	_, _, err := LoadIndex(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse failure: %v", err)
	}
}

func TestLoadIndex_HTTP404_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", 404)
	}))
	defer srv.Close()
	_, _, err := LoadIndex(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected fetch error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention status code: %v", err)
	}
}

// --- LoadManifest ------------------------------------------------------

func TestLoadManifest_FileFixture(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "packs/cmd.upper"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `name: cmd.upper
version: v1
author: tosin2013
license: Apache-2.0
description: Uppercase a string.
category: developer-tools

input_schema:
  required: [text]
  properties:
    text:
      type: string
      description: input string

output_schema:
  required: [text]
  properties:
    text:
      type: string

handler:
  type: command
  command: ["./upper"]
  timeout_s: 30
`
	if err := os.WriteFile(filepath.Join(dir, "packs/cmd.upper/helmdeck-pack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(context.Background(), "file://"+dir, "packs/cmd.upper")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "cmd.upper" || m.Version != "v1" {
		t.Errorf("unexpected manifest: %+v", m)
	}
	if m.Handler.Type != "command" || len(m.Handler.Command) != 1 || m.Handler.Command[0] != "./upper" {
		t.Errorf("handler = %+v", m.Handler)
	}
	if m.Handler.TimeoutSec != 30 {
		t.Errorf("timeout_s = %d, want 30", m.Handler.TimeoutSec)
	}
	if m.InputSchema.Properties["text"].Type != "string" {
		t.Errorf("input_schema.properties.text.type = %+v", m.InputSchema)
	}
}
