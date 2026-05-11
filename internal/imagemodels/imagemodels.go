// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package imagemodels exposes a curated catalog of image-generation
// models supported by the `image.generate` pack (#71), surfaced via
// the `helmdeck://image-models` MCP resource (#158).
//
// The catalog is hardcoded in-tree because the chained content packs
// (#146) need agents to know cost and latency without having to leave
// helmdeck to browse fal.ai's site. A future iteration can fetch the
// fal.ai model list dynamically; until then this list is the source
// of truth.
//
// Entries are kept narrow on purpose: 7 widely-used models covering
// the cost/quality spectrum, not the full fal.ai catalog. Updating
// is a one-line change to the slice below.
package imagemodels

import "context"

// Model describes a single image-generation model. Numbers are
// approximate, sourced from fal.ai's published pricing and quick
// benchmarks; treat them as ballparks for agent decision-making, not
// invoice-grade truth.
type Model struct {
	// ID is the fal.ai model identifier — what you pass to
	// image.generate's `model` input verbatim.
	ID string `json:"model_id"`
	// DisplayName is a human-friendly name for the UI / agent output.
	DisplayName string `json:"display_name"`
	// Provider is the upstream owner: "fal-ai" (or in future, "replicate").
	Provider string `json:"provider"`
	// Engine is the helmdeck engine string passed to image.generate.
	// Today always "fal"; reserved so we can add "replicate" without
	// schema churn.
	Engine string `json:"engine"`
	// ApproxCostPerImageUSD is the per-image cost at standard resolution.
	ApproxCostPerImageUSD float64 `json:"approx_cost_per_image_usd"`
	// P50LatencyS is the typical generation time in seconds for a
	// single image at default size.
	P50LatencyS float64 `json:"p50_latency_s"`
	// SupportsSeed indicates whether passing `seed` produces
	// reproducible output.
	SupportsSeed bool `json:"supports_seed"`
	// SupportsImageSize indicates whether `image_size` is honored.
	SupportsImageSize bool `json:"supports_image_size"`
	// MaxResolution is the largest pixel count the model handles
	// without quality loss (e.g. "1024x1024", "1536x1536").
	MaxResolution string `json:"max_resolution"`
	// Capabilities tags the model's strong suits — useful for an
	// agent deciding between models for a given prompt.
	Capabilities []string `json:"capabilities,omitempty"`
	// Notes is one short sentence on the model's trade-offs.
	Notes string `json:"notes,omitempty"`
}

// Catalog is the curated list of models surfaced via
// helmdeck://image-models. Order matters: cheapest/fastest first so
// agents picking the first entry land on the sensible default.
var Catalog = []Model{
	{
		ID:                    "fal-ai/flux/schnell",
		DisplayName:           "FLUX schnell",
		Provider:              "fal-ai",
		Engine:                "fal",
		ApproxCostPerImageUSD: 0.003,
		P50LatencyS:           2,
		SupportsSeed:          true,
		SupportsImageSize:     true,
		MaxResolution:         "1024x1024",
		Capabilities:          []string{"photorealistic", "fast", "default"},
		Notes:                 "Fastest and cheapest in the FLUX family. helmdeck's default image-gen model.",
	},
	{
		ID:                    "fal-ai/flux/dev",
		DisplayName:           "FLUX dev",
		Provider:              "fal-ai",
		Engine:                "fal",
		ApproxCostPerImageUSD: 0.025,
		P50LatencyS:           4,
		SupportsSeed:          true,
		SupportsImageSize:     true,
		MaxResolution:         "1024x1024",
		Capabilities:          []string{"photorealistic", "balanced"},
		Notes:                 "Better prompt-adherence than schnell at higher cost; good middle-ground for hero images.",
	},
	{
		ID:                    "fal-ai/flux-pro/v1.1",
		DisplayName:           "FLUX Pro v1.1",
		Provider:              "fal-ai",
		Engine:                "fal",
		ApproxCostPerImageUSD: 0.04,
		P50LatencyS:           8,
		SupportsSeed:          true,
		SupportsImageSize:     true,
		MaxResolution:         "1440x1440",
		Capabilities:          []string{"photorealistic", "high-quality", "marketing"},
		Notes:                 "Highest-quality FLUX tier. Use for production blog covers and marketing assets.",
	},
	{
		ID:                    "fal-ai/fast-sdxl",
		DisplayName:           "Fast SDXL",
		Provider:              "fal-ai",
		Engine:                "fal",
		ApproxCostPerImageUSD: 0.0025,
		P50LatencyS:           3,
		SupportsSeed:          true,
		SupportsImageSize:     true,
		MaxResolution:         "1024x1024",
		Capabilities:          []string{"illustration", "stylized", "fast"},
		Notes:                 "Stable Diffusion XL — strong on illustrative / stylized output; FLUX is better at photorealism.",
	},
	{
		ID:                    "fal-ai/flux-realism",
		DisplayName:           "FLUX Realism",
		Provider:              "fal-ai",
		Engine:                "fal",
		ApproxCostPerImageUSD: 0.025,
		P50LatencyS:           6,
		SupportsSeed:          true,
		SupportsImageSize:     true,
		MaxResolution:         "1024x1024",
		Capabilities:          []string{"photorealistic", "people", "documentary"},
		Notes:                 "FLUX dev fine-tuned for photographic realism, particularly people and scenes.",
	},
	{
		ID:                    "fal-ai/recraft-v3",
		DisplayName:           "Recraft v3",
		Provider:              "fal-ai",
		Engine:                "fal",
		ApproxCostPerImageUSD: 0.04,
		P50LatencyS:           5,
		SupportsSeed:          false,
		SupportsImageSize:     true,
		MaxResolution:         "2048x2048",
		Capabilities:          []string{"vector", "design", "logos", "icons"},
		Notes:                 "Best for vector / design / iconography. Native SVG export option.",
	},
	{
		ID:                    "fal-ai/ideogram/v2",
		DisplayName:           "Ideogram v2",
		Provider:              "fal-ai",
		Engine:                "fal",
		ApproxCostPerImageUSD: 0.08,
		P50LatencyS:           7,
		SupportsSeed:          true,
		SupportsImageSize:     true,
		MaxResolution:         "1536x1536",
		Capabilities:          []string{"typography", "text-rendering", "marketing"},
		Notes:                 "Strong at rendering readable text within images — useful for posters and slides with embedded copy.",
	},
}

// Lister is the dependency surface the MCP adapter consumes. Pure
// Go (no IO) today since the catalog is in-tree; future dynamic-
// fetch impls slot in here without API changes.
type Lister interface {
	List(ctx context.Context) ([]Model, error)
}

// StaticLister wraps the in-tree Catalog as a Lister. No caching
// needed (it's a slice literal); no errors possible.
type StaticLister struct{}

// List returns a defensive copy of Catalog so callers can't mutate
// the source slice.
func (StaticLister) List(_ context.Context) ([]Model, error) {
	out := make([]Model, len(Catalog))
	copy(out, Catalog)
	return out, nil
}
