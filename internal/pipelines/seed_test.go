// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import "testing"

// TestBuiltins_Valid asserts every starter is structurally sound — unique
// step IDs, refs point to earlier steps, valid JSON inputs — so a typo in
// seed.go fails CI rather than a deployment.
func TestBuiltins_Valid(t *testing.T) {
	b := Builtins()
	if len(b) != 15 {
		t.Errorf("expected 15 starter pipelines, got %d", len(b))
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
	for _, want := range []string{"builtin.grounded-deck", "builtin.grounded-blog", "builtin.repo-presentation", "builtin.prompt-video", "builtin.prompt-narrated-video"} {
		if !ids[want] {
			t.Errorf("missing expected starter %q", want)
		}
	}
}
