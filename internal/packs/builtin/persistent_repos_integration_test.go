// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

//go:build integration
// +build integration

package builtin_test

// persistent_repos_integration_test.go (issue #259, ADR 040) — the
// real-Docker test that exercises cross-session clone reuse on the
// persistent repos volume.
//
// Two repo.fetch calls in SEPARATE sessions against the same repo: the
// first full-clones into /repos/<caller>/<hash> (persistent=true,
// reused=false), the second — with no _session_id, a brand-new session —
// must hit the existing clone and `git fetch` instead (reused=true). This
// is the whole point of ADR 040, and it can only be verified end-to-end
// with a real volume mounted into real sidecars.
//
// Build-tagged `integration`; run with:
//
//   HELMDECK_INTEGRATION=1 go test -tags=integration ./internal/packs/builtin/... -run TestPersistentRepos -v

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/packs/builtin"
	"github.com/tosin2013/helmdeck/internal/session"
	dockerrt "github.com/tosin2013/helmdeck/internal/session/docker"
)

// publicCloneURL is a tiny, stable public repo used so the test doesn't
// need vault credentials and the clone stays fast.
const publicCloneURL = "https://github.com/octocat/Hello-World.git"

func TestPersistentReposReuse_SecondSessionFetchesNotClones(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker daemon not reachable")
	}
	if os.Getenv("HELMDECK_INTEGRATION") == "" {
		t.Skip("set HELMDECK_INTEGRATION=1 to run real-Docker integration tests")
	}

	// A throwaway, uniquely-named volume so a failed run can't poison the
	// next. Docker auto-creates it on first mount, but we create+remove
	// explicitly for a clean teardown.
	volume := "helmdeck-repos-it-" + strings.ReplaceAll(t.Name(), "/", "-")
	if out, err := exec.Command("docker", "volume", "create", volume).CombinedOutput(); err != nil {
		t.Fatalf("docker volume create: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })

	rt, err := dockerrt.New(dockerrt.WithReposVolume(volume))
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	ex, ok := any(rt).(session.Executor)
	if !ok {
		t.Fatal("docker runtime does not implement session.Executor")
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := packs.New(packs.WithRuntime(rt), packs.WithSessionExecutor(ex), packs.WithLogger(silent))

	// Public repo ⇒ no vault, no egress guard needed.
	fetch := builtin.RepoFetch(nil, nil)

	type fetchOut struct {
		ClonePath  string `json:"clone_path"`
		Persistent bool   `json:"persistent"`
		Reused     bool   `json:"reused"`
		Commit     string `json:"commit"`
		SessionID  string `json:"session_id"`
	}
	run := func() fetchOut {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		res, perr := eng.Execute(ctx, fetch, mustMarshal(t, map[string]any{"url": publicCloneURL}))
		if perr != nil {
			t.Fatalf("repo.fetch: %v", perr)
		}
		// Each call gets a fresh session (no _session_id), which the engine
		// keeps alive (PreserveSession) until the watchdog; clean it up now.
		if res.SessionID != "" {
			t.Cleanup(func() {
				tctx, tcancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer tcancel()
				_ = rt.Terminate(tctx, res.SessionID)
			})
		}
		var o fetchOut
		if err := json.Unmarshal(res.Output, &o); err != nil {
			t.Fatalf("unmarshal repo.fetch output: %v (raw %s)", err, res.Output)
		}
		return o
	}

	first := run()
	if !first.Persistent {
		t.Error("first fetch should report persistent=true on a repos volume")
	}
	if first.Reused {
		t.Error("first fetch should be a fresh clone, not reused")
	}
	if !strings.HasPrefix(first.ClonePath, "/repos/") {
		t.Errorf("first clone_path %q should live under /repos", first.ClonePath)
	}

	second := run()
	if !second.Reused {
		t.Error("second fetch (new session, same repo) should REUSE the on-volume clone")
	}
	if second.ClonePath != first.ClonePath {
		t.Errorf("reuse should hit the same path: first %q, second %q", first.ClonePath, second.ClonePath)
	}
	if second.Commit != first.Commit {
		t.Errorf("commit drift across reuse: %q vs %q", first.Commit, second.Commit)
	}
}
