// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package docker

import (
	"errors"
	"testing"
	"time"
)

// TestIsPermanentImageError — substrings the SDK surfaces for "image truly
// missing / unauthorized" must short-circuit the retry loop; everything
// else is treated as transient (rate limit, TLS, network blip).
func TestIsPermanentImageError(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"manifest unknown: alpine:9999 not found", true},
		{"Error response from daemon: pull access denied for foo/bar, repository does not exist or may require 'docker login'", true},
		{"unauthorized: authentication required", true},
		{"no such image: alpine:3", true},
		// Transient — must be retried.
		{"toomanyrequests: You have reached your pull rate limit", false},
		{"net/http: TLS handshake timeout", false},
		{"read tcp 1.2.3.4:443: connection reset by peer", false},
		{"", false},
	} {
		got := isPermanentImageError(errors.New(tc.msg))
		if tc.msg == "" {
			got = isPermanentImageError(nil)
		}
		if got != tc.want {
			t.Errorf("isPermanentImageError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

// TestPullBackoff — backoff schedule is hardcoded so a future tweak to the
// timing has to also justify the new numbers in code review.
func TestPullBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{
		{1, 0},               // first call — no wait
		{2, 2 * time.Second}, // first retry
		{3, 4 * time.Second}, // second retry
	} {
		if got := pullBackoff(tc.attempt); got != tc.want {
			t.Errorf("pullBackoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
