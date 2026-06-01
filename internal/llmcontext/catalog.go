// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package llmcontext

import (
	"encoding/json"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// RoutingGuide is the catalog-projection shape both helmdeck.route
// and helmdeck.plan ship to the LLM. The JSON shape mirrors
// internal/packs/builtin/route.go's catalogProjection so wire compat
// is preserved when LLM-backed packs swap their local type for this
// shared one. Defined here so CompactCatalog has a canonical input
// type to operate on; would otherwise have to live awkwardly in
// internal/packs/builtin.
type RoutingGuide struct {
	Packs     []Pack     `json:"packs"`
	Pipelines []Pipeline `json:"pipelines"`
}

// Pack carries one pack's catalog projection. Metadata fields are
// addressed individually because CompactCatalog strips them in a
// deterministic priority order; encoding metadata as
// packs.PackMetadata keeps the type compatible with the existing
// projection shape.
type Pack struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Metadata    packs.PackMetadata `json:"metadata,omitempty"`
}

// Pipeline carries one pipeline's catalog projection. Pipeline
// metadata stays json.RawMessage because pipelines.List() returns
// metadata as opaque JSON (pipelines have a richer schema than
// PackMetadata and we don't want to bake the full shape into this
// package). CompactCatalog parses the metadata as a map when it
// needs to strip specific fields.
type Pipeline struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}
