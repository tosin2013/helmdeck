// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package session

import "testing"

// TestComputeCPUFromHost — the clamp(hostCores-1, 1, 6) heuristic that
// ADR 045 calls out. Hardcoded so a future refactor that changes the
// math has to also justify the new mapping in code review.
func TestComputeCPUFromHost(t *testing.T) {
	for _, tc := range []struct {
		hostCores int
		want      float64
	}{
		{1, 1.0}, // degraded host — give it the only core we have
		{2, 1.0}, // leave 1 for the host
		{3, 2.0},
		{4, 3.0},
		{8, 6.0},  // saturation cap kicks in
		{16, 6.0}, // ffmpeg + Chromium don't benefit past ~6
		{64, 6.0},
	} {
		if got := computeCPUFromHost(tc.hostCores); got != tc.want {
			t.Errorf("computeCPUFromHost(%d) = %v, want %v", tc.hostCores, got, tc.want)
		}
	}
}

func TestResolveCPUProfile_DefaultsAndIO(t *testing.T) {
	// Make sure no operator override is leaking into the test env.
	t.Setenv("HELMDECK_IO_CPU_LIMIT", "")
	t.Setenv("HELMDECK_COMPUTE_CPU_LIMIT", "")

	if got := ResolveCPUProfile(""); got != 1.0 {
		t.Errorf("empty profile = %v, want 1.0 (legacy default)", got)
	}
	if got := ResolveCPUProfile(ProfileIO); got != 1.0 {
		t.Errorf("ProfileIO = %v, want 1.0", got)
	}
	// Compute must scale with host (whatever NumCPU reports here),
	// and never fall below 1.0.
	if got := ResolveCPUProfile(ProfileCompute); got < 1.0 {
		t.Errorf("ProfileCompute = %v, must be ≥ 1.0", got)
	}
}

func TestResolveCPUProfile_EnvOverrides(t *testing.T) {
	t.Setenv("HELMDECK_IO_CPU_LIMIT", "0.5")
	t.Setenv("HELMDECK_COMPUTE_CPU_LIMIT", "8")

	if got := ResolveCPUProfile(ProfileIO); got != 0.5 {
		t.Errorf("HELMDECK_IO_CPU_LIMIT=0.5 not honored: got %v", got)
	}
	if got := ResolveCPUProfile(ProfileCompute); got != 8.0 {
		t.Errorf("HELMDECK_COMPUTE_CPU_LIMIT=8 not honored: got %v", got)
	}
}

func TestResolveCPUProfile_BadEnvIgnored(t *testing.T) {
	// Garbage / zero / negative overrides must fall back to the
	// heuristic, not silently cap a render at 0 cores.
	for _, bad := range []string{"nonsense", "0", "-2"} {
		t.Setenv("HELMDECK_COMPUTE_CPU_LIMIT", bad)
		got := ResolveCPUProfile(ProfileCompute)
		if got < 1.0 {
			t.Errorf("bad override %q produced %v; should have fallen back to the heuristic (≥1.0)", bad, got)
		}
	}
}
