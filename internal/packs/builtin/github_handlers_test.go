// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// github_handlers_test.go covers the HTTP-call path of the github.* pack
// set. The existing github_cache_test.go exercises the engine-cache seam
// (read-through caching, opt-in metadata) by stubbing the handler — it
// never actually calls githubAPI. These tests do the opposite: they
// override githubAPIBase to point at an httptest.NewServer stub and run
// the real handlers through the real githubAPI helper, so the HTTP-call
// shape (method, path, headers, body) is pinned.
//
// Pattern per test:
//   1. Spin an httptest.NewServer that captures the request and replies
//      with a canned JSON body.
//   2. Override githubAPIBase via the test-only var seam. t.Cleanup
//      restores the production value so test ordering doesn't leak.
//   3. Build a vault with a github-token credential so the handler's
//      bearer-auth path runs.
//   4. Invoke pack.Handler directly with a minimal ExecutionContext.
//   5. Assert (a) the request the stub saw is correctly shaped, and
//      (b) the response surfaces through the handler unchanged.

// stubGitHubAPI spins an httptest.NewServer, returns it, and overrides
// the package-global githubAPIBase to point at the server's URL for
// the duration of the test. The handler captures the request body +
// headers + method+path so tests can assert on them.
type stubGitHubReq struct {
	Method string
	Path   string
	Auth   string
	Accept string
	APIVer string
	UA     string
	Body   string
}

func stubGitHubAPI(t *testing.T, status int, replyBody string) (*httptest.Server, *stubGitHubReq, *int) {
	t.Helper()
	captured := &stubGitHubReq{}
	hits := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		body, _ := io.ReadAll(r.Body)
		captured.Method = r.Method
		captured.Path = r.URL.Path + (func() string {
			if r.URL.RawQuery != "" {
				return "?" + r.URL.RawQuery
			}
			return ""
		})()
		captured.Auth = r.Header.Get("Authorization")
		captured.Accept = r.Header.Get("Accept")
		captured.APIVer = r.Header.Get("X-GitHub-Api-Version")
		captured.UA = r.Header.Get("User-Agent")
		captured.Body = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(replyBody))
	}))
	t.Cleanup(srv.Close)
	prev := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = prev })
	return srv, captured, &hits
}

// vaultWithGitHubToken seeds an in-memory vault with the
// `github-token` credential + a wildcard ACL so ResolveByName succeeds
// from the handler's `*` actor.
func vaultWithGitHubToken(t *testing.T, token string) *vault.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	master := make([]byte, 32)
	v, err := vault.New(db, master)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		return v
	}
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        "github-token",
		Type:        vault.TypeAPIKey,
		HostPattern: "api.github.com",
		Plaintext:   []byte(token),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	return v
}

// runGitHubPack invokes a github pack's Handler directly. We bypass
// eng.Execute so the test focuses on the HTTP-shape contract — engine
// behavior (audit, caching) is covered by github_cache_test.go.
func runGitHubPack(t *testing.T, pack *packs.Pack, input string) (json.RawMessage, error) {
	t.Helper()
	ec := &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(input),
	}
	return pack.Handler(context.Background(), ec)
}

// TestGitHubAPI_RequestShape pins the headers the helper sets on every
// call: Authorization (when token present), Accept, X-GitHub-Api-Version,
// User-Agent, Content-Type (when body present). A regression in any of
// these is a real bug — GitHub rejects calls missing Accept or APIVer
// with confusing errors, and bearer auth has to match the exact format.
func TestGitHubAPI_RequestShape(t *testing.T) {
	_, captured, _ := stubGitHubAPI(t, 201,
		`{"number":42,"html_url":"https://github.com/o/r/issues/42","url":"https://api.github.com/repos/o/r/issues/42"}`)
	v := vaultWithGitHubToken(t, "ghp_secret123")

	_, err := runGitHubPack(t, GitHubCreateIssue(v),
		`{"repo":"o/r","title":"Test","body":"hi","labels":["bug"]}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if captured.Method != "POST" {
		t.Errorf("Method = %q; want POST", captured.Method)
	}
	if captured.Path != "/repos/o/r/issues" {
		t.Errorf("Path = %q; want /repos/o/r/issues", captured.Path)
	}
	if captured.Auth != "Bearer ghp_secret123" {
		t.Errorf("Auth = %q; want Bearer ghp_secret123", captured.Auth)
	}
	if captured.Accept != "application/vnd.github+json" {
		t.Errorf("Accept = %q; want application/vnd.github+json", captured.Accept)
	}
	if captured.APIVer != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q; want 2022-11-28", captured.APIVer)
	}
	if !strings.HasPrefix(captured.UA, "Helmdeck/") {
		t.Errorf("User-Agent = %q; want Helmdeck/... prefix", captured.UA)
	}

	// Body must include title, body, and labels.
	var sentBody map[string]any
	_ = json.Unmarshal([]byte(captured.Body), &sentBody)
	if sentBody["title"] != "Test" || sentBody["body"] != "hi" {
		t.Errorf("body fields wrong: %+v", sentBody)
	}
	labels, _ := sentBody["labels"].([]any)
	if len(labels) != 1 || labels[0] != "bug" {
		t.Errorf("labels = %v; want [bug]", labels)
	}
}

// TestGitHubAPI_NoTokenSkipsAuth — when no token resolves, the helper
// must NOT set an Authorization header (public reads still work
// against the GitHub API; bearer-with-empty-string would fail oddly).
func TestGitHubAPI_NoTokenSkipsAuth(t *testing.T) {
	_, captured, _ := stubGitHubAPI(t, 200, `[]`)
	v := vaultWithGitHubToken(t, "") // no token seeded

	_, err := runGitHubPack(t, GitHubListPRs(v), `{"repo":"o/r","state":"open"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if captured.Auth != "" {
		t.Errorf("Auth = %q; want empty (no token → no bearer)", captured.Auth)
	}
}

// TestGitHubAPI_UpstreamErrorSurfaces — a 4xx/5xx from GitHub turns
// into a CodeHandlerFailed PackError whose message includes the status
// + the `message` field from the GitHub error envelope.
func TestGitHubAPI_UpstreamErrorSurfaces(t *testing.T) {
	stubGitHubAPI(t, 422, `{"message":"Validation Failed","errors":[{"field":"title"}]}`)
	v := vaultWithGitHubToken(t, "ghp_test")

	_, err := runGitHubPack(t, GitHubCreateIssue(v),
		`{"repo":"o/r","title":"Test"}`)
	var perr *packs.PackError
	if !errors.As(err, &perr) {
		t.Fatalf("err type = %T; want *PackError (%v)", err, err)
	}
	if perr.Code != packs.CodeHandlerFailed {
		t.Errorf("code = %q; want CodeHandlerFailed", perr.Code)
	}
	if !strings.Contains(perr.Message, "422") {
		t.Errorf("message should include status 422: %q", perr.Message)
	}
	if !strings.Contains(perr.Message, "Validation Failed") {
		t.Errorf("message should include GitHub's `message` field: %q", perr.Message)
	}
}

// TestGitHubCreateRelease_RequestShape covers the body shape +
// path for create_release — distinct from create_issue, the path
// includes /releases and the body has tag_name + name + body.
func TestGitHubCreateRelease_RequestShape(t *testing.T) {
	_, captured, _ := stubGitHubAPI(t, 201,
		`{"id":1,"html_url":"https://github.com/o/r/releases/tag/v1.2.3","tag_name":"v1.2.3"}`)
	v := vaultWithGitHubToken(t, "ghp_x")
	_, err := runGitHubPack(t, GitHubCreateRelease(v),
		`{"repo":"o/r","tag":"v1.2.3","name":"v1.2.3","body":"Release notes"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if captured.Path != "/repos/o/r/releases" {
		t.Errorf("Path = %q", captured.Path)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(captured.Body), &sent)
	// The GitHub API expects `tag_name` — the handler maps from the
	// pack's `tag` input field, so the wire body shape is GitHub's.
	if sent["tag_name"] != "v1.2.3" || sent["name"] != "v1.2.3" {
		t.Errorf("body = %+v", sent)
	}
	if sent["body"] != "Release notes" {
		t.Errorf("release body field = %v", sent["body"])
	}
}

// TestGitHubPostComment_RequestShape covers /issues/{n}/comments and
// the body shape (just `body`).
func TestGitHubPostComment_RequestShape(t *testing.T) {
	_, captured, _ := stubGitHubAPI(t, 201,
		`{"id":99,"html_url":"https://github.com/o/r/issues/7#issuecomment-99"}`)
	v := vaultWithGitHubToken(t, "ghp_x")
	_, err := runGitHubPack(t, GitHubPostComment(v),
		`{"repo":"o/r","issue_number":7,"body":"Looks good!"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if captured.Path != "/repos/o/r/issues/7/comments" {
		t.Errorf("Path = %q", captured.Path)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(captured.Body), &sent)
	if sent["body"] != "Looks good!" {
		t.Errorf("body = %+v", sent)
	}
}

// TestGitHubSearch_QueryStringEncoded — github.search builds a query
// string from `q` (and `type`); verify the URL the helper hits is
// well-shaped and the response surfaces back.
func TestGitHubSearch_QueryStringEncoded(t *testing.T) {
	_, captured, _ := stubGitHubAPI(t, 200,
		`{"total_count":1,"items":[{"name":"hello"}]}`)
	v := vaultWithGitHubToken(t, "ghp_x")
	_, err := runGitHubPack(t, GitHubSearch(v),
		`{"query":"helmdeck stars:>10","type":"repositories"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	// Search uses /search/{type}?q=...
	if !strings.HasPrefix(captured.Path, "/search/repositories?") {
		t.Errorf("Path = %q; want /search/repositories?...", captured.Path)
	}
	if !strings.Contains(captured.Path, "q=") {
		t.Errorf("Path should carry q query param: %q", captured.Path)
	}
}

// TestGitHubListPRs_QueryParams — list_prs translates input fields
// (state, head, base) into query-string params; assert the URL shape.
func TestGitHubListPRs_QueryParams(t *testing.T) {
	_, captured, _ := stubGitHubAPI(t, 200, `[]`)
	v := vaultWithGitHubToken(t, "ghp_x")
	_, err := runGitHubPack(t, GitHubListPRs(v),
		`{"repo":"o/r","state":"closed","head":"o:feature","base":"main"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.HasPrefix(captured.Path, "/repos/o/r/pulls?") {
		t.Errorf("Path = %q; want /repos/o/r/pulls?...", captured.Path)
	}
	for _, want := range []string{"state=closed", "head=", "base=main"} {
		if !strings.Contains(captured.Path, want) {
			t.Errorf("Path missing %q: %q", want, captured.Path)
		}
	}
}
