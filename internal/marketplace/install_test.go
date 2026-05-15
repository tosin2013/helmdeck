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
	"github.com/tosin2013/helmdeck/internal/session"
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

// fakeSidecarExec captures every ec.Exec call so tests can assert
// on the upload + chmod + run sequence the marketplace handler
// performs. Tracks "files written to the sidecar" by inspecting
// `sh -c "cat > <path>"` requests, so the test can read back what
// the handler uploaded.
//
// When the entrypoint is invoked (a non-sh command), the fake returns
// the configured fakeStdout — simulating the handler's response.
type fakeSidecarExec struct {
	calls       []session.ExecRequest
	files       map[string][]byte
	fakeStdout  []byte
	fakeExitCode int
}

func newFakeSidecarExec(stdout []byte) *fakeSidecarExec {
	return &fakeSidecarExec{files: map[string][]byte{}, fakeStdout: stdout}
}

func (f *fakeSidecarExec) Exec(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	f.calls = append(f.calls, req)
	// `sh -c "cat > <path>"` with stdin → record the file content.
	if len(req.Cmd) == 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" &&
		strings.HasPrefix(req.Cmd[2], "cat > ") {
		path := strings.TrimSpace(strings.TrimPrefix(req.Cmd[2], "cat > "))
		path = strings.Trim(path, `'`)
		f.files[path] = append([]byte{}, req.Stdin...)
		return session.ExecResult{ExitCode: 0}, nil
	}
	// `sh -c "rm -rf X && mkdir -p X"` — accept.
	if len(req.Cmd) == 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
		return session.ExecResult{ExitCode: 0}, nil
	}
	// mkdir / chmod — accept.
	if len(req.Cmd) > 0 && (req.Cmd[0] == "mkdir" || req.Cmd[0] == "chmod") {
		return session.ExecResult{ExitCode: 0}, nil
	}
	// Anything else is the handler entrypoint — return canned output.
	return session.ExecResult{
		Stdout:   f.fakeStdout,
		ExitCode: f.fakeExitCode,
	}, nil
}

func TestInstall_HotLoad_PackBuildsAsSidecarRoutedAndExecutesViaSession(t *testing.T) {
	mktDir := t.TempDir()
	// Handler body content doesn't matter for this test — the fake
	// sidecar returns canned stdout for the entrypoint invocation.
	// We just need the install to materialize a non-empty handler file.
	_ = scaffoldMarketplace(t, mktDir, map[string]string{"cmd.hot": "#!/usr/bin/env bash\necho '{}'\n"})
	inst := quietInstaller(t, mktDir)
	if _, err := inst.Install(context.Background(), "cmd.hot"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Per ADR 038: the registered pack routes through a sidecar.
	pack, err := inst.packReg.Get("cmd.hot", "")
	if err != nil {
		t.Fatalf("registry Get: %v", err)
	}
	if !pack.NeedsSession {
		t.Errorf("expected NeedsSession=true on marketplace pack")
	}
	if pack.SessionSpec.Image == "" {
		t.Errorf("expected SessionSpec.Image to be set")
	}
	// Default sidecar image when manifest has no override.
	if !strings.Contains(pack.SessionSpec.Image, "marketplace") {
		t.Errorf("default sidecar should be marketplace image, got %q", pack.SessionSpec.Image)
	}

	// Invoke the handler against a fake sidecar — should follow the
	// upload + chmod + run sequence per ADR 038.
	fake := newFakeSidecarExec([]byte(`{"text":"HOT-LOADED"}`))
	ec := &packs.ExecutionContext{
		Pack:   pack,
		Input:  json.RawMessage(`{"text":"any"}`),
		Exec:   fake.Exec,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	out, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Assert the sequence: at least one mkdir / cleanup, at least one
	// cat > (handler upload), at least one chmod +x, then run.
	sawMkdir, sawCat, sawChmod, sawRun := false, false, false, false
	for _, c := range fake.calls {
		switch {
		case len(c.Cmd) == 3 && c.Cmd[0] == "sh" && c.Cmd[1] == "-c" && strings.Contains(c.Cmd[2], "mkdir -p"):
			sawMkdir = true
		case len(c.Cmd) == 3 && c.Cmd[0] == "sh" && c.Cmd[1] == "-c" && strings.Contains(c.Cmd[2], "cat > "):
			sawCat = true
		case len(c.Cmd) > 0 && c.Cmd[0] == "chmod":
			sawChmod = true
		case len(c.Cmd) > 0 && strings.Contains(c.Cmd[0], "helmdeck-pack-"):
			sawRun = true
			if string(c.Stdin) != `{"text":"any"}` {
				t.Errorf("run stdin = %q, want pack input", c.Stdin)
			}
		}
	}
	if !sawMkdir {
		t.Errorf("expected mkdir for remote pack dir")
	}
	if !sawCat {
		t.Errorf("expected cat > to upload handler bytes")
	}
	if !sawChmod {
		t.Errorf("expected chmod +x on the entrypoint")
	}
	if !sawRun {
		t.Errorf("expected handler entrypoint invocation in sidecar")
	}

	// Handler's output should be the fake stdout passed through verbatim.
	if string(out) != `{"text":"HOT-LOADED"}` {
		t.Errorf("handler output = %q, want %q", out, `{"text":"HOT-LOADED"}`)
	}
}

func TestInstall_SidecarOverride_HonorsManifestImage(t *testing.T) {
	// Hand-write a manifest with handler.sidecar.image set.
	mktDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mktDir, "packs/cmd.custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `name: cmd.custom
version: v1
author: tester
description: A pack that needs a custom sidecar image
input_schema: {}
output_schema: {}
handler:
  type: command
  command: ["./handler"]
  sidecar:
    image: ghcr.io/some-author/special-toolchain:v1
`
	if err := os.WriteFile(filepath.Join(mktDir, "packs/cmd.custom/helmdeck-pack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mktDir, "packs/cmd.custom/handler"), []byte("#!/bin/bash\necho '{}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	idx := `catalog_version: v1
packs:
  - name: cmd.custom
    version: v1
    path: packs/cmd.custom
    description: custom sidecar pack
    author: tester
`
	if err := os.WriteFile(filepath.Join(mktDir, "index.yaml"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := quietInstaller(t, mktDir)
	if _, err := inst.Install(context.Background(), "cmd.custom"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	pack, _ := inst.packReg.Get("cmd.custom", "")
	if pack.SessionSpec.Image != "ghcr.io/some-author/special-toolchain:v1" {
		t.Errorf("manifest-declared sidecar not honored: got %q", pack.SessionSpec.Image)
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

// --- trust verification (stage A) -------------------------------------

func TestComputePackHash_DeterministicAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helmdeck-pack.yaml"), []byte("name: cmd.x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := computePackHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := computePackHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("hash not deterministic: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Errorf("hex digest should be 64 chars, got %d (%q)", len(a), a)
	}
}

func TestComputePackHash_SensitiveToFileChange(t *testing.T) {
	dir := t.TempDir()
	handlerPath := filepath.Join(dir, "handler")
	if err := os.WriteFile(handlerPath, []byte("v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "helmdeck-pack.yaml"), []byte("name: cmd.x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _ := computePackHash(dir)
	if err := os.WriteFile(handlerPath, []byte("v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, _ := computePackHash(dir)
	if first == second {
		t.Errorf("hash unchanged after file modification — should be sensitive")
	}
}

func TestComputePackHash_SensitiveToFileAdd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _ := computePackHash(dir)
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, _ := computePackHash(dir)
	if first == second {
		t.Errorf("hash unchanged after add — should be sensitive")
	}
}

func TestComputePackHash_SensitiveToRename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("same-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _ := computePackHash(dir)
	if err := os.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	second, _ := computePackHash(dir)
	if first == second {
		t.Errorf("hash unchanged after rename — should be sensitive (path is in the digest)")
	}
}

// scaffoldMarketplaceWithTrust extends scaffoldMarketplace with the
// ability to embed a trust block in each pack's manifest. The map
// value is the handler body; the trust map keyed by pack name
// supplies the trust block.
func scaffoldMarketplaceWithTrust(t *testing.T, dir string, handlers map[string]string, trust map[string]map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "packs"), 0o755); err != nil {
		t.Fatal(err)
	}
	idxBody := "catalog_version: v1\npacks:\n"
	for name, body := range handlers {
		packDir := filepath.Join(dir, "packs", name)
		if err := os.MkdirAll(packDir, 0o755); err != nil {
			t.Fatal(err)
		}
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
		if tb, ok := trust[name]; ok && len(tb) > 0 {
			manifest += "trust:\n"
			if v, ok := tb["signed_by"]; ok && v != "" {
				manifest += "  signed_by: " + v + "\n"
			}
			if v, ok := tb["sha256"]; ok && v != "" {
				manifest += "  sha256: " + v + "\n"
			}
		}
		if err := os.WriteFile(filepath.Join(packDir, "helmdeck-pack.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packDir, "handler"), []byte(body), 0o755); err != nil {
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

// expectedHashFor computes the hash of just the non-manifest files
// in a pack. Mirrors what computePackHash will do during the install
// (the manifest is excluded — see computePackHash's doc comment for
// the chicken-and-egg explanation). Used so trust-verified tests
// don't have to hard-code hex digests that would shift if the helper
// changes.
func expectedHashFor(t *testing.T, _packName, handlerBody string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler"), []byte(handlerBody), 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := computePackHash(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestInstall_MatchingSHA256_TrustVerified(t *testing.T) {
	mktDir := t.TempDir()
	handlerBody := "#!/usr/bin/env bash\necho '{\"text\":\"ok\"}'\n"
	hash := expectedHashFor(t, "cmd.signed", handlerBody)
	_ = scaffoldMarketplaceWithTrust(t,
		mktDir,
		map[string]string{"cmd.signed": handlerBody},
		map[string]map[string]string{"cmd.signed": {"signed_by": "tosin2013", "sha256": hash}},
	)
	inst := quietInstaller(t, mktDir)
	res, err := inst.Install(context.Background(), "cmd.signed")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !res.Pack.TrustVerified {
		t.Errorf("expected TrustVerified=true on matching hash; got note: %s", res.Pack.TrustNote)
	}
	if !strings.Contains(res.Pack.TrustNote, "sha256 verified") {
		t.Errorf("expected TrustNote to mention verification; got: %s", res.Pack.TrustNote)
	}
	if !strings.Contains(res.Pack.TrustNote, "signed_by=tosin2013") {
		t.Errorf("expected TrustNote to mention declared signer; got: %s", res.Pack.TrustNote)
	}
}

func TestInstall_MismatchSHA256_RefusesInstall(t *testing.T) {
	mktDir := t.TempDir()
	wrongHash := strings.Repeat("0", 64)
	_ = scaffoldMarketplaceWithTrust(t,
		mktDir,
		map[string]string{"cmd.tampered": "#!/usr/bin/env bash\necho '{}'\n"},
		map[string]map[string]string{"cmd.tampered": {"signed_by": "tosin2013", "sha256": wrongHash}},
	)
	inst := quietInstaller(t, mktDir)
	_, err := inst.Install(context.Background(), "cmd.tampered")
	if err == nil {
		t.Fatal("expected install to fail on SHA256 mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("expected sha256-mismatch error, got: %v", err)
	}
	// Pack files must NOT be left on disk after a rejected install —
	// otherwise a subsequent Install of a clean pack would race
	// against the tampered files.
	if _, err := os.Stat(filepath.Join(inst.installDir, "cmd.tampered")); !os.IsNotExist(err) {
		t.Errorf("expected install dir cleaned up after rejection, stat error: %v", err)
	}
	// Pack must NOT be registered.
	if got := inst.Installed(); len(got) != 0 {
		t.Errorf("expected no installed packs after rejection, got %+v", got)
	}
}

func TestInstall_SignedByButNoSHA256_RemainsUnsigned(t *testing.T) {
	// Manifest has trust.signed_by but no sha256 (the "stage A not
	// yet populated" case). Install proceeds, trust_verified=false,
	// note explains why.
	mktDir := t.TempDir()
	_ = scaffoldMarketplaceWithTrust(t,
		mktDir,
		map[string]string{"cmd.partial": "#!/usr/bin/env bash\necho '{}'\n"},
		map[string]map[string]string{"cmd.partial": {"signed_by": "tosin2013"}},
	)
	inst := quietInstaller(t, mktDir)
	res, err := inst.Install(context.Background(), "cmd.partial")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Pack.TrustVerified {
		t.Errorf("expected TrustVerified=false when sha256 absent")
	}
	if !strings.Contains(res.Pack.TrustNote, "no sha256") {
		t.Errorf("expected note about missing sha256, got: %s", res.Pack.TrustNote)
	}
	if !strings.Contains(res.Pack.TrustNote, "signed_by=tosin2013") {
		t.Errorf("expected note to mention declared identity, got: %s", res.Pack.TrustNote)
	}
}

// --- helper coverage --------------------------------------------------

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
