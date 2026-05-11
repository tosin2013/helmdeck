// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package imagemodels

import (
	"context"
	"strings"
	"testing"
)

func TestCatalog_HasDefault(t *testing.T) {
	// fal-ai/flux/schnell must be the first entry — image.generate's
	// default model points at it, and helmdeck://image-models orders
	// "agent picks the first one" → "agent picks the sensible default".
	if len(Catalog) == 0 {
		t.Fatal("Catalog must not be empty")
	}
	if Catalog[0].ID != "fal-ai/flux/schnell" {
		t.Errorf("first model = %q, want fal-ai/flux/schnell", Catalog[0].ID)
	}
}

func TestCatalog_EveryEntryWellFormed(t *testing.T) {
	for i, m := range Catalog {
		if m.ID == "" {
			t.Errorf("Catalog[%d]: ID empty", i)
		}
		if m.DisplayName == "" {
			t.Errorf("Catalog[%d] (%s): DisplayName empty", i, m.ID)
		}
		if m.Engine == "" {
			t.Errorf("Catalog[%d] (%s): Engine empty", i, m.ID)
		}
		if m.Provider == "" {
			t.Errorf("Catalog[%d] (%s): Provider empty", i, m.ID)
		}
		if m.ApproxCostPerImageUSD <= 0 {
			t.Errorf("Catalog[%d] (%s): ApproxCostPerImageUSD = %f, want > 0", i, m.ID, m.ApproxCostPerImageUSD)
		}
		if m.P50LatencyS <= 0 {
			t.Errorf("Catalog[%d] (%s): P50LatencyS = %f, want > 0", i, m.ID, m.P50LatencyS)
		}
		if !strings.Contains(m.MaxResolution, "x") {
			t.Errorf("Catalog[%d] (%s): MaxResolution = %q, want WxH form", i, m.ID, m.MaxResolution)
		}
	}
}

func TestStaticLister_ReturnsDefensiveCopy(t *testing.T) {
	l := StaticLister{}
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(Catalog) {
		t.Errorf("len = %d, want %d", len(got), len(Catalog))
	}
	// Mutating the returned slice must not affect Catalog.
	got[0].DisplayName = "MUTATED"
	if Catalog[0].DisplayName == "MUTATED" {
		t.Error("List returned a reference, not a defensive copy")
	}
}
