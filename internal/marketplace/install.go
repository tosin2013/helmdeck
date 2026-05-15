// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package marketplace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// Installer materializes a marketplace pack to disk and registers it
// with the live pack registry — the hot-load path that lets new packs
// appear in tools/list without a control-plane restart. Per ADR 034.
//
// One Installer per control-plane process; thread-safe.
//
// The installer is intentionally minimal in v0.13.0 beta:
//   - command-handler packs only (builtin/composite/wasm reject)
//   - git clone of the marketplace repo per install (no shared cache)
//   - cosign verification is a structured stub — the hook is wired
//     but a follow-up PR adds the real sigstore verification call
type Installer struct {
	svc        *Service
	packReg    *packs.Registry
	installDir string
	logger     *slog.Logger

	mu        sync.Mutex
	installed map[string]InstalledPack
}

// InstalledPack tracks one pack the operator has installed via the
// marketplace. Surfaced by the list-installed endpoint and used by
// uninstall to find the on-disk directory + the registered pack name.
type InstalledPack struct {
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Source        string    `json:"source"`        // marketplace URL the pack came from
	Path          string    `json:"path"`          // pack's path within the marketplace repo
	InstalledAt   time.Time `json:"installed_at"`
	InstallDir    string    `json:"install_dir"`   // local dir holding the materialized files
	TrustVerified bool      `json:"trust_verified"`
	TrustNote     string    `json:"trust_note,omitempty"` // reason if not verified
}

// NewInstaller constructs an installer.
//
//   svc        — the marketplace Service holding the cached catalog
//   packReg    — the live pack registry to hot-load into
//   installDir — root dir for installed packs (e.g. ~/.helmdeck/packs)
//   logger     — slog logger; nil falls back to slog.Default
func NewInstaller(svc *Service, packReg *packs.Registry, installDir string, logger *slog.Logger) *Installer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Installer{
		svc:        svc,
		packReg:    packReg,
		installDir: installDir,
		logger:     logger.With("subsystem", "marketplace.install"),
		installed:  make(map[string]InstalledPack),
	}
}

// InstallResult is what the REST handler returns on successful install.
type InstallResult struct {
	Pack          InstalledPack `json:"pack"`
	HotLoadedAs   string        `json:"hot_loaded_as"`
}

// Install resolves `name` against the catalog, materializes the pack
// to disk, verifies trust (best-effort cosign stub), and registers
// the handler with the live pack registry.
//
// Returns ErrPackNotInCatalog when the name isn't in the index;
// other errors surface verbatim with operator-friendly messages.
func (i *Installer) Install(ctx context.Context, name string) (*InstallResult, error) {
	idx, _, err := i.svc.Catalog()
	if err != nil {
		return nil, fmt.Errorf("marketplace catalog unavailable: %w", err)
	}
	entry := findEntry(idx, name)
	if entry == nil {
		return nil, ErrPackNotInCatalog
	}

	manifest, err := LoadManifest(ctx, i.svc.Source(), entry.Path)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest for %s: %w", name, err)
	}
	if manifest.Name != name {
		return nil, fmt.Errorf("manifest name %q does not match catalog entry %q", manifest.Name, name)
	}
	if manifest.Handler.Type != "command" {
		return nil, fmt.Errorf("handler type %q not yet supported (v0.13.0 beta installs command-type only)", manifest.Handler.Type)
	}
	if len(manifest.Handler.Command) == 0 {
		return nil, fmt.Errorf("manifest %s has empty handler.command", name)
	}

	packDir := filepath.Join(i.installDir, name)
	if err := i.materializeFromGit(ctx, entry.Path, packDir); err != nil {
		return nil, fmt.Errorf("materialize pack files: %w", err)
	}

	trustVerified, trustNote := i.verifyTrust(packDir, manifest)

	pack, err := buildPackFromManifest(manifest, packDir)
	if err != nil {
		// Clean up the just-written files so a partial install doesn't
		// leave stale state. Best-effort — if the cleanup itself fails
		// the operator can manually remove the dir.
		_ = os.RemoveAll(packDir)
		return nil, fmt.Errorf("build pack from manifest: %w", err)
	}
	if err := i.packReg.Register(pack); err != nil {
		_ = os.RemoveAll(packDir)
		return nil, fmt.Errorf("register pack in registry: %w", err)
	}

	installed := InstalledPack{
		Name:          name,
		Version:       manifest.Version,
		Source:        i.svc.Source(),
		Path:          entry.Path,
		InstalledAt:   time.Now().UTC(),
		InstallDir:    packDir,
		TrustVerified: trustVerified,
		TrustNote:     trustNote,
	}
	i.mu.Lock()
	i.installed[name] = installed
	i.mu.Unlock()

	i.logger.Info("marketplace pack installed",
		"name", name, "version", manifest.Version,
		"install_dir", packDir, "trust_verified", trustVerified)

	return &InstallResult{Pack: installed, HotLoadedAs: name}, nil
}

// Uninstall removes a previously-installed pack from disk + the pack
// registry. Returns ErrPackNotInstalled when the name has no install
// record. Core packs (those not in `i.installed`) reject — operators
// can't uninstall builtins through this surface.
func (i *Installer) Uninstall(ctx context.Context, name string) error {
	i.mu.Lock()
	entry, ok := i.installed[name]
	if !ok {
		i.mu.Unlock()
		return ErrPackNotInstalled
	}
	delete(i.installed, name)
	i.mu.Unlock()

	// Order matters: deregister BEFORE removing files. If we remove
	// files first and the registry deregister fails, the pack is
	// still callable but its handler can't execute — worse UX than
	// briefly having a pack registered against an empty dir.
	// Registry.Unregister is best-effort (no error return); calling
	// it for a pack that isn't registered is a no-op.
	i.packReg.Unregister(name, entry.Version)
	if err := os.RemoveAll(entry.InstallDir); err != nil {
		// Files are gone from registry but linger on disk. Log and
		// surface so the operator can clean up by hand.
		i.logger.Warn("uninstall left files on disk",
			"name", name, "dir", entry.InstallDir, "err", err)
		return fmt.Errorf("remove install dir %s: %w", entry.InstallDir, err)
	}
	i.logger.Info("marketplace pack uninstalled", "name", name)
	return nil
}

// Installed returns a snapshot of the install records. Used by the
// list endpoint; the returned slice is a copy so callers can sort /
// filter without holding the mutex.
func (i *Installer) Installed() []InstalledPack {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]InstalledPack, 0, len(i.installed))
	for _, v := range i.installed {
		out = append(out, v)
	}
	return out
}

// --- materializeFromGit ------------------------------------------------

// materializeFromGit clones the marketplace repo to a temp dir, copies
// just `packPath/` to `dst`, and chmod +x the handler entrypoint.
//
// Per ADR 034: `git clone --depth=1 --filter=blob:none`. The blob
// filter keeps the clone tiny — git lazily fetches blobs only for
// files we actually checkout. For the v0.13.0 beta where catalogs
// are dozens of small packs, this is ~1 MiB per install.
//
// File-URL sources (file:///path/to/marketplace) skip git and just
// cp -r from the local dir. Used by tests + air-gapped operators.
func (i *Installer) materializeFromGit(ctx context.Context, packPath, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	// Remove dst if a previous failed install left files. The caller
	// holds the install-mutex via the REST path, so this isn't racing.
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clean install dir: %w", err)
	}

	source := i.svc.Source()
	if strings.HasPrefix(source, "file://") {
		srcDir := strings.TrimPrefix(source, "file://") + "/" + strings.TrimPrefix(packPath, "/")
		return copyDir(srcDir, dst)
	}

	// Convert github.com/<owner>/<repo> → git clone URL.
	gitURL, err := gitURLForSource(source)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "helmdeck-pack-clone-")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	cmd := exec.CommandContext(ctx, "git",
		"clone", "--depth=1", "--filter=blob:none",
		gitURL, tmp)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		return fmt.Errorf("git clone %s: %s: %w", gitURL, strings.TrimSpace(string(out)), cerr)
	}
	srcDir := filepath.Join(tmp, packPath)
	return copyDir(srcDir, dst)
}

// gitURLForSource turns a marketplace base URL (github.com/<owner>/<repo>
// or a direct https://...git URL) into a clone-able URL.
func gitURLForSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if strings.HasSuffix(source, ".git") {
		return source, nil
	}
	if strings.HasPrefix(source, "https://github.com/") {
		// Strip trailing slash; git is fine with or without .git
		return strings.TrimSuffix(source, "/") + ".git", nil
	}
	return "", fmt.Errorf("unsupported marketplace source for install (only github.com URLs or .git URLs supported): %s", source)
}

// copyDir recursively copies srcDir to dstDir, preserving file modes
// (so chmod +x on the handler survives the round trip).
func copyDir(srcDir, dstDir string) error {
	srcAbs, err := filepath.Abs(srcDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(srcAbs); err != nil {
		return fmt.Errorf("source dir %s: %w", srcAbs, err)
	}
	return filepath.Walk(srcAbs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcAbs, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode())
		}
		return copyFile(path, dst, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// --- trust verification (stub) ----------------------------------------

// verifyTrust runs the cosign verification per ADR 034's "Signed"
// trust level. **v0.13.0 beta ships a structured stub** — the hook
// is wired so callers can read TrustVerified + TrustNote on every
// install, but the actual sigstore.dev cosign-verify call lands in
// a follow-up PR. For now: returns (false, "...") with a clear
// reason so the UI can show the "Unsigned" badge per ADR 034's
// Community-pack rules.
func (i *Installer) verifyTrust(packDir string, manifest *Manifest) (bool, string) {
	if manifest.Trust == nil {
		return false, "no trust block in manifest — pack is unsigned"
	}
	if manifest.Trust.SignedBy == "" {
		return false, "manifest trust block has no signed_by — pack is unsigned"
	}
	// TODO(#30 follow-up): real sigstore.dev cosign-verify call here.
	// For now we surface the structured "trust metadata exists but
	// hasn't been verified at runtime" answer so callers can decide.
	return false, fmt.Sprintf("cosign verification deferred (manifest declares signed_by=%s); v0.13.0 beta does not yet execute verify", manifest.Trust.SignedBy)
}

// --- pack-construction helper -----------------------------------------

// buildPackFromManifest turns a marketplace.Manifest + an on-disk
// pack dir into a packs.Pack ready to register. Only command-type
// handlers; the caller has already validated that.
func buildPackFromManifest(m *Manifest, packDir string) (*packs.Pack, error) {
	// Resolve the handler entrypoint path. The first argv element is
	// either relative (./upper) or absolute — we resolve relative
	// paths against the install dir so the spawned process finds them.
	handlerArgv := append([]string{}, m.Handler.Command...)
	if len(handlerArgv) == 0 {
		return nil, fmt.Errorf("handler.command is empty")
	}
	if !filepath.IsAbs(handlerArgv[0]) {
		handlerArgv[0] = filepath.Join(packDir, handlerArgv[0])
	}
	if _, err := os.Stat(handlerArgv[0]); err != nil {
		return nil, fmt.Errorf("handler entrypoint %s not found: %w", handlerArgv[0], err)
	}

	timeout := time.Duration(m.Handler.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	inSchema := manifestSchemaToBasic(m.InputSchema)
	outSchema := manifestSchemaToBasic(m.OutputSchema)

	pack := packs.NewCommandPack(
		m.Name,
		m.Version,
		m.Description,
		inSchema, outSchema,
		packs.CommandSpec{
			Path:           handlerArgv[0],
			Args:           handlerArgv[1:],
			Env:            m.Handler.Env,
			Timeout:        timeout,
			MaxOutputBytes: m.Handler.MaxOutputBytes,
		},
	)
	return pack, nil
}

// manifestSchemaToBasic converts the marketplace's schema shape to
// helmdeck's internal BasicSchema (used by the engine for input/
// output validation).
func manifestSchemaToBasic(s BasicSchema) packs.BasicSchema {
	out := packs.BasicSchema{Required: s.Required}
	if len(s.Properties) > 0 {
		out.Properties = make(map[string]string, len(s.Properties))
		for k, v := range s.Properties {
			out.Properties[k] = v.Type
		}
	}
	return out
}

// findEntry returns the IndexEntry for `name` or nil if absent.
func findEntry(idx *Index, name string) *IndexEntry {
	for i := range idx.Packs {
		if idx.Packs[i].Name == name {
			return &idx.Packs[i]
		}
	}
	return nil
}

// --- errors ------------------------------------------------------------

// ErrPackNotInCatalog is returned by Install when the requested pack
// name isn't present in the catalog index.
var ErrPackNotInCatalog = errors.New("pack not found in marketplace catalog")

// ErrPackNotInstalled is returned by Uninstall when the name has no
// install record. Core packs (those registered at startup, not via
// this installer) also surface as ErrPackNotInstalled.
var ErrPackNotInstalled = errors.New("pack not installed via marketplace")
