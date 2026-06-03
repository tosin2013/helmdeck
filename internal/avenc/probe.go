// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package avenc

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/session"
)

// ProbeAudioDuration runs `LC_ALL=C ffprobe -show_entries format=duration -of csv=p=0`
// against the supplied path and returns the duration in seconds.
// Rejects NaN, ±Inf, and non-positive values explicitly — strconv
// happily parses "NaN" and "+Inf" as valid floats and a locale-affected
// ffprobe can emit "0" on a malformed input; either would silently
// poison downstream pad / fade math. The LC_ALL=C prefix locks the
// numeric locale to a stable decimal separator (PR-#400 added the
// NaN/Inf rejection; the locale prefix is the external-research
// addition).
//
// Returns (duration, nil) on success.
// Returns (0, error) on transport error, non-zero exit, parse failure,
// or rejection — callers typically fall back to a default duration
// (see slides.narrate's slideDur and podcast.generate's per-turn floor).
func ProbeAudioDuration(ctx context.Context, exec Executor, path string) (float64, error) {
	res, err := exec(ctx, session.ExecRequest{
		Cmd: []string{"sh", "-c", LocalePrefix + "ffprobe -v error -show_entries format=duration -of csv=p=0 " + shellQuote(path)},
	})
	if err != nil {
		return 0, fmt.Errorf("ffprobe transport error on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("ffprobe exit %d on %s: %s",
			res.ExitCode, path, strings.TrimSpace(string(res.Stderr)))
	}
	raw := strings.TrimSpace(string(res.Stdout))
	dur, perr := strconv.ParseFloat(raw, 64)
	if perr != nil {
		return 0, fmt.Errorf("parse duration %q from %s: %w", raw, path, perr)
	}
	if math.IsNaN(dur) || math.IsInf(dur, 0) || dur <= 0 {
		return 0, fmt.Errorf("ffprobe returned non-positive/NaN/Inf duration %q (parsed=%v) on %s — treating as probe failure so the caller falls back to its default duration",
			raw, dur, path)
	}
	return dur, nil
}
