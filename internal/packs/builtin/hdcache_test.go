package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

func TestHdcacheBase(t *testing.T) {
	cases := []struct {
		reposPath, cwd, want string
	}{
		{"/repos", "/repos/alice/abc123", "/repos/alice/abc123/.hdcache"},
		// Anchors at the clone root even from a subdirectory.
		{"/repos", "/repos/alice/abc123/cmd/tool", "/repos/alice/abc123/.hdcache"},
		{"/repos/", "/repos/alice/abc123", "/repos/alice/abc123/.hdcache"},
		// Not on the volume ⇒ no cache.
		{"/repos", "/tmp/helmdeck-clone-x", ""},
		{"", "/repos/alice/abc123", ""},
		{"/repos", "", ""},
		// Missing the <hash> segment ⇒ not a clone path.
		{"/repos", "/repos/alice", ""},
	}
	for _, c := range cases {
		if got := hdcacheBase(c.reposPath, c.cwd); got != c.want {
			t.Errorf("hdcacheBase(%q,%q) = %q, want %q", c.reposPath, c.cwd, got, c.want)
		}
	}
}

func TestHdcacheEnvForBase(t *testing.T) {
	if hdcacheEnvForBase("") != nil {
		t.Error("empty base must yield nil env")
	}
	env := hdcacheEnvForBase("/repos/a/h/.hdcache")
	joined := strings.Join(env, " ")
	for _, want := range []string{
		"GOMODCACHE=/repos/a/h/.hdcache/go-mod",
		"npm_config_cache=/repos/a/h/.hdcache/npm",
		"PIP_CACHE_DIR=/repos/a/h/.hdcache/pip",
		"CARGO_HOME=/repos/a/h/.hdcache/cargo",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q\ngot: %v", want, env)
		}
	}
}

// captureExec records the last ExecRequest runWithCwd dispatches.
type captureExec struct{ last session.ExecRequest }

func (c *captureExec) run(_ context.Context, req session.ExecRequest) (session.ExecResult, error) {
	c.last = req
	return session.ExecResult{}, nil
}

func TestRunWithCwd_InjectsCacheOnPersistentVolume(t *testing.T) {
	cap := &captureExec{}
	ec := &packs.ExecutionContext{PersistentReposPath: "/repos", Exec: cap.run}

	if _, err := runWithCwd(context.Background(), ec, []string{"go", "build"}, "/repos/alice/abc123", nil); err != nil {
		t.Fatal(err)
	}
	if len(cap.last.Env) == 0 {
		t.Fatal("expected cache env to be injected on the persistent volume")
	}
	if !strings.Contains(strings.Join(cap.last.Env, " "), "GOMODCACHE=/repos/alice/abc123/.hdcache/go-mod") {
		t.Errorf("GOMODCACHE not pointed at .hdcache: %v", cap.last.Env)
	}
	script := strings.Join(cap.last.Cmd, " ")
	if !strings.Contains(script, "mkdir -p '/repos/alice/abc123/.hdcache'") {
		t.Errorf("expected mkdir of the cache root in script: %s", script)
	}
}

func TestRunWithCwd_NoCacheOffVolume(t *testing.T) {
	cap := &captureExec{}
	ec := &packs.ExecutionContext{PersistentReposPath: "/repos", Exec: cap.run}

	// cwd in /tmp (ephemeral clone) ⇒ no cache env, no mkdir.
	if _, err := runWithCwd(context.Background(), ec, []string{"go", "build"}, "/tmp/helmdeck-clone-x", nil); err != nil {
		t.Fatal(err)
	}
	if cap.last.Env != nil {
		t.Errorf("off-volume cwd must not inject cache env, got %v", cap.last.Env)
	}
	if strings.Contains(strings.Join(cap.last.Cmd, " "), "mkdir") {
		t.Error("off-volume cwd must not mkdir a cache dir")
	}
}

func TestRunWithCwd_NoSessionReposPath(t *testing.T) {
	cap := &captureExec{}
	ec := &packs.ExecutionContext{Exec: cap.run} // no repos volume

	if _, err := runWithCwd(context.Background(), ec, []string{"go", "build"}, "/repos/alice/abc123", nil); err != nil {
		t.Fatal(err)
	}
	if cap.last.Env != nil {
		t.Errorf("no repos volume ⇒ no cache env, got %v", cap.last.Env)
	}
}
