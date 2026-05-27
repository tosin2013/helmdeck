// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// Tier-1 hermetic pipeline tests: run every built-in pipeline through the
// REAL runner and a REAL packs.Engine (wired to an in-memory artifact
// store), but with STUB pack handlers so no sidecar/network/LLM is
// touched. This guards two classes of bug the single-pack tests can't:
//
//  1. Wiring/threading regressions — a seed step referencing an output
//     field a prior step doesn't produce (the grounded_text RefError class
//     that broke builtin.grounded-deck). Such a ref fails RunSync, so the
//     matching subtest goes red.
//  2. Artifact propagation — the engine collects what a handler wrote and
//     the runner now records it on the RunStep; we assert the final step's
//     artifact lands in both the run record and the store.
//
// The stub OUTPUTS mirror each pack's real output contract for the fields
// the builtins consume (synthesis / grounded_text / markdown /
// readme.content). Keep them in sync with the packs' OutputSchema; Tier 2
// (scripts/pipelines-smoke.sh) covers real-output drift against a live stack.

// stubSpec is a stub pack's canned behaviour. When artifact is true the
// handler writes one artifact (so the engine surfaces it in
// Result.Artifacts) and substitutes its key for the %KEY% placeholder in
// output.
type stubSpec struct {
	output      string
	artifact    bool
	artName     string
	contentType string
	content     []byte
}

// builtinPackStubs covers every pack referenced as a step by Builtins().
// A new built-in pipeline that adds an un-stubbed pack fails loudly in the
// resolver below (telling the author to add a stub here).
var builtinPackStubs = map[string]stubSpec{
	// Producers of text consumed by downstream steps.
	"research.deep":  {output: `{"synthesis":"# Synthesis\n\n---\n\n## Section two"}`},
	"content.ground": {output: `{"grounded_text":"# Grounded [source](https://example.com)\n\n---\n\n## Two"}`},
	"web.scrape":     {output: `{"markdown":"# Scraped\n\n---\n\n## Two"}`},
	"doc.parse":      {output: `{"markdown":"# Parsed\n\n---\n\n## Two"}`},
	"repo.fetch":     {output: `{"readme":{"content":"# README\n\n---\n\n## Two"},"clone_path":"/repos/x"}`},
	// Artifact producers (the terminal step of every built-in pipeline).
	"slides.render":      {output: `{"format":"pdf","artifact_key":"%KEY%","size":1024}`, artifact: true, artName: "deck.pdf", contentType: "application/pdf", content: []byte("%PDF-1.4 stub")},
	"slides.narrate":     {output: `{"video_artifact_key":"%KEY%","total_duration_s":5}`, artifact: true, artName: "deck.mp4", contentType: "video/mp4", content: []byte("\x00\x00\x00\x18ftypmp42 stub")},
	"podcast.generate":   {output: `{"audio_url":"https://example.com/a.mp3","artifact_key":"%KEY%"}`, artifact: true, artName: "podcast.mp3", contentType: "audio/mpeg", content: []byte("ID3 stub")},
	"blog.publish":       {output: `{"artifact_key":"%KEY%","format":"markdown"}`, artifact: true, artName: "post.md", contentType: "text/markdown", content: []byte("# Post")},
	"hyperframes.render": {output: `{"artifact_key":"%KEY%","format":"mp4"}`, artifact: true, artName: "video.mp4", contentType: "video/mp4", content: []byte("\x00\x00\x00\x18ftypmp42 stub")},
}

// stubHandler builds a handler that emits the spec's output and, when the
// spec produces an artifact, writes it through the engine-supplied store
// so Result.Artifacts is populated the real way.
func stubHandler(name string, spec stubSpec) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		out := spec.output
		if spec.artifact && ec.Artifacts != nil {
			art, err := ec.Artifacts.Put(ctx, name, spec.artName, spec.content, spec.contentType)
			if err != nil {
				return nil, err
			}
			out = strings.ReplaceAll(out, "%KEY%", art.Key)
		}
		return json.RawMessage(out), nil
	}
}

// stubRunner wires the real runner + real engine + in-memory artifact store
// to the stub packs. Returns the runner and the store for assertions.
func stubRunner(t *testing.T) (*Runner, *packs.MemoryArtifactStore) {
	t.Helper()
	stubs := make(map[string]*packs.Pack, len(builtinPackStubs))
	for name, spec := range builtinPackStubs {
		stubs[name] = &packs.Pack{Name: name, Version: "v1", Handler: stubHandler(name, spec)}
	}
	resolve := func(name, _ string) (*packs.Pack, error) {
		p, ok := stubs[name]
		if !ok {
			return nil, fmt.Errorf("no stub for pack %q — add it to builtinPackStubs", name)
		}
		return p, nil
	}
	mem := packs.NewMemoryArtifactStore()
	eng := packs.New(packs.WithArtifactStore(mem))
	return NewRunner(testStore(t), resolve, eng, nil), mem
}

// builtinRunInputs is a superset of every ${{ inputs.* }} the builtins
// reference, so any single pipeline's inputs resolve.
const builtinRunInputs = `{
  "markdown": "# Slide one\n\n---\n\n## Slide two\n\n---\n\n## Slide three",
  "query": "kubernetes operators",
  "url": "https://example.com",
  "repo_url": "https://github.com/example/repo",
  "source_url": "https://example.com/whitepaper.pdf",
  "title": "My Title",
  "composition_html": "<html><body>hi</body></html>"
}`

// TestBuiltins_RunEndToEnd runs every built-in pipeline through the runner
// with stub packs and asserts it succeeds, every step succeeds, and the
// terminal step's artifact is captured in the run record and the store.
func TestBuiltins_RunEndToEnd(t *testing.T) {
	for _, p := range Builtins() {
		p := p
		t.Run(p.ID, func(t *testing.T) {
			runner, mem := stubRunner(t)
			ctx := context.Background()
			run := &Run{ID: "run-" + p.ID, PipelineID: p.ID, StartedAt: runner.now()}
			if err := runner.store.CreateRun(ctx, run); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			if err := runner.RunSync(ctx, p, json.RawMessage(builtinRunInputs), run); err != nil {
				t.Fatalf("RunSync setup error: %v", err)
			}
			if run.Status != RunSucceeded {
				// A bad ${{ steps.X.output.field }} ref in seed.go lands
				// here (RunFailed with an "unresolved reference" error).
				t.Fatalf("pipeline %s: status=%s err=%s", p.ID, run.Status, run.Error)
			}
			if len(run.Steps) != len(p.Steps) {
				t.Fatalf("ran %d/%d steps", len(run.Steps), len(p.Steps))
			}
			for _, s := range run.Steps {
				if s.Status != RunSucceeded {
					t.Errorf("step %s (%s): status=%s err=%s", s.StepID, s.Pack, s.Status, s.Error)
				}
			}
			// Every built-in pipeline ends in an artifact-producing pack.
			last := run.Steps[len(run.Steps)-1]
			if len(last.Artifacts) == 0 {
				t.Fatalf("terminal step %s (%s) recorded no artifacts (Part A capture broken?)", last.StepID, last.Pack)
			}
			if _, _, err := mem.Get(ctx, last.Artifacts[0].Key); err != nil {
				t.Errorf("recorded artifact %q not retrievable from store: %v", last.Artifacts[0].Key, err)
			}
		})
	}
}

// TestRunner_BadOutputRefFails proves the wiring guard: a step that
// references an output field the prior step never produced makes the run
// fail (RunFailed) rather than passing a silent empty downstream. This is
// the regression that broke builtin.grounded-deck's zero-claims path.
func TestRunner_BadOutputRefFails(t *testing.T) {
	runner, _ := stubRunner(t)
	ctx := context.Background()
	p := &Pipeline{ID: "bad", Name: "bad", Steps: []Step{
		{ID: "research", Pack: "research.deep", Input: json.RawMessage(`{"query":"x"}`)},
		// research.deep produces "synthesis", not "nope".
		{ID: "render", Pack: "slides.render", Input: json.RawMessage(`{"markdown":"${{ steps.research.output.nope }}"}`)},
	}}
	run := &Run{ID: "run-bad", PipelineID: "bad", StartedAt: runner.now()}
	if err := runner.store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := runner.RunSync(ctx, p, nil, run); err != nil {
		t.Fatalf("RunSync setup error: %v", err)
	}
	if run.Status != RunFailed {
		t.Fatalf("expected RunFailed on bad output ref, got %s", run.Status)
	}
	if !strings.Contains(run.Error, "unresolved reference") {
		t.Errorf("expected unresolved-reference error, got %q", run.Error)
	}
}
