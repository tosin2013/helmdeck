// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

//go:build integration
// +build integration

package builtin_test

// session_reuse_integration_test.go (issue #232) — the missing
// real-Docker test that exercises cross-pack session reuse.
//
// Until this test, every pack test in the repo used recordingExecutor
// (a fake). That hid the bug where `repo.fetch` returns a clone_path
// living inside a session container, but the response shape doesn't
// surface the session_id the caller MUST pass to follow-on packs to
// see that clone. This file pins the contract down.
//
// Build-tagged `integration` so the unit suite isn't slowed by docker
// container starts. Run with:
//
//   HELMDECK_INTEGRATION=1 go test -tags=integration ./internal/packs/builtin/... -run TestSessionReuse -v

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/packs/builtin"
	"github.com/tosin2013/helmdeck/internal/session"
	dockerrt "github.com/tosin2013/helmdeck/internal/session/docker"
)

// dockerAvailable mirrors the helper in internal/session/docker so the
// integration tests skip cleanly on hosts without a daemon.
func dockerAvailable(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat("/var/run/docker.sock")
	return err == nil
}

// newSessionReuseEngine constructs a real-Docker pack engine wired with
// just the packs the issue #232 tests need (cmd.run + fs.read). Returns
// the engine, the registry, and the runtime so the test can pre-create
// a pinned session before invoking either pack.
func newSessionReuseEngine(t *testing.T) (*packs.Engine, *packs.Registry, *dockerrt.Runtime) {
	t.Helper()
	rt, err := dockerrt.New()
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	ex, ok := any(rt).(session.Executor)
	if !ok {
		t.Fatalf("docker runtime does not implement session.Executor")
	}

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := packs.New(
		packs.WithRuntime(rt),
		packs.WithSessionExecutor(ex),
		packs.WithLogger(silent),
	)

	reg := packs.NewPackRegistry()
	if err := reg.Register(builtin.FSRead()); err != nil {
		t.Fatalf("register fs.read: %v", err)
	}
	return eng, reg, rt
}

// TestSessionReuse_PinnedSessionSeesPriorWrite is the integration test
// issue #232 prescribes. cmd.run writes a file inside a session; fs.read
// against the same _session_id reads it back.
//
//   PASS  ⇒ hypothesis (b): session reuse works. The bug is that callers
//           don't reliably propagate session_id — the response doesn't
//           make the requirement obvious enough. Fix the trap.
//   FAIL  ⇒ hypothesis (a): session reuse is broken at the runtime level.
//           Investigate internal/session/docker/runtime.go or the engine
//           session-pinning logic in internal/packs/packs.go.
//
// The clear test outcome is what disambiguates the bug shape.
func TestSessionReuse_PinnedSessionSeesPriorWrite(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("docker daemon not reachable")
	}
	if os.Getenv("HELMDECK_INTEGRATION") == "" {
		t.Skip("set HELMDECK_INTEGRATION=1 to run real-Docker integration tests")
	}

	eng, reg, rt := newSessionReuseEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Pre-create a session both calls will pin to. This is what the
	// agent flow needs: repo.fetch creates a session (with
	// PreserveSession: true) and follow-on packs reuse it.
	sess, err := rt.Create(ctx, session.Spec{
		// Empty Image → uses the runtime's default (helmdeck-sidecar).
		// The sidecar's entrypoint is long-running; we just need a
		// container that stays up for exec calls. The packs under test
		// only need /bin/sh + printf + mkdir + cat — every sidecar has those.
		Label:       "issue-232-pinned",
		MemoryLimit: "256m",
		SHMSize:     "64m",
		CPULimit:    0.5,
	})
	if err != nil {
		t.Fatalf("rt.Create: %v", err)
	}
	t.Cleanup(func() {
		tctx, tcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer tcancel()
		_ = rt.Terminate(tctx, sess.ID)
	})

	fsRead, err := reg.Get("fs.read", "")
	if err != nil {
		t.Fatalf("registry.Get fs.read: %v", err)
	}

	// Step 1: write a marker file inside the session via the executor
	// directly. This simulates what repo.fetch's clone script does:
	// it creates /tmp/helmdeck-clone-* inside the session container.
	// We bypass cmd.run here because cmd.run requires its clone_path
	// to exist (it `cd`'s into it first) — that's a separate layer.
	setupRes, err := rt.Exec(ctx, sess.ID, session.ExecRequest{
		Cmd: []string{"sh", "-c", "mkdir -p /tmp/helmdeck-issue232 && printf 'hello\\n' > /tmp/helmdeck-issue232/marker.txt"},
	})
	if err != nil {
		t.Fatalf("setup exec: %v", err)
	}
	if setupRes.ExitCode != 0 {
		t.Fatalf("setup exec exit %d, stderr=%q", setupRes.ExitCode, string(setupRes.Stderr))
	}

	// Step 2: fs.read with `_session_id` reads the file back. If this
	// fails, hypothesis (a) is confirmed — the engine isn't actually
	// routing follow-up packs to the pinned session.
	readInput := mustMarshal(t, map[string]any{
		"_session_id": sess.ID,
		"clone_path":  "/tmp/helmdeck-issue232",
		"path":        "marker.txt",
	})
	readRes, perr := eng.Execute(ctx, fsRead, readInput)
	if perr != nil {
		t.Fatalf("fs.read against pinned session: %v\n\n"+
			"⇒ This is HYPOTHESIS (a): session reuse is broken at the runtime "+
			"level despite _session_id being passed correctly. Investigate "+
			"internal/session/docker/runtime.go and the engine session-pinning "+
			"path in internal/packs/packs.go:304-342.", perr)
	}
	if readRes.SessionID != sess.ID {
		t.Errorf("fs.read returned session_id %q, want pinned %q", readRes.SessionID, sess.ID)
	}

	var out struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(readRes.Output, &out); err != nil {
		t.Fatalf("unmarshal fs.read output: %v (raw: %s)", err, readRes.Output)
	}
	if out.Content != "hello\n" {
		t.Errorf("fs.read content = %q, want %q", out.Content, "hello\n")
	}
}

// TestSessionReuse_FreshSessionMissesPriorWrite demonstrates the trap.
// cmd.run writes a file in session A (pinned). fs.read called WITHOUT
// `_session_id` makes the engine spin up a fresh session B with an
// empty /tmp, so the read fails. This is exactly the failure mode the
// user reported on #232:
//
//	fs.read    file not readable: sh: 1: cannot open /tmp/...
//	cmd.run    can't cd to /tmp/helmdeck-clone-... — exit code 2
//	repo.map   {map:"", tokens_estimated:0, ...} — empty, no error
//
// Test PASSES (in the Go sense) when the read fails as expected. A
// pack-error from fs.read here is the contract this test pins down.
func TestSessionReuse_FreshSessionMissesPriorWrite(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("docker daemon not reachable")
	}
	if os.Getenv("HELMDECK_INTEGRATION") == "" {
		t.Skip("set HELMDECK_INTEGRATION=1 to run real-Docker integration tests")
	}

	eng, reg, rt := newSessionReuseEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sess, err := rt.Create(ctx, session.Spec{
		Label: "issue-232-trap",
	})
	if err != nil {
		t.Fatalf("rt.Create: %v", err)
	}
	t.Cleanup(func() {
		tctx, tcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer tcancel()
		_ = rt.Terminate(tctx, sess.ID)
	})

	fsRead, _ := reg.Get("fs.read", "")

	// Write the marker in session A via the executor directly. Same
	// reason as the pinned test: bypasses cmd.run's clone_path-must-exist
	// guard.
	setupRes, err := rt.Exec(ctx, sess.ID, session.ExecRequest{
		Cmd: []string{"sh", "-c", "mkdir -p /tmp/helmdeck-issue232-trap && printf 'hello\\n' > /tmp/helmdeck-issue232-trap/marker.txt"},
	})
	if err != nil {
		t.Fatalf("setup exec: %v", err)
	}
	if setupRes.ExitCode != 0 {
		t.Fatalf("setup exec exit %d, stderr=%q", setupRes.ExitCode, string(setupRes.Stderr))
	}

	// fs.read WITHOUT `_session_id` — engine creates a fresh session B
	// whose /tmp does not contain the file we wrote in A. Expect a
	// pack error.
	readInput := mustMarshal(t, map[string]any{
		"clone_path": "/tmp/helmdeck-issue232-trap",
		"path":       "marker.txt",
	})
	_, perr := eng.Execute(ctx, fsRead, readInput)
	if perr == nil {
		t.Fatalf("fs.read without _session_id unexpectedly succeeded. " +
			"That means either /tmp is shared across sessions (unexpected), " +
			"or session reuse silently re-pinned to the prior session " +
			"(also unexpected). Either way the trap from issue #232 doesn't " +
			"exist in this configuration — re-verify with the user.")
	}
	msg := perr.Error()
	if !strings.Contains(msg, "not readable") && !strings.Contains(msg, "No such file") {
		t.Logf("trap fired with a non-default error message — still demonstrates the bug. Error: %v", perr)
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
