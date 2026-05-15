// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// scaffoldMarketplace builds a file:// marketplace at `dir` with one
// or more packs. Each pack's name → handler-body map seeds a bash
// echo-style handler under packs/<name>/<handler>.
func scaffoldMarketplace(t *testing.T, dir string, packs map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "packs"), 0o755); err != nil {
		t.Fatal(err)
	}
	type idxEntry struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version"`
		Path        string `yaml:"path"`
		Description string `yaml:"description"`
		Author      string `yaml:"author"`
	}
	idxBody := "catalog_version: v1\npacks:\n"
	for name, handlerBody := range packs {
		packDir := filepath.Join(dir, "packs", name)
		if err := os.MkdirAll(packDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Manifest: minimal command-handler pack with one input/output.
		manifest := `name: ` + name + `
version: v1
author: tester
description: Test pack ` + name + `
input_schema:
  required: [text]
  properties:
    text:
      type: string
output_schema:
  required: [text]
  properties:
    text:
      type: string
handler:
  type: command
  command: ["./handler"]
  timeout_s: 5
`
		if err := os.WriteFile(filepath.Join(packDir, "helmdeck-pack.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packDir, "handler"), []byte(handlerBody), 0o755); err != nil {
			t.Fatal(err)
		}
		idxBody += "  - name: " + name + "\n"
		idxBody += "    version: v1\n"
		idxBody += "    path: packs/" + name + "\n"
		idxBody += "    description: Test pack\n"
		idxBody += "    author: tester\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(idxBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return "file://" + dir
}

// quietInstaller wires a fresh Installer + Service + pack registry
// against a file:// marketplace, with installDir under t.TempDir().
func quietInstaller(t *testing.T, marketplaceDir string) *Installer {
	t.Helper()
	src := "file://" + marketplaceDir
	svc := NewService(src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	reg := packs.NewPackRegistry()
	installDir := t.TempDir()
	return NewInstaller(svc, reg, installDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// --- happy path -------------------------------------------------------

func TestInstall_HappyPath_FileMarketplace(t *testing.T) {
	mktDir := t.TempDir()
	// Handler: echo-style — reads stdin, outputs `{text: "<input.text>"}`.
	handler := `#!/usr/bin/env bash
read -r line
echo '{"text":"installed-ok"}'
`
	_ = scaffoldMarketplace(t, mktDir, map[string]string{"cmd.test": handler})
	inst := quietInstaller(t, mktDir)

	res, err := inst.Install(context.Background(), "cmd.test")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Pack.Name != "cmd.test" {
		t.Errorf("installed name = %q", res.Pack.Name)
	}
	if res.Pack.Version != "v1" {
		t.Errorf("installed version = %q", res.Pack.Version)
	}
	if res.HotLoadedAs != "cmd.test" {
		t.Errorf("hot_loaded_as = %q", res.HotLoadedAs)
	}
	// Trust block missing → not verified, with a note.
	if res.Pack.TrustVerified {
		t.Errorf("expected TrustVerified=false for unsigned pack")
	}
	if res.Pack.TrustNote == "" {
		t.Errorf("expected TrustNote to explain why unsigned")
	}
	// Handler file actually copied to install dir + executable.
	handlerPath := filepath.Join(res.Pack.InstallDir, "handler")
	info, err := os.Stat(handlerPath)
	if err != nil {
		t.Fatalf("handler not copied: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Errorf("handler not executable: mode=%v", info.Mode())
	}
	// Pack registered in the registry.
	installed := inst.Installed()
	if len(installed) != 1 || installed[0].Name != "cmd.test" {
		t.Errorf("Installed() = %+v, want [{cmd.test}]", installed)
	}
}

// --- not in catalog ----------------------------------------------------

func TestInstall_PackNotInCatalog_ErrPackNotInCatalog(t *testing.T) {
	mktDir := t.TempDir()
	_ = scaffoldMarketplace(t, mktDir, map[string]string{})
	inst := quietInstaller(t, mktDir)
	_, err := inst.Install(context.Background(), "cmd.doesnotexist")
	if !errors.Is(err, ErrPackNotInCatalog) {
		t.Errorf("err = %v, want ErrPackNotInCatalog", err)
	}
}

// --- hot-load: registered pack callable from registry ----------------

func TestInstall_HotLoad_PackAppearsInRegistryAndDispatches(t *testing.T) {
	mktDir := t.TempDir()
	handler := `#!/usr/bin/env bash
read -r line
echo '{"text":"HOT-LOADED"}'
`
	_ = scaffoldMarketplace(t, mktDir, map[string]string{"cmd.hot": handler})
	inst := quietInstaller(t, mktDir)
	if _, err := inst.Install(context.Background(), "cmd.hot"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// The pack should now be in the registry; calling its handler
	// returns the canned JSON output.
	pack, err := inst.packReg.Get("cmd.hot", "")
	if err != nil {
		t.Fatalf("registry Get: %v", err)
	}
	if pack.Name != "cmd.hot" || pack.Version != "v1" {
		t.Errorf("got %s@%s, want cmd.hot@v1", pack.Name, pack.Version)
	}
	// Invoke the handler against a minimal ExecutionContext.
	ec := &packs.ExecutionContext{
		Pack:   pack,
		Input:  json.RawMessage(`{"text":"any"}`),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	out, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse handler output: %v", err)
	}
	if parsed.Text != "HOT-LOADED" {
		t.Errorf("handler output text = %q, want HOT-LOADED", parsed.Text)
	}
}

// --- uninstall --------------------------------------------------------

func TestUninstall_RemovesFromRegistryAndDisk(t *testing.T) {
	mktDir := t.TempDir()
	handler := `#!/usr/bin/env bash
read -r line
echo '{"text":"x"}'
`
	_ = scaffoldMarketplace(t, mktDir, map[string]string{"cmd.gone": handler})
	inst := quietInstaller(t, mktDir)
	if _, err := inst.Install(context.Background(), "cmd.gone"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	installRecord := inst.Installed()[0]

	if err := inst.Uninstall(context.Background(), "cmd.gone"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	// Disk: install dir gone.
	if _, err := os.Stat(installRecord.InstallDir); !os.IsNotExist(err) {
		t.Errorf("install dir still present after uninstall: %v", err)
	}
	// Registry: pack gone.
	if _, err := inst.packReg.Get("cmd.gone", ""); !errors.Is(err, packs.ErrPackNotFound) {
		t.Errorf("pack still in registry after uninstall: %v", err)
	}
	// Installed() empty.
	if got := inst.Installed(); len(got) != 0 {
		t.Errorf("Installed() = %+v, want empty", got)
	}
}

func TestUninstall_NotInstalled_ErrPackNotInstalled(t *testing.T) {
	mktDir := t.TempDir()
	_ = scaffoldMarketplace(t, mktDir, map[string]string{})
	inst := quietInstaller(t, mktDir)
	err := inst.Uninstall(context.Background(), "cmd.never_installed")
	if !errors.Is(err, ErrPackNotInstalled) {
		t.Errorf("err = %v, want ErrPackNotInstalled", err)
	}
}

// --- handler-type gating ----------------------------------------------

func TestInstall_NonCommandHandlerType_Rejects(t *testing.T) {
	// Hand-write a manifest with handler.type=builtin (reserved for core).
	mktDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mktDir, "packs/cmd.wasm"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `name: cmd.wasm
version: v1
author: tester
description: A wasm pack
input_schema: {}
output_schema: {}
handler:
  type: wasm
  module: handler.wasm
`
	if err := os.WriteFile(filepath.Join(mktDir, "packs/cmd.wasm/helmdeck-pack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := `catalog_version: v1
packs:
  - name: cmd.wasm
    version: v1
    path: packs/cmd.wasm
    description: wasm pack
    author: tester
`
	if err := os.WriteFile(filepath.Join(mktDir, "index.yaml"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := quietInstaller(t, mktDir)
	_, err := inst.Install(context.Background(), "cmd.wasm")
	if err == nil {
		t.Fatal("expected rejection of wasm handler type")
	}
	if !strings.Contains(err.Error(), "command-type") {
		t.Errorf("error should explain command-type-only, got: %v", err)
	}
}

// --- helper coverage --------------------------------------------------

func TestGitURLForSource(t *testing.T) {
	cases := map[string]struct {
		want   string
		errOK  bool
	}{
		"https://github.com/owner/repo":     {"https://github.com/owner/repo.git", false},
		"https://github.com/owner/repo/":    {"https://github.com/owner/repo.git", false},
		"https://github.com/owner/repo.git": {"https://github.com/owner/repo.git", false},
		"file:///tmp/foo":                   {"", true},  // file URLs go through cp, not git
	}
	for in, c := range cases {
		got, err := gitURLForSource(in)
		if c.errOK {
			if err == nil {
				t.Errorf("gitURLForSource(%q) expected error, got %q", in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("gitURLForSource(%q): %v", in, err)
			continue
		}
		if got != c.want {
			t.Errorf("gitURLForSource(%q) = %q, want %q", in, got, c.want)
		}
	}
}

func TestManifestSchemaToBasic_PropertiesMap(t *testing.T) {
	s := BasicSchema{
		Required: []string{"text", "count"},
		Properties: map[string]SchemaProperty{
			"text":  {Type: "string"},
			"count": {Type: "number"},
		},
	}
	got := manifestSchemaToBasic(s)
	if len(got.Required) != 2 {
		t.Errorf("required = %v", got.Required)
	}
	if got.Properties["text"] != "string" || got.Properties["count"] != "number" {
		t.Errorf("properties = %v", got.Properties)
	}
}
