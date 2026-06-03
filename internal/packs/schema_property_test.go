// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package packs

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Property-based tests for BasicSchema.Validate (PR D of the v0.24.0
// reliability arc).
//
// Why this matters: every pack declares an OutputSchema that the engine
// validates at Execute time. A schema/validator drift is the bug class
// that shipped in v0.17.1 (slides.narrate emitted tts_chars as an
// object, schema declared number, every unit test passed, prod runs
// failed). PR B's schema-contract tests pin specific pack/handler
// pairs; these properties pin the validator's contract independently —
// so a regression in scalarKind's first-byte discrimination or the
// required-field check fails loudly even when no pack test fires.

// genJSONScalarOf produces a JSON value of the given declared scalar
// kind. Used to seed the "conforming output passes validation"
// property.
func genJSONScalarOf(kind string) *rapid.Generator[json.RawMessage] {
	switch kind {
	case "string":
		return rapid.Map(rapid.String(),
			func(s string) json.RawMessage {
				b, _ := json.Marshal(s)
				return b
			})
	case "number":
		return rapid.Map(rapid.Float64(),
			func(f float64) json.RawMessage {
				// Reject NaN/Inf — they are not valid JSON.
				if f != f || f > 1e308 || f < -1e308 {
					return json.RawMessage("0")
				}
				b, _ := json.Marshal(f)
				return b
			})
	case "boolean":
		return rapid.Map(rapid.Bool(),
			func(b bool) json.RawMessage {
				if b {
					return json.RawMessage("true")
				}
				return json.RawMessage("false")
			})
	case "object":
		return rapid.Just(json.RawMessage(`{"k":"v"}`))
	case "array":
		return rapid.Just(json.RawMessage(`[]`))
	}
	return rapid.Just(json.RawMessage(`null`))
}

// TestProperty_ConformingOutputValidates — for any schema and any
// output that satisfies its required+typed contract, Validate must
// return nil. The "no false negatives" guard: a regression in
// scalarKind or the required-field loop would fail a conforming
// output and break every pack's happy path.
func TestProperty_ConformingOutputValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		kinds := []string{"string", "number", "boolean", "object", "array"}
		numFields := rapid.IntRange(1, 4).Draw(t, "numFields")

		required := make([]string, 0, numFields)
		properties := make(map[string]string, numFields)
		obj := make(map[string]json.RawMessage, numFields)

		usedNames := make(map[string]bool, numFields)
		for i := 0; i < numFields; i++ {
			var name string
			for {
				name = rapid.StringMatching(`[a-z][a-z0-9_]{0,7}`).Draw(t, fmt.Sprintf("field_%d", i))
				if !usedNames[name] {
					break
				}
			}
			usedNames[name] = true
			kind := rapid.SampledFrom(kinds).Draw(t, fmt.Sprintf("kind_%d", i))
			required = append(required, name)
			properties[name] = kind
			obj[name] = genJSONScalarOf(kind).Draw(t, fmt.Sprintf("value_%d", i))
		}

		schema := BasicSchema{Required: required, Properties: properties}
		body, _ := json.Marshal(obj)
		if err := schema.Validate(body); err != nil {
			t.Fatalf("conforming output rejected: %v\nschema=%+v\nbody=%s", err, schema, body)
		}
	})
}

// TestProperty_MissingRequiredAlwaysRejects — for any schema with a
// required field and any output missing that field, Validate must
// reject with a message that names the missing field. The LLM's
// recovery key is the field name — without it, the model can't
// correct its output.
func TestProperty_MissingRequiredAlwaysRejects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		missing := rapid.StringMatching(`[a-z][a-z0-9_]{0,7}`).Draw(t, "missing")
		// Build an output that has SOME fields but not the required one.
		other := rapid.StringMatching(`[a-z][a-z0-9_]{0,7}`).Draw(t, "other")
		if other == missing {
			t.Skip("collision")
		}
		schema := BasicSchema{
			Required:   []string{missing},
			Properties: map[string]string{missing: "string"},
		}
		body := json.RawMessage(fmt.Sprintf(`{%q:"present"}`, other))
		err := schema.Validate(body)
		if err == nil {
			t.Fatalf("missing required %q accepted in %s", missing, body)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error %q does not name missing field %q", err, missing)
		}
		if !strings.Contains(err.Error(), "missing required") {
			t.Errorf("error %q should say 'missing required'", err)
		}
	})
}

// TestProperty_TypeMismatchAlwaysRejects — for any property declared
// as type X and a value provided as type Y (Y ≠ X), Validate must
// reject. This is the v0.17.1 regression class: a schema declares
// `tts_chars: number`, handler emits an object. Without this property,
// a regression in scalarKind's discrimination would silently let the
// object through and the LLM's downstream processing breaks.
func TestProperty_TypeMismatchAlwaysRejects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		kinds := []string{"string", "number", "boolean", "object", "array"}
		declared := rapid.SampledFrom(kinds).Draw(t, "declared")
		// Pick an actual that's NOT declared.
		actualKinds := make([]string, 0, len(kinds)-1)
		for _, k := range kinds {
			if k != declared {
				actualKinds = append(actualKinds, k)
			}
		}
		actual := rapid.SampledFrom(actualKinds).Draw(t, "actual")

		field := "f"
		schema := BasicSchema{
			Required:   []string{field},
			Properties: map[string]string{field: declared},
		}
		value := genJSONScalarOf(actual).Draw(t, "value")
		body := json.RawMessage(fmt.Sprintf(`{"%s":%s}`, field, value))

		err := schema.Validate(body)
		if err == nil {
			t.Fatalf("declared=%s actual=%s accepted: %s", declared, actual, body)
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not name field %q", err, field)
		}
		if !strings.Contains(err.Error(), "expected") {
			t.Errorf("error %q should say 'expected ... got ...'", err)
		}
	})
}

// TestProperty_NonObjectInputAlwaysRejects — Validate's top-level
// promise is that input is a JSON object. Arrays, strings, numbers,
// booleans, null at the top level must all reject. The error must
// say "JSON object" so the LLM knows what shape to emit.
func TestProperty_NonObjectInputAlwaysRejects(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nonObject := rapid.SampledFrom([]string{
			`"plain string"`,
			`42`,
			`true`,
			`false`,
			`null`,
			`[]`,
			`[1,2,3]`,
		}).Draw(t, "input")
		schema := BasicSchema{}
		err := schema.Validate(json.RawMessage(nonObject))
		if err == nil {
			t.Fatalf("non-object %q accepted at top level", nonObject)
		}
		if !strings.Contains(err.Error(), "JSON object") {
			t.Errorf("error %q should say 'JSON object'", err)
		}
	})
}
