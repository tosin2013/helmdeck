// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

import (
	"math"
	"strings"
	"testing"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestEstimateElevenLabs_DefaultPlan(t *testing.T) {
	usd, breakdown := EstimateElevenLabs(2220, "")
	want := 2220 * 0.000135
	if !almostEqual(usd, want) {
		t.Errorf("usd = %v, want %v", usd, want)
	}
	if breakdown["plan"] != "creator" {
		t.Errorf("plan = %v, want creator", breakdown["plan"])
	}
	if breakdown["elevenlabs_chars"].(int) != 2220 {
		t.Errorf("chars in breakdown = %v", breakdown["elevenlabs_chars"])
	}
}

func TestEstimateElevenLabs_KnownPlan(t *testing.T) {
	usd, breakdown := EstimateElevenLabs(10000, "scale")
	if !almostEqual(usd, 10000*0.000090) {
		t.Errorf("usd = %v, want %v", usd, 10000*0.000090)
	}
	if breakdown["plan"] != "scale" {
		t.Errorf("plan = %v, want scale", breakdown["plan"])
	}
}

func TestEstimateElevenLabs_FreePlanIsZero(t *testing.T) {
	usd, _ := EstimateElevenLabs(50000, "free")
	if usd != 0 {
		t.Errorf("free-plan usd = %v, want 0 (plan covers under quota)", usd)
	}
}

func TestEstimateElevenLabs_UnknownPlanFallsBackToDefault(t *testing.T) {
	usd, breakdown := EstimateElevenLabs(1000, "enterprise-custom")
	if !almostEqual(usd, 1000*0.000135) {
		t.Errorf("fallback usd = %v, want %v", usd, 1000*0.000135)
	}
	src, _ := breakdown["rate_source"].(string)
	if src == "" || !strings.Contains(src,"fell-back-to:creator") {
		t.Errorf("rate_source should disclose the fallback: %q", src)
	}
}

func TestEstimateElevenLabs_EnvOverride(t *testing.T) {
	t.Setenv("HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD", "0.000200")
	usd, breakdown := EstimateElevenLabs(1000, "creator")
	if !almostEqual(usd, 1000*0.000200) {
		t.Errorf("env-override usd = %v, want %v", usd, 1000*0.000200)
	}
	src, _ := breakdown["rate_source"].(string)
	if !strings.Contains(src,"env:HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD") {
		t.Errorf("rate_source should disclose the env override: %q", src)
	}
}

func TestEstimateElevenLabs_BadEnvOverrideIgnored(t *testing.T) {
	t.Setenv("HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD", "not-a-number")
	usd, _ := EstimateElevenLabs(1000, "creator")
	if !almostEqual(usd, 1000*0.000135) {
		t.Errorf("bad-env usd = %v, want plan default %v", usd, 1000*0.000135)
	}
}

