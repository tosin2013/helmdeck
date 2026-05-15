// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package marketplace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"github.com/tosin2013/helmdeck/internal/session"
)

// DefaultMarketplaceSidecarImage is the published image the installer
// uses when a pack manifest doesn't declare its own handler.sidecar.
// Per ADR 038. Operators override via HELMDECK_SIDECAR_MARKETPLACE.
const DefaultMarketplaceSidecarImage = "ghcr.io/tosin2013/helmdeck-sidecar-marketplace:latest"

// MarketplaceSidecarImage returns the image the installer uses by
// default. Reads HELMDECK_SIDECAR_MARKETPLACE; falls back to the
// published default. Exposed as a package var (not a func) so tests
// can redirect to a local-built helmdeck-sidecar-marketplace:dev
// without env-var manipulation.
func MarketplaceSidecarImage() string {
	if v := os.Getenv("HELMDECK_SIDECAR_MARKETPLACE"); v != "" {
		return v
	}
	return DefaultMarketplaceSidecarImage
}

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
	// Hard-gate on a SHA256 mismatch (manifest declares a hash but
	// the materialized content doesn't match). Per ADR 034 §"Trust
	// model" + the stage-A spec: a mismatch means the bytes operators
	// are about to register differ from what the marketplace signed,
	// and we MUST refuse the install. trust_verified=false with no
	// declared hash → not a mismatch, install proceeds (unsigned).
	if !trustVerified && manifest.Trust != nil && manifest.Trust.SHA256 != "" {
		_ = os.RemoveAll(packDir)
		return nil, fmt.Errorf("trust verification failed: %s", trustNote)
	}

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

// --- trust verification (stage A: deterministic content hash) -------

// verifyTrust hashes the materialized pack files and compares against
// the manifest's trust.sha256, returning (verified, note) for the
// install record.
//
// v0.13.0 GA implements "stage A" of the trust model from ADR 034:
// deterministic content hashing. The hash spec is documented at
// computePackHash — every byte of every file under packDir, keyed by
// relative path, sorted lexically, concatenated and re-hashed. The
// marketplace's sign.yml workflow populates trust.sha256 on each
// release tag using the same algorithm.
//
// Stage B (full sigstore keyless verification of the manifest signer
// against the declared signed_by identity) is tracked as a v1.0
// hardening item — see the follow-up issue + the catalog.md trust
// model docs. Stage A catches:
//   - pack files modified between author sign + operator install
//   - corrupt downloads
//   - operator pointing at the wrong marketplace by accident
// Stage A does NOT catch a malicious author who controls both the
// files and the manifest. That's stage B's job.
//
// Behavior summary:
//   - manifest.trust missing                 → unsigned, install proceeds
//   - manifest.trust.sha256 empty            → unsigned, install proceeds
//   - manifest.trust.sha256 mismatch         → (false, error-style note); CALLER MUST REJECT
//   - manifest.trust.sha256 match            → (true, hash note)
func (i *Installer) verifyTrust(packDir string, manifest *Manifest) (bool, string) {
	if manifest.Trust == nil {
		return false, "no trust block in manifest — pack is unsigned"
	}
	if manifest.Trust.SHA256 == "" {
		// trust block exists but no hash. Could be a pack that has
		// only declared an identity (signed_by) but hasn't yet been
		// signed, or a pre-sign.yml-update pack. Either way: can't
		// verify integrity, surface as unsigned.
		if manifest.Trust.SignedBy != "" {
			return false, fmt.Sprintf("manifest declares signed_by=%s but no sha256 — pack hash not yet populated (stage A); see ADR 034", manifest.Trust.SignedBy)
		}
		return false, "no sha256 in manifest trust block — pack is unsigned"
	}

	got, err := computePackHash(packDir)
	if err != nil {
		return false, fmt.Sprintf("could not compute pack hash: %v", err)
	}
	if !strings.EqualFold(got, manifest.Trust.SHA256) {
		return false, fmt.Sprintf("sha256 mismatch: manifest says %s, computed %s — pack contents do not match what the marketplace signed", manifest.Trust.SHA256, got)
	}
	if manifest.Trust.SignedBy != "" {
		return true, fmt.Sprintf("sha256 verified (%s); manifest declares signed_by=%s (full cosign identity verification deferred to stage B)", got, manifest.Trust.SignedBy)
	}
	return true, fmt.Sprintf("sha256 verified (%s)", got)
}

// computePackHash returns the deterministic SHA256 of a marketplace
// pack directory's NON-MANIFEST files. Algorithm:
//
//  1. Walk packDir, skipping directories.
//  2. Skip `helmdeck-pack.yaml` — see "why the manifest is excluded"
//     below.
//  3. For each remaining file (filepath.Walk visits in lexical order)
//     write to the rolling SHA256:
//        <forward-slash-rel-path> \0 <file_sha256_hex> \n
//  4. Hex-encode the final digest.
//
// Why the manifest is excluded
//
// The trust.sha256 field this digest fills LIVES IN the manifest. If
// we included the manifest in the hash, populating trust.sha256 would
// change the manifest's bytes and invalidate the hash we just wrote
// — a circular dependency. Same pattern as Helm chart hashes
// (excludes Chart.lock), Cargo (excludes Cargo.lock from .toml hash),
// npm (excludes package-lock.json from package.json shasum).
//
// What this hash protects against
//
//   - Handler script / data files modified between author sign + install
//   - File renamed or added/removed under packs/<name>/
//   - Corrupt download
//
// What it does NOT protect against (manifest is excluded)
//
//   - A malicious author modifying the MANIFEST (e.g. changing the
//     handler.command argv to point at an exfiltrating binary).
//     Stage B (cosign keyless verify of the manifest signer's
//     identity) is the fix.
//   - File-mode changes (we don't hash mode bits; the marketplace
//     install always chmods +x in the sidecar regardless).
//
// Properties of the digest itself:
//   - Reproducible across platforms (no tar/gzip non-determinism,
//     no timestamp / uid / gid leakage into the bytes)
//   - Sensitive to any byte change in any non-manifest file
//   - Sensitive to file rename (path is in the digest)
//   - Sensitive to add/remove (length of input changes)
//
// The marketplace's sign.yml workflow MUST use the same algorithm
// when populating trust.sha256. A reference Bash port lives next to
// sign.yml in tosin2013/helmdeck-marketplace.
const packManifestFilename = "helmdeck-pack.yaml"

func computePackHash(packDir string) (string, error) {
	outer := sha256.New()
	err := filepath.Walk(packDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(packDir, path)
		if err != nil {
			return err
		}
		// Forward slashes for cross-platform reproducibility.
		rel = filepath.ToSlash(rel)

		// Exclude the manifest itself (see the "why the manifest is
		// excluded" doc comment above).
		if rel == packManifestFilename {
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		inner := sha256.Sum256(body)
		if _, err := fmt.Fprintf(outer, "%s\x00%x\n", rel, inner); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", outer.Sum(nil)), nil
}

// --- pack-construction helper -----------------------------------------

// buildPackFromManifest turns a marketplace.Manifest + an on-disk
// pack dir into a packs.Pack ready to register. Only command-type
// handlers; the caller has already validated that.
//
// Per ADR 038, the constructed pack:
//   - declares NeedsSession=true so the engine acquires a sidecar
//   - uses SessionSpec.Image = manifest.handler.sidecar.image, or
//     the default MarketplaceSidecarImage when not overridden
//   - executes the handler INSIDE the sidecar via ec.Exec rather
//     than spawning in-process (which would fail in distroless)
//
// The on-disk packDir holds the handler files copied during install;
// the handler closure reads them from disk and uploads to the sidecar
// on each call. Per-call upload matches the slides.narrate / hyperframes
// pattern and is microsecond-scale overhead for kB-sized handler scripts.
func buildPackFromManifest(m *Manifest, packDir string) (*packs.Pack, error) {
	handlerArgv := append([]string{}, m.Handler.Command...)
	if len(handlerArgv) == 0 {
		return nil, fmt.Errorf("handler.command is empty")
	}
	// argv[0] is the handler script's path *as the manifest declares it*
	// (typically "./handler.py"). The on-disk file exists at
	// packDir/<basename>; the in-sidecar path is
	// /tmp/helmdeck-pack-<name>/<basename>. The handler closure does the
	// translation; we just validate the file exists on disk here.
	entrypoint := handlerArgv[0]
	if filepath.IsAbs(entrypoint) {
		return nil, fmt.Errorf("handler.command[0] (%q) must be a path relative to the pack directory, not absolute", entrypoint)
	}
	entrypointOnDisk := filepath.Join(packDir, filepath.Clean(entrypoint))
	if _, err := os.Stat(entrypointOnDisk); err != nil {
		return nil, fmt.Errorf("handler entrypoint %s not found: %w", entrypointOnDisk, err)
	}

	timeout := time.Duration(m.Handler.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	sidecarImage := MarketplaceSidecarImage()
	if m.Handler.Sidecar != nil && m.Handler.Sidecar.Image != "" {
		sidecarImage = m.Handler.Sidecar.Image
	}

	inSchema := manifestSchemaToBasic(m.InputSchema)
	outSchema := manifestSchemaToBasic(m.OutputSchema)

	pack := &packs.Pack{
		Name:         m.Name,
		Version:      m.Version,
		Description:  m.Description,
		InputSchema:  inSchema,
		OutputSchema: outSchema,
		NeedsSession: true,
		SessionSpec: session.Spec{
			Image:   sidecarImage,
			Timeout: timeout,
		},
		Handler: marketplaceCommandHandler(m.Name, packDir, handlerArgv, m.Handler.Env, timeout),
	}
	return pack, nil
}

// marketplaceCommandHandler builds the HandlerFunc closure that
// uploads the pack's on-disk handler files into the spawned sidecar
// and executes the entrypoint with stdin = ec.Input. Per ADR 038.
//
// Flow per call:
//   1. recreate /tmp/helmdeck-pack-<name>/ inside the sidecar
//   2. upload every file from packDir via execWithStdin (recursive)
//   3. chmod +x the entrypoint
//   4. exec the entrypoint with ec.Input piped to stdin
//   5. return stdout as the pack output
func marketplaceCommandHandler(packName, packDir string, argv []string, env []string, timeout time.Duration) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
				Message: "marketplace pack requires a session executor"}
		}

		remoteDir := "/tmp/helmdeck-pack-" + sanitizeForPath(packName)

		// Step 1 — wipe + recreate the per-call upload dir.
		mk, err := ec.Exec(ctx, session.ExecRequest{
			Cmd: []string{"sh", "-c", "rm -rf " + remoteDir + " && mkdir -p " + remoteDir},
		})
		if err != nil || mk.ExitCode != 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("prepare remote pack dir: %v (exit %d)", err, mk.ExitCode)}
		}

		// Step 2 — upload every file under packDir, preserving
		// directory structure inside remoteDir.
		if err := uploadDirToSidecar(ctx, ec, packDir, remoteDir); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("upload pack files: %v", err)}
		}

		// Step 3 — chmod +x the entrypoint. uploadDirToSidecar writes
		// with mode 0644 from the bash redirect; we don't get to set
		// per-file modes via execWithStdin. Explicit chmod here.
		remoteEntrypoint := filepath.Join(remoteDir, filepath.Clean(argv[0]))
		chmod, err := ec.Exec(ctx, session.ExecRequest{
			Cmd: []string{"chmod", "+x", remoteEntrypoint},
		})
		if err != nil || chmod.ExitCode != 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("chmod handler: %v (exit %d)", err, chmod.ExitCode)}
		}

		// Step 4 — execute. The handler reads ec.Input from stdin,
		// writes JSON to stdout. Non-zero exit → handler_failed.
		runArgv := append([]string{remoteEntrypoint}, argv[1:]...)
		res, err := ec.Exec(ctx, session.ExecRequest{
			Cmd:   runArgv,
			Stdin: ec.Input,
			Env:   env,
		})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("execute handler: %v", err)}
		}
		if res.ExitCode != 0 {
			stderr := strings.TrimSpace(string(res.Stderr))
			if len(stderr) > 4096 {
				stderr = stderr[:4096] + "...(truncated)"
			}
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("handler exit %d: %s", res.ExitCode, stderr)}
		}
		if len(res.Stdout) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: "handler produced empty stdout (expected JSON)"}
		}
		// We don't validate stdout is JSON here — the engine validates
		// it against OutputSchema via packs.Engine.Execute after the
		// handler returns. Match the slides.narrate / hyperframes
		// pattern: return whatever bytes the handler emitted.
		return json.RawMessage(res.Stdout), nil
	}
}

// uploadDirToSidecar walks localDir and replays every file at the
// matching path under remoteDir via execWithStdin. Directory
// structure is preserved (so packs that ship multiple files — handler
// + a python module + a config — all land in the right relative
// positions for the entrypoint to resolve them).
func uploadDirToSidecar(ctx context.Context, ec *packs.ExecutionContext, localDir, remoteDir string) error {
	return filepath.Walk(localDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		remotePath := filepath.Join(remoteDir, rel)
		if info.IsDir() {
			res, err := ec.Exec(ctx, session.ExecRequest{
				Cmd: []string{"mkdir", "-p", remotePath},
			})
			if err != nil || res.ExitCode != 0 {
				return fmt.Errorf("mkdir %s: %v (exit %d)", remotePath, err, res.ExitCode)
			}
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Match the existing pattern from slides_narrate.go +
		// hyperframes_render.go: `sh -c "cat > <path>"` with stdin
		// bytes. Works in any POSIX shell; doesn't require scp / docker cp.
		res, err := ec.Exec(ctx, session.ExecRequest{
			Cmd:   []string{"sh", "-c", "cat > " + shellQuote(remotePath)},
			Stdin: body,
		})
		if err != nil || res.ExitCode != 0 {
			return fmt.Errorf("write %s: %v (exit %d)", remotePath, err, res.ExitCode)
		}
		return nil
	})
}

// shellQuote single-quotes a path for safe use in `sh -c`. The pack
// name + the per-call temp dir guarantee no single quotes appear in
// the path; this is belt-and-suspenders for future paths that might.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// sanitizeForPath strips characters that would be awkward in a tmp
// path. Pack names follow the <family>.<verb> form per the manifest
// schema, so we just keep alphanumerics + the dot.
func sanitizeForPath(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
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
