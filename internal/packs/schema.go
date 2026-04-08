package packs

import (
	"encoding/json"
	"fmt"
)

// BasicSchema is a deliberately tiny Schema implementation for packs
// whose inputs are simple shapes — a handful of required fields with
// scalar types. It is NOT a JSON Schema implementation; packs that
// need refs, oneOf, conditional validation, etc. should plug in
// santhosh-tekuri/jsonschema (T207 will likely add it as the default).
//
// What BasicSchema enforces:
//   - top-level value is a JSON object
//   - every Required key is present
//   - every property in Properties has the declared scalar type
//
// That's enough to validate `browser.screenshot_url` and
// `web.scrape_spa` without taking on a third-party dep in T205.
type BasicSchema struct {
	Required   []string
	Properties map[string]string // key -> "string" | "number" | "boolean" | "object" | "array"
}

// Validate checks data against the schema. Errors are descriptive
// enough to surface to a UI without leaking internals.
func (s BasicSchema) Validate(data json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("input must be a JSON object: %w", err)
	}
	for _, k := range s.Required {
		if _, ok := obj[k]; !ok {
			return fmt.Errorf("missing required field %q", k)
		}
	}
	for k, want := range s.Properties {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		if got := scalarKind(raw); got != want {
			return fmt.Errorf("field %q: expected %s, got %s", k, want, got)
		}
	}
	return nil
}

// scalarKind returns the JSON kind of raw without fully decoding it.
// Discriminating on the first non-space byte is enough for the
// surface BasicSchema cares about.
func scalarKind(raw json.RawMessage) string {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			return "string"
		case '{':
			return "object"
		case '[':
			return "array"
		case 't', 'f':
			return "boolean"
		case 'n':
			return "null"
		default:
			return "number"
		}
	}
	return "unknown"
}
