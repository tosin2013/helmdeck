// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Property-based tests for Validate (PR D of the v0.24.0 reliability
// arc). Example-based tests pin specific shapes; property tests pin
// the INVARIANT the function promises across a generated space. Two
// invariants matter for the LLM-driven environment story:
//
//   - Well-formed pipelines (unique step IDs, every pack non-empty,
//     every step ref pointing at an earlier step) must ALWAYS validate.
//   - Malformed pipelines (in specific ways the validator promises to
//     catch) must ALWAYS reject — and the rejection message must name
//     the offending element so the LLM's recovery has a target.
//
// Coverage is necessary but not sufficient: a Validate that accepts
// everything would hit 100% line coverage on every existing example
// test while silently breaking the LLM's pipeline-authoring loop. The
// properties below guard against that drift.

// genStepID is a generator for a non-empty step id. Avoids '$', '{',
// '}' so the generated id can't be confused with a step-ref token
// — the property test's well-formed shapes use these as plain literals.
var genStepID = rapid.StringMatching(`[a-z][a-z0-9_-]{0,15}`)

// genPackName is a non-empty pack identifier; dots are allowed (e.g.
// "slides.render") so we exercise the real shape.
var genPackName = rapid.StringMatching(`[a-z][a-z0-9._]{1,30}`)

// genInputJSON produces minimal valid JSON for a step's input. Step
// inputs in production carry ${{ }} refs; the property-test focus
// here is structural — the ref-targeting properties have their own
// targeted generator below.
var genInputJSON = rapid.Just(json.RawMessage(`{}`))

// TestProperty_WellFormedPipelineValidates asserts: any pipeline with a
// non-empty name, ≥1 step, unique step IDs, non-empty pack names, and
// empty/no step refs must validate. This is the "no false negatives"
// guard — Validate must not reject a structurally-honest pipeline.
func TestProperty_WellFormedPipelineValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.StringMatching(`[A-Za-z][A-Za-z0-9 _-]{0,30}`).Draw(t, "name")
		numSteps := rapid.IntRange(1, 6).Draw(t, "numSteps")

		used := make(map[string]bool, numSteps)
		steps := make([]Step, 0, numSteps)
		for i := 0; i < numSteps; i++ {
			// Generate fresh, unique step IDs.
			var id string
			for {
				id = genStepID.Draw(t, fmt.Sprintf("stepID_%d", i))
				if !used[id] {
					break
				}
			}
			used[id] = true
			steps = append(steps, Step{
				ID:    id,
				Pack:  genPackName.Draw(t, fmt.Sprintf("pack_%d", i)),
				Input: genInputJSON.Draw(t, fmt.Sprintf("input_%d", i)),
			})
		}
		p := &Pipeline{Name: name, Steps: steps}

		if err := Validate(p, nil); err != nil {
			t.Fatalf("well-formed pipeline rejected: %v\npipeline=%+v", err, p)
		}
	})
}

// TestProperty_DuplicateStepIDsAlwaysReject asserts: for any pipeline
// with two or more steps sharing an ID, Validate must return an error
// whose message names "duplicate". The LLM's recovery key is the word
// "duplicate" — if it changes, every pipeline-authoring agent breaks.
func TestProperty_DuplicateStepIDsAlwaysReject(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate ≥2 steps where at least two share an ID.
		dupID := genStepID.Draw(t, "dupID")
		otherCount := rapid.IntRange(0, 4).Draw(t, "otherCount")

		steps := []Step{
			{ID: dupID, Pack: "p", Input: json.RawMessage(`{}`)},
			{ID: dupID, Pack: "p", Input: json.RawMessage(`{}`)},
		}
		for i := 0; i < otherCount; i++ {
			steps = append(steps, Step{
				ID:    fmt.Sprintf("filler-%d-%s", i, dupID),
				Pack:  "p",
				Input: json.RawMessage(`{}`),
			})
		}
		// Shuffle deterministically via rapid so the duplicate pair
		// can land anywhere in the sequence.
		idx1 := rapid.IntRange(0, len(steps)-1).Draw(t, "idx1")
		idx2 := rapid.IntRange(0, len(steps)-1).Draw(t, "idx2")
		steps[idx1], steps[idx2] = steps[idx2], steps[idx1]

		err := Validate(&Pipeline{Name: "n", Steps: steps}, nil)
		if err == nil {
			t.Fatalf("duplicate step IDs accepted; steps=%+v", steps)
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("error %q does not mention 'duplicate' — LLM recovery key broken", err)
		}
	})
}

// TestProperty_EmptyPackAlwaysRejects asserts: any step with an empty
// pack field causes the pipeline to reject. The error must name the
// step id so the recovery target is clear.
func TestProperty_EmptyPackAlwaysRejects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		stepID := genStepID.Draw(t, "stepID")
		err := Validate(&Pipeline{
			Name:  "n",
			Steps: []Step{{ID: stepID, Pack: "", Input: json.RawMessage(`{}`)}},
		}, nil)
		if err == nil {
			t.Fatalf("step with empty pack accepted; id=%q", stepID)
		}
		if !strings.Contains(err.Error(), stepID) {
			t.Errorf("error %q does not name the offending step %q", err, stepID)
		}
		if !strings.Contains(err.Error(), "pack is required") {
			t.Errorf("error %q should say 'pack is required'", err)
		}
	})
}

// TestProperty_ForwardStepRefAlwaysRejects asserts: a step referencing
// another step that comes LATER in the sequence must fail with a
// clear "not an earlier step" message. Forward refs are the
// structurally-unsound shape — a pipeline that depends on a future
// output can never resolve at runtime, and silently accepting it
// would surface as a runtime null-deref or empty-output bug instead
// of a validation failure.
func TestProperty_ForwardStepRefAlwaysRejects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		laterID := genStepID.Draw(t, "laterID")
		earlierID := genStepID.Draw(t, "earlierID")
		if laterID == earlierID {
			t.Skip("collision")
		}
		// Step 0 references step 1 (forward — invalid).
		steps := []Step{
			{ID: earlierID, Pack: "p",
				Input: json.RawMessage(fmt.Sprintf(`{"x":"${{ steps.%s.output.y }}"}`, laterID))},
			{ID: laterID, Pack: "p", Input: json.RawMessage(`{}`)},
		}
		err := Validate(&Pipeline{Name: "n", Steps: steps}, nil)
		if err == nil {
			t.Fatalf("forward step ref accepted; steps=%+v", steps)
		}
		if !strings.Contains(err.Error(), "not an earlier step") {
			t.Errorf("error %q should say 'not an earlier step'", err)
		}
	})
}

// TestProperty_BackwardStepRefAlwaysAccepts asserts the dual: a step
// referencing an EARLIER step is the documented happy path. The
// validator must let it through.
func TestProperty_BackwardStepRefAlwaysAccepts(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		earlierID := genStepID.Draw(t, "earlierID")
		laterID := genStepID.Draw(t, "laterID")
		if laterID == earlierID {
			t.Skip("collision")
		}
		steps := []Step{
			{ID: earlierID, Pack: "p", Input: json.RawMessage(`{}`)},
			{ID: laterID, Pack: "p",
				Input: json.RawMessage(fmt.Sprintf(`{"x":"${{ steps.%s.output.y }}"}`, earlierID))},
		}
		if err := Validate(&Pipeline{Name: "n", Steps: steps}, nil); err != nil {
			t.Fatalf("backward ref rejected: %v\nsteps=%+v", err, steps)
		}
	})
}

// TestProperty_PackExistsCallbackHonored — when a packExists callback
// is provided and it returns false for every pack, every pipeline
// fails (assuming it would otherwise validate). The callback is the
// production seam where the registry is consulted; a Validate that
// silently skips the callback would silently let an LLM author a
// pipeline referencing a pack that doesn't exist, surfacing as a
// runtime failure mid-run instead of an authoring-time rejection.
func TestProperty_PackExistsCallbackHonored(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		stepID := genStepID.Draw(t, "stepID")
		packName := genPackName.Draw(t, "packName")
		err := Validate(&Pipeline{
			Name:  "n",
			Steps: []Step{{ID: stepID, Pack: packName, Input: json.RawMessage(`{}`)}},
		}, func(string, string) bool { return false })
		if err == nil {
			t.Fatalf("Validate accepted with packExists=>false for pack %q", packName)
		}
		if !strings.Contains(err.Error(), "unknown pack") {
			t.Errorf("error %q should say 'unknown pack'", err)
		}
	})
}
