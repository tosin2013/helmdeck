package builtin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// TestGitHubListIssuesOptsIntoCache asserts the cache exemplar (#258)
// is declaratively enabled on github.list_issues with a sane TTL. The
// engine seam — not the handler — does the caching, so this guards the
// one-line opt-in from regressing.
func TestGitHubListIssuesOptsIntoCache(t *testing.T) {
	p := GitHubListIssues(nil)
	if p.Memory == nil || !p.Memory.Cache {
		t.Fatal("github.list_issues should opt into the read-through cache")
	}
	if p.Memory.TTL != 5*time.Minute {
		t.Fatalf("expected 5m cache TTL, got %v", p.Memory.TTL)
	}
}

// TestGitHubListIssuesCacheSkipsAPI proves that, with a memory store
// wired, a 2nd identical github.list_issues call within the TTL is
// served from memory and does NOT reach the GitHub API.
//
// Approach: engine-level. github.go's githubAPI uses http.DefaultClient
// with no injectable transport seam, so we cannot count real HTTP calls
// without network. Instead we run the REAL github.list_issues pack
// metadata (Name + Memory config) through the engine with a counting
// handler standing in for githubHandler. Because the cache seam keys on
// (pack.Name, sha256(input)) and the github pack's Memory config, this
// exercises the exact same code path that gates githubAPI in production
// — a cache hit returns before the handler (and thus before githubAPI)
// would ever run. The counter is our proxy for "did we call githubAPI".
func TestGitHubListIssuesCacheSkipsAPI(t *testing.T) {
	apiCalls := 0
	// Reuse the real pack's identity + Memory config so the test tracks
	// production, but substitute a handler we can count.
	real := GitHubListIssues(nil)
	pack := &packs.Pack{
		Name:         real.Name,
		Version:      real.Version,
		InputSchema:  real.InputSchema,
		OutputSchema: real.OutputSchema,
		Memory:       real.Memory,
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			apiCalls++ // stands in for githubAPI(...)
			return json.RawMessage(`{"issues":[],"count":0}`), nil
		},
	}

	eng := packs.New(
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		packs.WithMemoryStore(memory.NewInMemoryStore()),
	)
	ctx := packs.WithCaller(context.Background(), "tester")
	input := json.RawMessage(`{"repo":"octocat/hello-world","state":"open"}`)

	if _, err := eng.Execute(ctx, pack, input); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := eng.Execute(ctx, pack, input); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if apiCalls != 1 {
		t.Fatalf("expected exactly 1 githubAPI call (2nd served from cache), got %d", apiCalls)
	}
}

// TestGitHubGetIssueShape — the new pack must reject inputs missing
// repo or issue_number (the issue-to-pr pipeline relies on both),
// accept an output that includes title + body (what swe.solve consumes),
// and opt into the same 5-minute read-through cache as list_issues so a
// pipeline rerun for the same issue doesn't re-hit the REST API.
func TestGitHubGetIssueShape(t *testing.T) {
	p := GitHubGetIssue(nil)
	if p.Name != "github.get_issue" {
		t.Errorf("pack name = %q", p.Name)
	}
	// Validate the schema enforcement publicly (the Schema interface
	// hides Required/Properties; .Validate is the production gate).
	for _, bad := range []string{`{}`, `{"repo":"o/r"}`, `{"issue_number":42}`} {
		if err := p.InputSchema.Validate(json.RawMessage(bad)); err == nil {
			t.Errorf("InputSchema should reject %q", bad)
		}
	}
	if err := p.InputSchema.Validate(json.RawMessage(`{"repo":"o/r","issue_number":42}`)); err != nil {
		t.Errorf("InputSchema rejected a well-formed input: %v", err)
	}
	// A realistic GitHub issue response satisfies the OutputSchema.
	if err := p.OutputSchema.Validate(json.RawMessage(`{"number":42,"title":"T","body":"B","state":"open","labels":[],"html_url":"https://github.com/o/r/issues/42","user":{"login":"alice"}}`)); err != nil {
		t.Errorf("OutputSchema rejected a realistic issue payload: %v", err)
	}
	if p.Memory == nil || !p.Memory.Cache {
		t.Fatal("github.get_issue should opt into the read-through cache")
	}
	if p.Memory.TTL != 5*time.Minute {
		t.Fatalf("expected 5m cache TTL (matching list_issues), got %v", p.Memory.TTL)
	}
}

// TestGitHubGetIssueCacheSkipsAPI mirrors the list_issues cache test: a
// 2nd identical github.get_issue call within the TTL must NOT re-invoke
// the handler (which stands in for the real githubAPI call).
func TestGitHubGetIssueCacheSkipsAPI(t *testing.T) {
	apiCalls := 0
	real := GitHubGetIssue(nil)
	pack := &packs.Pack{
		Name:         real.Name,
		Version:      real.Version,
		InputSchema:  real.InputSchema,
		OutputSchema: real.OutputSchema,
		Memory:       real.Memory,
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			apiCalls++
			return json.RawMessage(`{"number":42,"title":"T","body":"B","state":"open","labels":[],"html_url":"https://github.com/o/r/issues/42"}`), nil
		},
	}
	eng := packs.New(
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		packs.WithMemoryStore(memory.NewInMemoryStore()),
	)
	ctx := packs.WithCaller(context.Background(), "tester")
	input := json.RawMessage(`{"repo":"octocat/hello-world","issue_number":42}`)
	for i := 0; i < 2; i++ {
		if _, err := eng.Execute(ctx, pack, input); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if apiCalls != 1 {
		t.Fatalf("expected exactly 1 handler call (2nd served from cache), got %d", apiCalls)
	}
}
