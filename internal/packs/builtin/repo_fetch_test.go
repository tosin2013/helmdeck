package builtin

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// vaultWithSSHCred constructs an in-memory vault store with one SSH
// credential matching the given host. Returns the store + the
// credential id so tests can grant + assert on it.
func vaultWithSSHCred(t *testing.T, host string, payload []byte) *vault.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	v, err := vault.New(db, key)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name: "deploy-key", Type: vault.TypeSSH, HostPattern: host, Plaintext: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	return v
}

func newRepoEngine(t *testing.T, ex *recordingExecutor) *packs.Engine {
	t.Helper()
	return packs.New(
		packs.WithRuntime(fakeRuntime{}),
		packs.WithSessionExecutor(ex),
	)
}

func TestRepoFetch_HappyPath(t *testing.T) {
	v := vaultWithSSHCred(t, "github.com", []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfake\n-----END OPENSSH PRIVATE KEY-----"))
	envelope := `{"clone_path":"/tmp/helmdeck-clone-abc","commit":"deadbeef","files":42}`
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(envelope)},
	}}
	eng := newRepoEngine(t, ex)

	res, err := eng.Execute(context.Background(), RepoFetch(v, nil),
		json.RawMessage(`{"url":"git@github.com:tosin2013/helmdeck.git","ref":"main","depth":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ex.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(ex.calls))
	}
	// Stdin must be the SSH private key.
	if !strings.Contains(string(ex.calls[0].Stdin), "BEGIN OPENSSH PRIVATE KEY") {
		t.Errorf("ssh key not piped via stdin: %q", ex.calls[0].Stdin)
	}
	// Script must include git clone with the URL and the depth flag.
	script := strings.Join(ex.calls[0].Cmd, " ")
	if !strings.Contains(script, "git clone --depth 1") {
		t.Errorf("depth flag missing from script: %s", script)
	}
	if !strings.Contains(script, "tosin2013/helmdeck.git") {
		t.Errorf("repo URL missing from script: %s", script)
	}
	if !strings.Contains(script, "GIT_SSH_COMMAND") {
		t.Errorf("GIT_SSH_COMMAND missing: %s", script)
	}
	if !strings.Contains(script, "checkout 'main'") {
		t.Errorf("ref checkout missing: %s", script)
	}

	var out struct {
		URL        string `json:"url"`
		Commit     string `json:"commit"`
		Credential string `json:"credential"`
		Files      int    `json:"files"`
		ClonePath  string `json:"clone_path"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Commit != "deadbeef" || out.Files != 42 || out.ClonePath != "/tmp/helmdeck-clone-abc" {
		t.Errorf("envelope not surfaced: %+v", out)
	}
	if out.Credential != "deploy-key" {
		t.Errorf("credential name not echoed: %s", out.Credential)
	}
}

func TestRepoFetch_NoVaultMatch(t *testing.T) {
	v := vaultWithSSHCred(t, "github.com", []byte("key"))
	eng := newRepoEngine(t, &recordingExecutor{})
	_, err := eng.Execute(context.Background(), RepoFetch(v, nil),
		json.RawMessage(`{"url":"git@gitlab.com:foo/bar.git"}`))
	if err == nil {
		t.Fatal("expected error for unmatched host")
	}
	if !strings.Contains(err.Error(), "no vault credential matches") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestRepoFetch_HTTPSPublicClone(t *testing.T) {
	// HTTPS without a credential should attempt a public clone.
	rec := &recordingExecutor{
		replies: []session.ExecResult{{
			Stdout: []byte(`{"clone_path":"/tmp/helmdeck-clone-abc","commit":"deadbeef","files":3}`),
		}},
	}
	eng := newRepoEngine(t, rec)
	res, err := eng.Execute(context.Background(), RepoFetch(nil, nil),
		json.RawMessage(`{"url":"https://github.com/octocat/Hello-World.git"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out["clone_path"] != "/tmp/helmdeck-clone-abc" {
		t.Errorf("clone_path = %v", out["clone_path"])
	}
	// Verify the script does NOT contain GIT_ASKPASS (no credential)
	if len(rec.calls) > 0 {
		script := strings.Join(rec.calls[0].Cmd, " ")
		if strings.Contains(script, "GIT_ASKPASS") {
			t.Error("public clone should not set GIT_ASKPASS in the command")
		}
	}
}

func TestRepoFetch_WrongCredentialType(t *testing.T) {
	db, _ := store.Open(":memory:")
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	v, _ := vault.New(db, key)
	rec, _ := v.Create(context.Background(), vault.CreateInput{
		Name: "wrong", Type: vault.TypeAPIKey, HostPattern: "github.com", Plaintext: []byte("token"),
	})
	_ = v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"})

	eng := newRepoEngine(t, &recordingExecutor{})
	_, err := eng.Execute(context.Background(), RepoFetch(v, nil),
		json.RawMessage(`{"url":"git@github.com:foo/bar.git"}`))
	if err == nil {
		t.Fatal("expected error for non-ssh credential type")
	}
	if !strings.Contains(err.Error(), "expected ssh") {
		t.Errorf("wrong error: %v", err)
	}
}

func TestRepoFetch_GitCloneFailureBubbles(t *testing.T) {
	v := vaultWithSSHCred(t, "github.com", []byte("key"))
	ex := &recordingExecutor{replies: []session.ExecResult{
		{ExitCode: 128, Stderr: []byte("fatal: repository not found")},
	}}
	eng := newRepoEngine(t, ex)
	_, err := eng.Execute(context.Background(), RepoFetch(v, nil),
		json.RawMessage(`{"url":"git@github.com:nope/nope.git"}`))
	if err == nil {
		t.Fatal("expected git clone failure to surface")
	}
	if !strings.Contains(err.Error(), "exit 128") || !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("error should include git's stderr: %v", err)
	}
}

func TestRepoFetch_RequiresURL(t *testing.T) {
	v := vaultWithSSHCred(t, "github.com", []byte("key"))
	eng := newRepoEngine(t, &recordingExecutor{})
	_, err := eng.Execute(context.Background(), RepoFetch(v, nil), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing url")
	}
}

func TestParseGitHost(t *testing.T) {
	cases := []struct {
		url    string
		host   string
		scheme string
		err    bool
	}{
		{"git@github.com:tosin2013/helmdeck.git", "github.com", "ssh", false},
		{"ssh://git@github.com/tosin2013/helmdeck.git", "github.com", "ssh", false},
		{"https://github.com/tosin2013/helmdeck.git", "github.com", "https", false},
		{"http://gitlab.local:8080/foo/bar.git", "gitlab.local", "https", false},
		{"ftp://example.com/repo.git", "", "", true},
		{"not-a-url", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			host, scheme, err := parseGitHost(tc.url)
			if tc.err {
				if err == nil {
					t.Errorf("expected error")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if host != tc.host || scheme != tc.scheme {
				t.Errorf("got (%s, %s), want (%s, %s)", host, scheme, tc.host, tc.scheme)
			}
		})
	}
}

func TestBuildRepoFetchScript_OmitsCheckoutWithoutRef(t *testing.T) {
	script := buildRepoFetchSSHScript("git@github.com:foo/bar.git", "", 0)
	if strings.Contains(script, "checkout") {
		t.Errorf("empty ref should produce no checkout line")
	}
	if strings.Contains(script, "--depth") {
		t.Errorf("zero depth should produce no --depth flag")
	}
}

// T508 — verify the egress guard blocks the pack before any vault
// or executor work happens. Uses a stub resolver that returns the
// metadata IP for the requested host.
func TestRepoFetch_EgressGuardBlocksMetadataHost(t *testing.T) {
	v := vaultWithSSHCred(t, "evil.example", []byte("key"))
	eg := security.New(security.WithResolver(stubMetaResolver{}))
	ex := &recordingExecutor{}
	eng := newRepoEngine(t, ex)

	_, err := eng.Execute(context.Background(), RepoFetch(v, eg),
		json.RawMessage(`{"url":"git@evil.example:foo/bar.git"}`))
	if err == nil {
		t.Fatal("expected egress guard to block metadata-resolving host")
	}
	if !strings.Contains(err.Error(), "egress denied") {
		t.Errorf("wrong error: %v", err)
	}
	// The handler must short-circuit BEFORE the executor sees anything.
	if len(ex.calls) != 0 {
		t.Errorf("executor should not be invoked when egress is blocked, got %d calls", len(ex.calls))
	}
}

// stubMetaResolver returns the AWS/GCP/Azure cloud metadata IP for
// every lookup. Used to simulate the SSRF attack the egress guard
// is supposed to refuse.
type stubMetaResolver struct{}

func (stubMetaResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
}
