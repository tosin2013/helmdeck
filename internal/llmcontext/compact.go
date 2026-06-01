// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package llmcontext

import (
	"encoding/json"
	"fmt"
)

// Trim names what CompactCatalog stripped to fit the budget. Returned
// alongside the trimmed catalog so handlers can log the trim record
// when the reduction was significant. BeforeBytes and AfterBytes are
// the marshaled JSON sizes (the same heuristic the byte-budget loop
// uses). Dropped lists the field labels in the order they were
// removed.
type Trim struct {
	BeforeBytes int      `json:"before_bytes"`
	AfterBytes  int      `json:"after_bytes"`
	Dropped     []string `json:"dropped,omitempty"`
}

// CompactCatalog strips metadata fields from the catalog projection
// in a deterministic priority order until the marshaled JSON fits
// within budget.MaxCatalogBytes. When MaxCatalogBytes is 0 (Tier A
// frontier models), the catalog passes through unchanged.
//
// Trim priority — applied progressively until size constraint met:
//
//  1. Pack metadata.intent_keywords[]
//  2. Pack metadata.typical_use
//  3. Pack metadata.limitations[]
//  4. Pipeline metadata.steps[] bodies (replaced with step count)
//  5. Pipeline metadata input/output schemas (replaced with field names)
//  6. Description truncation to first sentence
//
// Pipeline metadata.supersedes is NEVER trimmed — it anchors plan's
// rule P2 (pipeline supersedes packs the user named by hand). Same
// for pack/pipeline name and id (those are dispatch identifiers).
//
// If after all six passes the catalog is still over budget, returns
// the most-compacted form anyway with a "still_over_budget" entry in
// Trim.Dropped. The caller should log and continue — over-budget
// catalog is better than no catalog, and the LLM may still produce a
// usable plan from the slimmed entries.
func CompactCatalog(full RoutingGuide, budget Budget) (RoutingGuide, Trim) {
	trim := Trim{}
	// Snapshot the before-size up front so the report is honest
	// regardless of what we modify in place.
	if b, err := json.Marshal(full); err == nil {
		trim.BeforeBytes = len(b)
	}
	if budget.MaxCatalogBytes <= 0 {
		// No compaction. Tier A skips the loop entirely.
		trim.AfterBytes = trim.BeforeBytes
		return full, trim
	}

	out := deepCopy(full)

	// Step 1: drop pack intent_keywords[].
	if !fits(out, budget.MaxCatalogBytes) {
		for i := range out.Packs {
			out.Packs[i].Metadata.IntentKeywords = nil
		}
		trim.Dropped = append(trim.Dropped, "pack.intent_keywords[]")
	}
	// Step 2: drop pack typical_use.
	if !fits(out, budget.MaxCatalogBytes) {
		for i := range out.Packs {
			out.Packs[i].Metadata.TypicalUse = ""
		}
		trim.Dropped = append(trim.Dropped, "pack.typical_use")
	}
	// Step 3: drop pack limitations[].
	if !fits(out, budget.MaxCatalogBytes) {
		for i := range out.Packs {
			out.Packs[i].Metadata.Limitations = nil
		}
		trim.Dropped = append(trim.Dropped, "pack.limitations[]")
	}
	// Step 4: slim pipeline metadata.steps[] bodies. supersedes,
	// accepts, produces, intent_keywords stay because they drive the
	// pipeline-aware rules in plan's system prompt.
	if !fits(out, budget.MaxCatalogBytes) {
		for i := range out.Pipelines {
			if v, changed := slimPipelineSteps(out.Pipelines[i].Metadata); changed {
				out.Pipelines[i].Metadata = v
			}
		}
		trim.Dropped = append(trim.Dropped, "pipeline.steps[].body")
	}
	// Step 5: replace pipeline input/output schemas with field-name
	// lists.
	if !fits(out, budget.MaxCatalogBytes) {
		for i := range out.Pipelines {
			if v, changed := slimPipelineSchemas(out.Pipelines[i].Metadata); changed {
				out.Pipelines[i].Metadata = v
			}
		}
		trim.Dropped = append(trim.Dropped, "pipeline.inputs/outputs.schema")
	}
	// Step 6: truncate descriptions to the first sentence. Last
	// resort — we'd rather lose secondary metadata than narrative
	// text, but if 1-5 still didn't fit, descriptions are next.
	if !fits(out, budget.MaxCatalogBytes) {
		for i := range out.Packs {
			out.Packs[i].Description = firstSentence(out.Packs[i].Description)
		}
		for i := range out.Pipelines {
			out.Pipelines[i].Description = firstSentence(out.Pipelines[i].Description)
		}
		trim.Dropped = append(trim.Dropped, "description.firstSentence")
	}

	if b, err := json.Marshal(out); err == nil {
		trim.AfterBytes = len(b)
		if trim.AfterBytes > budget.MaxCatalogBytes {
			trim.Dropped = append(trim.Dropped, fmt.Sprintf("still_over_budget(%d>%d)", trim.AfterBytes, budget.MaxCatalogBytes))
		}
	}
	return out, trim
}

// fits returns true if the marshaled catalog is within the byte cap.
// On marshal error we conservatively say it doesn't fit so the loop
// keeps trimming — the marshal will succeed once smaller.
func fits(rg RoutingGuide, cap int) bool {
	b, err := json.Marshal(rg)
	if err != nil {
		return false
	}
	return len(b) <= cap
}

// deepCopy returns a copy of rg safe to mutate without affecting the
// caller's reference. Pack metadata is a value type so a slice copy
// is enough; pipeline metadata is json.RawMessage which is just []byte,
// so we copy the underlying bytes too.
func deepCopy(rg RoutingGuide) RoutingGuide {
	out := RoutingGuide{
		Packs:     make([]Pack, len(rg.Packs)),
		Pipelines: make([]Pipeline, len(rg.Pipelines)),
	}
	for i, p := range rg.Packs {
		clone := p
		// Slice fields inside PackMetadata are reference types; we
		// reset them to nil after the first trim step assigns nil,
		// so an independent backing array isn't strictly necessary —
		// but cloning here keeps the deepCopy contract honest if a
		// future caller passes a shared RoutingGuide into multiple
		// CompactCatalog calls concurrently.
		clone.Metadata.IntentKeywords = append([]string(nil), p.Metadata.IntentKeywords...)
		clone.Metadata.Limitations = append([]string(nil), p.Metadata.Limitations...)
		clone.Metadata.Accepts = append([]string(nil), p.Metadata.Accepts...)
		clone.Metadata.Produces = append([]string(nil), p.Metadata.Produces...)
		out.Packs[i] = clone
	}
	for i, p := range rg.Pipelines {
		clone := p
		if len(p.Metadata) > 0 {
			clone.Metadata = append(json.RawMessage(nil), p.Metadata...)
		}
		out.Pipelines[i] = clone
	}
	return out
}

// slimPipelineSteps walks pipeline metadata, finds steps[] under
// "steps" or "metadata.steps", and replaces each step body with
// {"id": ..., "name": ...} only. Returns the new RawMessage and
// whether anything actually changed (so the caller can skip
// re-marshaling pipelines that had no steps to trim).
func slimPipelineSteps(meta json.RawMessage) (json.RawMessage, bool) {
	if len(meta) == 0 {
		return meta, false
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(meta, &obj); err != nil {
		return meta, false
	}
	steps, ok := obj["steps"].([]interface{})
	if !ok || len(steps) == 0 {
		return meta, false
	}
	for i, s := range steps {
		stepObj, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		// Keep only id and name; drop input_template, output_select,
		// when, retry, body, anything else.
		slim := map[string]interface{}{}
		if v, ok := stepObj["id"]; ok {
			slim["id"] = v
		}
		if v, ok := stepObj["name"]; ok {
			slim["name"] = v
		}
		if v, ok := stepObj["pack"]; ok {
			// pack name is essential for the model to understand
			// what each step does; cheap to keep.
			slim["pack"] = v
		}
		steps[i] = slim
	}
	obj["steps"] = steps
	out, err := json.Marshal(obj)
	if err != nil {
		return meta, false
	}
	return out, true
}

// slimPipelineSchemas replaces pipeline inputs/outputs schema bodies
// with field-name lists. The model only needs to know WHICH fields
// the pipeline accepts/produces; the per-field type/required/desc
// detail can come from a follow-up call to the full schema endpoint
// if the agent ends up dispatching the pipeline.
func slimPipelineSchemas(meta json.RawMessage) (json.RawMessage, bool) {
	if len(meta) == 0 {
		return meta, false
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(meta, &obj); err != nil {
		return meta, false
	}
	changed := false
	for _, field := range []string{"inputs", "outputs"} {
		raw, ok := obj[field]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case []interface{}:
			// Schema is a list of field objects: [{name, type, required, ...}, ...]
			names := make([]string, 0, len(v))
			for _, item := range v {
				if m, ok := item.(map[string]interface{}); ok {
					if n, ok := m["name"].(string); ok {
						names = append(names, n)
					}
				}
			}
			obj[field] = names
			changed = true
		case map[string]interface{}:
			// Schema is a {fieldName: schema, ...} map. Replace with
			// a list of keys.
			names := make([]string, 0, len(v))
			for k := range v {
				names = append(names, k)
			}
			obj[field] = names
			changed = true
		}
	}
	if !changed {
		return meta, false
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return meta, false
	}
	return out, true
}

// firstSentence keeps everything up to and including the first
// terminal period, question mark, or exclamation point. If no
// terminal mark is found, returns the original string unchanged
// (don't aggressively crop something that wasn't multi-sentence to
// begin with).
func firstSentence(s string) string {
	for i, r := range s {
		if r == '.' || r == '?' || r == '!' {
			// Include the terminator. Look one char ahead for a
			// space or end-of-string to confirm this is a sentence
			// boundary and not an ellipsis or version number.
			next := i + 1
			if next >= len(s) {
				return s[:next]
			}
			if s[next] == ' ' || s[next] == '\n' {
				return s[:next]
			}
		}
	}
	return s
}
