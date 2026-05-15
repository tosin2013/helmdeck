// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// writeManifest is the YAML counterpart to writeExecutable in the
// existing command_pack_example_test.go — drops a manifest file at
// dir/<basename>.helmdeck-pack.yaml.
func writeManifest(t *testing.T, dir, basename, body string) string {
	t.Helper()
	path := filepath.Join(dir, basename+manifestSuffix)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// asBasicSchema unwraps a Pack's Schema to the concrete BasicSchema
// so tests can assert on Required / Properties. NewCommandPack accepts
// any packs.Schema; the manifest loader always passes BasicSchema, so
// this cast should never fail when called on a manifest-loaded pack.
func asBasicSchema(t *testing.T, s packs.Schema) packs.BasicSchema {
	t.Helper()
	bs, ok := s.(packs.BasicSchema)
	if !ok {
		t.Fatalf("expected packs.BasicSchema, got %T", s)
	}
	return bs
}

func TestLoadCommandPacks_WithValidManifest_TypedSchemas(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "upper", `tr '[:lower:]' '[:upper:]'`)
	writeManifest(t, dir, "upper", `
name: cmd.upper
version: v2
description: Uppercase a string.
author: Test Author
input_schema:
  required: [text]
  properties:
    text: string
output_schema:
  required: [text]
  properties:
    text: string
`)
	got := LoadCommandPacks(context.Background(), nil, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(got))
	}
	p := got[0]
	if p.Name != "cmd.upper" {
		t.Errorf("name = %q, want cmd.upper", p.Name)
	}
	if p.Version != "v2" {
		t.Errorf("version = %q, want v2 (manifest override)", p.Version)
	}
	if p.Description != "Uppercase a string." {
		t.Errorf("description = %q, want manifest value", p.Description)
	}
	in := asBasicSchema(t, p.InputSchema)
	if len(in.Required) != 1 || in.Required[0] != "text" {
		t.Errorf("input Required = %v, want [text]", in.Required)
	}
	if in.Properties["text"] != "string" {
		t.Errorf("input Properties[text] = %q, want string", in.Properties["text"])
	}
	out := asBasicSchema(t, p.OutputSchema)
	if len(out.Required) != 1 || out.Required[0] != "text" {
		t.Errorf("output Required = %v, want [text]", out.Required)
	}
}

func TestLoadCommandPacks_MissingManifest_FallsBackToPassthrough(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "echo", `cat`)
	got := LoadCommandPacks(context.Background(), nil, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(got))
	}
	in := asBasicSchema(t, got[0].InputSchema)
	if len(in.Required) != 0 || len(in.Properties) != 0 {
		t.Errorf("expected empty (passthrough) schema, got %+v", in)
	}
}

func TestLoadCommandPacks_MalformedYAML_SkipsPack(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "bad", `cat`)
	writeManifest(t, dir, "bad", "this: is: not: valid: yaml: [unclosed\n")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	got := LoadCommandPacks(context.Background(), logger, dir)
	if len(got) != 0 {
		t.Errorf("expected 0 packs (malformed manifest should skip), got %d", len(got))
	}
	if !bytes.Contains(buf.Bytes(), []byte("manifest invalid")) {
		t.Errorf("expected 'manifest invalid' in log, got: %s", buf.String())
	}
}

func TestLoadCommandPacks_UnknownSchemaType_SkipsPack(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "wrong", `cat`)
	writeManifest(t, dir, "wrong", `
input_schema:
  properties:
    age: integer
`)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	got := LoadCommandPacks(context.Background(), logger, dir)
	if len(got) != 0 {
		t.Errorf("expected 0 packs (unknown type should skip), got %d", len(got))
	}
	if !bytes.Contains(buf.Bytes(), []byte("unknown type")) {
		t.Errorf("expected 'unknown type' in log, got: %s", buf.String())
	}
}

func TestLoadCommandPacks_NegativeTimeout_SkipsPack(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "negtimer", `cat`)
	writeManifest(t, dir, "negtimer", "timeout_s: -5\n")
	got := LoadCommandPacks(context.Background(), nil, dir)
	if len(got) != 0 {
		t.Errorf("expected 0 packs (negative timeout should skip), got %d", len(got))
	}
}

// Manifest-declared timeout actually takes effect: write a binary
// that sleeps for 2s, set the manifest timeout to 1s, assert the
// handler errors with a timeout message. End-to-end coverage that
// toCommandSpec correctly threads the manifest value into the spec.
func TestLoadCommandPacks_ManifestTimeoutEnforced(t *testing.T) {
	dir := t.TempDir()
	// `exec sleep 5` replaces sh with sleep so there's no orphan child
	// holding the stdout pipe open after kill. Without `exec` sh forks
	// sleep, gets killed by Context, but sleep keeps the pipe open and
	// cmd.Wait stalls until sleep exits naturally — defeating the test.
	writeExecutable(t, dir, "slowpoke", `exec sleep 5`)
	writeManifest(t, dir, "slowpoke", "timeout_s: 1\n")
	got := LoadCommandPacks(context.Background(), nil, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(got))
	}
	ec := &packs.ExecutionContext{Input: json.RawMessage(`{}`)}
	start := time.Now()
	_, err := got[0].Handler(context.Background(), ec)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("handler ran %s, expected ~1s with manifest timeout=1s (5s natural duration)", elapsed)
	}
	pe, ok := err.(*packs.PackError)
	if !ok {
		t.Fatalf("expected *packs.PackError, got %T: %v", err, err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("error code = %q, want CodeHandlerFailed", pe.Code)
	}
}

// Manifest-supplied env actually reaches the subprocess. We assert
// via a binary that echoes a chosen env var back as JSON, then verify
// the response payload contains the value we set.
func TestLoadCommandPacks_ManifestEnvPropagated(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "envprint", `printf '{"v":"%s"}' "$HELMDECK_TEST_VAR"`)
	writeManifest(t, dir, "envprint", `
env:
  - HELMDECK_TEST_VAR=hello-from-manifest
`)
	got := LoadCommandPacks(context.Background(), nil, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(got))
	}
	ec := &packs.ExecutionContext{Input: json.RawMessage(`{}`)}
	out, err := got[0].Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	var got1 map[string]string
	if err := json.Unmarshal(out, &got1); err != nil {
		t.Fatalf("unmarshal pack output: %v", err)
	}
	if got1["v"] != "hello-from-manifest" {
		t.Errorf("env var value = %q, want hello-from-manifest", got1["v"])
	}
}

// Confirm that schema validation against a manifest-loaded pack
// rejects malformed input (missing required field). The handler
// itself doesn't enforce — the engine does, by calling InputSchema.
// Validate before dispatching. This test goes through Validate directly
// to keep the unit test scope contained.
func TestLoadCommandPacks_ManifestSchema_RejectsMissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "upper", `cat`)
	writeManifest(t, dir, "upper", `
input_schema:
  required: [text]
  properties:
    text: string
`)
	got := LoadCommandPacks(context.Background(), nil, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(got))
	}
	if err := got[0].InputSchema.Validate(json.RawMessage(`{}`)); err == nil {
		t.Errorf("expected validation error for missing 'text', got nil")
	}
	if err := got[0].InputSchema.Validate(json.RawMessage(`{"text":"abc"}`)); err != nil {
		t.Errorf("expected validation pass with text field, got: %v", err)
	}
	if err := got[0].InputSchema.Validate(json.RawMessage(`{"text":123}`)); err == nil {
		t.Errorf("expected validation error for wrong type, got nil")
	}
}

// Loader log shape — manifest path is included for the manifest case
// and the passthrough log line is distinct. Operators rely on this
// to confirm which manifest applied at startup time.
func TestLoadCommandPacks_LogsDistinguishManifestVsPassthrough(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "with", `cat`)
	writeManifest(t, dir, "with", "version: v1\n")
	writeExecutable(t, dir, "without", `cat`)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	LoadCommandPacks(context.Background(), logger, dir)
	logStr := buf.String()
	if !bytes.Contains([]byte(logStr), []byte(`name=cmd.with`)) ||
		!bytes.Contains([]byte(logStr), []byte(`manifest=`)) {
		t.Errorf("expected 'cmd.with' log to mention manifest, got: %s", logStr)
	}
	if !bytes.Contains([]byte(logStr), []byte(`passthrough — no manifest`)) {
		t.Errorf("expected 'passthrough — no manifest' log for cmd.without, got: %s", logStr)
	}
}

// Manifest name field disagreeing with auto-derived basename emits a
// warning but still registers under the auto-derived name. Operators
// renaming a binary without updating manifest get a hint that they
// have a drift; the pack still works.
func TestLoadCommandPacks_ManifestNameMismatch_WarnsButRegisters(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "real", `cat`)
	writeManifest(t, dir, "real", "name: cmd.different\n")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	got := LoadCommandPacks(context.Background(), logger, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(got))
	}
	if got[0].Name != "cmd.real" {
		t.Errorf("name = %q, want cmd.real (auto-derived wins)", got[0].Name)
	}
	if !bytes.Contains(buf.Bytes(), []byte("manifest name disagrees")) {
		t.Errorf("expected warning about name disagreement, got: %s", buf.String())
	}
}

// io.Discard avoids polluting test output when the logger isn't
// under inspection; the existing TestLoadCommandPacks_* tests rely
// on this same pattern.
var _ = io.Discard
