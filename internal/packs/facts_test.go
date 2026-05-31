package packs

import (
	"testing"
	"time"
)

// TestValidateFact_Happy covers the success path: defaults applied,
// trimmed key/value, computed opts.
func TestValidateFact_Happy(t *testing.T) {
	out, opts, err := ValidateFact(StoreFactRequest{Key: "  prefs/x  ", Value: "  v  "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Key != "prefs/x" || out.Value != "v" {
		t.Errorf("trim failed: %+v", out)
	}
	if out.Category != DefaultFactCategory {
		t.Errorf("default category not applied: %q", out.Category)
	}
	if out.TTL != DefaultFactTTL {
		t.Errorf("default TTL not applied: %s", out.TTL)
	}
	if len(opts) < 2 {
		t.Errorf("want at least TTL+Category options, got %d", len(opts))
	}
}

// TestValidateFact_ReservedCategory rejects both reserved categories
// so agents can't write under engine audit prefixes.
func TestValidateFact_ReservedCategory(t *testing.T) {
	for _, cat := range []string{AuditCategoryPack, AuditCategoryPipeline} {
		_, _, err := ValidateFact(StoreFactRequest{Key: "x", Value: "y", Category: cat})
		if err == nil || err.Code != FactErrReservedCategory {
			t.Errorf("category %q want reserved-error, got %+v", cat, err)
		}
	}
}

// TestValidateFact_TTLGuards covers both bounds + zero-defaulting.
func TestValidateFact_TTLGuards(t *testing.T) {
	cases := []struct {
		name string
		ttl  time.Duration
		want FactStoreErrCode
	}{
		{"too-short", time.Second, FactErrTTLTooShort},
		{"too-long", 400 * 24 * time.Hour, FactErrTTLTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ValidateFact(StoreFactRequest{Key: "x", Value: "y", TTL: tc.ttl})
			if err == nil || err.Code != tc.want {
				t.Errorf("ttl %s want %q, got %+v", tc.ttl, tc.want, err)
			}
		})
	}
}

// TestValidateFact_MissingFields covers the empty-string and whitespace
// cases for both required fields.
func TestValidateFact_MissingFields(t *testing.T) {
	cases := []struct {
		name string
		req  StoreFactRequest
		want FactStoreErrCode
	}{
		{"no-key", StoreFactRequest{Value: "y"}, FactErrMissingKey},
		{"ws-key", StoreFactRequest{Key: "   ", Value: "y"}, FactErrMissingKey},
		{"no-value", StoreFactRequest{Key: "x"}, FactErrMissingValue},
		{"ws-value", StoreFactRequest{Key: "x", Value: "  "}, FactErrMissingValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ValidateFact(tc.req)
			if err == nil || err.Code != tc.want {
				t.Errorf("want %q, got %+v", tc.want, err)
			}
		})
	}
}
