// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"encoding/json"
	"errors"
	"testing"
)

func steps(m map[string]string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = json.RawMessage(v)
	}
	return out
}

func TestResolve_WholeValuePreservesType(t *testing.T) {
	st := steps(map[string]string{
		"research": `{"synthesis":"# Title","count":42,"sources":[{"url":"u1"},{"url":"u2"}]}`,
	})
	// Whole-value string ref → number stays number.
	out, err := Resolve(json.RawMessage(`{"n":"${{ steps.research.output.count }}"}`), nil, st)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if f, ok := got["n"].(float64); !ok || f != 42 {
		t.Errorf("number ref should stay a number, got %T %v", got["n"], got["n"])
	}
	// Whole-value ref to an array → array preserved.
	out, _ = Resolve(json.RawMessage(`{"s":"${{ steps.research.output.sources }}"}`), nil, st)
	_ = json.Unmarshal(out, &got)
	if _, ok := got["s"].([]any); !ok {
		t.Errorf("array ref should stay an array, got %T", got["s"])
	}
}

func TestResolve_StringAndArrayIndexPath(t *testing.T) {
	st := steps(map[string]string{
		"research": `{"synthesis":"# Deck","sources":[{"markdown":"first"},{"markdown":"second"}]}`,
	})
	out, err := Resolve(json.RawMessage(`{"markdown":"${{steps.research.output.synthesis}}","second":"${{ steps.research.output.sources[1].markdown }}"}`), nil, st)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["markdown"] != "# Deck" {
		t.Errorf("markdown = %v", got["markdown"])
	}
	if got["second"] != "second" {
		t.Errorf("array-index path = %v", got["second"])
	}
}

func TestResolve_EmbeddedRefCoerced(t *testing.T) {
	out, err := Resolve(json.RawMessage(`{"title":"Report: ${{ inputs.topic }} (n=${{ inputs.n }})"}`),
		map[string]any{"topic": "K8s", "n": float64(3)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["title"] != "Report: K8s (n=3)" {
		t.Errorf("embedded coercion = %q", got["title"])
	}
}

func TestResolve_NestedTraversal(t *testing.T) {
	out, err := Resolve(json.RawMessage(`{"a":{"b":["${{ inputs.x }}",{"c":"${{ inputs.x }}"}]}}`),
		map[string]any{"x": "v"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	b := got["a"].(map[string]any)["b"].([]any)
	if b[0] != "v" || b[1].(map[string]any)["c"] != "v" {
		t.Errorf("nested traversal = %v", got)
	}
}

func TestResolve_MissingRefsError(t *testing.T) {
	st := steps(map[string]string{"a": `{"x":[1,2]}`})
	cases := []string{
		`{"v":"${{ steps.nope.output.x }}"}`,    // unknown step
		`{"v":"${{ steps.a.output.missing }}"}`, // unknown field
		`{"v":"${{ steps.a.output.x[9] }}"}`,    // index out of range
		`{"v":"${{ bogus.x }}"}`,                // unknown namespace
	}
	for _, c := range cases {
		_, err := Resolve(json.RawMessage(c), nil, st)
		var re *RefError
		if !errors.As(err, &re) {
			t.Errorf("%s: want *RefError, got %v", c, err)
		}
	}
}

func TestResolve_SinglePassNoSecondOrder(t *testing.T) {
	// A resolved value that itself looks like a ref must NOT be re-expanded.
	st := steps(map[string]string{"a": `{"evil":"${{ inputs.secret }}"}`})
	out, err := Resolve(json.RawMessage(`{"v":"${{ steps.a.output.evil }}"}`), map[string]any{"secret": "X"}, st)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["v"] != "${{ inputs.secret }}" {
		t.Errorf("second-order expansion happened: %q", got["v"])
	}
}

func TestResolve_NoRefsUnchanged(t *testing.T) {
	in := json.RawMessage(`{"format":"pdf","n":3,"ok":true}`)
	out, err := Resolve(in, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(in, &a)
	_ = json.Unmarshal(out, &b)
	if a["format"] != b["format"] || a["n"] != b["n"] || a["ok"] != b["ok"] {
		t.Errorf("literal input changed: %s", out)
	}
}

func TestValidate(t *testing.T) {
	exists := func(name, _ string) bool { return name == "research.deep" || name == "slides.render" }
	good := &Pipeline{Name: "ok", Steps: []Step{
		{ID: "r", Pack: "research.deep", Input: json.RawMessage(`{"query":"${{inputs.q}}"}`)},
		{ID: "s", Pack: "slides.render", Input: json.RawMessage(`{"markdown":"${{steps.r.output.synthesis}}"}`)},
	}}
	if err := Validate(good, exists); err != nil {
		t.Fatalf("good pipeline rejected: %v", err)
	}
	// Forward reference.
	fwd := &Pipeline{Name: "fwd", Steps: []Step{
		{ID: "s", Pack: "slides.render", Input: json.RawMessage(`{"markdown":"${{steps.r.output.synthesis}}"}`)},
		{ID: "r", Pack: "research.deep", Input: json.RawMessage(`{}`)},
	}}
	if Validate(fwd, exists) == nil {
		t.Error("forward reference should be rejected")
	}
	// Duplicate ID.
	dup := &Pipeline{Name: "dup", Steps: []Step{
		{ID: "r", Pack: "research.deep", Input: json.RawMessage(`{}`)},
		{ID: "r", Pack: "slides.render", Input: json.RawMessage(`{}`)},
	}}
	if Validate(dup, exists) == nil {
		t.Error("duplicate step id should be rejected")
	}
	// Unknown pack.
	unk := &Pipeline{Name: "unk", Steps: []Step{{ID: "x", Pack: "nope.pack", Input: json.RawMessage(`{}`)}}}
	if Validate(unk, exists) == nil {
		t.Error("unknown pack should be rejected")
	}
}

// TestResolve_MissingInput_WholeValueDropsField — when a top-level
// inputs.X reference is the WHOLE value of a JSON field, a missing
// input drops the field from the output JSON entirely (so the
// receiving pack uses its declared zero-value default). This is what
// closes the typed-input gap: typed bool/number fields receive their
// natural default instead of an empty string that fails type
// validation.
//
// TestResolve_MissingInput_EmbeddedKeepsField — embedded references
// in a surrounding string keep the field and substitute "" for the
// missing piece. Dropping the field would unhelpfully truncate the
// caller's literal context.
//
// Inter-step references (${{ steps.* }}) keep failing loud —
// covered by TestResolve_MissingStepStillFails below.
func TestResolve_MissingInput_WholeValueDropsField(t *testing.T) {
	out, err := Resolve(json.RawMessage(`{"a":"${{ inputs.missing }}","present":"keep","also":"${{ inputs.also_missing }}"}`),
		map[string]any{"present_input": "v"}, nil)
	if err != nil {
		t.Fatalf("missing input should not error; got: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, has := got["a"]; has {
		t.Errorf("whole-value missing ref should DROP the field; got %q", got["a"])
	}
	if _, has := got["also"]; has {
		t.Errorf("whole-value missing ref should DROP the field; got %q", got["also"])
	}
	if got["present"] != "keep" {
		t.Errorf("non-ref fields must pass through untouched; got %q", got["present"])
	}
}

func TestResolve_MissingInput_EmbeddedKeepsField(t *testing.T) {
	out, err := Resolve(json.RawMessage(`{"b":"prefix-${{ inputs.missing }}-suffix"}`),
		nil, nil)
	if err != nil {
		t.Fatalf("missing input should not error; got: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["b"] != "prefix--suffix" {
		t.Errorf("embedded missing ref should splice as empty (preserve surrounding text); got %q", got["b"])
	}
}

// TestResolve_MissingInput_DropAcrossTypes — the motivating case
// for the typed-field fix. helmdeck pipelines pass bool / number
// fields as whole-value templates ("export_outline":"${{ inputs.export_outline }}").
// PR #377 made these resolve to "" which slides.outline rejected as
// "expected boolean, got string". This test pins that the new
// behavior — drop the field entirely — lets the receiving pack use
// its declared zero-value for bool (false), number (0), and array
// ([]) targets without runtime intervention.
func TestResolve_MissingInput_DropAcrossTypes(t *testing.T) {
	out, err := Resolve(json.RawMessage(`{
		"export_outline":"${{ inputs.export_outline }}",
		"include_image_prompts":"${{ inputs.include_image_prompts }}",
		"fade_ms":"${{ inputs.fade_ms }}",
		"voice_ids":"${{ inputs.voice_ids }}"
	}`), map[string]any{}, nil)
	if err != nil {
		t.Fatalf("missing inputs should not error; got: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	for _, k := range []string{"export_outline", "include_image_prompts", "fade_ms", "voice_ids"} {
		if _, has := got[k]; has {
			t.Errorf("field %q should be dropped on whole-value miss; got %v", k, got[k])
		}
	}
}

// TestResolve_MissingInput_ArrayBecomesNull — arrays can't drop an
// element without re-indexing the surrounding positions. Substitute
// JSON null so caller-meaningful index positions stay stable.
func TestResolve_MissingInput_ArrayBecomesNull(t *testing.T) {
	out, err := Resolve(json.RawMessage(`{"tags":["fixed","${{ inputs.t1 }}","also fixed","${{ inputs.t2 }}"]}`),
		map[string]any{}, nil)
	if err != nil {
		t.Fatalf("missing inputs should not error; got: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	tags, ok := got["tags"].([]any)
	if !ok || len(tags) != 4 {
		t.Fatalf("expected 4-element array; got %v", got["tags"])
	}
	if tags[0] != "fixed" || tags[2] != "also fixed" {
		t.Errorf("fixed entries should pass through: %v", tags)
	}
	if tags[1] != nil || tags[3] != nil {
		t.Errorf("missing-ref entries should resolve to JSON null: %v", tags)
	}
}

// TestResolve_MissingStepStillFails — the safety net for inter-step
// wiring bugs. A missing step output indicates a typo in the pipeline
// definition or a producer/consumer mismatch and MUST surface loud.
// (Contrast with TestResolve_MissingInputDefaultsToEmpty.)
func TestResolve_MissingStepStillFails(t *testing.T) {
	_, err := Resolve(json.RawMessage(`{"a":"${{ steps.nonexistent.output.x }}"}`),
		nil, steps(map[string]string{"other": `{"x":"v"}`}))
	if err == nil {
		t.Fatal("missing step reference should fail loud, not default to empty")
	}
	var rerr *RefError
	if !errors.As(err, &rerr) {
		t.Errorf("expected *RefError; got %T", err)
	}
}
