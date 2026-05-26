// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// refRe matches a single ${{ <expr> }} reference. The expr is captured
// with surrounding whitespace trimmed by the resolver.
var refRe = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

// maxResolveDepth bounds recursion into the decoded input tree so a
// pathological deeply-nested definition can't blow the stack.
const maxResolveDepth = 64

// RefError is returned when a ${{ ... }} reference can't be resolved.
// The runner turns it into a step failure rather than substituting an
// empty value — loud failure over silent drift.
type RefError struct {
	Ref    string
	Reason string
}

func (e *RefError) Error() string {
	return fmt.Sprintf("unresolved reference %q: %s", e.Ref, e.Reason)
}

// resolveCtx is the data a step's input is resolved against.
type resolveCtx struct {
	inputs map[string]any             // pipeline-run inputs
	steps  map[string]json.RawMessage // stepID -> that step's Result.Output
}

// Resolve walks a step's input JSON, replaces every ${{ ... }} reference
// with the corresponding value, and returns the resolved input. A string
// that is EXACTLY one reference takes the referent's native JSON type
// (number/array/object preserved); a reference embedded in surrounding
// text is string-coerced and spliced. Resolution is single-pass — a
// resolved value is never itself re-scanned for references — and the
// result is re-marshaled via encoding/json, so a resolved value can
// never break out of its JSON position (no injection).
func Resolve(input json.RawMessage, inputs map[string]any, steps map[string]json.RawMessage) (json.RawMessage, error) {
	if len(input) == 0 {
		return input, nil
	}
	var tree any
	if err := json.Unmarshal(input, &tree); err != nil {
		return nil, fmt.Errorf("step input is not valid JSON: %w", err)
	}
	rc := resolveCtx{inputs: inputs, steps: steps}
	out, err := rc.walk(tree, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// walk recursively resolves references in the decoded tree.
func (rc resolveCtx) walk(node any, depth int) (any, error) {
	if depth > maxResolveDepth {
		return nil, fmt.Errorf("pipeline input nested deeper than %d levels", maxResolveDepth)
	}
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			r, err := rc.walk(val, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			r, err := rc.walk(val, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case string:
		return rc.resolveString(v)
	default:
		return node, nil // numbers, bools, nil pass through
	}
}

// resolveString handles the two reference cases for a string node.
func (rc resolveCtx) resolveString(s string) (any, error) {
	loc := refRe.FindStringSubmatchIndex(s)
	if loc == nil {
		return s, nil // no reference
	}
	// Whole-value case: the trimmed string is exactly one reference.
	if strings.TrimSpace(s) == s[loc[0]:loc[1]] {
		expr := s[loc[2]:loc[3]]
		return rc.lookupExpr(expr)
	}
	// Embedded case: replace each reference with its string coercion.
	var outErr error
	res := refRe.ReplaceAllStringFunc(s, func(match string) string {
		m := refRe.FindStringSubmatch(match)
		val, err := rc.lookupExpr(m[1])
		if err != nil {
			outErr = err
			return ""
		}
		return coerceString(val)
	})
	if outErr != nil {
		return nil, outErr
	}
	return res, nil
}

// lookupExpr resolves a single reference expression to a value.
func (rc resolveCtx) lookupExpr(expr string) (any, error) {
	expr = strings.TrimSpace(expr)
	switch {
	case strings.HasPrefix(expr, "inputs."):
		path := strings.TrimPrefix(expr, "inputs.")
		v, err := lookupPath(rc.inputs, path)
		if err != nil {
			return nil, &RefError{Ref: expr, Reason: err.Error()}
		}
		return v, nil
	case strings.HasPrefix(expr, "steps."):
		// steps.<stepID>.output.<path>
		rest := strings.TrimPrefix(expr, "steps.")
		stepID, after, ok := cutFirst(rest, ".")
		if !ok {
			return nil, &RefError{Ref: expr, Reason: "expected steps.<id>.output.<path>"}
		}
		const outputPrefix = "output"
		if after != outputPrefix && !strings.HasPrefix(after, outputPrefix+".") {
			return nil, &RefError{Ref: expr, Reason: "step reference must address .output"}
		}
		raw, ok := rc.steps[stepID]
		if !ok {
			return nil, &RefError{Ref: expr, Reason: fmt.Sprintf("no prior step %q", stepID)}
		}
		var outTree any
		if err := json.Unmarshal(raw, &outTree); err != nil {
			return nil, &RefError{Ref: expr, Reason: "step output is not valid JSON"}
		}
		path := strings.TrimPrefix(strings.TrimPrefix(after, outputPrefix), ".")
		if path == "" {
			return outTree, nil // ${{ steps.x.output }} → whole output object
		}
		v, err := lookupPath(outTree, path)
		if err != nil {
			return nil, &RefError{Ref: expr, Reason: err.Error()}
		}
		return v, nil
	default:
		return nil, &RefError{Ref: expr, Reason: "unknown reference namespace (want inputs.* or steps.*)"}
	}
}

// lookupPath walks a dot-separated path with optional [n] array indices
// through a decoded JSON value, e.g. "sources[0].markdown".
func lookupPath(root any, path string) (any, error) {
	cur := root
	for _, seg := range strings.Split(path, ".") {
		key, idxs := parseSegment(seg)
		if key != "" {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("cannot index field %q on non-object", key)
			}
			val, ok := m[key]
			if !ok {
				return nil, fmt.Errorf("no field %q", key)
			}
			cur = val
		}
		for _, idx := range idxs {
			arr, ok := cur.([]any)
			if !ok {
				return nil, fmt.Errorf("cannot index [%d] on non-array", idx)
			}
			if idx < 0 || idx >= len(arr) {
				return nil, fmt.Errorf("index [%d] out of range (len %d)", idx, len(arr))
			}
			cur = arr[idx]
		}
	}
	return cur, nil
}

var idxRe = regexp.MustCompile(`\[(\d+)\]`)

// parseSegment splits "name[0][1]" into the field name and its indices.
func parseSegment(seg string) (string, []int) {
	name := seg
	if i := strings.IndexByte(seg, '['); i >= 0 {
		name = seg[:i]
	}
	var idxs []int
	for _, m := range idxRe.FindAllStringSubmatch(seg, -1) {
		n, _ := strconv.Atoi(m[1])
		idxs = append(idxs, n)
	}
	return name, idxs
}

// coerceString renders a resolved value for embedding inside a larger
// string. Strings pass through verbatim; everything else is JSON-encoded
// (numbers without quotes, objects/arrays as compact JSON).
func coerceString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// cutFirst is strings.Cut on the first occurrence of sep.
func cutFirst(s, sep string) (before, after string, found bool) {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}

// extractStepRefs returns the set of step IDs referenced by a step's
// input (used by Validate to enforce earlier-step-only references).
func extractStepRefs(input json.RawMessage) ([]string, error) {
	if len(input) == 0 {
		return nil, nil
	}
	var ids []string
	seen := map[string]bool{}
	for _, m := range refRe.FindAllStringSubmatch(string(input), -1) {
		expr := strings.TrimSpace(m[1])
		if !strings.HasPrefix(expr, "steps.") {
			continue
		}
		rest := strings.TrimPrefix(expr, "steps.")
		stepID, _, ok := cutFirst(rest, ".")
		if !ok || stepID == "" {
			return nil, &RefError{Ref: expr, Reason: "expected steps.<id>.output.<path>"}
		}
		if !seen[stepID] {
			seen[stepID] = true
			ids = append(ids, stepID)
		}
	}
	return ids, nil
}
