// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

//go:build integration
// +build integration

package builtin_test

// cli_surface_invariant_test.go (ADR 037 #214) — the auto-discovery
// half of the CLI-surface sentinel pattern. The Dockerfile RUN
// sentinels are a thin install-smoke (`<tool> --version`) so a
// yanked release or typo-squat fails the image build. This test
// is the second layer: it walks `internal/packs/builtin/*.go` via
// go/ast, extracts every `--flag` string the pack handlers pass
// positionally to a known sidecar binary, runs `<bin> <help-args>`
// in the corresponding sidecar image, and asserts every extracted
// flag appears in the help output.
//
// Why a Go test instead of more Dockerfile RUN lines:
//
//   1. Single source of truth — adding a flag to a pack's argv
//      automatically adds it to the assertion set. No second list
//      in a Dockerfile to drift from reality.
//   2. Named failures — a missing flag shows as "flag --foo passed
//      by Go pack but not in <bin> --help", not as a buildkit exit
//      code on a 40-line concatenated shell command.
//   3. Allowlisting deliberately-undocumented flags (marp --stdin
//      is silently accepted but isn't in --help) gets a structured
//      exception with a reason string, not a buried comment.
//
// Run with:
//
//   HELMDECK_INTEGRATION=1 go test -tags=integration \
//     ./internal/packs/builtin/... -run TestCLISurface -v
//
// The test skips cleanly if HELMDECK_INTEGRATION isn't set, if
// Docker isn't reachable, or if the sidecar image for a given
// case isn't present locally. CI is responsible for building the
// images before invoking the test.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliSurfaceCase describes one binary's expected flag set. The flag
// list itself is NOT hand-maintained here — it's derived at test
// time from Go pack source by argvFlagsForBinary. Skip is the
// structured exception list: flags the Go pack passes that don't
// appear in --help by design.
type cliSurfaceCase struct {
	// Binary name (must match the first element of the Go pack's
	// []string{"<binary>", "--flag", ...} argv literal).
	Binary string

	// Image to run the binary in. Must be locally available; CI's
	// sidecar build job is responsible for producing this tag.
	Image string

	// HelpArgs passed AFTER --entrypoint <Binary>. For tools whose
	// --help is on a subcommand (`hyperframes render --help`) this
	// captures the subcommand chain.
	HelpArgs []string

	// Skip lists flags the Go pack passes that are intentionally
	// not in --help. Each entry MUST have a Reason in the same
	// position in SkipReasons. Empty = strict (no exceptions).
	Skip        []string
	SkipReasons []string
}

var cliSurfaceCases = []cliSurfaceCase{
	{
		Binary:   "marp",
		Image:    sidecarImage("HELMDECK_SIDECAR_IMAGE", "helmdeck-sidecar:dev"),
		HelpArgs: []string{"--help"},
		Skip:     []string{"--stdin"},
		SkipReasons: []string{
			"marp v4.x reads stdin automatically when piped; --stdin is silently accepted but not documented in --help. Follow-up: drop from slides_render.go.",
		},
	},
	{
		Binary:   "hyperframes",
		Image:    sidecarImage("HELMDECK_SIDECAR_HYPERFRAMES", "helmdeck-sidecar-hyperframes:dev"),
		HelpArgs: []string{"render", "--help"},
	},
}

func sidecarImage(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return fallback
}

// TestCLISurface_FlagsAreDocumented asserts that every CLI flag a
// helmdeck pack passes by name to a known sidecar binary appears in
// that binary's `--help` output. A flag rename in an upstream
// release surfaces here as a named test failure instead of as a
// pack invocation error in production.
func TestCLISurface_FlagsAreDocumented(t *testing.T) {
	if os.Getenv("HELMDECK_INTEGRATION") == "" {
		t.Skip("set HELMDECK_INTEGRATION=1 to run the CLI-surface invariant tests")
	}
	if !dockerAvailable(t) {
		t.Skip("docker daemon not reachable")
	}

	pkgDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs pkg dir: %v", err)
	}

	for _, c := range cliSurfaceCases {
		t.Run(c.Binary, func(t *testing.T) {
			// Sanity: skip if the image isn't present. CI is
			// responsible for building it before invoking the test.
			if !imageAvailable(t, c.Image) {
				t.Skipf("image %q not available locally", c.Image)
			}

			flags := argvFlagsForBinary(t, pkgDir, c.Binary)
			if len(flags) == 0 {
				t.Skipf("no argv flags found for %s in Go pack source — nothing to assert", c.Binary)
			}

			help := helpText(t, c.Image, c.Binary, c.HelpArgs)

			skipSet := map[string]string{}
			for i, f := range c.Skip {
				reason := "(no reason given)"
				if i < len(c.SkipReasons) {
					reason = c.SkipReasons[i]
				}
				skipSet[f] = reason
			}

			missing := []string{}
			for _, f := range flags {
				if reason, skipped := skipSet[f]; skipped {
					t.Logf("skipping %s (allowlisted): %s", f, reason)
					continue
				}
				if !strings.Contains(help, f) {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				t.Errorf("flag(s) passed by helmdeck pack handlers but not in `%s %s` output: %v\n\n"+
					"This means either:\n"+
					"  (a) the upstream renamed/removed the flag — update the Go pack to match, or\n"+
					"  (b) the flag is intentionally accepted-but-undocumented — add it to the\n"+
					"      Skip list in cliSurfaceCases for %s with a reason.\n\n"+
					"Help output captured (first 4 KiB):\n%s",
					c.Binary, strings.Join(c.HelpArgs, " "), missing, c.Binary, truncate(help, 4096))
			}
		})
	}
}

// argvFlagsForBinary walks every non-test Go file under pkgDir,
// finds `[]string{...}` composite literals whose first element is
// the binary name, and collects every subsequent string literal
// that starts with "--". Flags with "=value" suffixes are stripped
// to the bare flag name. Returns a sorted, deduplicated list.
func argvFlagsForBinary(t *testing.T, pkgDir, binary string) []string {
	t.Helper()
	seen := map[string]bool{}
	fset := token.NewFileSet()

	err := filepath.Walk(pkgDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// Not fatal — a file that doesn't parse means we're
			// running against a broken tree. Surface the file in
			// the log so the user sees the cause.
			t.Logf("parse %s: %v (skipping)", path, parseErr)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok || len(cl.Elts) < 2 {
				return true
			}
			first, ok := stringLiteral(cl.Elts[0])
			if !ok || first != binary {
				return true
			}
			for _, e := range cl.Elts[1:] {
				s, ok := stringLiteral(e)
				if !ok || !strings.HasPrefix(s, "--") {
					continue
				}
				if eq := strings.Index(s, "="); eq > 0 {
					s = s[:eq]
				}
				seen[s] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", pkgDir, err)
	}

	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	return out
}

// stringLiteral returns the Go-string value of a BasicLit, if and
// only if the expression is a string literal. Returns "", false
// for anything else (idents, function calls, etc.).
func stringLiteral(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	// BasicLit.Value includes surrounding quotes; strip them.
	v := bl.Value
	if len(v) >= 2 && (v[0] == '"' || v[0] == '`') {
		v = v[1 : len(v)-1]
	}
	return v, true
}

// helpText runs `docker run --rm --entrypoint <bin> <image> <args...>`
// and returns the combined stdout+stderr. Many CLIs exit non-zero when
// printing --help with no TTY, so we don't check the exit code.
func helpText(t *testing.T, image, bin string, args []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dockerArgs := append([]string{"run", "--rm", "--entrypoint", bin, image}, args...)
	out, _ := exec.CommandContext(ctx, "docker", dockerArgs...).CombinedOutput()
	return string(out)
}

// imageAvailable returns true if the named image is in the local
// docker image cache. Cheaper than letting docker run pull on every
// missing image — we'd rather skip than block on a registry fetch.
func imageAvailable(t *testing.T, image string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", image).CombinedOutput()
	if err != nil {
		t.Logf("docker image inspect %s: %v\n%s", image, err, string(out))
		return false
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...(truncated)"
}
