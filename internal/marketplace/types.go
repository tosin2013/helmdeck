// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package marketplace implements the helmdeck pack-marketplace client
// — fetching the community catalog (index.yaml), parsing per-pack
// manifests (helmdeck-pack.yaml), and serving them through the control
// plane's REST surface. See ADR 034 for the design rationale.
//
// T810 scope (this commit): read-only catalog. The control plane
// fetches index.yaml from HELMDECK_MARKETPLACE_URL on startup and on
// POST /api/v1/marketplace/refresh; the catalog is served via
// GET /api/v1/marketplace/catalog. Install / cosign-verify / hot-load
// are T812's surface and land in a follow-up PR.
package marketplace

import "time"

// Manifest mirrors the helmdeck-pack.yaml shape. Each marketplace
// pack ships one. The schema lives at
// https://raw.githubusercontent.com/tosin2013/helmdeck-marketplace/main/schemas/helmdeck-pack.schema.json
// and is the source of truth — Go and JSON Schema must agree.
//
// Fields are tagged for both `yaml` and `json` so the same struct
// round-trips between (a) parsing a fetched helmdeck-pack.yaml and
// (b) serving it as JSON via the REST surface.
type Manifest struct {
	Name         string             `yaml:"name" json:"name"`
	Version      string             `yaml:"version" json:"version"`
	Author       string             `yaml:"author" json:"author"`
	License      string             `yaml:"license,omitempty" json:"license,omitempty"`
	Description  string             `yaml:"description" json:"description"`
	Category     string             `yaml:"category,omitempty" json:"category,omitempty"`
	Tags         []string           `yaml:"tags,omitempty" json:"tags,omitempty"`
	NeedsSession bool               `yaml:"needs_session,omitempty" json:"needs_session,omitempty"`
	InputSchema  BasicSchema        `yaml:"input_schema" json:"input_schema"`
	OutputSchema BasicSchema        `yaml:"output_schema" json:"output_schema"`
	Handler      HandlerSpec        `yaml:"handler" json:"handler"`
	Examples     []ManifestExample  `yaml:"examples,omitempty" json:"examples,omitempty"`
	Trust        *TrustInfo         `yaml:"trust,omitempty" json:"trust,omitempty"`
}

// BasicSchema mirrors the helmdeck-pack manifest schema's input/output
// declarations. Same shape as internal/packs.BasicSchema but with
// per-field type+description; the loader converts to packs.BasicSchema
// at install time.
type BasicSchema struct {
	Required   []string                  `yaml:"required,omitempty" json:"required,omitempty"`
	Properties map[string]SchemaProperty `yaml:"properties,omitempty" json:"properties,omitempty"`
}

// SchemaProperty is one entry in a BasicSchema's Properties map.
// Type is the only required field; Description is optional sugar that
// surfaces in the UI's pack-detail view.
type SchemaProperty struct {
	Type        string `yaml:"type" json:"type"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// HandlerSpec is the handler block of a manifest. The Type field
// determines which of the other fields apply.
//
// T810 (this commit) doesn't dispatch any of these — the catalog
// endpoint just returns them as opaque JSON. T812's install path
// will branch on Type and instantiate the right runtime.
type HandlerSpec struct {
	Type           string   `yaml:"type" json:"type"`
	Command        []string `yaml:"command,omitempty" json:"command,omitempty"`
	TimeoutSec     int      `yaml:"timeout_s,omitempty" json:"timeout_s,omitempty"`
	Env            []string `yaml:"env,omitempty" json:"env,omitempty"`
	MaxOutputBytes int64    `yaml:"max_output_bytes,omitempty" json:"max_output_bytes,omitempty"`

	// Sidecar overrides the default helmdeck-sidecar-marketplace image
	// for this pack. When nil/absent, the installer uses
	// HELMDECK_SIDECAR_MARKETPLACE (env) or the published default.
	// Per ADR 038 — packs that need a heavier toolchain (image
	// processing, ML inference) declare their own image here.
	Sidecar *SidecarSpec `yaml:"sidecar,omitempty" json:"sidecar,omitempty"`

	// Composite-handler fields.
	Steps []CompositeStep `yaml:"steps,omitempty" json:"steps,omitempty"`

	// WASM-handler fields (Phase 8, not yet executed by helmdeck).
	Module       string   `yaml:"module,omitempty" json:"module,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

// SidecarSpec lets a marketplace pack override the default execution
// runtime. Per ADR 038. Image is the only field for v0.13.0 beta;
// future fields (memory_mb, mount_paths) can land additively.
type SidecarSpec struct {
	Image string `yaml:"image" json:"image"`
}

// CompositeStep is one entry in a composite-handler's steps array.
type CompositeStep struct {
	Pack string         `yaml:"pack" json:"pack"`
	Args map[string]any `yaml:"args" json:"args"`
}

// ManifestExample is one worked input/output pair. The marketplace's
// validate.yml workflow asserts the handler produces the expected
// output subset for each example.
type ManifestExample struct {
	Name                 string         `yaml:"name" json:"name"`
	Description          string         `yaml:"description,omitempty" json:"description,omitempty"`
	Input                map[string]any `yaml:"input" json:"input"`
	ExpectedOutputSubset map[string]any `yaml:"expected_output_subset,omitempty" json:"expected_output_subset,omitempty"`
}

// TrustInfo is populated by the marketplace repo's signing workflow
// (sign.yml). Pack authors do NOT write this — the signing job
// computes the SHA256 and writes the signing identity.
type TrustInfo struct {
	SignedBy string `yaml:"signed_by,omitempty" json:"signed_by,omitempty"`
	SHA256   string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
}

// Index is the top-level shape of index.yaml. It enumerates every
// pack in the catalog so the control plane doesn't need to walk every
// packs/<name>/ directory just to render the catalog list.
type Index struct {
	CatalogVersion string       `yaml:"catalog_version,omitempty" json:"catalog_version,omitempty"`
	UpdatedAt      time.Time    `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	Packs          []IndexEntry `yaml:"packs" json:"packs"`
}

// IndexEntry is one row in the top-level index.yaml. It carries
// enough metadata for the catalog list UI without requiring a fetch
// of each per-pack manifest — the full Manifest is fetched on demand
// when a user opens a pack's detail view.
type IndexEntry struct {
	Name        string   `yaml:"name" json:"name"`
	Version     string   `yaml:"version" json:"version"`
	Path        string   `yaml:"path" json:"path"`
	Description string   `yaml:"description" json:"description"`
	Author      string   `yaml:"author" json:"author"`
	Category    string   `yaml:"category,omitempty" json:"category,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Installs    int      `yaml:"installs,omitempty" json:"installs,omitempty"`
	Stars       int      `yaml:"stars,omitempty" json:"stars,omitempty"`
}
