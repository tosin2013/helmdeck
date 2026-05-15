// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// command_pack_manifest.go (#173) — typed-schema manifest format for
// operator-supplied subprocess packs.
//
// Each command pack consists of (a) an executable file and (b) an
// adjacent YAML manifest. When the manifest is present, LoadCommandPacks
// registers the pack with typed input/output schemas and the optional
// execution overrides declared inside (timeout, env, max output bytes).
// When the manifest is absent, the loader falls back to BasicSchema{}
// passthrough (the v0.12.x MVP behavior).
//
// Manifest filename convention: for a binary `<basename>` (with any
// extension stripped — `upper`, `upper.sh`, `upper.py` all map to the
// same basename `upper`), the loader looks for a sibling file named
// `<basename>.helmdeck-pack.yaml`.
//
// A malformed manifest causes the pack to be SKIPPED entirely with an
// error logged. The operator clearly intended typed schemas; falling
// back to passthrough would silently mask their bug.

import (
	"fmt"
	"os"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// manifestSuffix is appended to the binary's sanitized basename to
// find the sibling manifest file.
const manifestSuffix = ".helmdeck-pack.yaml"

// allowedSchemaTypes mirrors BasicSchema's accepted property kinds.
// Kept here so manifest parsing rejects typos up front rather than
// silently producing schemas that never match.
var allowedSchemaTypes = map[string]struct{}{
	"string": {}, "number": {}, "boolean": {}, "object": {}, "array": {},
}

// schemaYAML is the manifest sub-block describing a BasicSchema. The
// shape mirrors packs.BasicSchema exactly so toSchemas() is a 1:1 copy.
type schemaYAML struct {
	Required   []string          `yaml:"required"`
	Properties map[string]string `yaml:"properties"`
}

// commandPackManifest is the on-disk YAML shape declared by an
// operator alongside their executable. All fields are optional — an
// empty manifest is equivalent to the passthrough default, and
// individual sections (input_schema, output_schema, env, …) can be
// omitted independently.
type commandPackManifest struct {
	Name           string     `yaml:"name"`
	Version        string     `yaml:"version"`
	Description    string     `yaml:"description"`
	Author         string     `yaml:"author"`
	InputSchema    schemaYAML `yaml:"input_schema"`
	OutputSchema   schemaYAML `yaml:"output_schema"`
	TimeoutS       int        `yaml:"timeout_s"`
	MaxOutputBytes int64      `yaml:"max_output_bytes"`
	Env            []string   `yaml:"env"`
}

// loadCommandPackManifest reads and parses a manifest from path.
//
// Returns:
//   - (manifest, nil) on success
//   - (nil, nil) when path doesn't exist (caller falls back to passthrough)
//   - (nil, err)  on any other failure (unreadable, malformed YAML,
//     unknown property type, negative timeout)
//
// The two-mode "missing → nil, nil" vs "invalid → nil, err" return
// shape lets the caller distinguish "no manifest, passthrough is fine"
// from "manifest is here but unusable, skip this pack".
func loadCommandPackManifest(path string) (*commandPackManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m commandPackManifest
	// KnownFields(true) would be stricter but rejects forward-compatible
	// additions; leave it permissive so a v0.14 manifest field doesn't
	// break v0.13 loaders. Validate the fields we DO know about instead.
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if err := validateSchemaTypes(m.InputSchema); err != nil {
		return nil, fmt.Errorf("manifest %s: input_schema: %w", path, err)
	}
	if err := validateSchemaTypes(m.OutputSchema); err != nil {
		return nil, fmt.Errorf("manifest %s: output_schema: %w", path, err)
	}
	if m.TimeoutS < 0 {
		return nil, fmt.Errorf("manifest %s: timeout_s must be >= 0, got %d", path, m.TimeoutS)
	}
	if m.MaxOutputBytes < 0 {
		return nil, fmt.Errorf("manifest %s: max_output_bytes must be >= 0, got %d", path, m.MaxOutputBytes)
	}
	return &m, nil
}

func validateSchemaTypes(s schemaYAML) error {
	for prop, typ := range s.Properties {
		if _, ok := allowedSchemaTypes[typ]; !ok {
			return fmt.Errorf("property %q: unknown type %q (allowed: string, number, boolean, object, array)", prop, typ)
		}
	}
	return nil
}

// toSchemas converts the manifest's input_schema / output_schema
// blocks into packs.BasicSchema values. Empty blocks produce empty
// BasicSchemas (which validate any JSON object — the passthrough
// behavior). Callers that want full passthrough should not call this
// at all and use packs.BasicSchema{} directly.
func (m *commandPackManifest) toSchemas() (packs.Schema, packs.Schema) {
	in := packs.BasicSchema{
		Required:   m.InputSchema.Required,
		Properties: m.InputSchema.Properties,
	}
	out := packs.BasicSchema{
		Required:   m.OutputSchema.Required,
		Properties: m.OutputSchema.Properties,
	}
	return in, out
}

// toCommandSpec layers the manifest's optional execution overrides on
// top of a base spec (which carries the resolved binary path). Unset
// fields in the manifest leave the base values untouched; this lets
// LoadCommandPacks pass a base with sensible defaults.
func (m *commandPackManifest) toCommandSpec(base packs.CommandSpec) packs.CommandSpec {
	out := base
	if m.TimeoutS > 0 {
		out.Timeout = time.Duration(m.TimeoutS) * time.Second
	}
	if m.MaxOutputBytes > 0 {
		out.MaxOutputBytes = m.MaxOutputBytes
	}
	if len(m.Env) > 0 {
		out.Env = m.Env
	}
	return out
}
