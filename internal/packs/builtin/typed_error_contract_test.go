// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// stubMemoryInterface is the minimum surface memory_forget needs to
// progress past the nil-memory no-op shortcut into its scope-validation
// branch. List always returns no entries; Delete is unreachable here.
type stubMemoryInterface struct{}

func (stubMemoryInterface) Store(string, []byte, ...memory.PutOption) error { return nil }
func (stubMemoryInterface) Recall(string) (*memory.Entry, error)            { return nil, nil }
func (stubMemoryInterface) List(string) ([]memory.Entry, error)             { return nil, nil }
func (stubMemoryInterface) Delete(string) error                             { return nil }
func (stubMemoryInterface) Namespace() string                               { return "test" }
func (stubMemoryInterface) Context() (*packs.SessionContext, error)         { return nil, nil }

// Typed-error contract — every builtin pack returns a *packs.PackError
// with a code in the closed-set from internal/packs/classify.go (ADR 008).
//
// The architectural promise is that no error escaping Engine.Execute
// carries a code outside `validCodes`. Pack handlers can return ad-hoc
// errors and the engine's wrap() will classify them, but a pack that
// returns a *PackError MUST carry a valid code or the wire envelope
// shows "internal" when the failure was actually caller-fixable — and
// the LLM's recovery breaks because the typed signal is wrong.
//
// This table exercises every LLM-facing builtin we can construct with
// nil deps. For each, we invoke the handler with the empty JSON object
// `{}` — every pack has at least one required input field, so `{}`
// reliably triggers the missing-required path. The assertion:
//
//   1. The error is non-nil (every pack rejects empty input).
//   2. errors.As(err, &perr) succeeds (the error is a *PackError).
//   3. packs.IsValidCode(perr.Code) is true (the code is in the closed set).
//
// Packs deliberately skipped here:
//   - browser_interact, screenshot_url, desktop_run_app: CDP-bound,
//     need a real session executor (cdpfake). Their per-pack tests
//     cover the typed-error path.
//   - Vault-backed packs (github.*, email_send, http.fetch, stock.search,
//     image.generate, podcast.generate, slides.render, repo.fetch,
//     repo.push, blog.publish, swe.solve, slides.narrate): the empty-
//     input path errors before vault lookup happens, BUT some of these
//     are constructed by main.go with a real vault — the contract test
//     here covers the no-deps shape. Packs with vault-dependent typed-
//     error paths (CodeCredentialInvalid) are covered by their per-pack
//     tests with a real *vault.Store.
//
// When a new pack lands, add a row here. The compile-time guard is that
// the row tuple must successfully construct the pack; the runtime guard
// is that the handler returns a typed error with a valid code. That's
// the contract — both sides break loudly if a pack drifts.

func TestTypedErrorContract_AllPacks(t *testing.T) {
	cases := []struct {
		name string
		pack *packs.Pack
		// input is the deliberately-malformed payload. Default is `{}`
		// (missing required fields), but some packs accept `{}` as
		// valid and need a more pointed shape to force a typed error.
		input string
		// withMemory wires an in-memory store into ec.Memory. Packs
		// whose nil-memory branch is a no-op success (memory_forget)
		// need this to reach their typed-error path.
		withMemory bool
	}{
		// LLM-backed packs — input shape is the model's surface.
		{name: "blog.append_cta", pack: BlogAppendCTA(nil)},
		{name: "blog.rewrite_for_audience", pack: BlogRewriteForAudience(nil)},
		{name: "content.ground", pack: ContentGround(nil)},
		{name: "helmdeck.plan", pack: Plan(nil, nil, nil)},
		{name: "helmdeck.route", pack: Route(nil, nil, nil)},
		{name: "research.deep", pack: ResearchDeep(nil)},
		{name: "slides.outline", pack: SlidesOutline(nil)},
		{name: "slides.narrate", pack: SlidesNarrate(nil, nil, nil)},
		{name: "podcast.generate", pack: PodcastGenerate(nil, nil, nil)},
		{name: "hyperframes.compose", pack: HyperframesCompose(nil)},
		{name: "blog.publish", pack: BlogPublish(nil, nil, nil)},

		// Filesystem + git primitives — input shape is required fields.
		{name: "fs.read", pack: FSRead()},
		{name: "fs.write", pack: FSWrite()},
		{name: "fs.patch", pack: FSPatch()},
		{name: "fs.list", pack: FSList()},
		{name: "fs.delete", pack: FSDelete()},
		{name: "cmd.run", pack: CmdRun()},
		{name: "git.commit", pack: GitCommit()},
		{name: "git.diff", pack: GitDiff()},
		{name: "git.log", pack: GitLog()},

		// Repo packs — require repo_url, etc.
		{name: "repo.fetch", pack: RepoFetch(nil, nil)},
		{name: "repo.map", pack: RepoMap()},
		{name: "repo.push", pack: RepoPush(nil, nil)},
		{name: "swe.solve", pack: SweSolve(nil, nil)},

		// HTTP / scraping / external services.
		{name: "http.fetch", pack: HTTPFetch(nil, nil)},
		{name: "web.scrape", pack: WebScrape(nil)},
		{name: "scrape_spa", pack: ScrapeSPA()},
		{name: "stock.search", pack: StockSearch(nil, nil)},
		{name: "image.generate", pack: ImageGenerate(nil, nil)},
		{name: "email.send", pack: EmailSend(nil)},

		// Docs / vision.
		{name: "doc.ocr", pack: DocOCR()},
		{name: "doc.parse", pack: DocParse(nil)},

		// Memory primitives. memory_store requires key/value. memory_forget
		// short-circuits to a no-op success when ec.Memory is nil, so the
		// contract test wires a real store + an invalid scope to reach
		// the CodeInvalidInput path.
		{name: "helmdeck.memory_store", pack: MemoryStore()},
		{name: "helmdeck.memory_forget", pack: MemoryForget(),
			input: `{"scope":"this_scope_is_unknown"}`, withMemory: true},

		// Slides / rendering.
		{name: "slides.render", pack: SlidesRender(nil, nil)},
		{name: "hyperframes.render", pack: HyperframesRender()},

		// Language sidecars.
		{name: "python.run", pack: PythonRun()},
		{name: "node.run", pack: NodeRun()},

		// GitHub packs — share a nil vault; empty input errors before
		// the credential lookup.
		{name: "github.create_issue", pack: GitHubCreateIssue(nil)},
		{name: "github.list_issues", pack: GitHubListIssues(nil)},
		{name: "github.get_issue", pack: GitHubGetIssue(nil)},
		{name: "github.list_prs", pack: GitHubListPRs(nil)},
		{name: "github.post_comment", pack: GitHubPostComment(nil)},
		{name: "github.create_release", pack: GitHubCreateRelease(nil)},
		{name: "github.create_pr", pack: GitHubCreatePR(nil)},
		{name: "github.search", pack: GitHubSearch(nil)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.pack == nil {
				t.Fatal("pack constructor returned nil")
			}
			input := tc.input
			if input == "" {
				input = `{}`
			}
			ec := &packs.ExecutionContext{
				Pack:  tc.pack,
				Input: json.RawMessage(input),
			}
			if tc.withMemory {
				ec.Memory = stubMemoryInterface{}
			}
			_, err := tc.pack.Handler(context.Background(), ec)
			if err == nil {
				t.Fatalf("%s accepted empty input %q without error; expected typed *PackError", tc.name, input)
			}
			var perr *packs.PackError
			if !errors.As(err, &perr) {
				t.Fatalf("%s returned a raw error, not *PackError: %v\n"+
					"every handler MUST return *PackError so the engine's typed-error contract holds",
					tc.name, err)
			}
			if !packs.IsValidCode(perr.Code) {
				t.Errorf("%s returned PackError with code %q which is NOT in the closed set; "+
					"add it to validCodes in internal/packs/classify.go or change the handler",
					tc.name, perr.Code)
			}
		})
	}
}
