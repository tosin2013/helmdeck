// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"strings"
	"testing"
)

// TestBuiltins_Valid asserts every starter is structurally sound — unique
// step IDs, refs point to earlier steps, valid JSON inputs — so a typo in
// seed.go fails CI rather than a deployment.
func TestBuiltins_Valid(t *testing.T) {
	b := Builtins()
	if len(b) != 21 {
		t.Errorf("expected 21 starter pipelines, got %d", len(b))
	}
	anyPack := func(_, _ string) bool { return true }
	ids := map[string]bool{}
	for _, p := range b {
		if ids[p.ID] {
			t.Errorf("duplicate builtin id %q", p.ID)
		}
		ids[p.ID] = true
		if !p.Builtin {
			t.Errorf("%s: Builtin flag must be true", p.ID)
		}
		if err := Validate(p, anyPack); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
	}
	// The two the user explicitly asked for must exist.
	for _, want := range []string{"builtin.grounded-deck", "builtin.brief-rewrite-blog", "builtin.repo-presentation", "builtin.prompt-video", "builtin.prompt-narrated-video"} {
		if !ids[want] {
			t.Errorf("missing expected starter %q", want)
		}
	}
}

// TestNarratePipelines_DoNotHardcodeAllowSilentOutput pins the
// design decision from PR #381: production *-narrate pipelines must
// NOT pass allow_silent_output:true literally — that bypasses the
// slides.narrate credential precheck and silently produces a video
// without audio when the ElevenLabs key is missing/rejected. Callers
// who genuinely want silence opt in by passing
// allow_silent_output:true on the pipeline run input, which threads
// through "${{ inputs.allow_silent_output }}".
//
// A regression here would re-introduce the exact failure mode the
// helmdeck-debug skill caught: "narrated" pipelines emitting silent
// video with has_narration=true.
func TestNarratePipelines_DoNotHardcodeAllowSilentOutput(t *testing.T) {
	mustNotHardcode := []string{
		"builtin.grounded-narrate",
		"builtin.research-narrate",
		"builtin.repo-presentation",
	}
	want := map[string]bool{}
	for _, id := range mustNotHardcode {
		want[id] = true
	}
	for _, p := range Builtins() {
		if !want[p.ID] {
			continue
		}
		for _, s := range p.Steps {
			if s.Pack != "slides.narrate" {
				continue
			}
			body := string(s.Input)
			// The literal hardcode that regressions would
			// reintroduce. The legitimate threaded form
			// ("allow_silent_output":"${{ inputs.allow_silent_output }}")
			// does NOT match this substring.
			if contains(body, `"allow_silent_output":true`) {
				t.Errorf("%s.narrate hardcodes allow_silent_output:true — must thread from inputs so missing ElevenLabs credentials fail fast. Step input: %s",
					p.ID, body)
			}
		}
	}
}

// TestVideoPipelines_DoNotHardcodeAspectRatio pins the design
// decision from PR #388: the public HyperFrames-backed pipelines
// (builtin.html-video, builtin.prompt-video, builtin.prompt-narrated-video)
// must thread aspect_ratio + resolution from caller inputs instead
// of hardcoding "16:9" + "1080p".
//
// The regression case was a real failure: a caller passed an HTML
// composition whose intrinsic dimensions were vertical (1080×1920
// for Shorts/TikTok), and hyperframes.render rejected with
// "outputResolution landscape does not match the composition" —
// the caller had no surface area to fix the mismatch.
func TestVideoPipelines_DoNotHardcodeAspectRatio(t *testing.T) {
	mustNotHardcode := map[string]bool{
		"builtin.html-video":            true,
		"builtin.prompt-video":          true,
		"builtin.prompt-narrated-video": true,
	}
	for _, p := range Builtins() {
		if !mustNotHardcode[p.ID] {
			continue
		}
		for _, s := range p.Steps {
			if s.Pack != "hyperframes.render" && s.Pack != "hyperframes.compose" {
				continue
			}
			body := string(s.Input)
			// Hardcoded "aspect_ratio":"16:9" is the regression
			// signature. The legitimate threaded form
			// "${{ inputs.aspect_ratio }}" does NOT match.
			if strings.Contains(body, `"aspect_ratio":"16:9"`) {
				t.Errorf("%s step %q hardcodes aspect_ratio:16:9 — must thread from inputs. Step input: %s",
					p.ID, s.ID, body)
			}
			if strings.Contains(body, `"resolution":"1080p"`) {
				t.Errorf("%s step %q hardcodes resolution:1080p — must thread from inputs. Step input: %s",
					p.ID, s.ID, body)
			}
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
