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
