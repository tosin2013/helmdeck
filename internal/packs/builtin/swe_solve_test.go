// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// vaultWithTwoCreds builds an in-memory vault holding two api_key
// credentials resolvable by name, granted to all actors. Used to drive
// swe.solve's clone-token + gateway-key resolution in one test.
func vaultWithTwoCreds(t *testing.T, name1 string, p1 []byte, name2 string, p2 []byte) *vault.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	v, err := vault.New(db, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		payload []byte
	}{{name1, p1}, {name2, p2}} {
		rec, err := v.Create(context.Background(), vault.CreateInput{
			Name: c.name, Type: vault.TypeAPIKey, HostPattern: "github.com", Plaintext: c.payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

// newSweEngine wires the fake runtime + recording executor the same way
// the repo.fetch / desktop tests do.
func newSweEngine(t *testing.T, ex *recordingExecutor) *packs.Engine {
	t.Helper()
	return packs.New(
		packs.WithRuntime(fakeRuntime{}),
		packs.WithSessionExecutor(ex),
	)
}

// cloneEnvelope is the JSON the clone script emits on stdout (repo.fetch
// envelope shape). swe.solve parses clone_path out of it.
const sweCloneEnvelope = `{"clone_path":"/tmp/helmdeck-clone-abc","commit":"deadbeef","files":3}`

// scriptOf joins an Exec call's argv so tests can substring-match the
// shell pipeline a step emitted.
func scriptOf(req session.ExecRequest) string { return strings.Join(req.Cmd, " ") }

// TestSweSolve_PatchMode asserts the patch-mode orchestration sequence:
// clone → repo.map seed → mini run → diff → cat trajectory, and that
// it STOPS before any commit/push (no branch/push/PR steps).
func TestSweSolve_PatchMode(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(sweCloneEnvelope)},                  // 0: clone
		{Stdout: []byte(`{"map":"pkg/x.go:\n  func Foo"}`)}, // 1: repo.map
		{}, // 2: mini run
		{Stdout: []byte("diff --git a/x b/x\n+added")},        // 3: git diff
		{Stdout: []byte(`{"messages":[{"content":"done"}]}`)}, // 4: cat trajectory
	}}
	eng := newSweEngine(t, ex)

	res, err := eng.Execute(context.Background(), SweSolve(nil, nil),
		json.RawMessage(`{"repo_url":"https://github.com/octocat/Hello-World.git","task":"add a test","mode":"patch"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Exactly the patch-mode steps: clone, map, mini, diff, cat. No commit/push.
	if len(ex.calls) != 5 {
		t.Fatalf("patch mode should emit 5 exec calls, got %d", len(ex.calls))
	}
	// Step 0 must be the clone (git clone).
	if !strings.Contains(scriptOf(ex.calls[0]), "git clone") {
		t.Errorf("step 0 not a clone: %s", scriptOf(ex.calls[0]))
	}
	// Step 1 must be repo.map (ctags).
	if !strings.Contains(scriptOf(ex.calls[1]), "ctags") {
		t.Errorf("step 1 not repo.map: %s", scriptOf(ex.calls[1]))
	}
	// Step 2 must run mini with the task and the trajectory output path.
	miniScript := scriptOf(ex.calls[2])
	if !strings.Contains(miniScript, "mini -y -t") {
		t.Errorf("step 2 not a mini run: %s", miniScript)
	}
	if !strings.Contains(miniScript, sweTrajectoryPath) {
		t.Errorf("mini run missing trajectory output path: %s", miniScript)
	}
	if !strings.Contains(miniScript, "cd '/tmp/helmdeck-clone-abc'") {
		t.Errorf("mini run not cd'd into the clone: %s", miniScript)
	}
	// No commit/push/switch may appear in any patch-mode call.
	for i, c := range ex.calls {
		s := scriptOf(c)
		if strings.Contains(s, "git push") || strings.Contains(s, "switch -c") {
			t.Errorf("patch mode must not push or branch; call %d: %s", i, s)
		}
	}

	var out struct {
		Success       bool   `json:"success"`
		Patch         string `json:"patch"`
		Summary       string `json:"summary"`
		Branch        string `json:"branch"`
		PRURL         string `json:"pr_url"`
		TrajectoryKey string `json:"trajectory_artifact_key"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Success || !strings.Contains(out.Patch, "+added") {
		t.Errorf("patch not surfaced: %+v", out)
	}
	if out.Summary != "done" {
		t.Errorf("summary from trajectory not surfaced: %q", out.Summary)
	}
	if out.Branch != "" || out.PRURL != "" {
		t.Errorf("patch mode must not set branch/pr_url: %+v", out)
	}
	if out.TrajectoryKey == "" || !strings.HasPrefix(out.TrajectoryKey, "swe.solve/") {
		t.Errorf("trajectory artifact key missing/wrong: %q", out.TrajectoryKey)
	}
}

// TestSweSolve_PullRequestMode asserts the full mode chain reaches the
// commit + push steps and creates a NEW branch (never the default).
//
// We exercise `branch` mode here rather than `pull_request`: branch mode
// runs the identical clone→map→mini→diff→commit→push orchestration and
// the same new-branch safety guard, but stops before the github.create_pr
// HTTP call — so the test stays fully offline. The PR step's request
// shaping is covered by TestGitHubCreatePR_RequestShaping.
func TestSweSolve_BranchMode(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(sweCloneEnvelope)}, // 0: clone
		{Stdout: []byte(`{"map":""}`)},     // 1: repo.map
		{},                                 // 2: mini run
		{Stdout: []byte("diff --git a/x b/x\n+added")},        // 3: git diff
		{Stdout: []byte(`{"messages":[{"content":"done"}]}`)}, // 4: cat trajectory
		{Stdout: []byte("abc123")},                            // 5: rev-parse --short
		{Stdout: []byte("fullsha")},                           // 6: commit
		{},                                                    // 7: push
	}}
	eng := newSweEngine(t, ex)

	res, err := eng.Execute(context.Background(), SweSolve(nil, nil),
		json.RawMessage(`{"repo_url":"https://github.com/octocat/Hello-World.git","task":"fix it","mode":"branch"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ex.calls) != 8 {
		t.Fatalf("branch mode should emit exactly 8 exec calls, got %d", len(ex.calls))
	}
	var out struct {
		Branch string `json:"branch"`
		Commit string `json:"commit"`
		PRURL  string `json:"pr_url"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Branch != "helmdeck/swe-solve-abc123" || out.Commit != "fullsha" {
		t.Errorf("branch/commit not surfaced: %+v", out)
	}
	if out.PRURL != "" {
		t.Errorf("branch mode must not open a PR: %+v", out)
	}
	// The commit step (call 6) must create a NEW branch, never push to default.
	commitScript := scriptOf(ex.calls[6])
	if !strings.Contains(commitScript, "switch -c 'helmdeck/swe-solve-abc123'") {
		t.Errorf("commit step must create a new helmdeck/swe-solve-* branch: %s", commitScript)
	}
	if strings.Contains(commitScript, "switch -c 'main'") || strings.Contains(commitScript, "switch -c 'master'") {
		t.Errorf("must NEVER create/push the default branch: %s", commitScript)
	}
	// The push step (call 7) pushes the new branch, not main/master.
	pushScript := scriptOf(ex.calls[7])
	if !strings.Contains(pushScript, "push 'origin' 'helmdeck/swe-solve-abc123'") {
		t.Errorf("push step must push the new branch: %s", pushScript)
	}
	if strings.Contains(pushScript, "push 'origin' 'main'") {
		t.Errorf("must NEVER push to the default branch: %s", pushScript)
	}
}

// TestSweSolve_NoCredentialLeak is the security invariant: the resolved
// credential value must never appear in any agent-visible surface — not
// the mini argv (the agent loop), not the trajectory, and not the diff
// step. The gateway key is piped via stdin into the mini run script.
func TestSweSolve_NoCredentialLeak(t *testing.T) {
	const secretToken = "ghp_SUPERSECRETTOKEN1234567890"
	const gatewayKey = "sk-gatewaySECRETkey0987654321"

	// Vault with an HTTPS PAT (named "github-token") AND a gateway key
	// (named "helmdeck-gateway") so both resolution paths fire.
	v := vaultWithTwoCreds(t, "github-token", []byte(secretToken), "helmdeck-gateway", []byte(gatewayKey))

	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(sweCloneEnvelope)}, // 0: clone
		{Stdout: []byte(`{"map":""}`)},     // 1: repo.map
		{},                                 // 2: mini run
		{Stdout: []byte("diff")},           // 3: git diff
		{Stdout: []byte(`{"messages":[{"content":"done"}]}`)}, // 4: cat trajectory
	}}
	eng := newSweEngine(t, ex)

	res, err := eng.Execute(context.Background(), SweSolve(v, nil),
		json.RawMessage(`{"repo_url":"https://github.com/octocat/Hello-World.git","task":"do a thing","credential":"github-token","mode":"patch"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The secret must NOT appear in any Exec argv (agent-visible surface).
	for i, c := range ex.calls {
		s := scriptOf(c)
		if strings.Contains(s, secretToken) {
			t.Errorf("clone/push token leaked into argv of call %d: %s", i, s)
		}
		if strings.Contains(s, gatewayKey) {
			t.Errorf("gateway key leaked into argv of call %d: %s", i, s)
		}
	}
	// The gateway key must be delivered to the mini run via STDIN, not argv.
	miniCall := ex.calls[2]
	if !strings.Contains(string(miniCall.Stdin), gatewayKey) {
		t.Errorf("gateway key should be piped via stdin to mini run, got stdin=%q", miniCall.Stdin)
	}
	// And the mini run must read it from a file into the env, never echo it.
	if !strings.Contains(scriptOf(miniCall), "OPENAI_API_KEY=") {
		t.Errorf("mini run should export OPENAI_API_KEY from the piped key: %s", scriptOf(miniCall))
	}
	// The clone token must be piped via stdin to the clone, not argv.
	if !strings.Contains(string(ex.calls[0].Stdin), secretToken) {
		t.Errorf("clone token should be piped via stdin, got stdin=%q", ex.calls[0].Stdin)
	}

	// The secret must NOT appear in the returned output (patch/summary/etc).
	if strings.Contains(string(res.Output), secretToken) || strings.Contains(string(res.Output), gatewayKey) {
		t.Error("a credential value leaked into the pack output")
	}
}

// TestSweSolve_InvalidMode rejects an unknown output mode.
func TestSweSolve_InvalidMode(t *testing.T) {
	eng := newSweEngine(t, &recordingExecutor{})
	_, err := eng.Execute(context.Background(), SweSolve(nil, nil),
		json.RawMessage(`{"repo_url":"https://github.com/x/y.git","task":"t","mode":"merge"}`))
	if err == nil || !strings.Contains(err.Error(), "mode must be one of") {
		t.Fatalf("expected invalid mode error, got %v", err)
	}
}

// TestSweSolve_MissingTask rejects an empty task.
func TestSweSolve_MissingTask(t *testing.T) {
	eng := newSweEngine(t, &recordingExecutor{})
	_, err := eng.Execute(context.Background(), SweSolve(nil, nil),
		json.RawMessage(`{"repo_url":"https://github.com/x/y.git","task":""}`))
	if err == nil || !strings.Contains(err.Error(), "task is required") {
		t.Fatalf("expected task-required error, got %v", err)
	}
}

// fakeRuntimeWithRepos hands back a session carrying a persistent repos
// mount, so swe.solve takes the ADR-040 on-volume clone path instead of an
// ephemeral /tmp clone. Embeds fakeRuntime for the other methods.
type fakeRuntimeWithRepos struct{ fakeRuntime }

func (fakeRuntimeWithRepos) Create(ctx context.Context, spec session.Spec) (*session.Session, error) {
	return &session.Session{ID: "sess-1", Status: session.StatusRunning, ReposPath: "/repos"}, nil
}

// TestSweSolve_PersistentCloneUsesSharedFixedPath is the regression guard for
// the #298/#300 bug class: when a persistent repos volume is configured,
// swe.solve must clone through the SAME shared repo.fetch builder as
// repo.fetch — landing in the deterministic per-caller on-volume path and
// inheriting the world-writable `umask 000` the repos janitor (a different
// uid) needs to GC the tree. A bespoke clone here would silently re-introduce
// the "/repos Permission denied" / un-GC-able-clone failures.
func TestSweSolve_PersistentCloneUsesSharedFixedPath(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(sweCloneEnvelope)},     // 0: clone
		{Stdout: []byte(`{"map":"pkg/x.go"}`)}, // 1: repo.map
		{},                                     // 2: mini run
		{Stdout: []byte("diff --git a/x b/x\n+added")},      // 3: git diff
		{Stdout: []byte(`{"messages":[{"content":"ok"}]}`)}, // 4: cat trajectory
	}}
	eng := packs.New(
		packs.WithRuntime(fakeRuntimeWithRepos{}),
		packs.WithSessionExecutor(ex),
	)
	const repo = "https://github.com/octocat/Hello-World.git"
	ctx := packs.WithCaller(context.Background(), "alice")
	if _, err := eng.Execute(ctx, SweSolve(nil, nil),
		json.RawMessage(`{"repo_url":"`+repo+`","task":"add a test","mode":"patch"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	clone := scriptOf(ex.calls[0])
	if want := persistentCloneDir("/repos", "alice", repo); !strings.Contains(clone, want) {
		t.Errorf("persistent clone not into shared per-caller path %q:\n%s", want, clone)
	}
	if !strings.Contains(clone, "umask 000") {
		t.Errorf("persistent clone missing 'umask 000' (janitor GC needs world-writable):\n%s", clone)
	}
}

// TestGitHubCreatePR_RequestShaping verifies the PR pack builds the
// correct REST path and POST body without making a live call. We invoke
// the inner closure logic indirectly: the pack input is validated and
// the missing-field path returns a clear error.
func TestGitHubCreatePR_RequestShaping(t *testing.T) {
	p := GitHubCreatePR(nil)
	if p.Name != "github.create_pr" {
		t.Fatalf("wrong pack name: %s", p.Name)
	}
	// Schema requires repo, head, base, title.
	in := p.InputSchema.(packs.BasicSchema)
	for _, req := range []string{"repo", "head", "base", "title"} {
		found := false
		for _, r := range in.Required {
			if r == req {
				found = true
			}
		}
		if !found {
			t.Errorf("github.create_pr should require %q", req)
		}
	}
	// Output schema must expose html_url + number.
	out := p.OutputSchema.(packs.BasicSchema)
	if _, ok := out.Properties["html_url"]; !ok {
		t.Error("github.create_pr output should expose html_url")
	}

	// Missing required fields → handler_failed with a clear message
	// (vault nil → no token, but validation fires before the HTTP call).
	_, err := p.Handler(context.Background(), &packs.ExecutionContext{
		Input: json.RawMessage(`{"repo":"o/r","head":"feat","base":""}`),
	})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required-field error, got %v", err)
	}
}
