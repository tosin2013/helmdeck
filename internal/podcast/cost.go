// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

// cost.go — character-based ElevenLabs cost estimation (#145).
//
// ElevenLabs charges per character of TTS output. A multi-turn podcast
// of ~8 minutes runs through ~7,200 characters of script (150 wpm ×
// 8 min × 6 chars/word). Operators on tight budgets need to preview
// cost before committing; dry_run mode in podcast.generate /
// slides.narrate calls into here to render the estimate.
//
// The plan rate table mirrors ElevenLabs' published pricing (as of
// 2026-04). Rates are per character (not per 1k chars) to avoid an
// off-by-1000 footgun in the math. Operators on a custom contract
// can override the rate via HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD.

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// PlanRates maps an ElevenLabs plan name to its per-character USD rate.
// Numbers reflect ElevenLabs' published 2026-04 pricing for the
// standard "v2 / multilingual" voice tier:
//
//	Free       — 10k chars/month included, $0 marginal (return 0)
//	Starter    — $5/month, ~30k chars
//	Creator    — $22/month, ~100k chars   → ~$0.000220/char marginal
//	Pro        — $99/month, ~500k chars   → ~$0.000180/char
//	Scale      — $330/month, ~2M chars    → ~$0.000135/char marginal
//
// Conservative-ish default is "Creator" since that's the most common
// plan for one-person creators using the platform. Operators on
// other plans pass plan="pro" / plan="scale" or override the rate.
var PlanRates = map[string]float64{
	"free":    0.0,
	"starter": 0.000180,
	"creator": 0.000135,
	"pro":     0.000110,
	"scale":   0.000090,
}

// DefaultPlan is the rate used when the caller doesn't pass a plan
// name. Matches the most common helmdeck operator profile.
const DefaultPlan = "creator"

// EstimateElevenLabs returns a USD cost estimate for synthesizing
// scriptChars characters under the named plan. A second return value
// breaks down the calculation for inclusion in the pack response —
// operators can audit the rate, the plan it came from, and whether
// an env-var override was honored.
//
// The cost is unconditionally returned; operators on the Free tier
// see $0 (the plan covers the chars under monthly quota — true cost
// is more nuanced but $0 is the sound first approximation for a
// per-call estimate).
func EstimateElevenLabs(scriptChars int, plan string) (float64, map[string]any) {
	planNorm := strings.ToLower(strings.TrimSpace(plan))
	if planNorm == "" {
		planNorm = DefaultPlan
	}
	rate, planFound := PlanRates[planNorm]
	rateSource := "plan-default:" + planNorm
	if !planFound {
		rate = PlanRates[DefaultPlan]
		rateSource = "plan-unknown(" + planNorm + ")-fell-back-to:" + DefaultPlan
		planNorm = DefaultPlan
	}
	if v := os.Getenv("HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD"); v != "" {
		if r, err := strconv.ParseFloat(v, 64); err == nil && r >= 0 {
			rate = r
			rateSource = "env:HELMDECK_ELEVENLABS_RATE_PER_CHAR_USD=" + v
		}
	}
	usd := float64(scriptChars) * rate
	breakdown := map[string]any{
		"elevenlabs_chars":             scriptChars,
		"elevenlabs_rate_per_char_usd": rate,
		"elevenlabs_subtotal_usd":      usd,
		"plan":                         planNorm,
		"rate_source":                  rateSource,
	}
	return usd, breakdown
}

// FormatUSD is a tiny helper for pretty-printing the estimate in
// non-JSON contexts (CLI output, log lines). Returns e.g. "$0.30".
func FormatUSD(usd float64) string {
	return fmt.Sprintf("$%.4f", usd)
}
