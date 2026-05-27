// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import "testing"

// Output-schema contract tests.
//
// slides.narrate and podcast.generate are exercised by unit tests that call
// pack.Handler directly (runNarrate / runPodcastGenerate). That path BYPASSES
// packs.Engine.Execute — the only place OutputSchema.Validate runs. So no test
// validated the handlers' real output against their declared schema, and the
// v0.17.1 cost-output declaration (`tts_chars: number`) shipped while the
// handlers emitted a per-X map[string]int. Real pipeline runs (which go
// through Execute) then failed with `invalid_output: field "tts_chars":
// expected number, got object`.
//
// These tests validate the real handler output against the declared
// OutputSchema — exactly what the engine enforces — closing the gap so an
// output/schema mismatch in these packs fails in CI instead of in production.

func TestSlidesNarrate_RealOutputMatchesSchema(t *testing.T) {
	schema := SlidesNarrate(nil, nil, nil).OutputSchema
	disp := &scriptedDispatcherWT{replies: []string{
		`{"title":"T","description":"d","tags":["x"],"category":"Education","language":"en"}`,
	}}
	raw, err := runNarrate(t, disp, nil, &narrateExecScript{},
		`{"markdown":"---\nmarp: true\n---\n\n# A\n\n<!-- some notes -->","metadata_model":"openai/gpt-4o-mini","allow_silent_output":true}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if verr := schema.Validate(raw); verr != nil {
		t.Errorf("slides.narrate real-run output violates its declared OutputSchema (Execute would reject it): %v\noutput: %s", verr, raw)
	}
}

func TestPodcastGenerate_RealOutputMatchesSchema(t *testing.T) {
	schema := PodcastGenerate(nil, nil, nil).OutputSchema
	v := vaultWithElevenKey(t, "")
	ex := &podcastTestExecutor{mp3Bytes: []byte("\xff\xfb\x90finalmp3goeshere")}
	raw, err := runPodcastGenerate(t, v, ex,
		`{"speakers":{"Alex":"v1","Jordan":"v2"},"script":[{"speaker":"Alex","text":"Hi."},{"speaker":"Jordan","text":"Hello."}],"theme":"deep-dive","allow_silent_output":true}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if verr := schema.Validate(raw); verr != nil {
		t.Errorf("podcast.generate real-run output violates its declared OutputSchema (Execute would reject it): %v\noutput: %s", verr, raw)
	}
}
