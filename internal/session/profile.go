// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package session

import (
	"os"
	"runtime"
	"strconv"
)

// ResolveCPUProfile maps a workload-class hint to a concrete CPU cap in
// fractional cores, using the host's available CPU count for the
// compute-bound class. Called from every Runtime backend that honors
// Spec.CPUProfile (Docker today; a K8s impl would translate the same
// number to a Pod resource request). See ADR 045.
//
// Operator overrides (env vars) win over the autodetect heuristic so a
// deployment can pin the cap without forking the binary:
//
//   - HELMDECK_IO_CPU_LIMIT      — fractional cores for ProfileIO
//   - HELMDECK_COMPUTE_CPU_LIMIT — fractional cores for ProfileCompute
//
// Both accept the same float format docker --cpus does (e.g. "1.5", "4").
func ResolveCPUProfile(profile CPUProfile) float64 {
	switch profile {
	case ProfileCompute:
		if v, ok := cpuEnvFloat("HELMDECK_COMPUTE_CPU_LIMIT"); ok {
			return v
		}
		return computeCPUFromHost(runtime.NumCPU())
	case ProfileIO, "":
		fallthrough
	default:
		if v, ok := cpuEnvFloat("HELMDECK_IO_CPU_LIMIT"); ok {
			return v
		}
		return 1.0
	}
}

// computeCPUFromHost is the autodetect heuristic for ProfileCompute:
// clamp(hostCores - 1, 1, 6) — leave one core for everything else, and
// don't exceed 6 because ffmpeg + headless Chromium saturate around
// that point (further cores sit idle on most encodes). Pulled out as a
// pure function so it's unit-testable without faking runtime.NumCPU.
//
//	1 core  → 1 (degraded but functional — give it the whole box)
//	2 cores → 1 (leave 1 for the host)
//	4 cores → 3
//	8 cores → 6
//	16+     → 6 (encoder saturation)
func computeCPUFromHost(hostCores int) float64 {
	if hostCores < 2 {
		return 1.0
	}
	cores := hostCores - 1
	if cores > 6 {
		cores = 6
	}
	return float64(cores)
}

func cpuEnvFloat(key string) (float64, bool) {
	raw := os.Getenv(key)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}
